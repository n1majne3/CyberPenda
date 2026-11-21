package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

func TestRejectedSessionRuntimeTurnsDoNotPersistAttachments(t *testing.T) {
	for _, endpoint := range []string{"messages", "steer"} {
		t.Run(endpoint, func(t *testing.T) {
			providerSession := fakeprovider.New(fakeprovider.Config{
				SessionID: "session-rejected-" + endpoint,
				Capabilities: runtimeplugin.Capabilities{
					PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
				},
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
			profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
				DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
			})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}

			create := httptest.NewRecorder()
			createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(
				`{"input":"inspect","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
			))
			createRequest.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(create, createRequest)
			if create.Code != http.StatusCreated {
				t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
			}
			var created struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
				t.Fatalf("decode Session: %v", err)
			}
			found, err := server.sessions.Get(created.ID)
			if err != nil {
				t.Fatalf("read Session Workdir: %v", err)
			}

			filename := "rejected-" + endpoint + ".txt"
			request := multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/"+endpoint,
				`{"message":"must be rejected","runner":"sandbox"}`, filename, "must not persist")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusConflict {
				t.Fatalf("rejected %s status = %d, body=%s", endpoint, response.Code, response.Body.String())
			}
			events, err := server.sessions.Events(created.ID)
			if err != nil {
				t.Fatalf("read Session Events: %v", err)
			}
			for _, event := range events {
				if event.Kind == session.EventKindAttachment && event.Payload["filename"] == filename {
					t.Fatalf("rejected %s persisted attachment Event: %#v", endpoint, event)
				}
			}
			if _, err := os.Stat(filepath.Join(found.Workdir, filename)); !os.IsNotExist(err) {
				t.Fatalf("rejected %s attachment remains in Workdir: %v", endpoint, err)
			}
		})
	}
}

func TestSessionProviderRuntimeOutputKeepsReasoningCorrelationFields(t *testing.T) {
	kind, payload := sessionProviderEventPayload(task.EventKindRuntimeOutput, task.EventPayload{
		"provider": "claude_code", "provider_event": "claude/runtime_output",
		"session_id": "claude-1", "provider_turn_id": "turn-1", "provider_item_id": "turn-1-thinking-0",
		"phase": "streaming", "stream": "stream_event", "text": `{"type":"content_block_delta","delta":{"thinking":"bearer secret-session-token-123456"}}`,
		"raw": "must not persist",
	}, "continuation-1")
	if kind != session.EventKindRuntimeOutput {
		t.Fatalf("provider runtime output kind = %q, want %q", kind, session.EventKindRuntimeOutput)
	}
	if payload["provider_item_id"] != "turn-1-thinking-0" || payload["phase"] != "streaming" || payload["continuation_id"] != "continuation-1" {
		t.Fatalf("provider runtime output lost reasoning correlation: %#v", payload)
	}
	text, _ := payload["text"].(string)
	if text != `{"type":"content_block_delta","delta":{"thinking":"bearer secret-session-token-123456"}}` {
		t.Fatalf("session reasoning must persist byte-for-byte (issue #288), got: %q", text)
	}
	if _, leaked := payload["raw"]; leaked {
		t.Fatalf("provider runtime output leaked raw payload: %#v", payload)
	}
}

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
	decorated, err := restarted.decorateSession(created)
	if err != nil {
		t.Fatalf("decorate recovered Session: %v", err)
	}
	if !decorated.RuntimeControls.QueueSteerAvailable {
		t.Fatal("recovered Session does not allow the message that starts its replacement Runtime")
	}
}

func TestSessionRestartCarriesNativeProviderIdentityIntoTheNewContinuation(t *testing.T) {
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID:    "native-session-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, ResumeSession: true},
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
	created, err := server.sessions.Create(session.CreateRequest{Input: "resume this Session"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	previous, err := server.sessions.CreateContinuation(created.ID, profile.ID, "codex", session.RunnerHost, testSessionProfileRuntimeSnapshot(t, server, profile, session.RunnerHost))
	if err != nil {
		t.Fatalf("create previous continuation: %v", err)
	}
	previous, err = server.sessions.UpdateContinuationRuntimeMetadata(previous.ID, "", "native-session-1", "/sessions/one.jsonl")
	if err != nil {
		t.Fatalf("store native identity: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(previous.ID, session.RuntimeStatusStopped); err != nil {
		t.Fatalf("stop previous continuation: %v", err)
	}

	if _, err := server.startSessionRuntime(t.Context(), created, "continue", sessionRuntimeInput{
		Runner: "host", HostActivated: true,
	}); err != nil {
		t.Fatalf("restart Session Runtime: %v", err)
	}
	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("factory requests = %d, want 1", len(requests))
	}
	if requests[0].Continuation.NativeSessionID != previous.NativeSessionID || requests[0].Continuation.NativeSessionPath != previous.NativeSessionPath {
		t.Fatalf("restart lost native identity: %#v", requests[0].Continuation)
	}
}

func TestResolveSessionRuntimeProfileHonorsNewModelProviderAfterPreviousContinuation(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	glm, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "GLM", BaseURL: "https://glm.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"glm-5.2"}, DefaultModel: "glm-5.2"},
	})
	if err != nil {
		t.Fatalf("create glm provider: %v", err)
	}
	hub, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "HUB", BaseURL: "https://hub.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"MiniMax-M3", "deepseek-v4-flash"}, DefaultModel: "deepseek-v4-flash"},
	})
	if err != nil {
		t.Fatalf("create hub provider: %v", err)
	}
	t.Setenv(glm.APIKeyEnv, "sk-glm")
	t.Setenv(hub.APIKeyEnv, "sk-hub")

	glmProfile, err := server.profiles.Create("Pi · GLM · glm-5.2", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: glm.ID, ModelOverride: "glm-5.2", DefaultRunner: "sandbox",
	})
	if err != nil {
		t.Fatalf("create glm profile: %v", err)
	}
	created, err := server.sessions.Create(session.CreateRequest{
		Input: "switch provider",
		InitialRuntime: &session.CreateContinuationRequest{
			RuntimeProfileID: glmProfile.ID, RuntimeProvider: "pi", Runner: session.RunnerSandbox,
			RuntimeConfig: testSessionProfileRuntimeSnapshot(t, server, glmProfile, session.RunnerSandbox),
		},
	})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	previous, err := server.sessions.LatestContinuation(created.ID)
	if err != nil || previous == nil {
		t.Fatalf("latest continuation: %#v, %v", previous, err)
	}

	resolved, err := server.resolveSessionRuntimeProfile(sessionRuntimeInput{
		ModelProviderID: hub.ID,
		Model:           "MiniMax-M3",
	}, previous)
	if err != nil {
		t.Fatalf("resolve Session Runtime Profile: %v", err)
	}
	if resolved.Fields.ModelProviderID != hub.ID {
		t.Fatalf("model provider = %q, want %q", resolved.Fields.ModelProviderID, hub.ID)
	}
	if resolved.Fields.ModelOverride != "MiniMax-M3" {
		t.Fatalf("model override = %q, want MiniMax-M3", resolved.Fields.ModelOverride)
	}

	if _, err := server.prepareSessionRuntime(t.Context(), session.BlackboardModeInteractive, sessionRuntimeInput{
		ModelProviderID: hub.ID,
		Model:           "MiniMax-M3",
	}, previous); err != nil {
		t.Fatalf("prepare Session Runtime after Model Provider switch: %v", err)
	}
}

func TestSessionProviderControlsUseTheSameOwnerContractForBuiltInProviders(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi,
	} {
		t.Run(string(provider), func(t *testing.T) {
			providerSession := fakeprovider.New(fakeprovider.Config{
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
			modelProvider := createSnapshotTestModelProvider(t, server, "session-model")
			profile, err := server.profiles.Create(string(provider), provider, runtimeprofile.Fields{
				DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: modelProvider.ID, ModelOverride: "session-model",
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
