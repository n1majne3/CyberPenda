package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

var errUnsupportedEffortForTest = errors.New("reasoning effort max is not supported for this model")

// #145 primary seam: real domain services + SQLite + FakeProviderSession.
// Same-provider model/effort changes stay on the existing Codex session with a
// complete Runtime Turn Selection and no Runtime Config Version.

func TestCodexNativeSteerSameProviderModelAndEffortUsesExistingSession(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "codex-thread-1",
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
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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
	waitForHarnessActive(t, server, created.ID, true)
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
		t.Fatalf("FakeProviderSession identity changed to %q", session.SessionID())
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
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
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
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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
	waitForHarnessActive(t, server, created.ID, true)
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
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
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
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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

func TestCodexNativeSteerUnsupportedEffortFailsTurnWithoutDowngrade(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "codex-thread-effort-fail",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
		Failures: map[runtime.ProviderSessionMode]error{
			runtime.ProviderSessionModeInterruptThenReplace: errUnsupportedEffortForTest,
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
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-test", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(`{
		"request_id":"effort-fail-1",
		"message":"try max effort",
		"model_provider_id":`+quoteJSON(provider.ID)+`,
		"model":"gpt-test",
		"reasoning_effort":"max"
	}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
	}

	var failed task.Event
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		// Provider adapters emit a bare "failed" outcome first; the daemon then
		// appends the redacted public failure with error_code.
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			if event.Kind == task.EventKindSteering && event.Payload["request_id"] == "effort-fail-1" && event.Payload["outcome"] == "failed" && event.Payload["error_code"] != nil {
				failed = event
				return true
			}
		}
		return false
	})
	if failed.Payload["error_code"] != "unsupported_reasoning_effort" {
		t.Fatalf("error_code = %#v, want unsupported_reasoning_effort", failed.Payload["error_code"])
	}
	if failed.Payload["error"] != "requested reasoning effort is not supported" {
		t.Fatalf("public error = %#v", failed.Payload["error"])
	}
	// Raw provider text must not leak into any public failed Task Event.
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Payload["request_id"] != "effort-fail-1" {
			continue
		}
		raw := fmt.Sprint(event.Payload)
		if strings.Contains(raw, errUnsupportedEffortForTest.Error()) || strings.Contains(raw, "not supported for this model") {
			t.Fatalf("raw provider error leaked into Task Event: %#v", event.Payload)
		}
	}
	requests := session.LastRequests()
	if len(requests) != 1 {
		t.Fatalf("expected single attempt without downgrade retry, got %#v", requests)
	}
	if requests[0].RequestedReasoningEffort != "max" {
		t.Fatalf("effort rewritten to %q", requests[0].RequestedReasoningEffort)
	}
}

func TestCurrentTurnSelectionUsesCapturedSnapshotNotEmptyProfileFields(t *testing.T) {
	// Launch-resolved / legacy profiles may leave ModelProviderID empty on the
	// Profile while the captured Runtime Config snapshot holds the real values.
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		// Intentionally empty ModelProviderID / ModelOverride on the Profile.
		Model: "legacy-model", ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, map[string]any{
		"runtime_profile_id": profile.ID,
		"model_provider_snapshot": map[string]any{
			"model_provider_id": "snap-provider",
			"model":             "snap-model",
		},
		"requested_reasoning_effort": "medium",
	}); err != nil {
		t.Fatal(err)
	}

	selection, err := server.currentTurnSelection(created)
	if err != nil {
		t.Fatal(err)
	}
	if selection.ModelProviderID != "snap-provider" {
		t.Fatalf("provider from snapshot = %q, want snap-provider", selection.ModelProviderID)
	}
	if selection.Model != "snap-model" {
		t.Fatalf("model from snapshot = %q, want snap-model", selection.Model)
	}
	if selection.RequestedReasoningEffort != "medium" {
		t.Fatalf("effort = %q, want medium", selection.RequestedReasoningEffort)
	}

	// Same provider as snapshot must not be treated as a provider switch.
	resolved, ok := server.resolveNativeTurnSelection(httptest.NewRecorder(), created, taskContinuationSelectionInput{
		ModelProviderID: "snap-provider",
		Model:           "snap-model-2",
		ReasoningEffort: "high",
	})
	if !ok {
		t.Fatal("same-provider selection from snapshot rejected")
	}
	if resolved.Model != "snap-model-2" || resolved.RequestedReasoningEffort != "high" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestNativeSteerIdempotencyTreatsMissingPriorEffortAsHigh(t *testing.T) {
	// Legacy conversation events may omit effort. A retry that resolves to high
	// must not false-conflict.
	prior := task.Event{
		Kind: task.EventKindConversation,
		Payload: task.EventPayload{
			"text": "continue", "model_provider_id": "p1", "model": "m1",
			// no requested_reasoning_effort
		},
	}
	if conflict := nativeSteerIdempotencyConflict(prior, "continue", runtime.ProviderSessionRequest{
		ModelProviderID: "p1", Model: "m1", RequestedReasoningEffort: "high",
	}); conflict != "" {
		t.Fatalf("unexpected conflict for legacy high default: %s", conflict)
	}
	if conflict := nativeSteerIdempotencyConflict(prior, "continue", runtime.ProviderSessionRequest{
		ModelProviderID: "p1", Model: "m1", RequestedReasoningEffort: "max",
	}); conflict == "" {
		t.Fatal("expected conflict when retrying with different effort")
	}
}

