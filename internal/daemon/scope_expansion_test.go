package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
)

func TestScopeExpansionHTTPRequiresApprovalBeforeItChangesScope(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version", DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("create Server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectID := createProject(t, server, `{"name":"Engagement","kind":"pentest","scope":{"domains":["example.com"]}}`)

	propose := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/scope-expansions", strings.NewReader(`{
		"addition":{"domains":["api.example.com"],"ports":["8443"]},
		"discovery_source":"Project Fact fact:api",
		"reason":"New application endpoint",
		"risk":"Adds authenticated testing"
	}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, propose)
	if response.Code != http.StatusCreated {
		t.Fatalf("propose status %d body %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "origin_") || strings.Contains(response.Body.String(), "continuation_id") {
		t.Fatalf("ordinary Scope Expansion response exposed Trusted Origin: %s", response.Body.String())
	}
	var proposal struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&proposal); err != nil || proposal.Status != "proposed" {
		t.Fatalf("decode proposal = %#v, %v", proposal, err)
	}

	getBefore := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID, nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, getBefore)
	if strings.Contains(response.Body.String(), "api.example.com") {
		t.Fatalf("Scope changed before approval: %s", response.Body.String())
	}

	approve := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/scope-expansions/"+proposal.ID+"/approve", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, approve)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"approved"`) {
		t.Fatalf("approve status %d body %s", response.Code, response.Body.String())
	}

	getAfter := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID, nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, getAfter)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "api.example.com") || !strings.Contains(response.Body.String(), "8443") {
		t.Fatalf("approved Scope = status %d body %s", response.Code, response.Body.String())
	}
}
