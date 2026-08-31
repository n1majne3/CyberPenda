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
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/task"
)

func TestSaveRuntimeProfileRollsBackWhenSkillSelectionFails(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Atomic profile save", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRecorder()
	server.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks", strings.NewReader(`{
		"type":"pentest","goal":"capture no skills","runtime_plugin_id":"fake","model_provider_id":"none","model":"fake","runner":"host","run_controls":{"host_activated":true}
	}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create direct Task status=%d body=%s", create.Code, create.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if _, err := server.skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "late-skill", Name: "Late Skill"}, Files: map[string]string{"SKILL.md": "# Late Skill"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.db.Exec(`CREATE TRIGGER fail_profile_opt_out BEFORE INSERT ON skill_profile_opt_outs BEGIN SELECT RAISE(ABORT,'forced opt-out failure'); END`); err != nil {
		t.Fatal(err)
	}
	before, err := server.profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/runtime-profile",
		strings.NewReader(`{"name":"Must roll back","confirm":true}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("save Runtime Profile status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := server.profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed save changed Runtime Profile count: %d -> %d", len(before), len(after))
	}
}

func TestCloneSnapshotForUnchangedTurnKeepsCapturedModelProvider(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	prior := runtimeconfig.RuntimeConfigurationSnapshot{
		SnapshotVersion: runtimeconfig.SnapshotVersion,
		RuntimePluginID: "codex",
		Runner:          "sandbox",
		ModelProvider: modelprovider.Snapshot{
			ModelProviderID: "deleted-provider", ModelProviderName: "Captured Provider",
			EndpointBaseURL: "https://captured.example.test/v1", Protocol: modelprovider.ProtocolOpenAIResponses,
			Model: "captured-model", APIKeyEnv: "CAPTURED_API_KEY",
		},
		TurnSelection: runtimeconfig.RuntimeTurnSelection{
			ModelProviderID: "deleted-provider", Model: "captured-model", ReasoningEffort: "high",
		},
		Settings: map[string]any{}, EnabledSkillIDs: []string{}, ConfigProjection: map[string]any{},
	}

	cloned, err := server.cloneSnapshotForTurn(prior, "", runtimeconfig.RuntimeTurnSelection{})
	if err != nil {
		t.Fatalf("clone unchanged Snapshot: %v", err)
	}
	if cloned.ModelProvider.EndpointBaseURL != prior.ModelProvider.EndpointBaseURL || cloned.ModelProvider.ModelProviderID != prior.ModelProvider.ModelProviderID {
		t.Fatalf("cloned Model Provider = %#v, want captured %#v", cloned.ModelProvider, prior.ModelProvider)
	}
}

func waitForRuntimeConfigurationTaskStatus(t *testing.T, server *Server, projectID, taskID string, want task.Status) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("get Task status=%d body=%s", response.Code, response.Body.String())
		}
		var found task.Task
		if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
			t.Fatal(err)
		}
		if found.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Task %s did not reach %s", taskID, want)
}

func runtimeConfigurationTaskEventCount(t *testing.T, server *Server, projectID, taskID string) int {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/events", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get Task events status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Events []task.Event `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return len(body.Events)
}

func TestExplicitRuntimeProfileLaunchAcceptsModelAndReasoningOverrides(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Profile override", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Advanced fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{
		Model: "profile-model", ReasoningEffort: "medium", DefaultRunner: "host",
	})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks", strings.NewReader(`{
		"type":"pentest","goal":"verify profile override","runtime_profile_id":"`+profile.ID+`",
		"model":"launch-model","reasoning_effort":"xhigh","runner":"host","run_controls":{"host_activated":true}
	}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("profile Task create status=%d body=%s", response.Code, response.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.RuntimeConfiguration == nil || created.RuntimeConfiguration.RuntimeProfileID != profile.ID || created.RuntimeConfiguration.Model != "launch-model" || created.RuntimeConfiguration.ReasoningEffort != "xhigh" {
		t.Fatalf("profile Runtime Configuration summary = %#v", created.RuntimeConfiguration)
	}
}

