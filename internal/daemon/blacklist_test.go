package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListFindingsReturnsAllFindings proves the finding browser list route
// returns every finding for a project, including status and severity.
func TestListFindingsReturnsAllFindings(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"P"}`)

	postJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/findings/confirmed-1", `{
		"title":"Confirmed vuln",
		"status":"confirmed",
		"target":"x","proof":"p","impact":"i","recommendation":"r",
		"cvss_version":"4.0","cvss_vector":"CVSS:4.0/AV:N/VC:H/VI:H"
	}`)
	postJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/findings/unconfirmed-1", `{
		"title":"Maybe an issue"
	}`)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/findings", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected list findings 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Findings []struct {
			FindingKey string `json:"finding_key"`
			Title      string `json:"title"`
			Status     string `json:"status"`
			Severity   string `json:"severity"`
		} `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(body.Findings))
	}
	byKey := map[string]string{}
	for _, f := range body.Findings {
		byKey[f.FindingKey] = f.Status
	}
	if byKey["confirmed-1"] != "confirmed" {
		t.Fatalf("expected confirmed-1 status confirmed, got %q", byKey["confirmed-1"])
	}
	if byKey["unconfirmed-1"] != "unconfirmed" {
		t.Fatalf("expected unconfirmed-1 status unconfirmed, got %q", byKey["unconfirmed-1"])
	}
}

// TestListFindingsEmptyReturnsArray proves the list never returns null.
func TestListFindingsEmptyReturnsArray(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"P"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/findings", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	var body struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Findings == nil {
		t.Fatal("expected findings array, got null")
	}
}

// TestListEvidenceReturnsAllEvidence proves the evidence browser list route.
func TestListEvidenceReturnsAllEvidence(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"P"}`)
	postJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/findings/f1", `{"title":"f"}`)
	postJSON(t, server, http.MethodPost, "/api/projects/"+projectID+"/evidence", `{
		"evidence_key":"ev1","attach_to_type":"finding","attach_to_key":"f1",
		"artifact_type":"screenshot","source_path":"/task/x.png"
	}`)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/evidence", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected list evidence 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Evidence []struct {
			EvidenceKey  string `json:"evidence_key"`
			ArtifactType string `json:"artifact_type"`
			AttachToKey  string `json:"attach_to_key"`
		} `json:"evidence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Evidence) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(body.Evidence))
	}
	if body.Evidence[0].EvidenceKey != "ev1" || body.Evidence[0].AttachToKey != "f1" {
		t.Fatalf("unexpected evidence: %#v", body.Evidence[0])
	}
}
