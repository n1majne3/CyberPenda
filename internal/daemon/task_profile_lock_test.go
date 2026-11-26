package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runtimeprofile"
)

func TestSteerTaskRejectsRuntimeProfileSwitch(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileA := createRuntimeProfile(t, server, `{"name":"Fake A","provider":"fake"}`)
	profileB := createRuntimeProfile(t, server, `{"name":"Fake B","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileA)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus on http services before dns brute force",
		"runtime_profile_id":`+quoteJSON(profileB)+`,
		"submitted_by":"operator"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "runtime_profile_locked") {
		t.Fatalf("expected locked profile status 400, got %d with body %s", resp.Code, resp.Body.String())
	}
}

// TestTaskRoutesRejectUnknownProject pins the cross-cutting invariant that
// every project-scoped task route returns 404 for a project that does not
// exist, the same way the blackboard / credential / dashboard routes do.
// Without an explicit project-exists check the list route returns 200 with an
// empty body and the {task_id} routes only guard against cross-project access
// to a *real* task, never against a bogus project id.
func TestTaskRoutesRejectUnknownProject(t *testing.T) {
	server := newDaemon(t)
	const bogus = "does-not-exist"

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list tasks", http.MethodGet, "/api/projects/" + bogus + "/tasks", ""},
		{"create task", http.MethodPost, "/api/projects/" + bogus + "/tasks", `{"type":"pentest","goal":"x","runner":"sandbox"}`},
		{"get task", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything", ""},
		{"task events", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything/events", ""},
		{"stop task", http.MethodPost, "/api/projects/" + bogus + "/tasks/anything/stop", ""},
		{"steer task", http.MethodPost, "/api/projects/" + bogus + "/tasks/anything/steer", `{"directive":"focus"}`},
		{"resume", http.MethodPost, "/api/projects/" + bogus + "/tasks/anything/resume", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Reader
			if tc.body == "" {
				body = bytes.NewReader(nil)
			} else {
				body = bytes.NewReader([]byte(tc.body))
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			server.ServeHTTP(resp, req)

			if resp.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for %s on unknown project, got %d with body %s", tc.name, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestFreshResumeRejectsCrossProjectTaskWithoutEffect(t *testing.T) {
	server := newDaemon(t)
	sourceProjectID := createProject(t, server, `{"name":"Source","scope":{"domains":["source.example"]}}`)
	otherProjectID := createProject(t, server, `{"name":"Other","scope":{"domains":["other.example"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, sourceProjectID, `{
		"type":"pentest","goal":"enumerate source.example",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, sourceProjectID, taskID, "completed")
	eventsBefore := getTaskEvents(t, server, sourceProjectID, taskID)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+otherProjectID+"/tasks/"+taskID+"/resume", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("cross-Project fresh resume status = %d, want 404; body=%s", resp.Code, resp.Body.String())
	}

	eventsAfter := getTaskEvents(t, server, sourceProjectID, taskID)
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("cross-Project handoff changed source Task events: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/projects/"+sourceProjectID+"/tasks/"+taskID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get source Task after cross-Project handoff: %d %s", response.Code, response.Body.String())
	}
	var found struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		t.Fatalf("decode source Task after cross-Project handoff: %v", err)
	}
	if found.Status != "completed" {
		t.Fatalf("source Task status after cross-Project handoff = %q, want completed", found.Status)
	}
}

func TestSteerTaskRejectsRuntimeProviderChange(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	fakeProfileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	codexProfileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex","fields":{"model":"gpt-test"}}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(fakeProfileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"switch runtimes",
		"runtime_profile_id":`+quoteJSON(codexProfileID)+`
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected steer status 400 for provider change, got %d with body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "runtime_profile_locked") {
		t.Fatalf("expected locked profile error, got %s", resp.Body.String())
	}
}

func TestResumeTaskKeepsSnapshotAfterLockedRuntimeProfileSwitch(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileA := createRuntimeProfile(t, server, `{"name":"Fake A","provider":"fake"}`)
	profileB := createRuntimeProfile(t, server, `{"name":"Fake B","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileA)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	steerResp := httptest.NewRecorder()
	steerReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"use profile b next",
		"runtime_profile_id":`+quoteJSON(profileB)+`
	}`)))
	steerReq.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(steerResp, steerReq)
	if steerResp.Code != http.StatusBadRequest || !strings.Contains(steerResp.Body.String(), "runtime_profile_locked") {
		t.Fatalf("expected locked profile status 400, got %d with body %s", steerResp.Code, steerResp.Body.String())
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resp.Code, resp.Body.String())
	}

	var resumed struct {
		LatestContinuation *struct {
			RuntimeProfileID string `json:"runtime_profile_id"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resumed); err != nil {
		t.Fatalf("decode resumed task: %v", err)
	}
	if resumed.LatestContinuation == nil {
		t.Fatal("expected latest continuation in resume response")
	}
	if resumed.LatestContinuation.RuntimeProfileID != profileA {
		t.Fatalf("expected captured continuation profile %q, got %q", profileA, resumed.LatestContinuation.RuntimeProfileID)
	}
	waitForTaskStatus(t, server, projectID, taskID, "completed")
}

func TestQueueSteerRejectsSameProviderRuntimeProfileSwitch(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileA := createRuntimeProfile(t, server, `{"name":"Fake A","provider":"fake"}`)
	profileB := createRuntimeProfile(t, server, `{"name":"Fake B","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileA)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	steerResp := httptest.NewRecorder()
	steerReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer/queue", bytes.NewReader([]byte(`{
		"directive":"use profile b next",
		"runtime_profile_id":`+quoteJSON(profileB)+`
	}`)))
	steerReq.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(steerResp, steerReq)
	if steerResp.Code != http.StatusBadRequest || !strings.Contains(steerResp.Body.String(), "runtime_profile_locked") {
		t.Fatalf("expected locked profile status 400, got %d with body %s", steerResp.Code, steerResp.Body.String())
	}

	resumeResp := httptest.NewRecorder()
	resumeReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/resume", nil)
	server.ServeHTTP(resumeResp, resumeReq)
	if resumeResp.Code != http.StatusAccepted {
		t.Fatalf("expected resume status 202, got %d with body %s", resumeResp.Code, resumeResp.Body.String())
	}
	var resumed struct {
		LatestContinuation *struct {
			RuntimeProfileID string `json:"runtime_profile_id"`
		} `json:"latest_continuation"`
	}
	if err := json.NewDecoder(resumeResp.Body).Decode(&resumed); err != nil {
		t.Fatalf("decode resumed task: %v", err)
	}
	if resumed.LatestContinuation == nil || resumed.LatestContinuation.RuntimeProfileID != profileA {
		t.Fatalf("expected captured runtime profile %q, got %#v", profileA, resumed.LatestContinuation)
	}
	waitForTaskStatus(t, server, projectID, taskID, "completed")
}

func TestQueueSteerRecordsSameRuntimeModelSelection(t *testing.T) {
	runtimeRoot := t.TempDir()
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		DisableBuiltinSkills: true,
	})
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	binary := filepath.Join(t.TempDir(), "codex-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho codex-provider:$*\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}
	profileID := createLocalRuntimeProfile(t, server, "Codex Test", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		BinaryPath: binary,
		Model:      "gpt-test",
	})
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"host",
		"run_controls":{"host_activated":true}
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")

	t.Setenv("MIMO_API_KEY", "sk-test")
	providerID := createModelProvider(t, server, `{
		"name":"MiMo",
		"base_url":"https://api.example.test/v1",
		"protocols":["openai_responses"],
		"catalog":{"manual":["mimo-v2-flash","mimo-v2-pro"],"default_model":"mimo-v2-flash"}
	}`)

	steerResp := httptest.NewRecorder()
	steerReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer/queue", bytes.NewReader([]byte(`{
		"directive":"continue with mimo pro",
		"model_provider_id":`+quoteJSON(providerID)+`,
		"model":"mimo-v2-pro"
	}`)))
	steerReq.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(steerResp, steerReq)
	if steerResp.Code != http.StatusOK {
		t.Fatalf("expected queue steer status 200, got %d with body %s", steerResp.Code, steerResp.Body.String())
	}
	var queued struct {
		RuntimeConfigVersion *struct {
			RuntimeProfileID string         `json:"runtime_profile_id"`
			Config           map[string]any `json:"config"`
		} `json:"runtime_config_version"`
	}
	if err := json.NewDecoder(steerResp.Body).Decode(&queued); err != nil {
		t.Fatalf("decode queue steer: %v", err)
	}
	if queued.RuntimeConfigVersion == nil {
		t.Fatal("expected queued runtime config version")
	}
	if queued.RuntimeConfigVersion.RuntimeProfileID != profileID {
		t.Fatalf("expected immutable profile provenance %q, got %q", profileID, queued.RuntimeConfigVersion.RuntimeProfileID)
	}
	turn, _ := queued.RuntimeConfigVersion.Config["runtime_turn_selection"].(map[string]any)
	if turn["model_provider_id"] != providerID {
		t.Fatalf("expected queued model provider %q, got %#v", providerID, queued.RuntimeConfigVersion.Config)
	}
	if turn["model"] != "mimo-v2-pro" {
		t.Fatalf("expected queued model override, got %#v", queued.RuntimeConfigVersion.Config)
	}
}
