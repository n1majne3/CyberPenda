package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// #163 primary acceptance seam: the daemon Task HTTP API, real Task and
// Blackboard v2 services, SQLite, and a deterministic fake ProviderSession.
func TestAssistedWorkTurnWithTerminalToolResultBecomesPending(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create assisted Task status = %d body %s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode created Task: %v", err)
	}
	waitForAssistedProviderRequests(t, session, 1)

	for _, observation := range []runtime.ProviderSessionObservation{
		{
			Kind:           runtime.ProviderSessionObservationToolUse,
			ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell",
		},
		{
			Kind:           runtime.ProviderSessionObservationToolResult,
			ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
		},
		{
			Kind:           runtime.ProviderSessionObservationTurnCompleted,
			ProviderTurnID: "work-turn-1", Status: "completed",
		},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatalf("emit observation %#v: %v", observation, err)
		}
	}

	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStatePending)
	if found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted {
		t.Fatalf("stored conclusion mode = %q, want assisted", found.RunControls.BlackboardConclusionMode)
	}
	if found.BlackboardConclusion.Mode != task.BlackboardConclusionModeAssisted || found.BlackboardConclusion.SourceTurnID != "work-turn-1" {
		t.Fatalf("Blackboard conclusion view = %#v", found.BlackboardConclusion)
	}
	if found.Status != task.StatusRunning || found.RuntimeActivity.Liveness != "live" || found.RuntimeActivity.TurnActivity != "idle" {
		t.Fatalf("Task lifecycle/activity changed by pending conclusion: status=%q activity=%#v", found.Status, found.RuntimeActivity)
	}

	events := assistedTaskEvents(t, server, projectID, created.ID)
	pendingEvents := 0
	for _, event := range events {
		if event.Kind != task.EventKindBlackboardConclusion {
			continue
		}
		pendingEvents++
		if event.ContinuationID == "" || event.Payload["phase"] != "pending_detected" || event.Payload["source_turn_id"] != "work-turn-1" {
			t.Fatalf("pending Harness event = %#v", event)
		}
		for _, forbidden := range []string{"args", "input", "output", "text", "raw", "message", "reasoning"} {
			if _, ok := event.Payload[forbidden]; ok {
				t.Fatalf("pending Harness event leaked %q: %#v", forbidden, event.Payload)
			}
		}
	}
	if pendingEvents != 1 {
		t.Fatalf("pending Harness events = %d, want 1; events=%#v", pendingEvents, events)
	}

	timelineRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/timeline", nil)
	timelineResponse := httptest.NewRecorder()
	server.ServeHTTP(timelineResponse, timelineRequest)
	if timelineResponse.Code != http.StatusOK {
		t.Fatalf("get Timeline status = %d body %s", timelineResponse.Code, timelineResponse.Body.String())
	}
	var timelineBody struct {
		Items []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.NewDecoder(timelineResponse.Body).Decode(&timelineBody); err != nil {
		t.Fatalf("decode Timeline: %v", err)
	}
	harnessItems := 0
	for _, item := range timelineBody.Items {
		if item.Type == "harness" {
			harnessItems++
			if !strings.Contains(item.Content, "Blackboard conclusion pending") {
				t.Fatalf("Harness Timeline item = %#v", item)
			}
		}
	}
	if harnessItems != 1 {
		t.Fatalf("Harness Timeline items = %d, want 1; items=%#v", harnessItems, timelineBody.Items)
	}

	transcriptRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+created.ID+"/transcript", nil)
	transcriptResponse := httptest.NewRecorder()
	server.ServeHTTP(transcriptResponse, transcriptRequest)
	if transcriptResponse.Code != http.StatusOK {
		t.Fatalf("get transcript status = %d body %s", transcriptResponse.Code, transcriptResponse.Body.String())
	}
	if strings.Contains(strings.ToLower(transcriptResponse.Body.String()), "pending_detected") {
		t.Fatalf("Harness pending marker leaked into Task Conversation: %s", transcriptResponse.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 1 {
		t.Fatalf("provider requests = %d, want initial work Turn only", len(requests))
	}
}

func TestAssistedLaunchRejectsProviderWithoutConclusionObservations(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, false)

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "assisted_conclusion_unsupported") {
		t.Fatalf("unsupported assisted launch status = %d body %s", response.Code, response.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 0 {
		t.Fatalf("unsupported assisted launch reached provider: %#v", requests)
	}
}

func TestRuntimePluginAPIProjectsEffectiveAssistedConclusionCapability(t *testing.T) {
	for _, test := range []struct {
		name    string
		support bool
	}{
		{name: "supported", support: true},
		{name: "unsupported", support: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _, _, _ := newAssistedConclusionFixture(t, test.support)
			request := httptest.NewRequest(http.MethodGet, "/api/runtime-plugins/codex", nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("get runtime plugin status = %d body %s", response.Code, response.Body.String())
			}
			var plugin runtimeplugin.Plugin
			if err := json.NewDecoder(response.Body).Decode(&plugin); err != nil {
				t.Fatal(err)
			}
			if plugin.Capabilities.AssistedConclusion != test.support {
				t.Fatalf("effective assisted_conclusion = %v, want %v", plugin.Capabilities.AssistedConclusion, test.support)
			}
		})
	}
}

