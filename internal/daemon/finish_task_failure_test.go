package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

type failingCloseSession struct {
	*fakeprovider.Session
	closed     chan struct{}
	failClose  atomic.Bool
	closeCount atomic.Int32
}

func (s *failingCloseSession) Close(ctx context.Context) error {
	s.closeCount.Add(1)
	if s.failClose.Load() {
		return errors.New("injected provider close failure")
	}
	err := s.Session.Close(ctx)
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return err
}

// silentCloseSession closes the provider handle but never signals the run
// adapter closed channel. Harness exit is then driven only by StopAndWait cancel
// under finish intent — which must not write terminal status (sole owner is
// handleFinishTask after harness exit).
type silentCloseSession struct {
	*fakeprovider.Session
}

func (s *silentCloseSession) Close(ctx context.Context) error {
	return s.Session.Close(ctx)
}

func TestFinishTaskSoleOwnerAfterCancelDrivenHarnessExit(t *testing.T) {
	neverClosed := make(chan struct{})
	fake := fakeprovider.New(fakeprovider.Config{
		SessionID: "finish-silent-close",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &silentCloseSession{Session: fake}
	adapter := runtime.NewProviderSessionRunAdapter(session, neverClosed)
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	before, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || before == nil {
		t.Fatalf("before continuation: %v %#v", err, before)
	}

	resp := postFinish(server, created.ProjectID, created.ID)
	if resp.Code != http.StatusOK {
		t.Fatalf("finish status = %d body %s", resp.Code, resp.Body.String())
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawShutdown, sawOperatorFinish bool
	for _, event := range events {
		if event.Kind != task.EventKindLifecycle {
			continue
		}
		switch event.Payload["phase"] {
		case "finish_shutdown":
			sawShutdown = true
		case "completed":
			if event.Payload["reason"] != "operator_finish" {
				t.Fatalf("completed lifecycle without operator_finish: %#v", event.Payload)
			}
			sawOperatorFinish = true
		}
	}
	if !sawShutdown {
		t.Fatal("expected finish_shutdown from harness exit-without-terminal-write")
	}
	if !sawOperatorFinish {
		t.Fatal("expected operator_finish from handleFinishTask sole owner")
	}
	cont, err := server.tasks.Continuation(before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cont.Status != task.StatusCompleted || cont.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("continuation after sole-owner finish = %#v", cont)
	}
}

func TestFinishTaskClearsIntentWhenProviderCloseFails(t *testing.T) {
	inner := fakeprovider.New(fakeprovider.Config{
		SessionID: "finish-close-fail",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &failingCloseSession{
		Session: inner,
		closed:  make(chan struct{}),
	}
	session.failClose.Store(true)
	adapter := runtime.NewProviderSessionRunAdapter(session, session.closed)
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	server.runtimeStopTimeout = 2 * time.Second
	launchFinishTask(t, server, created)

	resp := postFinish(server, created.ProjectID, created.ID)
	if resp.Code == http.StatusOK {
		t.Fatalf("finish must fail when Close fails, body %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "provider_session_close") {
		t.Fatalf("expected provider_session_close stage error, got %s", resp.Body.String())
	}
	if server.harness.FinishIntentActive(created.ID) {
		t.Fatal("finish intent still active after Close failure")
	}

	session.failClose.Store(false)
	stopResp := postStop(server, created.ProjectID, created.ID)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && stopResp.Code != http.StatusOK {
		time.Sleep(20 * time.Millisecond)
		stopResp = postStop(server, created.ProjectID, created.ID)
	}
	if stopResp.Code != http.StatusOK {
		t.Fatalf("stop after failed finish status = %d body %s", stopResp.Code, stopResp.Body.String())
	}

	var found task.Task
	var err error
	for time.Now().Before(deadline) {
		found, err = server.tasks.Get(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if found.Status == task.StatusStopped || found.Status == task.StatusFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found.Status == task.StatusCompleted {
		t.Fatal("Task must not be operator-completed after aborted Finish intent")
	}
	if found.Status != task.StatusStopped && found.Status != task.StatusFailed {
		t.Fatalf("after Stop status = %q, want stopped or failed", found.Status)
	}

	latest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest continuation: %v %#v", err, latest)
	}
	if latest.Status == task.StatusCompleted {
		t.Fatal("Continuation must not be completed via residual finish intent after Close failure")
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle && event.Payload["phase"] == "completed" && event.Payload["reason"] == "operator_finish" {
			t.Fatal("operator_finish must not be recorded after aborted Finish")
		}
	}
}

// hangCloseSession blocks Close until ctx deadline (Finish timeout), then
// fails fast so server cleanup and residual Stop cannot hang.
type hangCloseSession struct {
	*fakeprovider.Session
	closed   chan struct{}
	started  chan struct{}
	once     sync.Once
	timedOut atomic.Bool
}

func (s *hangCloseSession) Close(ctx context.Context) error {
	s.once.Do(func() {
		if s.started != nil {
			close(s.started)
		}
	})
	if s.timedOut.Load() {
		err := s.Session.Close(ctx)
		select {
		case <-s.closed:
		default:
			close(s.closed)
		}
		return err
	}
	select {
	case <-ctx.Done():
		s.timedOut.Store(true)
		return ctx.Err()
	case <-time.After(3 * time.Second):
		s.timedOut.Store(true)
		return errors.New("hang close safety timeout")
	}
}

func TestFinishTaskClearsIntentWhenProviderCloseTimesOut(t *testing.T) {
	inner := fakeprovider.New(fakeprovider.Config{
		SessionID: "finish-close-timeout",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &hangCloseSession{
		Session: inner,
		closed:  make(chan struct{}),
		started: make(chan struct{}),
	}
	adapter := runtime.NewProviderSessionRunAdapter(session, session.closed)
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	server.runtimeStopTimeout = 80 * time.Millisecond
	launchFinishTask(t, server, created)

	resp := postFinish(server, created.ProjectID, created.ID)
	if resp.Code == http.StatusOK {
		t.Fatalf("finish must fail on close timeout, body %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "provider_session_close") {
		t.Fatalf("expected provider_session_close error, got %s", resp.Body.String())
	}
	if server.harness.FinishIntentActive(created.ID) {
		t.Fatal("finish intent still active after close timeout")
	}

	// Residual intent must not complete Continuation when harness later exits.
	server.harness.Stop(created.ID)
	_ = server.closeProviderSession(created.ID)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !server.harness.IsActive(created.ID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status == task.StatusCompleted {
		t.Fatal("Task completed after timed-out Finish — finish intent leaked")
	}
	latest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest continuation: %v %#v", err, latest)
	}
	if latest.Status == task.StatusCompleted {
		t.Fatal("Continuation completed after timed-out Finish — finish intent leaked")
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle && event.Payload["reason"] == "operator_finish" {
			t.Fatal("operator_finish after timed-out Finish")
		}
	}
}
