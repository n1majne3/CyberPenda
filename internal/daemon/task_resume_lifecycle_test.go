package daemon

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

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
	waitForTaskRunning(t, server, created.ID)

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
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPair(fmt.Sprintf("failed-resume-%d", open))
	}
	seed, seedAdapter := newSession(0)
	factory := &finishSessionFactory{session: seed, adapter: seedAdapter, newSession: newSession}
	server, created, mp := newFinishTaskFixture(t, factory)

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
	waitForTaskRunning(t, server, created.ID)
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

func TestInterruptedTaskHTTPResumeCarriesNativeSessionIntoReplacementContinuation(t *testing.T) {
	newSession := func(open int) (runtime.ProviderSession, runtime.Adapter) {
		return newFinishSessionPair(fmt.Sprintf("native-resume-%d", open))
	}
	seed, seedAdapter := newSession(0)
	factory := &finishSessionFactory{session: seed, adapter: seedAdapter, newSession: newSession}
	server, created, _ := newFinishTaskFixture(t, factory)

	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusInterrupted); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := server.tasks.CreateContinuation(created.ID, profile.ID, string(profile.Provider), task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	previous, err = server.tasks.UpdateContinuationRuntimeMetadata(previous.ID, "stale-container", "native-session-prior", "/sessions/prior.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(previous.ID, task.StatusInterrupted); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.MarkContinuationReconciliation(context.Background(), previous.ID, task.ReconciliationCompleted, "test-reconcile", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/resume", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body %s", response.Code, response.Body.String())
	}
	waitForTaskRunning(t, server, created.ID)

	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider session opens = %d, want 1", len(requests))
	}
	replacement := requests[0].Continuation
	if replacement.ID == previous.ID {
		t.Fatalf("resume reused prior Continuation pin: %#v", replacement)
	}
	if replacement.NativeSessionID != "native-session-prior" || replacement.NativeSessionPath != "/sessions/prior.jsonl" {
		t.Fatalf("replacement Continuation native session = (%q, %q), want prior durable identity", replacement.NativeSessionID, replacement.NativeSessionPath)
	}
	if replacement.ContainerID != "" {
		t.Fatalf("replacement Continuation retained stale container %q", replacement.ContainerID)
	}
	server.harness.StopAndWait(created.ID, 2*time.Second)
}
