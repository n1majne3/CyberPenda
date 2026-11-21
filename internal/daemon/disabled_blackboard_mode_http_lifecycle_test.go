package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/finishreadiness"
	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

func TestDisabledTaskHTTPSteerCreatesOrdinaryContinuationWithoutBlackboard(t *testing.T) {
	server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest",
		"goal":"inspect ordinary continuation behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Task status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	// The accepted create response precedes durable Runtime startup; native
	// steer requires the running state.
	waitForDisabledTaskPublicStatus(t, server, projectID, created.ID, task.StatusRunning)

	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"disabled-task-steer",
		"message":"focus on the alternate path"
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer disabled Task status = %d body %s", steerResponse.Code, steerResponse.Body.String())
	}

	detail := waitForDisabledTaskPublicContinuation(t, server, projectID, created.ID, 2)
	assertDisabledTaskPublicContinuation(t, detail, 2, task.StatusRunning)
	factory.mu.Lock()
	provider, _ := factory.session.(*fakeprovider.Session)
	factory.mu.Unlock()
	if provider == nil {
		t.Fatal("disabled Task provider session is unavailable")
	}
	requests := provider.LastRequests()
	if len(requests) == 0 || requests[len(requests)-1].Message != "focus on the alternate path" ||
		strings.Contains(requests[len(requests)-1].Message, disabledBlackboardStateFileReminder) {
		t.Fatalf("ordinary Task steer request = %#v", requests)
	}
}

func TestDisabledSessionHTTPSteerCreatesOrdinaryContinuationWithoutBlackboard(t *testing.T) {
	server, _, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{
		"input":"inspect Session continuation behavior",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"host_activated":true,
		"run_controls":{"blackboard_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Session status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	initial := waitForDisabledSessionPublicContinuation(t, server, created.ID, 1)
	assertDisabledSessionPublicContinuation(t, initial, 1, session.RuntimeStatusRunning)

	steer := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"disabled-session-steer",
		"message":"focus on the alternate Session path"
	}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer disabled Session status = %d body %s", steerResponse.Code, steerResponse.Body.String())
	}

	detail := waitForDisabledSessionPublicContinuation(t, server, created.ID, 2)
	assertDisabledSessionPublicContinuation(t, detail, 2, session.RuntimeStatusRunning)
	factory.mu.Lock()
	provider, _ := factory.session.(*fakeprovider.Session)
	factory.mu.Unlock()
	if provider == nil {
		t.Fatal("disabled Session provider session is unavailable")
	}
	requests := provider.LastRequests()
	if len(requests) == 0 || requests[len(requests)-1].Message != "focus on the alternate Session path" ||
		strings.Contains(requests[len(requests)-1].Message, disabledBlackboardStateFileReminder) {
		t.Fatalf("ordinary Session steer request = %#v", requests)
	}
}

