package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

// holdAdapter keeps the harness IsActive until release is closed.
// Used to deterministically occupy the terminal+active harness window that
// handleResumeTask must wait through (not bypass by pre-waiting in fixtures).
type holdAdapter struct {
	release <-chan struct{}
}

func (holdAdapter) Name() string { return "hold" }

func (a holdAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	_ = goal
	emit(task.EventKindLifecycle, task.EventPayload{"phase": "hold_started"})
	select {
	case <-a.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func postTaskResume(server *Server, projectID, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	return resp
}

// Force durable terminal Task while harness remains active (no provider session).
// This is the exact production race resume must wait out.
func forceTerminalWithActiveHarness(t *testing.T, server *Server, created task.Task, release chan struct{}) (cont task.TaskContinuation, launchDone <-chan error) {
	t.Helper()
	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		t.Fatal(err)
	}
	cont, err = server.tasks.CreateContinuation(created.ID, profile.ID, string(profile.Provider), created.Runner)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:         created.ID,
			Goal:           created.Goal,
			ContinuationID: cont.ID,
			Adapter:        holdAdapter{release: release},
		})
	}()
	waitForHarnessActive(t, server, created.ID, true)

	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusCompleted {
		t.Fatalf("setup status = %q, want completed", found.Status)
	}
	if !server.harness.IsActive(created.ID) {
		t.Fatal("setup: harness must remain active while durable status is terminal")
	}
	if _, bound := server.providerSessions.get(created.ID); bound {
		t.Fatal("setup must not bind a provider session (wait path is harness-only)")
	}
	return cont, done
}

func TestResumeWaitsForTerminalHarnessReleaseThenLaunchesOnce(t *testing.T) {
	// #154: POST /resume must wait through terminal+IsActive, then accept and
	// start exactly one replacement Runtime after the old harness exits.
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPair("resume-wait-ok-" + strconv.Itoa(open))
	}
	seed, seedAdapter := newSession(0)
	factory := &finishSessionFactory{session: seed, adapter: seedAdapter, newSession: newSession}
	server, created, _ := newFinishTaskFixture(t, factory)
	server.runtimeStopTimeout = 2 * time.Second

	release := make(chan struct{})
	beforeCont, launchDone := forceTerminalWithActiveHarness(t, server, created, release)
	opensBefore := factory.openCount()
	if opensBefore != 0 {
		t.Fatalf("hold launch must not use factory, opens=%d", opensBefore)
	}

	type resumeResult struct {
		code int
		body string
	}
	done := make(chan resumeResult, 1)
	go func() {
		resp := postTaskResume(server, created.ProjectID, created.ID)
		done <- resumeResult{code: resp.Code, body: resp.Body.String()}
	}()

	// Resume must not return while the old harness is still active.
	select {
	case got := <-done:
		t.Fatalf("resume returned before harness release: status=%d body=%s", got.code, got.body)
	case <-time.After(50 * time.Millisecond):
	}
	if !server.harness.IsActive(created.ID) {
		t.Fatal("harness became inactive before intentional release")
	}

	close(release)
	select {
	case err := <-launchDone:
		if err != nil {
			t.Fatalf("hold launch exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hold launch did not exit after release")
	}

	var got resumeResult
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("resume did not return after harness release")
	}
	if got.code != http.StatusAccepted {
		t.Fatalf("resume status = %d body %s, want 202", got.code, got.body)
	}
	if factory.openCount() != 1 {
		t.Fatalf("replacement Runtime opens = %d, want exactly 1", factory.openCount())
	}
	afterCont, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || afterCont == nil {
		t.Fatalf("latest continuation after resume: %#v %v", afterCont, err)
	}
	if afterCont.ID == beforeCont.ID || afterCont.Number != beforeCont.Number+1 {
		t.Fatalf("want one new Continuation after resume, before=%#v after=%#v", beforeCont, afterCont)
	}
	waitForHarnessActive(t, server, created.ID, true)
}

func TestResumeTimesOutWhenTerminalHarnessStaysActive(t *testing.T) {
	// #154: short runtimeStopTimeout; stuck harness → stable 409, no Continuation/Runtime.
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPair("resume-wait-timeout-" + strconv.Itoa(open))
	}
	seed, seedAdapter := newSession(0)
	factory := &finishSessionFactory{session: seed, adapter: seedAdapter, newSession: newSession}
	server, created, _ := newFinishTaskFixture(t, factory)
	server.runtimeStopTimeout = 50 * time.Millisecond

	release := make(chan struct{})
	defer close(release) // release after assertions so harness can exit for cleanup
	beforeCont, _ := forceTerminalWithActiveHarness(t, server, created, release)
	opensBefore := factory.openCount()

	start := time.Now()
	first := postTaskResume(server, created.ProjectID, created.ID)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("resume waited %v, want timeout near runtimeStopTimeout=%v", elapsed, server.runtimeStopTimeout)
	}
	if first.Code != http.StatusConflict {
		t.Fatalf("resume status = %d body %s, want 409", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), "runtime harness is still active") {
		t.Fatalf("resume body = %q, want stable harness-active error", first.Body.String())
	}

	second := postTaskResume(server, created.ProjectID, created.ID)
	if second.Code != first.Code || second.Body.String() != first.Body.String() {
		t.Fatalf("timeout error not stable: first=%d %q second=%d %q",
			first.Code, first.Body.String(), second.Code, second.Body.String())
	}

	if factory.openCount() != opensBefore {
		t.Fatalf("timeout resume opened Runtime: before=%d after=%d", opensBefore, factory.openCount())
	}
	afterCont, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || afterCont == nil {
		t.Fatalf("latest continuation: %#v %v", afterCont, err)
	}
	if afterCont.ID != beforeCont.ID || afterCont.Number != beforeCont.Number {
		t.Fatalf("timeout resume created Continuation: before=%#v after=%#v", beforeCont, afterCont)
	}
	if !server.harness.IsActive(created.ID) {
		t.Fatal("timeout path must leave the stuck harness active")
	}
	if _, bound := server.providerSessions.get(created.ID); bound {
		t.Fatal("timeout resume bound a provider session")
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusCompleted {
		t.Fatalf("timeout resume mutated Task status to %q", found.Status)
	}
}
