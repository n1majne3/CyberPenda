package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/modelprovider"
	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type failingContinuationBindSession struct {
	runtime.ProviderSession
}

func (s failingContinuationBindSession) TurnState() runtime.ProviderSessionTurnState {
	if reporter, ok := s.ProviderSession.(runtime.ProviderSessionTurnStateReporter); ok {
		return reporter.TurnState()
	}
	return runtime.ProviderSessionTurnState{SessionID: s.SessionID()}
}

type stopRaceProviderSession struct {
	runtime.ProviderSession
	closed chan struct{}
	once   sync.Once
}

func (s *stopRaceProviderSession) Close(ctx context.Context) error {
	err := s.ProviderSession.Close(ctx)
	if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
		s.once.Do(func() { close(s.closed) })
	}
	return err
}

type delayedStopProviderSession struct {
	runtime.ProviderSession
	mu      sync.Mutex
	active  bool
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (s *delayedStopProviderSession) SendTurn(ctx context.Context, _ runtime.ProviderSessionRequest, _ runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	s.mu.Lock()
	s.active = true
	s.once.Do(func() { close(s.started) })
	s.mu.Unlock()
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
	return runtime.ProviderSessionResult{}, ctx.Err()
}

func (s *delayedStopProviderSession) Close(ctx context.Context) error {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active {
		return runtime.ErrProviderSessionControlConflict
	}
	err := s.ProviderSession.Close(ctx)
	if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
		select {
		case <-s.closed:
		default:
			close(s.closed)
		}
	}
	return err
}

type permissionProviderTransport struct {
	mu        sync.Mutex
	responses map[string]runtime.SandboxBridgeResponse
	requests  []runtime.SandboxBridgeRequest
}

func (t *permissionProviderTransport) Send(_ context.Context, request runtime.SandboxBridgeRequest) (runtime.SandboxBridgeResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests = append(t.requests, request)
	if response, ok := t.responses[request.Method]; ok {
		response.ID = request.ID
		return response, nil
	}
	return runtime.SandboxBridgeResponse{ID: request.ID, Result: []byte(`{"status":"completed"}`)}, nil
}

func (*permissionProviderTransport) Close(context.Context) error { return nil }

func (t *permissionProviderTransport) snapshot() []runtime.SandboxBridgeRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]runtime.SandboxBridgeRequest(nil), t.requests...)
}

func (failingContinuationBindSession) BindContinuation(string) error {
	return errors.New("continuation bind rejected")
}

