package daemon

import (
	"encoding/json"
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
	"pentest/internal/testsupport/fakeprovider"
)

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
	session := fakeprovider.New(fakeprovider.Config{
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true,
		},
	})
	closed := make(chan struct{})
	factory := &effortProviderSessionFactory{session: session, adapter: runtime.NewProviderSessionRunAdapter(session, closed)}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
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
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls:   task.RunControls{HostActivated: true},
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerHost),
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
	// Pin the prior conversation time instead of depending on wall-clock resolution.
	if _, err := server.db.Exec(`UPDATE task_events SET created_at=? WHERE task_id=? AND kind=?`, "2000-01-01T00:00:00Z", created.ID, task.EventKindConversation); err != nil {
		t.Fatal(err)
	}

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
	session := fakeprovider.New(fakeprovider.Config{
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true,
		},
	})
	closed := make(chan struct{})
	factory := &effortProviderSessionFactory{session: session, adapter: runtime.NewProviderSessionRunAdapter(session, closed)}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
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
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "deepseek-v4-flash",
		ReasoningEffort: "high", BinaryPath: binary,
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
	if _, err := server.db.Exec(`UPDATE task_events SET created_at=? WHERE task_id=? AND kind=?`, "2000-01-01T00:00:00Z", created.ID, task.EventKindConversation); err != nil {
		t.Fatal(err)
	}

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
