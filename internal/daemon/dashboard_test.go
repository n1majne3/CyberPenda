package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDashboardReturnsScopeStatusAndCounts proves the project dashboard summary
// endpoint surfaces scope status and the four placeholder counts. Scope status
// reflects the named assets and testing limits defined on the project.
func TestDashboardReturnsScopeStatusAndCounts(t *testing.T) {
	server := newDaemon(t)

	// Create a project with scope so the dashboard has something to summarize.
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{
			"domains":["example.com","api.example.com"],
			"urls":["https://example.com"],
			"testing_limits":["no destructive payloads"],
			"notes":"business hours"
		}
	}`)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/dashboard", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var body struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Scope     struct {
			Domains          int  `json:"domains"`
			IPs              int  `json:"ips"`
			CIDRs            int  `json:"cidrs"`
			URLs             int  `json:"urls"`
			Ports            int  `json:"ports"`
			Excluded         int  `json:"excluded"`
			HasTestingLimits bool `json:"has_testing_limits"`
			HasNotes         bool `json:"has_notes"`
			Ready            bool `json:"ready"`
		} `json:"scope"`
		Counts struct {
			Tasks    int `json:"tasks"`
			Facts    int `json:"facts"`
			Findings int `json:"findings"`
			Evidence int `json:"evidence"`
		} `json:"counts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode dashboard response: %v", err)
	}
	if body.ProjectID != projectID {
		t.Fatalf("expected project_id %q, got %q", projectID, body.ProjectID)
	}
	if body.Name != "Acme" {
		t.Fatalf("expected name Acme, got %q", body.Name)
	}
	// Scope status reflects the configured assets.
	if body.Scope.Domains != 2 {
		t.Fatalf("expected 2 domains, got %d", body.Scope.Domains)
	}
	if body.Scope.URLs != 1 {
		t.Fatalf("expected 1 url, got %d", body.Scope.URLs)
	}
	if !body.Scope.HasTestingLimits {
		t.Fatal("expected has_testing_limits true")
	}
	if !body.Scope.HasNotes {
		t.Fatal("expected has_notes true")
	}
	// A project with named assets is launch-ready.
	if !body.Scope.Ready {
		t.Fatal("expected scope ready (has at least one named asset)")
	}
	// Counts are placeholders until the task/blackboard domains land.
	if body.Counts.Tasks != 0 || body.Counts.Facts != 0 || body.Counts.Findings != 0 || body.Counts.Evidence != 0 {
		t.Fatalf("expected zero counts, got %#v", body.Counts)
	}
}

// TestDashboardScopeNotReadyForEmptyScope proves a project with no named assets
// is not launch-ready, so meaningful testing is gated on scope.
func TestDashboardScopeNotReadyForEmptyScope(t *testing.T) {
	server := newDaemon(t)

	projectID := createProject(t, server, `{"name":"Empty"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/dashboard", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d", resp.Code)
	}
	var body struct {
		Scope struct {
			Ready bool `json:"ready"`
		} `json:"scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Scope.Ready {
		t.Fatal("expected empty scope to not be ready")
	}
}

func TestDashboardCountsTasksWithoutReadingLegacyBlackboard(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)

	createTask(t, server, projectID, `{
		"goal":"enumerate example.com",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/dashboard", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected dashboard status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Counts struct {
			Tasks int `json:"tasks"`
			Facts int `json:"facts"`
		} `json:"counts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Counts.Tasks != 1 {
		t.Fatalf("expected 1 task, got %d", body.Counts.Tasks)
	}
	if body.Counts.Facts != 0 {
		t.Fatalf("expected unavailable v2 Fact count to remain zero, got %d", body.Counts.Facts)
	}
}

func TestDashboardMissingProjectReturnsNotFound(t *testing.T) {
	server := newDaemon(t)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/missing/dashboard", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected dashboard for missing project to 404, got %d", resp.Code)
	}
}