func TestProviderRuntimeOutputPersistsOnlyTranscriptFields(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, "profile", "claude_code", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	server.persistProviderSessionEvent(created.ID, task.EventKindRuntimeOutput, task.EventPayload{
		"provider": "claude_code", "provider_event": "claude/runtime_output",
		"session_id": "claude-1", "provider_turn_id": "turn-1", "provider_item_id": "turn-1-thinking-0",
		"phase":  "streaming",
		"stream": "assistant", "text": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"use bearer secret-provider-token-123456 next"}]}}`,
		"raw": "must not persist",
	})

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != task.EventKindRuntimeOutput {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Payload["stream"] != "assistant" || events[0].Payload["text"] == "" {
		t.Fatalf("runtime output payload = %#v", events[0].Payload)
	}
	if events[0].Payload["provider_item_id"] != "turn-1-thinking-0" || events[0].Payload["phase"] != "streaming" {
		t.Fatalf("runtime output lost reasoning correlation fields: %#v", events[0].Payload)
	}
	text, _ := events[0].Payload["text"].(string)
	if strings.Contains(text, "secret-provider-token-123456") || !strings.Contains(text, "bearer [REDACTED]") {
		t.Fatalf("runtime output reasoning was not shape-redacted: %q", text)
	}
	if _, leaked := events[0].Payload["raw"]; leaked {
		t.Fatalf("runtime output leaked raw provider payload: %#v", events[0].Payload)
	}
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
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "fake", task.RunnerSandbox)
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

func TestNativeSteerRejectsModelProviderSelectionInsteadOfIgnoringIt(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	createdProject, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "claude_code", task.RunnerSandbox)
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
		SessionID: "claude-session-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{
		"request_id":"req-provider-switch",
		"message":"continue with the alternate provider",
		"model_provider_id":"alternate"
	}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("steer status = %d, body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "restart the continuation") {
		t.Fatalf("steer body = %s", response.Body.String())
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "req-provider-switch" {
			t.Fatalf("native steer recorded a conversation before rejecting provider selection: %#v", event)
		}
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

// createTestRuntimeProfile stores a minimal profile so native steer can resolve
// Runtime Turn Selection without depending on launch projection.
func createTestRuntimeProfile(t *testing.T, server *Server) runtimeprofile.Profile {
	t.Helper()
	profile, err := server.profiles.Create("Test Runtime", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("create runtime profile: %v", err)
	}
	return profile
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
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
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
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-fail",
		ActiveTurnID: "turn-fail",
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

func TestNativeSteerNonSteerableTurnRequiresExplicitOperatorRecovery(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-review", ActiveTurnID: "turn-review",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true},
		Failures:     map[runtime.ProviderSessionMode]error{runtime.ProviderSessionModeInTurnSteer: runtime.ErrProviderTurnNotSteerable},
	})
	if err := server.BindProviderSession(created.ID, provider); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"review-steer","message":"focus"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Kind == task.EventKindSteering && event.Payload["request_id"] == "review-steer" &&
				event.Payload["outcome"] == "action_required" && event.Payload["error_code"] == "active_turn_not_steerable" {
				if event.Payload["error"] != "active provider Runtime Turn is not steerable" {
					t.Fatalf("public error = %#v", event.Payload["error"])
				}
				return true
			}
		}
		return false
	})
	if requests := provider.LastRequests(); len(requests) != 1 {
		t.Fatalf("provider requests = %#v, want no automatic interrupt fallback", requests)
	}
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID, nil)
	detailResponse := httptest.NewRecorder()
	server.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	var detailed task.Task
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detailed); err != nil {
		t.Fatal(err)
	}
	if detailed.RuntimeControls.NativeSteerState != "action_required" ||
		detailed.RuntimeControls.NativeSteerErrorCode != "active_turn_not_steerable" ||
		detailed.RuntimeControls.NativeSteerError != "active provider Runtime Turn is not steerable" {
		t.Fatalf("Runtime Controls = %#v", detailed.RuntimeControls)
	}
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
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "fake", task.RunnerSandbox)
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
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
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
	if !detailed.RuntimeControls.NativeSteerAvailable || detailed.RuntimeControls.NativeSteerMode != string(runtime.ProviderSessionModeSendTurn) || detailed.RuntimeControls.NativeSteerState != "idle" {
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
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
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

func TestStopClosesProviderSessionBeforeWaitingForRuntimeResources(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	// A load-safe budget: CI runners stretched a 50ms deadline past its bound
	// and flaked with "runtime did not stop in time". The assertions here guard
	// the close ordering, not the stop latency bound.
	server.runtimeStopTimeout = 5 * time.Second

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "claude_code", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	session := &stopRaceProviderSession{
		ProviderSession: runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
			SessionID:    "session-stop-race",
			Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
		}),
		closed: make(chan struct{}),
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}
	adapter := runtime.NewProviderSessionRunAdapter(session, session.closed)
	adapter.BindContinuation(continuation.ID)
	launchDone := make(chan error, 1)
	go func() {
		launchDone <- server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID: created.ID, Goal: created.Goal, ContinuationID: continuation.ID, Adapter: adapter,
			StopConfirmation: func(timeout time.Duration) error {
				select {
				case <-session.closed:
					return nil
				case <-time.After(timeout):
					return errors.New("provider bridge still running")
				}
			},
		})
	}()
	waitForHarnessActive(t, server, created.ID, true)

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/stop", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stop status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("stop retained provider session binding")
	}
	if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "after-race-stop", Message: "must fail"}, nil); !errors.Is(err, runtime.ErrProviderSessionClosed) {
		t.Fatalf("session after stop error = %v, want closed", err)
	}
	select {
	case <-launchDone:
	case <-time.After(time.Second):
		t.Fatal("runtime launch did not exit after provider session close")
	}
}

func TestStopWaitsForActiveProviderControlBeforeClosingSession(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	// Load-safe stop budget: the delayed provider close (cancel wake-up plus the
	// fake's 20ms settle) must finish inside the stop deadline; 100ms flaked on
	// loaded CI runners. The test asserts the wait-for-control ORDER, not a
	// latency bound.
	server.runtimeStopTimeout = 5 * time.Second

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "claude_code", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	session := &delayedStopProviderSession{
		ProviderSession: runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
			SessionID:    "session-active-stop",
			Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
		}),
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}
	adapter := runtime.NewProviderSessionRunAdapter(session, session.closed)
	adapter.BindContinuation(continuation.ID)
	go func() {
		_ = server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID: created.ID, Goal: created.Goal, ContinuationID: continuation.ID, Adapter: adapter,
		})
	}()
	waitForHarnessActive(t, server, created.ID, true)
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("provider control did not start")
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/stop", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stop status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("stop retained provider session after active control settled")
	}
}

func TestProviderPermissionRequestIsPersistedAndCanBeAnsweredThroughTaskRoute(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, "profile", "claude_code", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	transport := &permissionProviderTransport{responses: map[string]runtime.SandboxBridgeResponse{
		"claude/permission/respond": {Result: []byte(`{"session_id":"session-perm","permission_request_id":"perm-1","decision":"allow"}`)},
	}}
	session := runtime.NewClaudeCodeProviderSession(runtime.ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "session-perm", ActiveTurnID: "turn-1"})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}
	session.HandleEvent(runtime.SandboxBridgeEvent{Method: "claude/permission/requested", Params: []byte(`{"session_id":"session-perm","turn_id":"turn-1","permission_request_id":"perm-1","tool_input":{"token":"secret"}}`)}, nil)
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Payload["phase"] == "provider_permission_requested" && event.Payload["permission_request_id"] == "perm-1" {
				return true
			}
		}
		return false
	})
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/permissions/perm-1/respond", bytes.NewBufferString(`{"request_id":"permission-1","decision":"allow"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("permission response status = %d, body=%s", response.Code, response.Body.String())
	}
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Payload["phase"] == "provider_permission_response_applied" && event.Payload["permission_request_id"] == "perm-1" {
				return true
			}
		}
		return false
	})
	if len(transport.snapshot()) != 1 || transport.snapshot()[0].Method != "claude/permission/respond" {
		t.Fatalf("permission frames = %#v", transport.snapshot())
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Payload["permission_request_id"] == "perm-1" && event.Payload["tool_input"] != nil {
			t.Fatalf("permission event persisted raw tool payload: %#v", event.Payload)
		}
	}
	retry := httptest.NewRecorder()
	server.ServeHTTP(retry, httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/tasks/"+created.ID+"/permissions/perm-1/respond", bytes.NewBufferString(`{"request_id":"permission-1","decision":"allow"}`)))
	if retry.Code != http.StatusAccepted {
		t.Fatalf("idempotent permission response status = %d, body=%s", retry.Code, retry.Body.String())
	}
	if len(transport.snapshot()) != 1 {
		t.Fatalf("idempotent permission response sent %d frames, want 1", len(transport.snapshot()))
	}
}