func TestDirectTaskLaunchDoesNotCreateRuntimeProfileAndCanBeSavedExplicitly(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Direct", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}

	before, err := server.profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	preflight := httptest.NewRecorder()
	server.ServeHTTP(preflight, httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/preflight", strings.NewReader(`{
		"runtime_plugin_id":"fake","model_provider_id":"none","model":"fake","runner":"host","run_controls":{"host_activated":true}
	}`)))
	afterPreflight, _ := server.profiles.List()
	if len(afterPreflight) != len(before) {
		t.Fatalf("Preflight changed Runtime Profile count: %d -> %d", len(before), len(afterPreflight))
	}

	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+createdProject.ID+"/tasks", strings.NewReader(`{
		"type":"pentest","goal":"verify direct Snapshot",
		"runtime_plugin_id":"fake","model_provider_id":"none","model":"fake","runner":"host","run_controls":{"host_activated":true}
	}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("direct Task create status=%d body=%s", create.Code, create.Body.String())
	}
	var created task.Task
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	afterCreate, _ := server.profiles.List()
	if len(afterCreate) != len(before) {
		t.Fatalf("direct Task changed Runtime Profile count: %d -> %d", len(before), len(afterCreate))
	}
	if created.RuntimeConfiguration == nil || created.RuntimeConfiguration.RuntimePluginID != "fake" || created.RuntimeConfiguration.Model != "fake" || created.RuntimeConfiguration.RuntimeProfileID != "" {
		t.Fatalf("direct Runtime Configuration summary = %#v", created.RuntimeConfiguration)
	}
	waitForRuntimeConfigurationTaskStatus(t, server, createdProject.ID, created.ID, task.StatusCompleted)
	beforeLockedSteer := runtimeConfigurationTaskEventCount(t, server, createdProject.ID, created.ID)
	lockedSteer := httptest.NewRecorder()
	lockedSteerRequest := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/steer/queue",
		strings.NewReader(`{"directive":"forbidden Profile switch","runtime_profile_id":"forbidden"}`))
	lockedSteerRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(lockedSteer, lockedSteerRequest)
	if lockedSteer.Code != http.StatusBadRequest || !strings.Contains(lockedSteer.Body.String(), "runtime_profile_locked") {
		t.Fatalf("locked queue steer status=%d body=%s", lockedSteer.Code, lockedSteer.Body.String())
	}
	if after := runtimeConfigurationTaskEventCount(t, server, createdProject.ID, created.ID); after != beforeLockedSteer {
		t.Fatalf("locked queue steer changed Task Event count: %d -> %d", beforeLockedSteer, after)
	}
	lockedNativeSteer := httptest.NewRecorder()
	lockedNativeSteerRequest := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/steer",
		strings.NewReader(`{"message":"forbidden Profile switch","runtime_profile_id":"forbidden"}`))
	lockedNativeSteerRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(lockedNativeSteer, lockedNativeSteerRequest)
	if lockedNativeSteer.Code != http.StatusBadRequest || !strings.Contains(lockedNativeSteer.Body.String(), "runtime_profile_locked") {
		t.Fatalf("locked native steer status=%d body=%s", lockedNativeSteer.Code, lockedNativeSteer.Body.String())
	}
	if after := runtimeConfigurationTaskEventCount(t, server, createdProject.ID, created.ID); after != beforeLockedSteer {
		t.Fatalf("locked native steer changed Task Event count: %d -> %d", beforeLockedSteer, after)
	}

	steered := httptest.NewRecorder()
	steerRequest := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/steer",
		strings.NewReader(`{"message":"continue from the captured Snapshot"}`))
	steerRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(steered, steerRequest)
	if steered.Code != http.StatusOK {
		t.Fatalf("direct Task steer status=%d body=%s", steered.Code, steered.Body.String())
	}

	locked := httptest.NewRecorder()
	server.ServeHTTP(locked, httptest.NewRequest(http.MethodPost,
		"/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/resume",
		strings.NewReader(`{"runtime_profile_id":"forbidden"}`)))
	if locked.Code != http.StatusBadRequest || !strings.Contains(locked.Body.String(), "runtime_profile_locked") {
		t.Fatalf("locked resume status=%d body=%s", locked.Code, locked.Body.String())
	}

	saved := httptest.NewRecorder()
	saveRequest := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+createdProject.ID+"/tasks/"+created.ID+"/runtime-profile",
		bytes.NewBufferString(`{"name":"Saved direct configuration","confirm":true}`))
	saveRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(saved, saveRequest)
	if saved.Code != http.StatusCreated {
		t.Fatalf("save Runtime Profile status=%d body=%s", saved.Code, saved.Body.String())
	}
	afterSave, _ := server.profiles.List()
	if len(afterSave) != len(before)+1 {
		t.Fatalf("explicit save Runtime Profile count=%d, want %d", len(afterSave), len(before)+1)
	}
}