func TestDisabledTaskHTTPFinishIgnoresOnlyBlackboardReconciliationDebt(t *testing.T) {
	server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"type":"pentest",
		"goal":"finish disabled work",
		"runtime_profile_id":"`+profileID+`",
		"runner":"host",
		"run_controls":{"host_activated":true,"blackboard_mode":"disabled"}
	}`))
	create.Header.Set("Content-Type", "application/json")
	createdResponse := httptest.NewRecorder()
	server.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled Task status = %d body %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	busyFinish := httptest.NewRecorder()
	server.ServeHTTP(busyFinish, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish", nil))
	if busyFinish.Code != http.StatusConflict || !strings.Contains(busyFinish.Body.String(), "busy") {
		t.Fatalf("busy Disabled Finish status = %d body %s", busyFinish.Code, busyFinish.Body.String())
	}

	factory.mu.Lock()
	provider, _ := factory.session.(*fakeprovider.Session)
	factory.mu.Unlock()
	if provider == nil {
		t.Fatal("disabled Task provider session is unavailable")
	}
	if err := provider.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: "disabled-initial-turn",
		ProviderTurnID: "disabled-owner-initial-turn", Status: "completed",
	}); err != nil {
		t.Fatalf("settle disabled initial Turn: %v", err)
	}
	detail := waitForDisabledTaskRuntimeIdleHTTP(t, server, projectID, created.ID)
	if detail.LatestContinuation == nil {
		t.Fatalf("Disabled Task public detail has no Continuation: %#v", detail)
	}
	seedLegacyDisabledFinishDebtFixture(t, server.db, created.ID, detail.LatestContinuation.ID)
	server.tasks.SetContinuationReconciler(failingFinishReconciler{err: errors.New("Disabled must not call Blackboard reconciliation")})

	readinessResponse := httptest.NewRecorder()
	server.ServeHTTP(readinessResponse, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish-readiness", nil))
	if readinessResponse.Code != http.StatusOK {
		t.Fatalf("Disabled Finish Readiness status = %d body %s", readinessResponse.Code, readinessResponse.Body.String())
	}
	var readiness finishreadiness.Readiness
	if err := json.NewDecoder(readinessResponse.Body).Decode(&readiness); err != nil {
		t.Fatal(err)
	}
	if !readiness.ReadyToFinish || len(readiness.Blockers) != 0 {
		t.Fatalf("Disabled Finish Readiness retained Blackboard blocker: %#v", readiness)
	}

	finishedResponse := httptest.NewRecorder()
	server.ServeHTTP(finishedResponse, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/finish", nil))
	if finishedResponse.Code != http.StatusOK {
		t.Fatalf("finish Disabled Task status = %d body %s", finishedResponse.Code, finishedResponse.Body.String())
	}
	var finished task.Task
	if err := json.NewDecoder(finishedResponse.Body).Decode(&finished); err != nil {
		t.Fatal(err)
	}
	if finished.Status != task.StatusCompleted || finished.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		t.Fatalf("finished Disabled Task = %#v", finished)
	}
	if !provider.SessionClosed() {
		t.Fatal("Disabled Finish did not close the ProviderSession")
	}
	waitForDisabledOwnerTimeline(t, server, "/api/projects/"+projectID+"/tasks/"+created.ID+"/timeline", "Lifecycle: completed")
}

func TestDisabledTaskHTTPStopIgnoresTerminalBlackboardReconcilerFailure(t *testing.T) {
	server, projectID, profileID, factory := newDisabledBlackboardHTTPFixture(t)
	created := createDisabledTaskHTTP(t, server, projectID, profileID, "stop disabled work")
	// The accepted create response precedes durable Runtime startup; Stop must
	// observe the running state so its cancel reaches the registered Harness
	// run instead of settling only durable state.
	waitForDisabledTaskPublicStatus(t, server, projectID, created.ID, task.StatusRunning)
	release := make(chan struct{})
	server.runtimeStopTimeout = 100 * time.Millisecond
	server.tasks.SetContinuationReconciler(blockingDisabledTerminalReconciler{release: release})

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil))
	close(release)
	if response.Code != http.StatusOK {
		t.Fatalf("stop Disabled Task status = %d body %s", response.Code, response.Body.String())
	}
	var stopped task.Task
	if err := json.NewDecoder(response.Body).Decode(&stopped); err != nil {
		t.Fatal(err)
	}
	assertDisabledTaskPublicContinuation(t, stopped, 1, task.StatusStopped)

	provider := disabledFakeProvider(t, factory)
	if !provider.SessionClosed() {
		t.Fatal("Disabled Stop did not close the ProviderSession")
	}
	waitForDisabledOwnerTimeline(t, server, "/api/projects/"+projectID+"/tasks/"+created.ID+"/timeline", "Lifecycle: stopped")
	assertDisabledOwnerTimelineOmitsContent(t, server, "/api/projects/"+projectID+"/tasks/"+created.ID+"/timeline", "Lifecycle: failed")
}

type blockingDisabledTerminalReconciler struct {
	release <-chan struct{}
}

func (reconciler blockingDisabledTerminalReconciler) ReconcileTerminalContinuation(context.Context, string, string) error {
	<-reconciler.release
	return nil
}

func TestDisabledTaskHTTPResumeAfterDaemonRecoveryIgnoresLegacyBlackboardReconciliationFailure(t *testing.T) {
	root := t.TempDir()
	initialFactory := newDisabledBlackboardProviderFactory("disabled-restart-initial", "disabled-restart-turn")
	server := openDisabledBlackboardHTTPServer(t, root, initialFactory)
	projectID, profileID := seedDisabledBlackboardHTTPProject(t, server)
	created := createDisabledTaskHTTP(t, server, projectID, profileID, "recover disabled work")
	if created.LatestContinuation == nil {
		t.Fatalf("created Disabled Task has no public Continuation: %#v", created)
	}
	seedLegacyDisabledTerminalReconciliationFailureFixture(t, server.db, projectID, created.LatestContinuation.ID)
	if err := server.Close(); err != nil {
		t.Fatalf("close initial daemon: %v", err)
	}

	recoveryFactory := newDisabledBlackboardProviderFactory("disabled-restart-recovery", "")
	restarted := openDisabledBlackboardHTTPServer(t, root, recoveryFactory)
	t.Cleanup(func() { _ = restarted.Close() })
	interrupted := waitForDisabledTaskPublicStatus(t, restarted, projectID, created.ID, task.StatusInterrupted)
	if interrupted.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		t.Fatalf("recovered Task lost Disabled mode: %#v", interrupted)
	}

	resume := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/resume", bytes.NewBufferString(`{}`))
	resume.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	restarted.ServeHTTP(response, resume)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume recovered Disabled Task status = %d body %s", response.Code, response.Body.String())
	}
	var resumed task.Task
	if err := json.NewDecoder(response.Body).Decode(&resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		t.Fatalf("resume response lost Disabled mode: %#v", resumed)
	}
	resumed = waitForDisabledTaskPublicContinuation(t, restarted, projectID, created.ID, 2)
	assertDisabledTaskPublicContinuation(t, resumed, 2, task.StatusRunning)
	requests := recoveryFactory.Requests()
	if len(requests) != 1 {
		t.Fatalf("recovery Runtime launches = %d, want 1", len(requests))
	}
	if strings.Count(requests[0].LaunchGoal, disabledBlackboardStateFileReminder) != 1 {
		t.Fatalf("recovery launch reminder count = %d goal = %q", strings.Count(requests[0].LaunchGoal, disabledBlackboardStateFileReminder), requests[0].LaunchGoal)
	}
	assertNoBlackboardLaunchMaterial(t, requests[0])
}
