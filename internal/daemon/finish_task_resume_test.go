package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

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
	waitForTaskRunning(t, server, created.ID)
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