func TestAssistedLaunchRejectsSessionThatCannotEmitConclusionObservations(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixtureWithCapabilities(t, true, false)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"assisted"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "assisted_conclusion_unsupported") {
		t.Fatalf("session capability mismatch status = %d body %s", response.Code, response.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 0 {
		t.Fatalf("capability mismatch reached provider Turn: %#v", requests)
	}
}

func TestTaskLaunchRejectsUnknownBlackboardConclusionMode(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"automatic"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "interactive or assisted") {
		t.Fatalf("invalid conclusion mode status = %d body %s", response.Code, response.Body.String())
	}
	if requests := session.LastRequests(); len(requests) != 0 {
		t.Fatalf("invalid conclusion mode reached provider: %#v", requests)
	}
}

func TestAssistedWorkTurnWithoutToolResultStaysClean(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind:           runtime.ProviderSessionObservationTurnCompleted,
		ProviderTurnID: "work-turn-1", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.SourceTurnID != "" {
		t.Fatalf("clean conclusion retained source Turn: %#v", found.BlackboardConclusion)
	}
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion {
			t.Fatalf("no-tool Turn created conclusion Event: %#v", event)
		}
	}
}

func TestInteractiveWorkTurnWithToolResultStaysClean(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, false)
	created := launchConclusionTask(t, server, projectID, profileID, "interactive")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeInteractive || found.BlackboardConclusion.Mode != task.BlackboardConclusionModeInteractive {
		t.Fatalf("interactive conclusion view = %#v", found.BlackboardConclusion)
	}
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion {
			t.Fatalf("interactive Turn created conclusion Event: %#v", event)
		}
	}
}

func TestTaskLaunchWithoutConclusionModeDefaultsToInteractive(t *testing.T) {
	server, projectID, profileID, _ := newAssistedConclusionFixture(t, false)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("legacy Task launch status = %d body %s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeInteractive ||
		created.BlackboardConclusion != (task.BlackboardConclusion{Mode: task.BlackboardConclusionModeInteractive, State: task.BlackboardConclusionStateClean}) {
		t.Fatalf("legacy Task conclusion defaults = %#v / %#v", created.RunControls, created.BlackboardConclusion)
	}
}

func TestDuplicateAssistedTurnCompletionCreatesOnePendingMarker(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for replay := 0; replay < 2; replay++ {
		if err := session.EmitObservation(runtime.ProviderSessionObservation{
			Kind:           runtime.ProviderSessionObservationToolResult,
			ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded",
		}); err != nil {
			t.Fatal(err)
		}
		if err := session.EmitObservation(runtime.ProviderSessionObservation{
			Kind:           runtime.ProviderSessionObservationTurnCompleted,
			ProviderTurnID: "work-turn-1", Status: "completed",
		}); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStatePending)
	markers := 0
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("duplicate completion markers = %d, want 1", markers)
	}
}

