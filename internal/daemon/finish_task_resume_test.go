package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// failingFinishReconciler injects Continuation reconciliation failure so Finish
// cannot claim Task completed without a durable recon marker.
type failingFinishReconciler struct {
	err error
}

func (f failingFinishReconciler) ReconcileTerminalContinuation(context.Context, string, string) error {
	return f.err
}

// countingFinishReconciler records successful recon for order assertions.
type countingFinishReconciler struct {
	mu    sync.Mutex
	calls int
	inner task.ContinuationReconciler
}

func (c *countingFinishReconciler) ReconcileTerminalContinuation(ctx context.Context, continuationID, reason string) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.inner != nil {
		return c.inner.ReconcileTerminalContinuation(ctx, continuationID, reason)
	}
	return nil
}

func (c *countingFinishReconciler) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// #153: operator Finish Task + resumable terminal conversations.
// Seam: daemon Task HTTP + Runtime Activity gate + Continuation close.

type finishSessionFactory struct {
	mu      sync.Mutex
	session runtime.ProviderSession
	adapter runtime.Adapter
	opens   int
	// newSession builds a fresh session/adapter per Open so resume after Finish
	// cannot rebind a closed handle (mirrors production factory behavior).
	newSession func(open int) (runtime.ProviderSession, runtime.Adapter)
}

func (f *finishSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	session, adapter := f.session, f.adapter
	if f.newSession != nil {
		session, adapter = f.newSession(f.opens)
		f.session, f.adapter = session, adapter
	}
	if binder, ok := session.(runtime.ProviderSessionContinuationBinder); ok {
		_ = binder.BindContinuation(request.Continuation.ID)
	}
	if runAdapter, ok := adapter.(*runtime.ProviderSessionRunAdapter); ok {
		runAdapter.BindContinuation(request.Continuation.ID)
	}
	return ProviderSessionBinding{Session: session, Adapter: adapter}, nil
}

func (f *finishSessionFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

// finishBoundSession mirrors production bridges: session Close signals the run
// adapter closed channel so Finish can waitHarnessInactive without cancel.
type finishBoundSession struct {
	*runtime.FakeProviderSession
	closed chan struct{}
	once   sync.Once
}

func (s *finishBoundSession) Close(ctx context.Context) error {
	err := s.FakeProviderSession.Close(ctx)
	if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
		s.signalClosed()
	}
	return err
}

func (s *finishBoundSession) signalClosed() {
	if s.closed == nil {
		return
	}
	s.once.Do(func() {
		select {
		case <-s.closed:
		default:
			close(s.closed)
		}
	})
}

func newFinishSessionPair(sessionID string) (runtime.ProviderSession, runtime.Adapter) {
	return newFinishSessionPairWithCaps(sessionID, runtimeplugin.Capabilities{
		PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
	})
}

func newFinishSessionPairWithCaps(sessionID string, caps runtimeplugin.Capabilities) (runtime.ProviderSession, runtime.Adapter) {
	closed := make(chan struct{})
	fake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: sessionID, Capabilities: caps,
	})
	session := &finishBoundSession{FakeProviderSession: fake, closed: closed}
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	return session, adapter
}

// forceBusySession reports busy turn activity without holding the daemon
// control lock (native steer holds the lock for the whole operation).
type forceBusySession struct {
	*finishBoundSession
	busy bool
}

func (s *forceBusySession) ControlBusy() bool {
	if s.busy {
		return true
	}
	return s.finishBoundSession.ControlBusy()
}

func newFinishTaskFixture(t *testing.T, factory ProviderSessionFactory) (*Server, task.Task, modelprovider.Provider) {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	mp, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mp.APIKeyEnv, "sk-test")

	binary := filepath.Join(root, "codex")
	if err := writeExecutable(binary, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: mp.ID, ModelOverride: "gpt-test", ReasoningEffort: "medium",
		BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, created, mp
}

func launchFinishTask(t *testing.T, server *Server, created task.Task) {
	t.Helper()
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
	waitForLiveIdle(t, server, created)
}