func TestRestartMarksProviderSessionRecoveryExplicitlyAndPreservesMetadata(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{DBPath: filepath.Join(root, "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, "profile", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationRuntimeMetadata(continuation.ID, "container-1", "thread-1", "/sessions/thread-1.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServer(Config{DBPath: filepath.Join(root, "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	found, err := restarted.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusInterrupted {
		t.Fatalf("restarted task status = %q, want interrupted", found.Status)
	}
	latest, err := restarted.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest continuation = %#v, err=%v", latest, err)
	}
	if latest.NativeSessionID != "thread-1" || latest.NativeSessionPath != "/sessions/thread-1.jsonl" {
		t.Fatalf("restart lost durable provider metadata: %#v", latest)
	}
	events, err := restarted.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var recovery bool
	for _, event := range events {
		if event.Payload["phase"] == "provider_session_recovery_required" && event.Payload["recovery_state"] == "failed_closed" {
			recovery = true
		}
	}
	if !recovery {
		t.Fatalf("restart did not record explicit fail-closed recovery event: %#v", events)
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
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: project.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
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

// TestNativeSteerReplacementCarriesBlackboardGrant proves an
// interrupt_then_replace native steer keeps the in-container MCP capability
// token writable: the still-open grant is rebound from the completed old
// Continuation to the running replacement, so a Blackboard change sent with the
// ORIGINAL token lands on the replacement instead of failing closed_continuation.
func TestNativeSteerReplacementCarriesBlackboardGrant(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"),
		RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if server.blackboardV2Continuity == nil || server.projectInterfaceGrants == nil {
		t.Fatal("blackboard v2 grant surface is not wired")
	}

	createdProject, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile := createTestRuntimeProfile(t, server)
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "inspect target", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: created.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerSandbox,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatalf("launch Continuation: %v", err)
	}
	if strings.TrimSpace(launch.Token) == "" {
		t.Fatal("Continuation launch omitted opaque capability token")
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"req-1","message":"focus on admin"}`))
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

	replacement, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || replacement == nil {
		t.Fatalf("replacement Continuation = %#v, err=%v", replacement, err)
	}
	if replacement.ID == launch.Continuation.ID {
		t.Fatalf("expected a fresh replacement Continuation, got the original %q", replacement.ID)
	}

	grant, err := server.projectInterfaceGrants.Resolve(context.Background(), launch.Token)
	if err != nil {
		t.Fatalf("resolve original grant after steer: %v", err)
	}
	if grant.ContinuationID != replacement.ID {
		t.Fatalf("original grant Continuation = %q, want replacement %q", grant.ContinuationID, replacement.ID)
	}

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v2/projects/" + createdProject.ID
	workBody := `{"schema":"semantic-change-batch/v2","changes":[{"op":"create","key":"objective:steer","type":"objective","record":{"status":"open","objective":"post-steer proof"}}]}`
	writeResult := doV2HTTP(t, http.MethodPost, base+"/blackboard/changes", launch.Token, "", "post-steer-write", workBody)
	if writeResult.status < 200 || writeResult.status >= 300 {
		t.Fatalf("post-steer Blackboard write with original token = %d %s", writeResult.status, writeResult.body)
	}
	if bytes.Contains(writeResult.body, []byte(`"closed_continuation"`)) {
		t.Fatalf("post-steer write returned closed_continuation: %s", writeResult.body)
	}
	if !bytes.Contains(writeResult.body, []byte(`"schema":"semantic-change-result/v2"`)) {
		t.Fatalf("post-steer write result = %s", writeResult.body)
	}

	// Reads and history with the original token also resolve to the replacement.
	readResult := doV2HTTP(t, http.MethodGet, base+"/blackboard/records/objective:steer", launch.Token, "", "", "")
	if readResult.status != http.StatusOK || !bytes.Contains(readResult.body, []byte(`"key":"objective:steer"`)) {
		t.Fatalf("post-steer read = %d %s", readResult.status, readResult.body)
	}
	historyResult := doV2HTTP(t, http.MethodGet, base+"/blackboard/records/objective:steer/history?limit=1", launch.Token, "", "", "")
	if historyResult.status != http.StatusOK || !bytes.Contains(historyResult.body, []byte(`"schema":"semantic-history/v2"`)) {
		t.Fatalf("post-steer history = %d %s", historyResult.status, historyResult.body)
	}

	oldAfter, err := server.tasks.Continuation(launch.Continuation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldAfter.Status != task.StatusCompleted {
		t.Fatalf("old Continuation status = %q, want completed", oldAfter.Status)
	}
}

type countingTurnStateSession struct {
	runtime.ProviderSession
	calls int
	state runtime.ProviderSessionTurnState
}

func (session *countingTurnStateSession) TurnState() runtime.ProviderSessionTurnState {
	session.calls++
	return session.state
}

func TestPrepareNativeSteerUsesOneTurnSnapshotForModeAndFence(t *testing.T) {
	base := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-atomic",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true, InterruptThenReplace: true,
		},
	})
	session := &countingTurnStateSession{
		ProviderSession: base,
		state: runtime.ProviderSessionTurnState{
			SessionID: "session-atomic", ActiveTurnID: "turn-atomic", ActiveTurnKind: runtime.RuntimeTurnKindWork,
		},
	}

	prepared, err := prepareNativeSteer(session, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if session.calls != 1 {
		t.Fatalf("TurnState calls = %d, want 1", session.calls)
	}
	if prepared.Mode != runtime.ProviderSessionModeInTurnSteer || prepared.ExpectedProviderTurnID != "turn-atomic" {
		t.Fatalf("prepared steer = %#v", prepared)
	}
}

func TestPrepareNativeSteerReplacesNonWorkActiveTurn(t *testing.T) {
	base := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-control",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true, InterruptThenReplace: true,
		},
	})
	session := &countingTurnStateSession{
		ProviderSession: base,
		state: runtime.ProviderSessionTurnState{
			SessionID: "session-control", ActiveTurnID: "turn-control", ActiveTurnKind: runtime.RuntimeTurnKindControl,
		},
	}
	prepared, err := prepareNativeSteer(session, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Mode != runtime.ProviderSessionModeInterruptThenReplace || prepared.ExpectedProviderTurnID != "turn-control" {
		t.Fatalf("prepared control-Turn steer = %#v", prepared)
	}
}

func TestPrepareAcceptedNativeSteerStartsAfterPendingOwnerSettlement(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-concluding", ActiveTurnID: "turn-conclusion", ActiveTurnKind: runtime.RuntimeTurnKindControl,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true,
		},
	})
	settlement := func(context.Context, bool) (bool, error) { return false, nil }

	prepared, err := prepareAcceptedNativeSteer(context.Background(), session, false, false, settlement)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Mode != runtime.ProviderSessionModeSendTurn || prepared.ExpectedProviderTurnID != "" {
		t.Fatalf("prepared deferred steer = %#v", prepared)
	}
}

func TestPrepareNativeSteerCanExplicitlyReplaceActiveTurn(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-replace", ActiveTurnID: "turn-old",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true, InterruptThenReplace: true,
		},
	})
	prepared, err := prepareNativeSteer(session, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Mode != runtime.ProviderSessionModeInterruptThenReplace || prepared.ExpectedProviderTurnID != "turn-old" {
		t.Fatalf("prepared steer = %#v", prepared)
	}
}

func TestClassifyNativeSteerFailureDistinguishesCompletedTarget(t *testing.T) {
	completed := classifyNativeSteerFailure(runtime.ErrProviderTurnUnavailable)
	if completed.state != owner.SteeringActionRequired || completed.reason != owner.SteeringReasonTargetTurnCompleted || completed.code != "target_turn_completed" {
		t.Fatalf("completed classification = %#v", completed)
	}
	changed := classifyNativeSteerFailure(runtime.ErrProviderTurnChanged)
	if changed.state != owner.SteeringActionRequired || changed.reason != owner.SteeringReasonTargetTurnChanged || changed.code != "target_turn_changed" {
		t.Fatalf("changed classification = %#v", changed)
	}
}

func TestSteeringConflictUsesExactClientControlledSelectionIdentity(t *testing.T) {
	recordedSelection := canonicalSteeringClientSelectionIdentity("provider-1", "model-1", "high")
	record := &owner.SteeringRecord{
		OperatorMessage: "focus", Message: "focus", Mode: owner.SteeringModeInTurnSteer, State: owner.SteeringPending,
		ExpectedProviderTurnID: "turn-1", ModelProviderID: "provider-1", Model: "model-1",
		RequestedReasoningEffort: "high", ClientSelectionIdentity: recordedSelection,
	}
	identity := steeringReplayIdentity{
		requestID: "req-1", operatorMessage: "focus", clientSelectionIdentity: recordedSelection,
	}
	if conflict := steeringConflictMessage(record, identity); conflict != "" {
		t.Fatalf("server-selected delivery fields caused conflict = %q", conflict)
	}

	identity.clientSelectionIdentity = canonicalSteeringClientSelectionIdentity("", "", "")
	if conflict := steeringConflictMessage(record, identity); conflict != "steer request id already belongs to a different turn selection" {
		t.Fatalf("omitted retry selection conflict = %q", conflict)
	}

	identity.clientSelectionIdentity = canonicalSteeringClientSelectionIdentity("provider-2", "model-1", "high")
	if conflict := steeringConflictMessage(record, identity); conflict != "steer request id already belongs to a different turn selection" {
		t.Fatalf("changed client selection conflict = %q", conflict)
	}
}

func TestSteeringConflictKeepsLegacySelectionFallback(t *testing.T) {
	record := &owner.SteeringRecord{
		OperatorMessage: "focus", Message: "focus", ModelProviderID: "provider-1",
	}
	identity := steeringReplayIdentity{requestID: "req-1", operatorMessage: "focus"}
	if conflict := steeringConflictMessage(record, identity); conflict != "" {
		t.Fatalf("legacy omitted selection conflict = %q", conflict)
	}
	identity.modelProviderID = "provider-2"
	if conflict := steeringConflictMessage(record, identity); conflict != "steer request id already belongs to a different turn selection" {
		t.Fatalf("legacy changed selection conflict = %q", conflict)
	}
}