func TestQueueSteerModelOnlySelectionCreatesConfigVersion(t *testing.T) {
	// hasSelection must include selectedModel(), not only provider/profile.
	root := t.TempDir()
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-old", ReasoningEffort: "high", BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	versionsBefore, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"directive":"use stronger model","model":"gpt-new"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer/queue", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("queue status = %d body %s", resp.Code, resp.Body.String())
	}
	var queued struct {
		RuntimeConfigVersion *struct {
			Config map[string]any `json:"config"`
		} `json:"runtime_config_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if queued.RuntimeConfigVersion == nil {
		t.Fatal("model-only selection must create Runtime Config Version")
	}
	if queued.RuntimeConfigVersion.Config["model"] != "gpt-new" && queued.RuntimeConfigVersion.Config["model_override"] != "gpt-new" {
		t.Fatalf("config model = %#v", queued.RuntimeConfigVersion.Config)
	}
	versionsAfter, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter) <= len(versionsBefore) {
		t.Fatalf("expected new config version for model-only selection")
	}
}

func TestQueueSteerProviderChangeCreatesConfigVersionAndKeepsMessage(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
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
		BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	versionsBefore, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	body := `{
		"directive":"continue with alternate",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2",
		"reasoning_effort":"max"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer/queue", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("queue status = %d body %s", resp.Code, resp.Body.String())
	}
	var queued struct {
		Event struct {
			Payload map[string]any `json:"payload"`
		} `json:"event"`
		RuntimeConfigVersion *struct {
			Version int            `json:"version"`
			Config  map[string]any `json:"config"`
		} `json:"runtime_config_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if queued.Event.Payload["directive"] != "continue with alternate" {
		t.Fatalf("queued message dropped: %#v", queued.Event.Payload)
	}
	if queued.Event.Payload["model"] != "m2" {
		t.Fatalf("queued event model = %#v", queued.Event.Payload["model"])
	}
	if queued.RuntimeConfigVersion == nil {
		t.Fatal("provider change must create Runtime Config Version")
	}
	if queued.RuntimeConfigVersion.Config["model_provider_id"] != alternate.ID {
		t.Fatalf("config model_provider_id = %#v", queued.RuntimeConfigVersion.Config)
	}
	if queued.RuntimeConfigVersion.Config["model"] != "m2" && queued.RuntimeConfigVersion.Config["model_override"] != "m2" {
		t.Fatalf("config model fields = %#v", queued.RuntimeConfigVersion.Config)
	}
	if got := queued.RuntimeConfigVersion.Config["requested_reasoning_effort"]; got != "max" {
		t.Fatalf("config requested_reasoning_effort = %#v, want max", got)
	}
	versionsAfter, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter) <= len(versionsBefore) {
		t.Fatalf("expected new config version, before=%d after=%d", len(versionsBefore), len(versionsAfter))
	}
}

// #146 primary seam for Claude Code: same FakeProviderSession path as Codex,
// with a Claude Runtime Profile so acceptance proves shared turn selection
// reaches the Claude provider-session request boundary.

func TestClaudeNativeSteerSameProviderModelAndEffortUsesExistingSession(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "claude-session-1",
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
		Name: "Anthropic Primary", BaseURL: "https://api.anthropic.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-sonnet", "claude-opus"}, DefaultModel: "claude-sonnet"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-ant-test")
	profile, err := server.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "claude-sonnet",
		SandboxImage: "cyberpenda:test", ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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
	waitForHarnessActive(t, server, created.ID, true)
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
		"request_id":"claude-turn-select-1",
		"message":"use a stronger model",
		"model_provider_id":` + quoteJSON(provider.ID) + `,
		"model":"claude-opus",
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
	if last.Model != "claude-opus" {
		t.Fatalf("model = %q, want claude-opus", last.Model)
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
		t.Fatalf("RuntimeConfigVersions changed on same-provider native Claude turn: before=%d after=%d", len(versionsBefore), len(versionsAfter))
	}

	eventsAfter, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	conversationCount := 0
	for _, event := range eventsAfter {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "claude-turn-select-1" {
			conversationCount++
			if event.Payload["model_provider_id"] != provider.ID {
				t.Fatalf("conversation missing model_provider_id: %#v", event.Payload)
			}
			if event.Payload["model"] != "claude-opus" {
				t.Fatalf("conversation missing model: %#v", event.Payload)
			}
			if event.Payload["requested_reasoning_effort"] != "xhigh" {
				t.Fatalf("conversation missing requested_reasoning_effort: %#v", event.Payload)
			}
		}
		if event.Kind != task.EventKindConversation && event.Kind != task.EventKindSteering && event.Kind != task.EventKindLifecycle && event.Kind != task.EventKindRuntimeOutput {
			if event.Seq > lastBeforeSeq {
				t.Fatalf("unexpected new Task Event kind for Claude selection: %#v", event)
			}
		}
		if event.Kind == task.EventKindSteering && event.Payload["phase"] == "steering_requested" && event.Seq > lastBeforeSeq {
			t.Fatalf("native Claude selection must not create steering_requested Task Event: %#v", event)
		}
	}
	if conversationCount != 1 {
		t.Fatalf("conversation events for turn = %d, want 1", conversationCount)
	}

	bound, ok := server.providerSessions.get(created.ID)
	if !ok || bound.SessionID() != "claude-session-1" {
		t.Fatalf("Claude session identity changed: bound=%v id=%q want claude-session-1", ok, sessionIDOf(bound))
	}
	if session.SessionID() != "claude-session-1" {
		t.Fatalf("FakeProviderSession identity changed to %q", session.SessionID())
	}

	server.harness.StopAndWait(created.ID, 2*time.Second)
}

func TestClaudeNativeSteerAlwaysSendsCompleteResolvedSelection(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "claude-session-complete",
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
		Name: "Anthropic", BaseURL: "https://api.anthropic.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-sonnet"}, DefaultModel: "claude-sonnet"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-ant-test")
	profile, err := server.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "claude-sonnet",
		SandboxImage: "cyberpenda:test", ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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
	waitForHarnessActive(t, server, created.ID, true)
	waitForProviderRequests(t, session, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(`{
		"request_id":"claude-turn-default-1",
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
	if last.ModelProviderID != provider.ID || last.Model != "claude-sonnet" || last.RequestedReasoningEffort != "low" {
		t.Fatalf("resolved Claude selection incomplete: %#v", last)
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
		detail.RuntimeControls.TurnSelection.Model != "claude-sonnet" ||
		detail.RuntimeControls.TurnSelection.ReasoningEffort != "low" {
		t.Fatalf("turn_selection = %#v", detail.RuntimeControls.TurnSelection)
	}

	server.harness.StopAndWait(created.ID, 2*time.Second)
}

func TestClaudeNativeSteerProviderChangeRequiresRestartAndDoesNotTouchLiveSession(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "claude-session-provider",
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
		Name: "Primary", BaseURL: "https://a.example/anthropic",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/anthropic",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"claude-provider-switch-1",
		"message":"switch provider",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2",
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
		t.Fatalf("Claude provider change must not reach live session: %#v", session.LastRequests())
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

func TestClaudeNativeSteerUnsupportedEffortFailsTurnWithoutDowngrade(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "claude-session-effort-fail",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
		Failures: map[runtime.ProviderSessionMode]error{
			runtime.ProviderSessionModeInterruptThenReplace: errUnsupportedEffortForTest,
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
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/anthropic",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-haiku"}, DefaultModel: "claude-haiku"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "claude-haiku", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
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
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(`{
		"request_id":"claude-effort-fail-1",
		"message":"try max effort",
		"model_provider_id":`+quoteJSON(provider.ID)+`,
		"model":"claude-haiku",
		"reasoning_effort":"max"
	}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
	}

	var failed task.Event
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			if event.Kind == task.EventKindSteering && event.Payload["request_id"] == "claude-effort-fail-1" && event.Payload["outcome"] == "failed" && event.Payload["error_code"] != nil {
				failed = event
				return true
			}
		}
		return false
	})
	if failed.Payload["error_code"] != "unsupported_reasoning_effort" {
		t.Fatalf("error_code = %#v, want unsupported_reasoning_effort", failed.Payload["error_code"])
	}
	if failed.Payload["error"] != "requested reasoning effort is not supported" {
		t.Fatalf("public error = %#v", failed.Payload["error"])
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Payload["request_id"] != "claude-effort-fail-1" {
			continue
		}
		raw := fmt.Sprint(event.Payload)
		if strings.Contains(raw, errUnsupportedEffortForTest.Error()) || strings.Contains(raw, "not supported for this model") {
			t.Fatalf("raw provider error leaked into Task Event: %#v", event.Payload)
		}
	}
	requests := session.LastRequests()
	if len(requests) != 1 {
		t.Fatalf("expected single attempt without downgrade retry, got %#v", requests)
	}
	if requests[0].RequestedReasoningEffort != "max" {
		t.Fatalf("effort rewritten to %q", requests[0].RequestedReasoningEffort)
	}
}

func waitForProviderRequests(t *testing.T, session *runtime.FakeProviderSession, min int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(session.LastRequests()) >= min {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d provider requests; got %#v", min, session.LastRequests())
}

func sessionIDOf(session runtime.ProviderSession) string {
	if session == nil {
		return ""
	}
	return session.SessionID()
}

// TestPiNativeSteerAcceptsCrossProviderWithoutRestart proves ADR 0015: a Pi
// runtime that already projected multiple Model Providers switches providers
// natively (no Config Projection restart) and carries the full turn selection.
func TestPiNativeSteerAcceptsCrossProviderWithoutRestart(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "pi-session-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true,
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
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(primary.APIKeyEnv, "sk-a")
	t.Setenv(alternate.APIKeyEnv, "sk-b")

	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	// Capture the projected multi-provider set the way Config Projection does.
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, map[string]any{
		"model_provider_id":            primary.ID,
		"model":                        "m1",
		"requested_reasoning_effort":   "high",
		"projected_model_provider_ids": []string{primary.ID, alternate.ID},
		"model_provider_snapshot": map[string]any{
			"model_provider_id": primary.ID,
			"model":             "m1",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	versionsBefore, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"pi-provider-switch-1",
		"message":"switch to alternate",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2",
		"reasoning_effort":"xhigh"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
	}

	waitForProviderRequests(t, session, 1)
	requests := session.LastRequests()
	last := requests[len(requests)-1]
	if last.ModelProviderID != alternate.ID {
		t.Fatalf("model_provider_id = %q, want %q", last.ModelProviderID, alternate.ID)
	}
	if last.Model != "m2" {
		t.Fatalf("model = %q, want m2", last.Model)
	}
	if last.RequestedReasoningEffort != "xhigh" {
		t.Fatalf("effort = %q, want xhigh", last.RequestedReasoningEffort)
	}

	versionsAfter, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versionsAfter) != len(versionsBefore) {
		t.Fatalf("Pi cross-provider native turn must not mint Runtime Config Version: before=%d after=%d", len(versionsBefore), len(versionsAfter))
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundConversation := false
	for _, event := range events {
		if event.Kind == task.EventKindConversation && event.Payload["request_id"] == "pi-provider-switch-1" {
			foundConversation = true
			if event.Payload["model_provider_id"] != alternate.ID || event.Payload["model"] != "m2" {
				t.Fatalf("conversation selection = %#v", event.Payload)
			}
			if event.Payload["requested_reasoning_effort"] != "xhigh" {
				t.Fatalf("conversation effort = %#v", event.Payload["requested_reasoning_effort"])
			}
			if event.Payload["delivery"] != "native_steer" {
				t.Fatalf("delivery = %#v", event.Payload["delivery"])
			}
		}
	}
	if !foundConversation {
		t.Fatal("expected native_steer conversation event")
	}
}

// TestPiNativeSteerRejectsProviderOutsideProjectedSet keeps the projected set
// fixed until the next Config Projection / Runtime restart.
func TestPiNativeSteerRejectsProviderOutsideProjectedSet(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "pi-session-2",
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
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	outside, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Outside", BaseURL: "https://out.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"out"}, DefaultModel: "out"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, map[string]any{
		"model_provider_id":            primary.ID,
		"projected_model_provider_ids": []string{primary.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"pi-outside-1",
		"message":"use outside",
		"model_provider_id":` + quoteJSON(outside.ID) + `,
		"model":"out"
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
		t.Fatalf("outside provider must not reach live session: %#v", session.LastRequests())
	}
}

// TestPiNativeSteerFailsClosedWithoutProjectedSet records ADR 0015
// fixed-at-restart semantics: legacy tasks missing projected_model_provider_ids
// cannot perform arbitrary native cross-provider selection.
func TestPiNativeSteerFailsClosedWithoutProjectedSet(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "pi-session-legacy",
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
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	// Legacy runtime config: no projected_model_provider_ids field.
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, map[string]any{
		"model_provider_id": primary.ID,
		"model":             "m1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.BindProviderSession(created.ID, session); err != nil {
		t.Fatal(err)
	}

	body := `{
		"request_id":"pi-legacy-cross-1",
		"message":"switch without projected set",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409 fail-closed", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "restart") {
		t.Fatalf("expected restart guidance, body %s", resp.Body.String())
	}
	if len(session.LastRequests()) != 0 {
		t.Fatalf("legacy missing projected set must not native-switch providers: %#v", session.LastRequests())
	}
}

// TestPiV2LaunchDoesNotDeadlockListingGlobalProviders proves the daemon lists
// global Model Providers before CreateContinuation and projects from the
// immutable snapshot, so Pi launch never re-enters modelProviders.Service
// while the continuity transaction holds SQLite locks.
func TestPiV2LaunchDoesNotDeadlockListingGlobalProviders(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1", "m2"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-alt"}, DefaultModel: "claude-alt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(primary.APIKeyEnv, "sk-primary")
	t.Setenv(alternate.APIKeyEnv, "sk-alternate")

	profile, err := server.profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", BinaryPath: "/usr/bin/pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
		if err != nil {
			done <- err
			return
		}
		if plan.GlobalModelProviderSnapshot == nil {
			done <- fmt.Errorf("launch plan missing GlobalModelProviderSnapshot")
			return
		}
		if len(plan.GlobalModelProviderSnapshot.Providers) < 2 {
			done <- fmt.Errorf("snapshot providers = %d, want >= 2", len(plan.GlobalModelProviderSnapshot.Providers))
			return
		}
		_, bound, err := server.prepareBlackboardV2ContinuationLaunch(created, plan, created.Goal)
		if err != nil {
			done <- err
			return
		}
		// Projected set must be fixed into captured runtime config.
		ids, _ := bound.CapturedRuntimeConfig["projected_model_provider_ids"].([]string)
		if len(ids) == 0 {
			if raw, ok := bound.CapturedRuntimeConfig["projected_model_provider_ids"].([]any); ok {
				for _, v := range raw {
					ids = append(ids, fmt.Sprint(v))
				}
			}
		}
		if len(ids) < 2 {
			done <- fmt.Errorf("projected_model_provider_ids = %#v", bound.CapturedRuntimeConfig["projected_model_provider_ids"])
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Pi v2 launch: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Pi v2 launch deadlocked listing global Model Providers inside CreateContinuation")
	}
}

func TestHasSelectionIncludesReasoningEffortOnly(t *testing.T) {
	if !(taskContinuationSelectionInput{ReasoningEffort: "xhigh"}).hasSelection() {
		t.Fatal("effort-only input must count as a Runtime Turn Selection")
	}
	if (taskContinuationSelectionInput{}).hasSelection() {
		t.Fatal("empty input must not count as a selection")
	}
}

// Resume must retain an explicit reasoning_effort from the request body on
// turn_selection even when an older conversation turn still says high.
func TestResumeReasoningEffortOnlyUpdatesTurnSelection(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test", ReasoningEffort: "high", BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate turn 1 conversation selection stuck at high (older than the
	// resume config that follows).
	if _, err := server.tasks.AppendEvent(created.ID, task.EventKindConversation, task.EventPayload{
		"role":                       "user",
		"text":                       "first turn",
		"model_provider_id":          "",
		"model":                      "gpt-test",
		"requested_reasoning_effort": "high",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	// Ensure conversation timestamp is strictly older than the resume config.
	time.Sleep(5 * time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/resume", strings.NewReader(`{
		"reasoning_effort":"xhigh"
	}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		RuntimeControls struct {
			TurnSelection *struct {
				ReasoningEffort string `json:"reasoning_effort"`
				Model           string `json:"model"`
			} `json:"turn_selection"`
		} `json:"runtime_controls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RuntimeControls.TurnSelection == nil {
		t.Fatal("expected turn_selection on resume response")
	}
	if body.RuntimeControls.TurnSelection.ReasoningEffort != "xhigh" {
		t.Fatalf("resume turn_selection.reasoning_effort = %q, want xhigh", body.RuntimeControls.TurnSelection.ReasoningEffort)
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
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"turn_selection"`
		} `json:"runtime_controls"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.RuntimeControls.TurnSelection == nil || detail.RuntimeControls.TurnSelection.ReasoningEffort != "xhigh" {
		t.Fatalf("GET turn_selection after resume = %#v, want xhigh", detail.RuntimeControls.TurnSelection)
	}

	// Stop harness so the test process can exit cleanly.
	if server.harness != nil {
		server.harness.StopAndWait(created.ID, 2*time.Second)
	}
}

func TestResumeFullSelectionKeepsXHighOverOlderConversation(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
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
		Name: "Hub", BaseURL: "https://hub.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"deepseek-v4-flash"}, DefaultModel: "deepseek-v4-flash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_API_KEY", "sk-test")
	// Bind env for launch-ready if needed — profile uses explicit model.
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "deepseek-v4-flash",
		ReasoningEffort: "high", BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.AppendEvent(created.ID, task.EventKindConversation, task.EventPayload{
		"role":                       "user",
		"text":                       "first turn",
		"model_provider_id":          provider.ID,
		"model":                      "deepseek-v4-flash",
		"requested_reasoning_effort": "high",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	body := fmt.Sprintf(`{
		"model_provider_id":%q,
		"model":"deepseek-v4-flash",
		"reasoning_effort":"xhigh"
	}`, provider.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks/"+created.ID+"/resume", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body %s", resp.Code, resp.Body.String())
	}
	var accepted struct {
		RuntimeControls struct {
			TurnSelection *struct {
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"turn_selection"`
		} `json:"runtime_controls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.RuntimeControls.TurnSelection == nil || accepted.RuntimeControls.TurnSelection.ReasoningEffort != "xhigh" {
		t.Fatalf("resume turn_selection = %#v, want xhigh", accepted.RuntimeControls.TurnSelection)
	}
	if server.harness != nil {
		server.harness.StopAndWait(created.ID, 2*time.Second)
	}
}
