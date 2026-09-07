package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestRetiredHermesProfileRemainsReadableButCannotLaunch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a retained row from before Runtime retirement.
	_, err = db.Exec(`INSERT INTO runtime_profiles (id, name, provider, fields_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, "legacy-hermes", "Retained Hermes", "hermes", `{"custom_config_file":"skills:\n  autoload: false\n"}`, "2026-09-01T00:00:00Z", "2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	history := session.NewService(db, t.TempDir())
	retained, err := history.Create(session.CreateRequest{
		Input: "Retained Hermes conversation",
		InitialRuntime: &session.CreateContinuationRequest{
			RuntimeProfileID: "legacy-hermes", RuntimeProvider: "hermes", Runner: session.RunnerHost,
			RuntimeConfig: map[string]any{
				"snapshot_version": 1, "runtime_plugin_id": "hermes", "runner": "host",
				"runtime_profile": map[string]any{"id": "legacy-hermes", "name": "Retained Hermes"},
				"settings":        map[string]any{},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := history.LatestContinuation(retained.ID)
	if err != nil || continuation == nil {
		t.Fatalf("retained continuation: %#v, %v", continuation, err)
	}
	if _, err := history.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := history.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusStopped); err != nil {
		t.Fatal(err)
	}
	projects := project.NewService(db)
	projectHistory, err := projects.Create("Retained Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	tasks := task.NewService(db, projects)
	retainedTask, err := tasks.Create(task.CreateRequest{
		ProjectID: projectHistory.ID, Type: task.TypePentest, Goal: "Retained Hermes Task",
		RuntimeProfileID: "legacy-hermes", Runner: task.RunnerHost,
		RunControls: task.RunControls{BlackboardMode: task.BlackboardModeDisabled},
		RuntimeConfig: map[string]any{
			"snapshot_version": 1, "runtime_plugin_id": "hermes", "runner": "host",
			"runtime_profile": map[string]any{"id": "legacy-hermes", "name": "Retained Hermes"},
			"settings":        map[string]any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateStatus(retainedTask.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateStatus(retainedTask.ID, task.StatusStopped); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := daemon.NewServer(daemon.Config{DBPath: dbPath, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := request(http.MethodGet, "/api/runtime-profiles/legacy-hermes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history status %d: %s", rec.Code, rec.Body.String())
	}
	var profile runtimeprofile.Profile
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Provider != runtimeprofile.ProviderHermes || profile.Fields.CustomConfigFile != "skills:\n  autoload: false\n" {
		t.Fatalf("retained profile changed: %#v", profile)
	}
	rec = request(http.MethodPost, "/api/runtime-profiles", `{"name":"New Hermes","provider":"hermes"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	rec = request(http.MethodPost, "/api/sessions/preflight", `{"runtime_profile_id":"legacy-hermes","runner":"host","host_activated":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "runtime_configuration" && check.Status == "fail" && strings.Contains(check.Detail, "hermes") {
			found = true
		}
	}
	if result.Pass || !found {
		t.Fatalf("retired Runtime must fail preflight: %s", rec.Body.String())
	}
	rec = request(http.MethodPost, "/api/sessions", `{"input":"continue work","runtime_profile_id":"legacy-hermes","runner":"host","host_activated":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("launch status %d: %s", rec.Code, rec.Body.String())
	}
	rec = request(http.MethodGet, "/api/runtime-profiles/legacy-hermes", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Retained Hermes") {
		t.Fatalf("failed launch lost history: %s", rec.Body.String())
	}
	rec = request(http.MethodPost, "/api/sessions/"+retained.ID+"/messages", `{"message":"resume","host_activated":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "hermes") {
		t.Fatalf("resume status %d: %s", rec.Code, rec.Body.String())
	}
	rec = request(http.MethodGet, "/api/sessions/"+retained.ID, "")
	var foundSession session.Session
	if rec.Code != http.StatusOK {
		t.Fatalf("retained Session status %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &foundSession); err != nil {
		t.Fatal(err)
	}
	if foundSession.LatestContinuation == nil || foundSession.LatestContinuation.ID != continuation.ID || foundSession.LatestContinuation.RuntimeProvider != "hermes" {
		t.Fatalf("rejected resume changed the retained continuation: %#v", foundSession.LatestContinuation)
	}
	if foundSession.RuntimeControls.QueueSteerAvailable || foundSession.RuntimeControls.NativeResumeAvailable {
		t.Fatalf("retired Runtime exposes launch controls: %#v", foundSession.RuntimeControls)
	}
	taskPath := "/api/projects/" + projectHistory.ID + "/tasks/" + retainedTask.ID
	rec = request(http.MethodPost, taskPath+"/resume", `{"host_activated":true}`)
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("Task resume status %d: %s", rec.Code, rec.Body.String())
	}
	rec = request(http.MethodGet, taskPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retained Task status %d: %s", rec.Code, rec.Body.String())
	}
	var foundTask task.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &foundTask); err != nil {
		t.Fatal(err)
	}
	if foundTask.Status != task.StatusStopped || foundTask.LatestContinuation != nil {
		t.Fatalf("rejected resume changed Task history: %#v", foundTask)
	}
	if foundTask.RuntimeControls.ResumeAvailable || foundTask.RuntimeControls.QueueSteerAvailable {
		t.Fatalf("retired Task exposes launch controls: %#v", foundTask.RuntimeControls)
	}
}
