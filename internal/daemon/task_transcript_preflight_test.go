package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTaskTranscriptEndpointProjectsRetainedEvents(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{
		"type":"pentest","goal":"map app",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	waitForEventText(t, server, projectID, taskID, "enumerating in-scope assets")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/transcript", nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected transcript status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		TaskID  string           `json:"task_id"`
		Entries []map[string]any `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if body.TaskID != taskID {
		t.Fatalf("expected task id %q, got %q", taskID, body.TaskID)
	}
	if !hasTranscriptEntry(body.Entries, "message", "user", "map app") {
		t.Fatalf("expected goal message in transcript, got %#v", body.Entries)
	}
	if !hasTranscriptEntry(body.Entries, "runtime_output", "runtime", "enumerating in-scope assets") {
		t.Fatalf("expected retained runtime output in transcript, got %#v", body.Entries)
	}
}

func TestTaskTranscriptEndpointRejectsCrossProjectTask(t *testing.T) {
	server := newDaemon(t)
	projectA := createProject(t, server, `{"name":"A","scope":{"domains":["a.example"]}}`)
	projectB := createProject(t, server, `{"name":"B","scope":{"domains":["b.example"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectA, `{
		"type":"pentest","goal":"map app",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectB+"/tasks/"+taskID+"/transcript", nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected transcript cross-project status 404, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func TestLaunchTaskFailsPreflightWhenRuntimeProfileMissing(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(`{
		"type":"pentest","goal":"enumerate example.com",
		"runtime_profile_id":"missing-profile",
		"runner":"sandbox"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected task launch preflight failure status 400, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode preflight launch failure: %v", err)
	}
	if body.Error != "runtime profile not found" {
		t.Fatalf("expected missing profile error, got %q", body.Error)
	}

	listResp := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks", nil)
	server.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list tasks status 200, got %d with body %s", listResp.Code, listResp.Body.String())
	}
	var listed struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode tasks list: %v", err)
	}
	if len(listed.Tasks) != 0 {
		t.Fatalf("preflight failure must not create a task, got %#v", listed.Tasks)
	}
}

func TestLaunchTaskDoesNotResolveRemovedProjectRuntimeProfileDefault(t *testing.T) {
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"]},
		"defaults":{"runtime_profile":`+quoteJSON(profileID)+`,"runner":"sandbox"}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(`{
		"type":"pentest","goal":"enumerate example.com"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected create task status 400 without a launch selection, got %d with body %s", resp.Code, resp.Body.String())
	}
}
