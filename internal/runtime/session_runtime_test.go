package runtime_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/store"
	"pentest/internal/task"
)

type sessionSlowAdapter struct{}

func (sessionSlowAdapter) Name() string { return "session-slow" }

func (sessionSlowAdapter) Run(ctx context.Context, _ string, emit func(task.EventKind, task.EventPayload)) error {
	emit(task.EventKindRuntimeOutput, task.EventPayload{"stream": "stdout", "text": "Session runtime ready"})
	<-ctx.Done()
	return ctx.Err()
}

func TestSessionHarnessRebindsFinalEventsAndConfirmsStop(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "session-runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := session.NewService(db, filepath.Join(t.TempDir(), "sessions"))
	created, err := sessions.Create(session.CreateRequest{Input: "Keep the Session runtime alive"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	first, err := sessions.CreateContinuation(created.ID, "profile:session", "codex", session.RunnerHost)
	if err != nil {
		t.Fatalf("create first continuation: %v", err)
	}

	var confirmed atomic.Bool
	harness := runtime.NewSessionHarness(sessions)
	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.SessionLaunchRequest{
			SessionID: created.ID, Goal: "continue the Session conversation", Adapter: sessionSlowAdapter{},
			ContinuationID: first.ID,
			StopConfirmation: func(time.Duration) error {
				confirmed.Store(true)
				return nil
			},
		})
	}()
	waitForSessionHarnessActive(t, harness, created.ID)

	first, err = sessions.Continuation(first.ID)
	if err != nil {
		t.Fatalf("reload first continuation: %v", err)
	}
	replacement, err := sessions.CreateReplacementContinuation(first, nil)
	if err != nil {
		t.Fatalf("create replacement continuation: %v", err)
	}
	if _, err := sessions.UpdateContinuationStatus(replacement.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatalf("mark replacement running: %v", err)
	}
	if err := harness.RebindContinuation(created.ID, replacement.ID); err != nil {
		t.Fatalf("rebind Session Harness: %v", err)
	}
	if _, err := sessions.UpdateContinuationStatus(first.ID, session.RuntimeStatusCompleted); err != nil {
		t.Fatalf("complete first continuation: %v", err)
	}

	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("Session Harness did not stop and confirm the provider")
	}
	if !confirmed.Load() {
		t.Fatal("stop confirmation was not invoked")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Session Harness error = %v, want context canceled", err)
	}

	firstAfter, err := sessions.Continuation(first.ID)
	if err != nil {
		t.Fatalf("reload first continuation: %v", err)
	}
	replacementAfter, err := sessions.Continuation(replacement.ID)
	if err != nil {
		t.Fatalf("reload replacement continuation: %v", err)
	}
	if firstAfter.Status != session.RuntimeStatusCompleted {
		t.Fatalf("first continuation status = %q, want completed", firstAfter.Status)
	}
	if replacementAfter.Status != session.RuntimeStatusStopped {
		t.Fatalf("replacement continuation status = %q, want stopped", replacementAfter.Status)
	}

	events, err := sessions.Events(created.ID)
	if err != nil {
		t.Fatalf("read Session events: %v", err)
	}
	var sawFirst, sawReplacementStopped bool
	for _, event := range events {
		continuationID, _ := event.Payload["continuation_id"].(string)
		if continuationID == first.ID {
			sawFirst = true
		}
		if event.Kind == session.EventKindLifecycle && event.Payload["phase"] == "stopped" {
			if continuationID != replacement.ID {
				t.Fatalf("stopped event continuation = %q, want replacement %q", continuationID, replacement.ID)
			}
			sawReplacementStopped = true
		}
	}
	if !sawFirst || !sawReplacementStopped {
		t.Fatalf("Session event correlation incomplete: saw first=%v replacement stopped=%v events=%#v", sawFirst, sawReplacementStopped, events)
	}
}

func waitForSessionHarnessActive(t *testing.T, harness *runtime.SessionHarness, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if harness.IsActive(sessionID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Session Harness did not become active")
}
