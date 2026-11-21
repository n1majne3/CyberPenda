package daemon

import (
	"encoding/json"
	"errors"
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
	"pentest/internal/testsupport/fakeprovider"
)

var errUnsupportedEffortForTest = errors.New("reasoning effort max is not supported for this model")

func TestCodexNativeSteerSameProviderModelAndEffortUsesExistingSession(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "codex-thread-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &effortProviderSessionFactory{session: session, adapter: adapter}

	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test", "gpt-strong"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-test",
		SandboxImage: "cyberpenda:test", ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForTaskRunning(t, server, created.ID)
	waitForProviderRequests(t, session, 1)

	versionsBefore, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastBeforeSeq := 0
	if len(eventsBefore) > 0 {
		lastBeforeSeq = eventsBefore[len(eventsBefore)-1].Seq
	}

	body := `{
		"request_id":"turn-select-1",
		"message":"use a stronger model",
		"model_provider_id":` + quoteJSON(provider.ID) + `,
		"model":"gpt-strong",
		"reasoning_effort":"xhigh"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("steer status = %d body %s", resp.Code, resp.Body.String())
	}

	waitForProviderRequests(t, session, 2)
	requests := session.LastRequests()
	last := requests[len(requests)-1]
	if last.ModelProviderID != provider.ID {
		t.Fatalf("model_provider_id = %q, want %q", last.ModelProviderID, provider.ID)
	}
	if last.Model != "gpt-strong" {
		t.Fatalf("model = %q, want gpt-strong", last.Model)
	}
	if last.RequestedReasoningEffort != "xhigh" {
		t.Fatalf("requested effort = %q, want xhigh", last.RequestedReasoningEffort)
	}
	if last.EffectiveReasoningEffort != "" {
		t.Fatalf("effective effort must stay unknown, got %q", last.EffectiveReasoningEffort)
	}

	versionsAfter, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter) != len(versionsBefore) {
		t.Fatalf("RuntimeConfigVersions changed on same-provider native turn: before=%d after=%d", len(versionsBefore), len(versionsAfter))
	}

	eventsAfter, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	conversationCount := 0
	for _, event := range eventsAfter {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "turn-select-1" {
			conversationCount++
			if event.Payload["mode"] != string(runtime.ProviderSessionModeInterruptThenReplace) {
				t.Fatalf("selection-changing steer mode = %#v, want interrupt_then_replace", event.Payload["mode"])
			}
			if event.Payload["model_provider_id"] != provider.ID {
				t.Fatalf("conversation missing model_provider_id: %#v", event.Payload)
			}
			if event.Payload["model"] != "gpt-strong" {
				t.Fatalf("conversation missing model: %#v", event.Payload)
			}
			if event.Payload["requested_reasoning_effort"] != "xhigh" {
				t.Fatalf("conversation missing requested_reasoning_effort: %#v", event.Payload)
			}
		}
		// Selection must only attach to conversation/steering turn records.
		if event.Kind != task.EventKindConversation && event.Kind != task.EventKindSteering && event.Kind != task.EventKindLifecycle && event.Kind != task.EventKindRuntimeOutput {
			if event.Seq > lastBeforeSeq {
				t.Fatalf("unexpected new Task Event kind for selection: %#v", event)
			}
		}
		if event.Kind == task.EventKindSteering && event.Payload["phase"] == "steering_requested" && event.Seq > lastBeforeSeq {
			t.Fatalf("native selection must not create steering_requested Task Event: %#v", event)
		}
	}
	if conversationCount != 1 {
		t.Fatalf("conversation events for turn = %d, want 1", conversationCount)
	}

	bound, ok := server.providerSessions.get(created.ID)
	if !ok || bound.SessionID() != "codex-thread-1" {
		t.Fatalf("session/thread identity changed: bound=%v id=%q want codex-thread-1", ok, sessionIDOf(bound))
	}
	if session.SessionID() != "codex-thread-1" {
		t.Fatalf("fake provider session identity changed to %q", session.SessionID())
	}

	// Idempotent retry with the same selection is accepted.
	retry := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	retry.Header.Set("Content-Type", "application/json")
	retryResp := httptest.NewRecorder()
	server.ServeHTTP(retryResp, retry)
	if retryResp.Code != http.StatusAccepted {
		t.Fatalf("idempotent retry status = %d body %s", retryResp.Code, retryResp.Body.String())
	}
	// Same request_id with a different model/effort must 409.
	conflictBody := `{
		"request_id":"turn-select-1",
		"message":"use a stronger model",
		"model_provider_id":` + quoteJSON(provider.ID) + `,
		"model":"gpt-strong",
		"reasoning_effort":"max"
	}`
	conflictReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(conflictBody))
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictResp := httptest.NewRecorder()
	server.ServeHTTP(conflictResp, conflictReq)
	if conflictResp.Code != http.StatusConflict {
		t.Fatalf("selection conflict status = %d body %s", conflictResp.Code, conflictResp.Body.String())
	}
	if !strings.Contains(conflictResp.Body.String(), "turn selection") {
		t.Fatalf("expected turn selection conflict, body %s", conflictResp.Body.String())
	}

	server.harness.StopAndWait(created.ID, 2*time.Second)
}

func TestCodexNativeSteerAlwaysSendsCompleteResolvedSelection(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "codex-thread-complete",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &effortProviderSessionFactory{session: session, adapter: adapter}

	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
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
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-test",
		SandboxImage: "cyberpenda:test", ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForTaskRunning(t, server, created.ID)
	waitForProviderRequests(t, session, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(`{
		"request_id":"turn-default-1",
		"message":"continue"
	}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("steer status = %d body %s", resp.Code, resp.Body.String())
	}
	waitForProviderRequests(t, session, 2)
	last := session.LastRequests()[1]
	if last.ModelProviderID != provider.ID || last.Model != "gpt-test" || last.RequestedReasoningEffort != "low" {
		t.Fatalf("resolved selection incomplete: %#v", last)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID, nil)
	detailResp := httptest.NewRecorder()
	server.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("detail status = %d body %s", detailResp.Code, detailResp.Body.String())
	}
	var detail struct {
		RuntimeControls struct {
			TurnSelection *struct {
				ModelProviderID string `json:"model_provider_id"`
				Model           string `json:"model"`
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"turn_selection"`
		} `json:"runtime_controls"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.RuntimeControls.TurnSelection == nil {
		t.Fatal("expected turn_selection on runtime_controls")
	}
	if detail.RuntimeControls.TurnSelection.ModelProviderID != provider.ID ||
		detail.RuntimeControls.TurnSelection.Model != "gpt-test" ||
		detail.RuntimeControls.TurnSelection.ReasoningEffort != "low" {
		t.Fatalf("turn_selection = %#v", detail.RuntimeControls.TurnSelection)
	}

	server.harness.StopAndWait(created.ID, 2*time.Second)
}

func TestCodexNativeSteerRejectsProviderChangeWithoutRecordingConversation(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "codex-thread-provider",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
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
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"provider-switch-1",
		"message":"switch provider",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model_override":"m2",
		"reasoning_effort":"max"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "restart") {
		t.Fatalf("expected restart guidance, body %s", resp.Body.String())
	}
	if len(session.LastRequests()) != 0 {
		t.Fatalf("provider change must not reach live session: %#v", session.LastRequests())
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == task.EventKindConversation {
			t.Fatalf("provider change must not record conversation before restart: %#v", event)
		}
	}
}
