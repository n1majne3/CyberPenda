package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

// forceBusySession reports busy turn activity without holding the daemon
// control lock (native steer holds the lock for the whole operation).
type forceBusySession struct {
	runtime.ProviderSession
	busy bool
}

func (s *forceBusySession) ControlBusy() bool {
	if s.busy {
		return true
	}
	if reporter, ok := s.ProviderSession.(interface{ ControlBusy() bool }); ok {
		return reporter.ControlBusy()
	}
	return false
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
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
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
}

func TestFinishTaskRejectsBusyRuntime(t *testing.T) {
	inner := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-busy",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	session := &forceBusySession{ProviderSession: inner}
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
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
	_ = closed
}

func TestFinishTaskControlLockConflictsWithConcurrentStop(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-lock",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
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
	_ = closed
}

func TestFinishTaskIdempotentSecondFinishConflicts(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "finish-idem",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
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
	_ = closed
}

func TestCompletedTaskMessageQueuesOnceAndResumesSameTask(t *testing.T) {
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
			SessionID: fmt.Sprintf("resume-completed-%d", open),
			Capabilities: runtimeplugin.Capabilities{
				PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true,
			},
		})
		closed := make(chan struct{})
		adapter := runtime.NewProviderSessionRunAdapter(session, closed)
		return session, adapter
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

func TestFailedTaskResumeFallsBackWithoutDroppingMessage(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "codex")
	if err := writeExecutable(binary, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test", BinaryPath: binary, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusFailed); err != nil {
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

	queueReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer/queue",
		strings.NewReader(`{"directive":"recover after failure","reasoning_effort":"max"}`))
	queueReq.Header.Set("Content-Type", "application/json")
	queueResp := httptest.NewRecorder()
	server.ServeHTTP(queueResp, queueReq)
	if queueResp.Code != http.StatusOK {
		t.Fatalf("queue status = %d body %s", queueResp.Code, queueResp.Body.String())
	}

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
	if strings.TrimSpace(goal) == "" {
		t.Fatal("fresh fallback dropped resume goal/context")
	}
	if plan.NativeResumeSessionID != "" {
		t.Fatalf("missing native metadata must not invent resume id %q", plan.NativeResumeSessionID)
	}
	if err := server.ensureRuntimeAbsentBeforeLaunch(created.ID); err != nil {
		t.Fatalf("orphan resolve: %v", err)
	}
}

func TestBlackboardFinishDoesNotCompleteTask(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "bb-finish-guard",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
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
	_ = closed
}