func TestPendingBlackboardConclusionSurvivesDaemonRestart(t *testing.T) {
	root := t.TempDir()
	server, projectID, profileID, session := newAssistedConclusionFixtureAt(t, root, true, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, ProviderTurnID: "work-turn-1", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStatePending)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	found := waitForBlackboardConclusionState(t, reopened, projectID, created.ID, task.BlackboardConclusionStatePending)
	if found.BlackboardConclusion.SourceTurnID != "work-turn-1" {
		t.Fatalf("restarted conclusion view = %#v", found.BlackboardConclusion)
	}
}

func TestAssistedConclusionIgnoresControlTurnsAndTrustedBlackboardTools(t *testing.T) {
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created := launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequestID := session.LastRequests()[0].RequestID
	controlResult, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "control-request-1", Message: "reconcile", TurnKind: runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: "control-request-1", ProviderTurnID: controlResult.ProviderTurnID, ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: "control-request-1", ProviderTurnID: controlResult.ProviderTurnID, Status: "completed"},
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequestID, ProviderTurnID: "work-turn-1", ToolCallID: "tool-2", ToolName: "blackboard_change", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequestID, ProviderTurnID: "work-turn-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind == task.EventKindBlackboardConclusion {
			t.Fatalf("control or trusted Blackboard Tool created conclusion Event: %#v", event)
		}
	}
}

func launchConclusionTask(t *testing.T, server *Server, projectID, profileID, mode string) task.Task {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewBufferString(`{
		"goal":"inspect example.com",
		"runtime_profile_id":"`+profileID+`",
		"runner":"sandbox",
		"run_controls":{"blackboard_conclusion_mode":"`+mode+`"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create %s Task status = %d body %s", mode, response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

type assistedConclusionSessionFactory struct {
	session *runtime.FakeProviderSession
	adapter *runtime.ProviderSessionRunAdapter
	support bool
}

func (factory *assistedConclusionSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	if err := factory.session.BindContinuation(request.Continuation.ID); err != nil {
		return ProviderSessionBinding{}, err
	}
	factory.adapter.BindContinuation(request.Continuation.ID)
	return ProviderSessionBinding{Session: factory.session, Adapter: factory.adapter}, nil
}

func (factory *assistedConclusionSessionFactory) SupportsAssistedConclusion(provider runtimeprofile.Provider) bool {
	return factory.support && provider == runtimeprofile.ProviderCodex
}

func newAssistedConclusionFixture(t *testing.T, support bool) (*Server, string, string, *runtime.FakeProviderSession) {
	return newAssistedConclusionFixtureWithCapabilities(t, support, support)
}

func newAssistedConclusionFixtureWithCapabilities(t *testing.T, reporterSupport, sessionSupport bool) (*Server, string, string, *runtime.FakeProviderSession) {
	t.Helper()
	return newAssistedConclusionFixtureAt(t, t.TempDir(), reporterSupport, sessionSupport)
}

func newAssistedConclusionFixtureAt(t *testing.T, root string, reporterSupport, sessionSupport bool) (*Server, string, string, *runtime.FakeProviderSession) {
	t.Helper()
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "assisted-session", ActiveTurnID: "work-turn-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: sessionSupport,
		},
	})
	adapter := runtime.NewProviderSessionRunAdapter(session, make(chan struct{}))
	factory := &assistedConclusionSessionFactory{session: session, adapter: adapter, support: reporterSupport}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	createdProject, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	binary := filepath.Join(root, "codex")
	if err := writeExecutable(binary, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-test", BinaryPath: binary, SandboxImage: "cyberpenda:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, createdProject.ID, profile.ID, session
}

func waitForAssistedProviderRequests(t *testing.T, session *runtime.FakeProviderSession, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(session.LastRequests()) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("provider requests = %d, want at least %d", len(session.LastRequests()), count)
}

func waitForBlackboardConclusionState(t *testing.T, server *Server, projectID, taskID string, state task.BlackboardConclusionState) task.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			var found task.Task
			if err := json.NewDecoder(response.Body).Decode(&found); err == nil && found.BlackboardConclusion.State == state {
				return found
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Task %s did not reach Blackboard conclusion state %q", taskID, state)
	return task.Task{}
}

func assistedTaskEvents(t *testing.T, server *Server, projectID, taskID string) []task.Event {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/events", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get Task events status = %d body %s", response.Code, response.Body.String())
	}
	var body struct {
		Events []task.Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode Task events: %v", err)
	}
	return body.Events
}
