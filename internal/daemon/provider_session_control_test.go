package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type failingContinuationBindSession struct {
	runtime.ProviderSession
}

func (failingContinuationBindSession) BindContinuation(string) error {
	return errors.New("continuation bind rejected")
}

func TestNativeSteerRecordsCanonicalConversationAndOrderedProviderEvents(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-1",
		ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"req-1","message":"focus on admin"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status = %d, body=%s", response.Code, response.Body.String())
	}

	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Kind == task.EventKindSteering && event.Payload["outcome"] == "started" {
				return true
			}
		}
		return false
	})

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var conversation int
	var providerEvents []task.Event
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "req-1" {
			conversation++
			if event.Payload["role"] != "user" || event.Payload["text"] != "focus on admin" {
				t.Fatalf("unexpected canonical conversation event: %#v", event.Payload)
			}
		}
		if event.Kind == task.EventKindSteering && event.Payload["request_id"] == "req-1" && event.Payload["session_id"] == "session-1" {
			providerEvents = append(providerEvents, event)
		}
	}
	if conversation != 1 {
		t.Fatalf("conversation count = %d, want 1; events=%#v", conversation, events)
	}
	if len(providerEvents) < 4 {
		t.Fatalf("provider events = %#v, want request/ack/settled/replacement", providerEvents)
	}
	if providerEvents[0].Payload["outcome"] != "requested" || providerEvents[1].Payload["outcome"] != "acknowledged" || providerEvents[2].Payload["outcome"] != "settled" || providerEvents[3].Payload["outcome"] != "started" {
		t.Fatalf("provider event order = %#v", providerEvents)
	}
	if providerEvents[0].Payload["mode"] != string(runtime.ProviderSessionModeInterruptThenReplace) {
		t.Fatalf("provider mode = %#v", providerEvents[0].Payload["mode"])
	}
	oldAfter, err := server.tasks.Continuation(continuation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldAfter.Status != task.StatusCompleted {
		t.Fatalf("old Continuation status = %q, want completed", oldAfter.Status)
	}
	activeAfter, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || activeAfter == nil {
		t.Fatalf("replacement active Continuation = %#v, err=%v", activeAfter, err)
	}
	if activeAfter.ID == continuation.ID || activeAfter.Status != task.StatusRunning {
		t.Fatalf("replacement Continuation = %#v", activeAfter)
	}
	for _, event := range providerEvents {
		switch event.Payload["outcome"] {
		case "settled":
			if event.ContinuationID != continuation.ID {
				t.Fatalf("settled event Continuation = %q, want old %q", event.ContinuationID, continuation.ID)
			}
		case "started":
			if event.ContinuationID != activeAfter.ID {
				t.Fatalf("replacement started event Continuation = %q, want %q", event.ContinuationID, activeAfter.ID)
			}
		}
	}

	retry := httptest.NewRecorder()
	retryRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"req-1","message":"focus on admin"}`))
	server.ServeHTTP(retry, retryRequest)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, body=%s", retry.Code, retry.Body.String())
	}
	latest, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	conversation = 0
	for _, event := range latest {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "req-1" {
			conversation++
		}
	}
	if conversation != 1 {
		t.Fatalf("retry created %d canonical messages, want 1", conversation)
	}
}

func waitForTaskEvent(t *testing.T, server *Server, taskID string, predicate func([]task.Event) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := server.tasks.Events(taskID)
		if err == nil && predicate(events) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, _ := server.tasks.Events(taskID)
	t.Fatalf("timed out waiting for task event; events=%#v", events)
}

func TestNativeSteerRejectsUnsupportedSessionWithoutConversation(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{SessionID: "session-2", Capabilities: runtimeplugin.Capabilities{PersistentSession: true}})); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"req-unsupported","message":"focus"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindConversation {
			t.Fatalf("unsupported steer persisted conversation: %#v", event)
		}
	}
}

func TestNativeSteerProviderFailureIsAcceptedThenProjectedAsFailed(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-fail",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
		Failures:     map[runtime.ProviderSessionMode]error{runtime.ProviderSessionModeInTurnSteer: errors.New("rejected")},
	})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"req-fail","message":"stop"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Kind == task.EventKindSteering && event.Payload["request_id"] == "req-fail" && event.Payload["outcome"] == "failed" {
				if event.Payload["error_code"] == "provider_rejected" {
					return true
				}
			}
		}
		return false
	})
}

func TestNativeSteerReplacementContinuationFailureFailsClosedWithoutApplied(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	inner := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-bind-fail", ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	session := failingContinuationBindSession{ProviderSession: inner}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"req-bind-fail","message":"focus"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Payload["request_id"] == "req-bind-fail" && event.Payload["phase"] == "replacement_continuation_failed" {
				return true
			}
		}
		return false
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		found, _ := server.tasks.Get(created.ID)
		if found.Status == task.StatusFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusFailed {
		t.Fatalf("Task status = %q, want failed", found.Status)
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("failed replacement retained provider session ownership")
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Payload["request_id"] == "req-bind-fail" && event.Payload["phase"] == "steering_applied" {
			t.Fatalf("failed continuation transition emitted applied: %#v", event)
		}
	}
}

func TestTaskDetailExposesNativeSteerModeAndIdleState(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-controls", Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})); err != nil {
		t.Fatal(err)
	}
	detailed, err := server.taskDetail(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !detailed.RuntimeControls.NativeSteerAvailable || detailed.RuntimeControls.NativeSteerMode != string(runtime.ProviderSessionModeInTurnSteer) || detailed.RuntimeControls.NativeSteerState != "idle" {
		t.Fatalf("native steer controls = %#v", detailed.RuntimeControls)
	}
}

func TestStopClosesBoundProviderSession(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{SessionID: "session-stop", Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true}})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/stop", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stop status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "after-stop", Message: "should fail"}, nil); !errors.Is(err, runtime.ErrProviderSessionClosed) {
		t.Fatalf("session after stop error = %v, want closed", err)
	}
}

func TestServerCloseDrainsInFlightProviderSteerBeforeClosingDatabase(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-close", ManualAcknowledge: true,
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"close-1","message":"stop"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		_ = server.Close()
		t.Fatalf("steer status = %d, body=%s", response.Code, response.Body.String())
	}

	closed := make(chan error, 1)
	go func() { closed <- server.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server close did not drain provider control")
	}
	if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "after-close", Message: "must fail"}, nil); !errors.Is(err, runtime.ErrProviderSessionClosed) {
		t.Fatalf("session after close error = %v, want closed", err)
	}
}
