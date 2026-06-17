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

func TestSteerTaskRecordsDirectiveAndRuntimeProfileSwitch(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":"fake-profile-a",
		"runner":"sandbox"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer", bytes.NewReader([]byte(`{
		"directive":"focus on http services before dns brute force",
		"runtime_profile_id":"fake-profile-b",
		"submitted_by":"operator"
	}`)))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected steer status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var steered struct {
		Event struct {
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
		} `json:"event"`
		RuntimeConfigVersion struct {
			Version          int    `json:"version"`
			RuntimeProfileID string `json:"runtime_profile_id"`
		} `json:"runtime_config_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&steered); err != nil {
		t.Fatalf("decode steer response: %v", err)
	}
	if steered.Event.Kind != "steering" {
		t.Fatalf("expected steering event, got %q", steered.Event.Kind)
	}
	if steered.Event.Payload["directive"] != "focus on http services before dns brute force" {
		t.Fatalf("expected directive payload, got %#v", steered.Event.Payload)
	}
	if steered.RuntimeConfigVersion.Version != 2 {
		t.Fatalf("expected second runtime config version, got %d", steered.RuntimeConfigVersion.Version)
	}
	if steered.RuntimeConfigVersion.RuntimeProfileID != "fake-profile-b" {
		t.Fatalf("expected switched profile, got %q", steered.RuntimeConfigVersion.RuntimeProfileID)
	}

	events := getTaskEvents(t, server, projectID, taskID)
	last := events[len(events)-1]
	if last["kind"] != "steering" {
		t.Fatalf("expected last event steering, got %#v", last)
	}
}

func TestTaskContinuationReturnsSummaryOrMechanicalHandoff(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"],"notes":"approved only"}
	}`)
	taskID := createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":"fake-profile",
		"runner":"sandbox"
	}`)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/continuation", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected continuation status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var handoff struct {
		Mode    string `json:"mode"`
		Summary *struct {
			Summary string `json:"summary"`
		} `json:"summary"`
		Handoff struct {
			Goal             string   `json:"goal"`
			RuntimeProfileID string   `json:"runtime_profile_id"`
			ScopeDomains     []string `json:"scope_domains"`
		} `json:"handoff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&handoff); err != nil {
		t.Fatalf("decode handoff: %v", err)
	}
	if handoff.Mode != "mechanical_handoff" {
		t.Fatalf("expected mechanical handoff, got %q", handoff.Mode)
	}
	if handoff.Summary != nil {
		t.Fatalf("expected no summary, got %#v", handoff.Summary)
	}
	if handoff.Handoff.Goal != "enumerate example.com" {
		t.Fatalf("expected task goal in handoff, got %q", handoff.Handoff.Goal)
	}
	if got := handoff.Handoff.ScopeDomains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope domains in handoff, got %#v", got)
	}

	putTaskSummary(t, server, projectID, taskID, `{
		"summary":"Enumerated example.com and found one web service.",
		"submitted_by":"fake"
	}`)

	resp = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/continuation", nil)
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected continuation with summary status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var summarized struct {
		Mode    string `json:"mode"`
		Summary struct {
			Summary string `json:"summary"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&summarized); err != nil {
		t.Fatalf("decode summarized continuation: %v", err)
	}
	if summarized.Mode != "summary" {
		t.Fatalf("expected summary mode, got %q", summarized.Mode)
	}
	if summarized.Summary.Summary != "Enumerated example.com and found one web service." {
		t.Fatalf("expected latest summary, got %q", summarized.Summary.Summary)
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
		{"create task", http.MethodPost, "/api/projects/" + bogus + "/tasks", `{"goal":"x","runner":"sandbox"}`},
		{"get task", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything", ""},
		{"task events", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything/events", ""},
		{"stop task", http.MethodPost, "/api/projects/" + bogus + "/tasks/anything/stop", ""},
		{"steer task", http.MethodPost, "/api/projects/" + bogus + "/tasks/anything/steer", `{"directive":"focus"}`},
		{"task continuation", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything/continuation", ""},
		{"put task summary", http.MethodPut, "/api/projects/" + bogus + "/tasks/anything/summary", `{"summary":"s"}`},
		{"get task summary", http.MethodGet, "/api/projects/" + bogus + "/tasks/anything/summary", ""},
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
