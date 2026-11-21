package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/testsupport/fakeprovider"
)

func TestCodexNativeSteerUnsupportedEffortFailsTurnWithoutDowngrade(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "codex-thread-effort-fail",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
		Failures: map[runtime.ProviderSessionMode]error{
			runtime.ProviderSessionModeSendTurn:             errUnsupportedEffortForTest,
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
		ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox)
	captured["model_provider_snapshot"].(map[string]any)["model_provider_id"] = "snap-provider"
	captured["model_provider_snapshot"].(map[string]any)["model"] = "snap-model"
	captured["runtime_turn_selection"] = map[string]any{
		"model_provider_id": "snap-provider", "model": "snap-model", "requested_reasoning_effort": "medium",
	}
	if _, err := server.tasks.RecordRuntimeConfig(created.ID, profile.ID, captured); err != nil {
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
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Queue Model Provider", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-old", "gpt-new"}, DefaultModel: "gpt-old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "gpt-old", ReasoningEffort: "high", BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls:   task.RunControls{HostActivated: true},
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerHost),
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
	turn, _ := queued.RuntimeConfigVersion.Config["runtime_turn_selection"].(map[string]any)
	if turn["model"] != "gpt-new" {
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
	t.Setenv(primary.APIKeyEnv, "sk-primary")
	t.Setenv(alternate.APIKeyEnv, "sk-alternate")
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", ReasoningEffort: "high",
		BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls:   task.RunControls{HostActivated: true},
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerHost),
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
	turn, _ := queued.RuntimeConfigVersion.Config["runtime_turn_selection"].(map[string]any)
	if turn["model_provider_id"] != alternate.ID {
		t.Fatalf("config model_provider_id = %#v", queued.RuntimeConfigVersion.Config)
	}
	if turn["model"] != "m2" {
		t.Fatalf("config model fields = %#v", queued.RuntimeConfigVersion.Config)
	}
	if got := turn["requested_reasoning_effort"]; got != "max" {
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
