package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLaunchTaskRunsFakeRuntimeAndStreamsEvents proves the Slice 3 tracer bullet
// through HTTP: launching a fake-runtime task from a project captures the goal,
// runtime profile, runner, and scope snapshot, and the task emits events that
// can be read back.
func TestLaunchTaskRunsFakeRuntimeAndStreamsEvents(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"],"notes":"in scope"}
	}`)

	body := []byte(`{
		"goal":"enumerate example.com",
		"runtime_profile_id":"fake-profile",
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create task status 201, got %d with body %s", resp.Code, resp.Body.String())
	}

	var created struct {
		ID               string `json:"id"`
		ProjectID        string `json:"project_id"`
		Goal             string `json:"goal"`
		Runner           string `json:"runner"`
		RuntimeProfileID string `json:"runtime_profile_id"`
		ScopeSnapshot    struct {
			Domains []string `json:"domains"`
			Notes   string   `json:"notes"`
		} `json:"scope_snapshot"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected task id")
	}
	if created.Goal != "enumerate example.com" {
		t.Fatalf("expected goal, got %q", created.Goal)
	}
	if created.Runner != "sandbox" {
		t.Fatalf("expected sandbox runner, got %q", created.Runner)
	}
	if created.RuntimeProfileID != "fake-profile" {
		t.Fatalf("expected runtime profile id, got %q", created.RuntimeProfileID)
	}
	// Scope snapshot is captured at launch.
	if got := created.ScopeSnapshot.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope snapshot domain, got %#v", got)
	}
	if created.ScopeSnapshot.Notes != "in scope" {
		t.Fatalf("expected scope snapshot notes, got %q", created.ScopeSnapshot.Notes)
	}

	// The fake runtime runs synchronously at launch, so events are present.
	events := getTaskEvents(t, server, projectID, created.ID)
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e["kind"].(string)] = true
	}
	if !kinds["lifecycle"] || !kinds["runtime_output"] {
		t.Fatalf("expected lifecycle and runtime_output events, got %#v", kinds)
	}
}

func TestTaskSummaryUpdatesAreAcceptedAndVersioned(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":"fake-profile",
		"runner":"sandbox"
	}`)

	putTaskSummary(t, server, projectID, taskID, `{
		"summary":"Found example.com as the primary target.",
		"submitted_by":"fake"
	}`)
	putTaskSummary(t, server, projectID, taskID, `{
		"summary":"Found example.com and confirmed no subdomains yet.",
		"submitted_by":"fake"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/summary", nil)
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected summary status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var body struct {
		Summary struct {
			Version int    `json:"version"`
			Summary string `json:"summary"`
		} `json:"summary"`
		Versions []struct {
			Version int    `json:"version"`
			Summary string `json:"summary"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if body.Summary.Version != 2 {
		t.Fatalf("expected latest version 2, got %d", body.Summary.Version)
	}
	if body.Summary.Summary != "Found example.com and confirmed no subdomains yet." {
		t.Fatalf("expected latest summary, got %q", body.Summary.Summary)
	}
	if len(body.Versions) != 2 {
		t.Fatalf("expected 2 summary versions, got %d", len(body.Versions))
	}
}

// getTaskEvents reads the task timeline as a list of generic maps.
func getTaskEvents(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID string) []map[string]any {
	t.Helper()
	// server is *daemon.Server; use a type assertion-free path via httptest.
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/events", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected events status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return body.Events
}

func createTask(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, body string) string {
	t.Helper()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create task status 201, got %d with body %s", resp.Code, resp.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return created.ID
}

func putTaskSummary(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, taskID, body string) {
	t.Helper()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/tasks/"+taskID+"/summary", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected put summary status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
}
