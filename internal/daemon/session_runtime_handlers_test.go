package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
)

func TestSessionProviderResultsRemainOnTimelineOutsideConversation(t *testing.T) {
	kind, payload := sessionProviderEventPayload(task.EventKindConversation, task.EventPayload{
		"provider": "codex", "request_id": "request-1", "outcome": "applied", "phase": "provider_turn_applied",
	}, "continuation-1")
	if kind != session.EventKindTurn {
		t.Fatalf("provider result event kind = %q, want %q", kind, session.EventKindTurn)
	}
	if _, ok := payload["continuation_id"]; !ok {
		t.Fatalf("provider result lost continuation correlation: %#v", payload)
	}
}

func TestSessionRuntimeRecoveryFailsClosedAcrossDaemonRestart(t *testing.T) {
	root := t.TempDir()
	config := Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	}
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	created, err := server.sessions.Create(session.CreateRequest{Input: "recover this Session"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Session: %v", err)
	}
	continuation, err := server.sessions.CreateContinuation(created.ID, "profile:restart", "codex", session.RunnerSandbox)
	if err != nil {
		_ = server.Close()
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationRuntimeMetadata(continuation.ID, "", "native-session", "/managed/native-session.jsonl"); err != nil {
		_ = server.Close()
		t.Fatalf("persist native metadata: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusRunning); err != nil {
		_ = server.Close()
		t.Fatalf("mark continuation running: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close initial daemon: %v", err)
	}

	restarted, err := NewServer(config)
	if err != nil {
		t.Fatalf("restart daemon: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	active, err := restarted.sessions.ActiveContinuation(created.ID)
	if err != nil {
		t.Fatalf("read recovered active continuation: %v", err)
	}
	if active != nil {
		t.Fatalf("restart left an active Session continuation: %#v", active)
	}
	latest, err := restarted.sessions.LatestContinuation(created.ID)
	if err != nil || latest == nil || latest.Status != session.RuntimeStatusInterrupted {
		t.Fatalf("recovered continuation = %#v, %v; want interrupted", latest, err)
	}
	events, err := restarted.sessions.Events(created.ID)
	if err != nil {
		t.Fatalf("read recovery events: %v", err)
	}
	var recoveryEvent bool
	for _, event := range events {
		if event.Payload["phase"] == "provider_session_recovery_required" && event.Payload["recovery_state"] == "failed_closed" {
			recoveryEvent = true
		}
	}
	if !recoveryEvent {
		t.Fatalf("restart did not record Session recovery event: %#v", events)
	}
}

func TestSessionProviderControlsUseTheSameOwnerContractForBuiltInProviders(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi,
	} {
		t.Run(string(provider), func(t *testing.T) {
			providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
				SessionID:    "session-" + string(provider),
				Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
			})
			factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
				DisableBuiltinSkills: true, ProviderSessionFactory: factory,
			})
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			t.Cleanup(func() { _ = server.Close() })
			profile, err := server.profiles.Create(string(provider), provider, runtimeprofile.Fields{
				DefaultRunner: "host", BinaryPath: "/bin/sh", Model: "session-model",
			})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(
				`{"input":"provider conformance","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
			))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("create Session status = %d, body=%s", response.Code, response.Body.String())
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && len(factory.Requests()) == 0 {
				time.Sleep(time.Millisecond)
			}
			requests := factory.Requests()
			if len(requests) != 1 || requests[0].Provider != provider {
				t.Fatalf("provider launch requests = %#v", requests)
			}
			if requests[0].Owner.Kind != "session" || requests[0].Owner.SessionID == "" || requests[0].Owner.ProjectID != "" {
				t.Fatalf("provider owner contract = %#v", requests[0].Owner)
			}
		})
	}
}

func TestSessionLaunchUsesProjectFreeOwnerAndPersistentProviderControls(t *testing.T) {
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:         "session-provider-1",
		ManualAcknowledge: true,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true,
			InterruptThenReplace: true, PermissionResponse: true,
		},
	})
	factory := &recordingProviderSessionFactory{
		session: providerSession,
		adapter: &persistentTestAdapter{},
	}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", Model: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"input":"inspect the session target","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created Session has no id")
	}

	var launchRequest ProviderSessionLaunchRequest
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		requests := factory.Requests()
		if len(requests) > 0 {
			launchRequest = requests[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if launchRequest.Owner.ID != created.ID {
		t.Fatalf("provider owner id = %q, want %q", launchRequest.Owner.ID, created.ID)
	}
	if err := launchRequest.Owner.Validate(); err != nil {
		t.Fatalf("Session owner contract: %v", err)
	}
	if launchRequest.Owner.ProjectID != "" || launchRequest.Task.ID != "" || launchRequest.Owner.Capabilities.ProjectScope {
		t.Fatalf("provider launch crossed Project/Task boundary: owner=%#v task=%#v", launchRequest.Owner, launchRequest.Task)
	}

	message := httptest.NewRecorder()
	messageRequest := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/messages", bytes.NewBufferString(`{"message":"continue with the second pass"}`))
	messageRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(message, messageRequest)
	if message.Code != http.StatusAccepted {
		t.Fatalf("Session message status = %d, body=%s", message.Code, message.Body.String())
	}

	conversation := httptest.NewRecorder()
	server.ServeHTTP(conversation, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/conversation", nil))
	if conversation.Code != http.StatusOK {
		t.Fatalf("conversation status = %d, body=%s", conversation.Code, conversation.Body.String())
	}
	var conversationPayload struct {
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.NewDecoder(conversation.Body).Decode(&conversationPayload); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if len(conversationPayload.Events) != 2 {
		t.Fatalf("conversation events = %#v, want initial and continuation input", conversationPayload.Events)
	}
	waitForProviderRequests(t, providerSession, 1)

	steer := httptest.NewRecorder()
	steerRequest := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/steer", bytes.NewBufferString(`{"message":"interrupt and focus on the login flow"}`))
	steerRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(steer, steerRequest)
	if steer.Code != http.StatusAccepted {
		t.Fatalf("Session steer status = %d, body=%s", steer.Code, steer.Body.String())
	}
	waitForProviderRequests(t, providerSession, 2)
	requests := providerSession.LastRequests()
	steerRequestID := requests[len(requests)-1].RequestID
	if err := providerSession.Acknowledge(steerRequestID); err != nil {
		t.Fatalf("acknowledge Session replacement: %v", err)
	}
	replacementDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(replacementDeadline) {
		active, activeErr := server.sessions.ActiveContinuation(created.ID)
		if activeErr != nil {
			t.Fatalf("read active Session continuation: %v", activeErr)
		}
		if active != nil && active.Number == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	active, err := server.sessions.ActiveContinuation(created.ID)
	if err != nil || active == nil || active.Number != 2 {
		t.Fatalf("Session replacement continuation = %#v, %v", active, err)
	}
	events, err := server.sessions.Events(created.ID)
	if err != nil {
		t.Fatalf("read Session replacement events: %v", err)
	}
	var sawReplacement bool
	for _, event := range events {
		if event.Payload["continuation_id"] == active.ID {
			sawReplacement = true
		}
	}
	if !sawReplacement {
		t.Fatalf("Session replacement emitted no events on continuation %q: %#v", active.ID, events)
	}

	timeline := httptest.NewRecorder()
	server.ServeHTTP(timeline, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/timeline", nil))
	if timeline.Code != http.StatusOK {
		t.Fatalf("timeline status = %d, body=%s", timeline.Code, timeline.Body.String())
	}
	var timelinePayload struct {
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.NewDecoder(timeline.Body).Decode(&timelinePayload); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	for _, event := range timelinePayload.Events {
		if event.Kind == "conversation" {
			t.Fatalf("timeline duplicated conversation event: %#v", timelinePayload.Events)
		}
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("Session stop status = %d, body=%s", stop.Code, stop.Body.String())
	}
}
