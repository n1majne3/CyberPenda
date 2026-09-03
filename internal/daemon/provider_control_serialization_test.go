package daemon

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

type cancelableNativeSteerSession struct {
	*runtime.FakeProviderSession
	started  chan struct{}
	canceled chan struct{}
}

type cancelablePermissionSession struct {
	*runtime.FakeProviderSession
	started  chan struct{}
	canceled chan struct{}
}

func (session *cancelablePermissionSession) Capabilities() runtimeplugin.Capabilities {
	capabilities := session.FakeProviderSession.Capabilities()
	capabilities.PermissionResponse = true
	return capabilities
}

func (session *cancelablePermissionSession) RespondPermission(ctx context.Context, _ runtime.ProviderSessionRequest, _ runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	close(session.started)
	<-ctx.Done()
	close(session.canceled)
	return runtime.ProviderSessionResult{}, ctx.Err()
}

func (session *cancelableNativeSteerSession) Capabilities() runtimeplugin.Capabilities {
	capabilities := session.FakeProviderSession.Capabilities()
	capabilities.InTurnSteer = true
	return capabilities
}

func (session *cancelableNativeSteerSession) SteerInTurn(ctx context.Context, _ runtime.ProviderSessionRequest, _ runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	close(session.started)
	<-ctx.Done()
	close(session.canceled)
	return runtime.ProviderSessionResult{}, ctx.Err()
}

func TestStopCancelsTaskScopedNativeSteerWithoutAutoFinish(t *testing.T) {
	var native *cancelableNativeSteerSession
	server, projectID, profileID, session := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			native = &cancelableNativeSteerSession{
				FakeProviderSession: fake, started: make(chan struct{}), canceled: make(chan struct{}),
			}
			return native
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)

	steer := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"cancel-me","message":"continue"}`))
	steer.Header.Set("Content-Type", "application/json")
	steerResponse := httptest.NewRecorder()
	server.ServeHTTP(steerResponse, steer)
	if steerResponse.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", steerResponse.Code, steerResponse.Body.String())
	}
	select {
	case <-native.started:
	case <-time.After(time.Second):
		t.Fatal("native steer did not start")
	}

	stop := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil)
	stopResponse := httptest.NewRecorder()
	server.ServeHTTP(stopResponse, stop)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("Stop during native steer status=%d body=%s", stopResponse.Code, stopResponse.Body.String())
	}
	select {
	case <-native.canceled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel Task-scoped native steer context")
	}
	stopped := getConclusionTask(t, server, projectID, created.ID)
	if stopped.Status != task.StatusStopped {
		t.Fatalf("Task status=%q, want stopped (never auto-completed)", stopped.Status)
	}
}

func TestStopCancelsTaskScopedPermissionResponse(t *testing.T) {
	var permission *cancelablePermissionSession
	server, projectID, profileID, _ := newAssistedConclusionFixtureAtWithDecorator(
		t, t.TempDir(), true, true,
		func(fake *runtime.FakeProviderSession) runtime.ProviderSession {
			permission = &cancelablePermissionSession{
				FakeProviderSession: fake, started: make(chan struct{}), canceled: make(chan struct{}),
			}
			return permission
		},
	)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	if _, err := server.tasks.AppendEvent(created.ID, task.EventKindLifecycle, task.EventPayload{
		"phase": "provider_permission_requested", "request_id": "provider-permission-request",
		"permission_request_id": "perm-cancel", "session_id": permission.SessionID(),
	}); err != nil {
		t.Fatal(err)
	}

	respond := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/permissions/perm-cancel/respond", bytes.NewBufferString(`{"request_id":"permission-cancel","decision":"allow"}`))
	respond.Header.Set("Content-Type", "application/json")
	respondResponse := httptest.NewRecorder()
	server.ServeHTTP(respondResponse, respond)
	if respondResponse.Code != http.StatusAccepted {
		t.Fatalf("permission response status=%d body=%s", respondResponse.Code, respondResponse.Body.String())
	}
	select {
	case <-permission.started:
	case <-time.After(time.Second):
		t.Fatal("permission response did not start")
	}

	stop := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+created.ID+"/stop", nil)
	stopResponse := httptest.NewRecorder()
	server.ServeHTTP(stopResponse, stop)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("Stop during permission response status=%d body=%s", stopResponse.Code, stopResponse.Body.String())
	}
	select {
	case <-permission.canceled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel Task-scoped permission context")
	}
}

func TestReleasedProviderControlDoesNotMakeUnrelatedTaskControlPreemptible(t *testing.T) {
	server, _, _, _ := newAssistedConclusionFixture(t, true)
	const taskID = "provider-control-lifetime"
	if !server.acquireProviderTaskControl(taskID) {
		t.Fatal("failed to acquire provider control")
	}
	providerContext := server.providerTaskContext(taskID)
	server.releaseProviderTaskControl(taskID)
	if providerContext.Err() != nil {
		t.Fatal("completed provider dispatch canceled the context needed by asynchronous terminal callbacks")
	}
	if !server.acquireTaskControl(taskID) {
		t.Fatal("failed to acquire ordinary Task control")
	}
	defer server.releaseTaskControl(taskID)
	if server.cancelProviderTaskControls(taskID) {
		t.Fatal("ordinary Task control was misclassified as a preemptible provider control")
	}
}

func TestStopCancellationIncludesProviderDispatchQueuedBeforeOwnership(t *testing.T) {
	server, _, _, _ := newAssistedConclusionFixture(t, true)
	const taskID = "queued-provider-control"
	if !server.acquireTaskControl(taskID) {
		t.Fatal("failed to hold Task control")
	}
	run := make(chan struct{}, 1)
	if !server.enqueueProviderTaskControl(taskID, func(context.Context) { run <- struct{}{} }) {
		t.Fatal("failed to enqueue provider control")
	}
	if !server.cancelProviderTaskControls(taskID) {
		t.Fatal("queued provider control was not recognized as preemptible")
	}
	server.releaseTaskControl(taskID)
	server.providerControlWG.Wait()
	select {
	case <-run:
		t.Fatal("canceled queued provider control acquired ownership and ran")
	default:
	}
}