func waitForLiveIdle(t *testing.T, server *Server, created task.Task) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body := getTaskActivity(t, server, created.ProjectID, created.ID)
		if body.Status == "running" && body.RuntimeActivity.Liveness == "live" && body.RuntimeActivity.TurnActivity == "idle" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	body := getTaskActivity(t, server, created.ProjectID, created.ID)
	t.Fatalf("want running live idle, got status=%q activity=%#v", body.Status, body.RuntimeActivity)
}

func postFinish(server *Server, projectID, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/finish", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	return resp
}

func postStop(server *Server, projectID, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/stop", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	return resp
}

func TestFinishTaskCompletesLiveIdleRuntimeAndClosesResources(t *testing.T) {
	session, adapter := newFinishSessionPair("finish-session")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	detailReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID, nil)
	detailResp := httptest.NewRecorder()
	server.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("detail status = %d body %s", detailResp.Code, detailResp.Body.String())
	}
	var before struct {
		RuntimeControls struct {
			FinishAvailable bool `json:"finish_available"`
		} `json:"runtime_controls"`
		RuntimeActivity struct {
			Liveness     string `json:"liveness"`
			TurnActivity string `json:"turn_activity"`
		} `json:"runtime_activity"`
	}
	if err := json.Unmarshal(detailResp.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if !before.RuntimeControls.FinishAvailable {
		t.Fatalf("finish_available = false for live idle activity %#v", before.RuntimeActivity)
	}

	resp := postFinish(server, created.ProjectID, created.ID)
	if resp.Code != http.StatusOK {
		t.Fatalf("finish status = %d body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Status          string `json:"status"`
		RuntimeActivity struct {
			Liveness string `json:"liveness"`
		} `json:"runtime_activity"`
		LatestContinuation *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"latest_continuation"`
		RuntimeControls struct {
			FinishAvailable bool `json:"finish_available"`
			ResumeAvailable bool `json:"resume_available"`
		} `json:"runtime_controls"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "completed" {
		t.Fatalf("task status = %q, want completed", body.Status)
	}
	if body.LatestContinuation == nil || body.LatestContinuation.Status != "completed" {
		t.Fatalf("continuation = %#v, want completed", body.LatestContinuation)
	}
	if body.RuntimeActivity.Liveness == "live" {
		t.Fatalf("runtime still live after finish: %#v", body.RuntimeActivity)
	}
	if body.RuntimeControls.FinishAvailable {
		t.Fatal("finish_available should be false after finish")
	}
	if !body.RuntimeControls.ResumeAvailable {
		t.Fatal("completed task must remain resumable")
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("provider session still bound after finish")
	}
	if server.harness.IsActive(created.ID) {
		t.Fatal("harness still active after finish")
	}

	cont, err := server.tasks.Continuation(body.LatestContinuation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cont.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("reconciliation = %q, want completed", cont.BlackboardReconciliationStatus)
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawOperatorFinish bool
	for _, event := range events {
		if event.Payload["kind"] == "runtime_activity" || event.Payload["phase"] == "runtime_activity" {
			t.Fatalf("runtime_activity audit event leaked: %#v", event)
		}
		if event.Kind == task.EventKindLifecycle && event.Payload["phase"] == "completed" && event.Payload["reason"] == "operator_finish" {
			sawOperatorFinish = true
		}
	}
	if !sawOperatorFinish {
		t.Fatal("expected lifecycle completed reason=operator_finish")
	}
	// Operator finish is the sole completed lifecycle claim (no harness duplicate).
	var completedPhases int
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle && event.Payload["phase"] == "completed" {
			completedPhases++
			if event.Payload["reason"] != "operator_finish" {
				t.Fatalf("unexpected completed lifecycle without operator_finish: %#v", event.Payload)
			}
		}
	}
	if completedPhases != 1 {
		t.Fatalf("completed lifecycle events = %d, want exactly 1 operator_finish", completedPhases)
	}
}

func TestFinishTaskRejectsWhenContinuationReconciliationFails(t *testing.T) {
	session, adapter := newFinishSessionPair("finish-recon-fail")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, mp := newFinishTaskFixture(t, factory)
	// Inject failing reconciler after server assembly (replaces blackboardv2).
	server.tasks.SetContinuationReconciler(failingFinishReconciler{err: errors.New("injected recon failure")})
	launchFinishTask(t, server, created)

	resp := postFinish(server, created.ProjectID, created.ID)
	if resp.Code == http.StatusOK {
		t.Fatalf("finish must not succeed when reconciliation fails, body %s", resp.Body.String())
	}
	if resp.Code != http.StatusConflict && resp.Code != http.StatusInternalServerError {
		t.Fatalf("finish recon failure status = %d body %s, want 409 or 500", resp.Code, resp.Body.String())
	}
	// Stable public stage only — no raw injected error in body.
	if !strings.Contains(resp.Body.String(), "finish failed at continuation_") {
		t.Fatalf("expected stable stage error, got %s", resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "injected recon failure") {
		t.Fatalf("raw error leaked in HTTP body: %s", resp.Body.String())
	}

	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status == task.StatusCompleted {
		t.Fatal("Task must not be completed when Continuation reconciliation fails")
	}
	if found.Status == task.StatusRunning || found.Status == task.StatusPaused {
		t.Fatalf("Task left durable active %q after post-runtime finish failure", found.Status)
	}
	if found.Status != task.StatusFailed {
		// Prefer failed; allow already-settled non-active terminals if domain diverged.
		t.Fatalf("Task status = %q, want failed after recon failure settle", found.Status)
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawFinishFailed bool
	for _, event := range events {
		if event.Payload["kind"] == "runtime_activity" || event.Payload["phase"] == "runtime_activity" {
			t.Fatalf("runtime_activity audit event leaked: %#v", event)
		}
		if event.Kind == task.EventKindLifecycle && event.Payload["phase"] == "completed" && event.Payload["reason"] == "operator_finish" {
			t.Fatal("operator_finish must not be recorded when finish is rejected")
		}
		if event.Kind == task.EventKindLifecycle && event.Payload["phase"] == "finish_failed" {
			sawFinishFailed = true
		}
	}
	if !sawFinishFailed {
		t.Fatal("expected finish_failed lifecycle diagnostic")
	}

	// Resume after recon-fail settle: restore reconciler, queue once, HTTP resume.
	if server.blackboardV2 != nil {
		server.tasks.SetContinuationReconciler(server.blackboardV2)
	} else {
		server.tasks.SetContinuationReconciler(nil)
	}
	// Ensure latest Continuation is reconcilable for replacement launch.
	latest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest: %v %#v", err, latest)
	}
	if latest.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		if _, err := server.tasks.MarkContinuationReconciliation(context.Background(), latest.ID, task.ReconciliationCompleted, "post-finish-abort", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}

	queueBody := `{"directive":"recover after recon fail","model_provider_id":` + quoteJSON(mp.ID) + `,"model":"gpt-test","reasoning_effort":"high"}`
	queueReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer/queue", bytes.NewReader([]byte(queueBody)))
	queueReq.Header.Set("Content-Type", "application/json")
	queueResp := httptest.NewRecorder()
	server.ServeHTTP(queueResp, queueReq)
	if queueResp.Code != http.StatusOK {
		t.Fatalf("queue status = %d body %s", queueResp.Code, queueResp.Body.String())
	}

	// Fresh session for resume launch.
	resumeSession, resumeAdapter := newFinishSessionPair("finish-recon-resume")
	factory.session, factory.adapter = resumeSession, resumeAdapter
	factory.newSession = func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPair(fmt.Sprintf("finish-recon-resume-%d", open))
	}
	opensBefore := factory.openCount()

	resumeReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/resume", bytes.NewReader([]byte(`{}`)))
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeResp := httptest.NewRecorder()
	server.ServeHTTP(resumeResp, resumeReq)
	if resumeResp.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body %s", resumeResp.Code, resumeResp.Body.String())
	}
	waitForHarnessActive(t, server, created.ID, true)
	if factory.openCount() != opensBefore+1 {
		t.Fatalf("opens before=%d after=%d", opensBefore, factory.openCount())
	}
	running, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.ID != created.ID || running.Status != task.StatusRunning {
		t.Fatalf("resume task = %#v", running)
	}
}

func TestFinishTaskRejectsBusyRuntime(t *testing.T) {
	bound, adapter := newFinishSessionPair("finish-busy")
	session := &forceBusySession{finishBoundSession: bound.(*finishBoundSession)}
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	// Force live/busy without holding the daemon control lock so Finish can
	// acquire the lock and apply the activity gate.
	session.busy = true
	activity := getTaskActivity(t, server, created.ProjectID, created.ID)
	if activity.RuntimeActivity.Liveness != "live" || activity.RuntimeActivity.TurnActivity != "busy" {
		t.Fatalf("setup want live busy, got %#v", activity.RuntimeActivity)
	}

	resp := postFinish(server, created.ProjectID, created.ID)
	if resp.Code != http.StatusConflict {
		t.Fatalf("finish busy status = %d body %s, want 409", resp.Code, resp.Body.String())
	}
	lower := strings.ToLower(resp.Body.String())
	if !strings.Contains(lower, "idle") && !strings.Contains(lower, "busy") {
		t.Fatalf("finish busy error should mention idle/busy: %s", resp.Body.String())
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusRunning {
		t.Fatalf("busy finish must leave task running, got %q", found.Status)
	}
	if _, ok := server.providerSessions.get(created.ID); !ok {
		t.Fatal("busy finish must not close the provider session")
	}

	// Stop remains interruption and leaves stopped/resumable.
	session.busy = false
	stopResp := postStop(server, created.ProjectID, created.ID)
	if stopResp.Code != http.StatusOK {
		t.Fatalf("stop status = %d body %s", stopResp.Code, stopResp.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	var stopped task.Task
	for time.Now().Before(deadline) {
		stopped, err = server.tasks.Get(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stopped.Status == task.StatusStopped {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stopped.Status != task.StatusStopped {
		t.Fatalf("after stop status = %q, want stopped", stopped.Status)
	}
	detailed, err := server.taskDetail(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !detailed.RuntimeControls.ResumeAvailable {
		t.Fatal("stopped task must be resumable")
	}
}

func TestFinishTaskControlLockConflictsWithConcurrentStop(t *testing.T) {
	session, adapter := newFinishSessionPair("finish-lock")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	if !server.acquireTaskControl(created.ID) {
		t.Fatal("expected to acquire control for setup")
	}
	finishResp := postFinish(server, created.ProjectID, created.ID)
	stopResp := postStop(server, created.ProjectID, created.ID)
	server.releaseTaskControl(created.ID)

	if finishResp.Code != http.StatusConflict {
		t.Fatalf("finish under lock status = %d body %s", finishResp.Code, finishResp.Body.String())
	}
	if stopResp.Code != http.StatusConflict {
		t.Fatalf("stop under lock status = %d body %s", stopResp.Code, stopResp.Body.String())
	}
	if !strings.Contains(finishResp.Body.String(), "task control operation already active") {
		t.Fatalf("finish conflict body = %s", finishResp.Body.String())
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusRunning {
		t.Fatalf("status = %q after rejected concurrent controls", found.Status)
	}
}

func TestFinishTaskIdempotentSecondFinishConflicts(t *testing.T) {
	session, adapter := newFinishSessionPair("finish-idem")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	first := postFinish(server, created.ProjectID, created.ID)
	if first.Code != http.StatusOK {
		t.Fatalf("first finish status = %d body %s", first.Code, first.Body.String())
	}
	second := postFinish(server, created.ProjectID, created.ID)
	if second.Code != http.StatusConflict {
		t.Fatalf("second finish status = %d body %s, want 409", second.Code, second.Body.String())
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusCompleted {
		t.Fatalf("status after second finish = %q", found.Status)
	}
}

func TestCompletedTaskMessageQueuesOnceAndResumesSameTask(t *testing.T) {
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPairWithCaps(fmt.Sprintf("resume-completed-%d", open), runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true,
		})
	}
	seedSession, seedAdapter := newSession(0)
	factory := &finishSessionFactory{
		session: seedSession, adapter: seedAdapter, newSession: newSession,
	}
	server, created, mp := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	finishResp := postFinish(server, created.ProjectID, created.ID)
	if finishResp.Code != http.StatusOK {
		t.Fatalf("finish status = %d body %s", finishResp.Code, finishResp.Body.String())
	}
	latest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest continuation: %v %#v", err, latest)
	}
	if _, err := server.tasks.UpdateContinuationRuntimeMetadata(latest.ID, "", "resume-completed", ""); err != nil {
		t.Fatal(err)
	}

	queueBody := `{
		"directive":"continue after finish",
		"model_provider_id":` + quoteJSON(mp.ID) + `,
		"model":"gpt-test",
		"reasoning_effort":"xhigh"
	}`
	queueReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer/queue", bytes.NewReader([]byte(queueBody)))
	queueReq.Header.Set("Content-Type", "application/json")
	queueResp := httptest.NewRecorder()
	server.ServeHTTP(queueResp, queueReq)
	if queueResp.Code != http.StatusOK {
		t.Fatalf("queue status = %d body %s", queueResp.Code, queueResp.Body.String())
	}

	opensBefore := factory.openCount()
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/resume", bytes.NewReader([]byte(`{}`)))
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeResp := httptest.NewRecorder()
	server.ServeHTTP(resumeResp, resumeReq)
	if resumeResp.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body %s", resumeResp.Code, resumeResp.Body.String())
	}
	waitForHarnessActive(t, server, created.ID, true)

	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusRunning {
		t.Fatalf("resumed status = %q, want running", found.Status)
	}
	if found.ID != created.ID {
		t.Fatal("resume replaced Task identity")
	}
	if factory.openCount() != opensBefore+1 {
		t.Fatalf("expected exactly one replacement Runtime open, before=%d after=%d", opensBefore, factory.openCount())
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var queued int
	for _, event := range events {
		if event.Kind == task.EventKindSteering && event.Payload["phase"] == "steering_requested" && event.Payload["directive"] == "continue after finish" {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("canonical queued messages = %d, want 1", queued)
	}

	var sawEffort bool
	for _, event := range events {
		if event.Payload["directive"] == "continue after finish" && event.Payload["reasoning_effort"] == "xhigh" {
			sawEffort = true
		}
	}
	if !sawEffort {
		t.Fatal("queued selection effort was dropped")
	}
}

func TestFailedTaskHTTPResumeQueuesOnceAndLaunchesFreshRuntime(t *testing.T) {
	// Real HTTP queue + resume + launch for failed Task without native session.
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPair(fmt.Sprintf("failed-resume-%d", open))
	}
	seed, seedAdapter := newSession(0)
	factory := &finishSessionFactory{session: seed, adapter: seedAdapter, newSession: newSession}
	server, created, mp := newFinishTaskFixture(t, factory)

	// Durable failed Task + Continuation (no native session → fresh fallback).
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusFailed); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.MarkContinuationReconciliation(context.Background(), continuation.ID, task.ReconciliationCompleted, "test-reconcile", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	queueBody := `{
		"directive":"recover after failure",
		"model_provider_id":` + quoteJSON(mp.ID) + `,
		"model":"gpt-test",
		"reasoning_effort":"max"
	}`
	queueReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer/queue", bytes.NewReader([]byte(queueBody)))
	queueReq.Header.Set("Content-Type", "application/json")
	queueResp := httptest.NewRecorder()
	server.ServeHTTP(queueResp, queueReq)
	if queueResp.Code != http.StatusOK {
		t.Fatalf("queue status = %d body %s", queueResp.Code, queueResp.Body.String())
	}

	// Capture prepare plan shape before launch (fresh, no native id).
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, goal, plan, err := server.prepareResumeContinuation(found, "")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ID != created.ID {
		t.Fatal("resume replaced Task identity")
	}
	if !strings.Contains(goal, "recover after failure") && !strings.Contains(goal, created.Goal) {
		// Resume goal builds from Task goal + steering; steering is consumed at launch.
		if strings.TrimSpace(goal) == "" {
			t.Fatal("fresh fallback dropped resume goal/context")
		}
	}
	if plan.NativeResumeSessionID != "" {
		t.Fatalf("missing native metadata must not invent resume id %q", plan.NativeResumeSessionID)
	}

	opensBefore := factory.openCount()
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/resume", bytes.NewReader([]byte(`{}`)))
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeResp := httptest.NewRecorder()
	server.ServeHTTP(resumeResp, resumeReq)
	if resumeResp.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body %s", resumeResp.Code, resumeResp.Body.String())
	}
	waitForHarnessActive(t, server, created.ID, true)
	if factory.openCount() != opensBefore+1 {
		t.Fatalf("expected single Runtime open, before=%d after=%d", opensBefore, factory.openCount())
	}
	running, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.ID != created.ID || running.Status != task.StatusRunning {
		t.Fatalf("running after resume = %#v", running)
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var queued int
	var sawEffort bool
	for _, event := range events {
		if event.Kind == task.EventKindSteering && event.Payload["phase"] == "steering_requested" && event.Payload["directive"] == "recover after failure" {
			queued++
			if event.Payload["reasoning_effort"] == "max" {
				sawEffort = true
			}
		}
	}
	if queued != 1 {
		t.Fatalf("canonical queued messages = %d, want 1", queued)
	}
	if !sawEffort {
		t.Fatal("queued selection effort not retained")
	}
}

func TestStopSettlesRunningTaskWhenHarnessAndSessionAlreadyGone(t *testing.T) {
	server, created, _ := newFinishTaskFixture(t, nil)
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	// No harness activity, no bound session — Stop must still write stopped.
	resp := postStop(server, created.ProjectID, created.ID)
	if resp.Code != http.StatusOK {
		t.Fatalf("stop status = %d body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "stopped" {
		t.Fatalf("stop body status = %q, want stopped", body.Status)
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusStopped {
		t.Fatalf("durable status = %q, want stopped", found.Status)
	}
}

func TestOrphanOwnershipResolvedBeforeReplacementLaunch(t *testing.T) {
	session, adapter := newFinishSessionPair("orphan-cleanup")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)
	if !server.harness.IsActive(created.ID) {
		t.Fatal("setup: harness should be active")
	}
	if _, ok := server.providerSessions.get(created.ID); !ok {
		t.Fatal("setup: session should be bound")
	}

	// ensureRuntimeAbsentBeforeLaunch must stop harness and unbind session.
	if err := server.ensureRuntimeAbsentBeforeLaunch(created.ID); err != nil {
		t.Fatalf("orphan resolve: %v", err)
	}
	if server.harness.IsActive(created.ID) {
		t.Fatal("harness still active after ensureRuntimeAbsentBeforeLaunch")
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("provider session still bound after ensureRuntimeAbsentBeforeLaunch")
	}
	// Second call is safe (proven absence).
	if err := server.ensureRuntimeAbsentBeforeLaunch(created.ID); err != nil {
		t.Fatalf("second orphan resolve: %v", err)
	}

	// Replacement launch succeeds without dual ownership.
	replacement, repAdapter := newFinishSessionPair("orphan-replacement")
	factory.session, factory.adapter = replacement, repAdapter
	// Mark prior cont terminal so CreateContinuation can proceed.
	if cont, err := server.tasks.LatestContinuation(created.ID); err == nil && cont != nil {
		if cont.Status == task.StatusRunning || cont.Status == task.StatusPending {
			_, _ = server.tasks.UpdateContinuationStatus(cont.ID, task.StatusStopped)
		}
		if cont.BlackboardReconciliationStatus != task.ReconciliationCompleted {
			_, _ = server.tasks.MarkContinuationReconciliation(context.Background(), cont.ID, task.ReconciliationCompleted, "orphan", time.Now().UTC())
		}
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusStopped); err != nil {
		t.Fatal(err)
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, goal, plan, err := server.prepareResumeContinuation(found, "")
	if err != nil {
		t.Fatal(err)
	}
	opensBefore := factory.openCount()
	if err := server.launchTaskInBackground(found, plan, goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
	if factory.openCount() != opensBefore+1 {
		t.Fatalf("replacement opens before=%d after=%d", opensBefore, factory.openCount())
	}
}

func TestBlackboardFinishDoesNotCompleteTask(t *testing.T) {
	session, adapter := newFinishSessionPair("bb-finish-guard")
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	cont, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || cont == nil {
		t.Fatalf("active continuation: %v %#v", err, cont)
	}
	if server.blackboardV2 != nil {
		// Blackboard Finish may succeed or fail on open work; either way Task must stay active.
		_, _ = server.blackboardV2.FinishContinuation(context.Background(), created.ProjectID, cont.ID, blackboardv2.FinishContinuationRequest{
			IdempotencyKey: "bb-only-finish",
		})
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status == task.StatusCompleted {
		t.Fatal("Blackboard Finish must not complete the Task")
	}
	if _, ok := server.providerSessions.get(created.ID); !ok {
		t.Fatal("Blackboard Finish must not close Runtime ownership by itself")
	}
}

// delayedFinishCloseSession blocks inside Close so Finish cannot complete
// reconciliation before provider resources shut down.
type delayedFinishCloseSession struct {
	*runtime.FakeProviderSession
	closed       chan struct{}
	closeEntered chan struct{}
	allowClose   chan struct{}
	once         sync.Once
}

func (s *delayedFinishCloseSession) Close(ctx context.Context) error {
	select {
	case <-s.closeEntered:
	default:
		close(s.closeEntered)
	}
	select {
	case <-s.allowClose:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := s.FakeProviderSession.Close(ctx)
	s.once.Do(func() { close(s.closed) })
	return err
}

func TestFinishTaskShutdownHappensBeforeReconciliation(t *testing.T) {
	inner := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-order",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &delayedFinishCloseSession{
		FakeProviderSession: inner,
		closed:              make(chan struct{}),
		closeEntered:        make(chan struct{}),
		allowClose:          make(chan struct{}),
	}
	adapter := runtime.NewProviderSessionRunAdapter(session, session.closed)
	factory := &finishSessionFactory{session: session, adapter: adapter}
	server, created, _ := newFinishTaskFixture(t, factory)

	counter := &countingFinishReconciler{inner: nil}
	// Preserve production recon when present; still count calls for order.
	if server.blackboardV2 != nil {
		counter.inner = server.blackboardV2
	}
	server.tasks.SetContinuationReconciler(counter)
	launchFinishTask(t, server, created)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postFinish(server, created.ProjectID, created.ID)
	}()

	select {
	case <-session.closeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Finish did not enter provider session Close")
	}
	// While Close is blocked, reconciliation and Task completed must not run.
	if counter.callCount() != 0 {
		t.Fatalf("reconciliation ran before session Close completed: calls=%d", counter.callCount())
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status == task.StatusCompleted {
		t.Fatal("Task completed before provider resources closed")
	}

	close(session.allowClose)
	var resp *httptest.ResponseRecorder
	select {
	case resp = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Finish did not return after Close released")
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("finish status = %d body %s", resp.Code, resp.Body.String())
	}
	if counter.callCount() < 1 {
		t.Fatal("expected reconciliation after resource close")
	}
	found, err = server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusCompleted {
		t.Fatalf("status after ordered finish = %q", found.Status)
	}
}

func TestConcurrentResumeSecondConflictsWithoutStoppingFirst(t *testing.T) {
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPairWithCaps(fmt.Sprintf("resume-race-%d", open), runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true,
		})
	}
	seedSession, seedAdapter := newSession(0)
	factory := &finishSessionFactory{session: seedSession, adapter: seedAdapter, newSession: newSession}
	server, created, _ := newFinishTaskFixture(t, factory)
	launchFinishTask(t, server, created)

	// Finish so Task is terminal and resumable.
	finishResp := postFinish(server, created.ProjectID, created.ID)
	if finishResp.Code != http.StatusOK {
		t.Fatalf("finish status = %d body %s", finishResp.Code, finishResp.Body.String())
	}
	beforeLatest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || beforeLatest == nil {
		t.Fatalf("latest continuation before resume: %v %#v", err, beforeLatest)
	}
	beforeNumber := beforeLatest.Number
	opensBefore := factory.openCount()

	const n = 8
	type result struct {
		code int
		body string
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/resume", bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			server.ServeHTTP(resp, req)
			results <- result{code: resp.Code, body: resp.Body.String()}
		}()
	}
	wg.Wait()
	close(results)

	var accepted, conflicted int
	for r := range results {
		switch r.code {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			conflicted++
		default:
			t.Fatalf("unexpected resume status %d body %s", r.code, r.body)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted resumes = %d, want 1 (rest conflict)", accepted)
	}
	if conflicted != n-1 {
		t.Fatalf("conflicted resumes = %d, want %d", conflicted, n-1)
	}
	// Exactly one replacement Runtime — second must not stop/replace the first.
	if factory.openCount() != opensBefore+1 {
		t.Fatalf("opens before=%d after=%d, want exactly one new Runtime", opensBefore, factory.openCount())
	}
	waitForHarnessActive(t, server, created.ID, true)
	if _, ok := server.providerSessions.get(created.ID); !ok {
		t.Fatal("first resume Runtime ownership lost after concurrent resumes")
	}
	latest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest continuation: %v %#v", err, latest)
	}
	// At most one new Continuation beyond the finished one.
	if latest.Number > beforeNumber+1 {
		t.Fatalf("continuation number jumped from %d to %d after concurrent resumes", beforeNumber, latest.Number)
	}
}

func postResume(server *Server, projectID, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	return resp
}

// failingCloseSession fails Close once (Finish abort), then allows cleanup/Stop.
type failingCloseSession struct {
	*runtime.FakeProviderSession
	closed     chan struct{}
	failClose  atomic.Bool
	closeCount atomic.Int32
}

func (s *failingCloseSession) Close(ctx context.Context) error {
	s.closeCount.Add(1)
	if s.failClose.Load() {
		return errors.New("injected provider close failure")
	}
	err := s.FakeProviderSession.Close(ctx)
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
	*runtime.FakeProviderSession
}

func (s *silentCloseSession) Close(ctx context.Context) error {
	return s.FakeProviderSession.Close(ctx)
}

func TestFinishTaskSoleOwnerAfterCancelDrivenHarnessExit(t *testing.T) {
	neverClosed := make(chan struct{})
	fake := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-silent-close",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &silentCloseSession{FakeProviderSession: fake}
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
	inner := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-close-fail",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &failingCloseSession{
		FakeProviderSession: inner,
		closed:              make(chan struct{}),
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

	// Intent cleared: later Stop settles stopped/failed, not operator completed.
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
	*runtime.FakeProviderSession
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
		// After first deadline, allow process exit without permanent hang.
		err := s.FakeProviderSession.Close(ctx)
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
		// Safety for Background cleanup paths.
		s.timedOut.Store(true)
		return errors.New("hang close safety timeout")
	}
}

func TestFinishTaskClearsIntentWhenProviderCloseTimesOut(t *testing.T) {
	inner := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-close-timeout",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &hangCloseSession{
		FakeProviderSession: inner,
		closed:              make(chan struct{}),
		started:             make(chan struct{}),
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
