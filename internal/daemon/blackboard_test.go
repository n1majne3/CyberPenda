package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProjectFactUpsertAppearsInCompactFactIndex proves the first trusted
// project-interface tracer: a runtime can write a reusable project fact and the
// runtime-facing fact index returns compact current context without the full
// body.
func TestProjectFactUpsertAppearsInCompactFactIndex(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"]}
	}`)

	upsertBody := []byte(`{
		"fact_key":"target:example.com",
		"category":"target",
		"summary":"example.com is the primary in-scope domain",
		"body":"Full notes with commands, observations, and reproduction context.",
		"confidence":"confirmed",
		"scope_status":"in_scope"
	}`)
	upsertReq := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/facts/target:example.com", bytes.NewReader(upsertBody))
	upsertReq.Header.Set("Content-Type", "application/json")
	upsertResp := httptest.NewRecorder()
	server.ServeHTTP(upsertResp, upsertReq)

	if upsertResp.Code != http.StatusOK {
		t.Fatalf("expected upsert status 200, got %d with body %s", upsertResp.Code, upsertResp.Body.String())
	}

	indexReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/facts/index", nil)
	indexResp := httptest.NewRecorder()
	server.ServeHTTP(indexResp, indexReq)

	if indexResp.Code != http.StatusOK {
		t.Fatalf("expected index status 200, got %d with body %s", indexResp.Code, indexResp.Body.String())
	}

	var index struct {
		Facts []map[string]any `json:"facts"`
	}
	if err := json.NewDecoder(indexResp.Body).Decode(&index); err != nil {
		t.Fatalf("decode fact index: %v", err)
	}
	if len(index.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(index.Facts))
	}
	fact := index.Facts[0]
	if fact["fact_key"] != "target:example.com" {
		t.Fatalf("expected fact key, got %#v", fact["fact_key"])
	}
	if fact["summary"] != "example.com is the primary in-scope domain" {
		t.Fatalf("expected summary, got %#v", fact["summary"])
	}
	if fact["confidence"] != "confirmed" {
		t.Fatalf("expected confidence confirmed, got %#v", fact["confidence"])
	}
	if fact["scope_status"] != "in_scope" {
		t.Fatalf("expected in_scope status, got %#v", fact["scope_status"])
	}
	if _, ok := fact["body"]; ok {
		t.Fatalf("fact index must not expose full body: %#v", fact)
	}
}

func TestProjectFactUpdatePreservesBodyWhenBodyIsEmpty(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	upsertFact(t, server, projectID, "target:example.com", `{
		"category":"target",
		"summary":"initial summary",
		"body":"Detailed reproduction and command notes",
		"confidence":"tentative"
	}`)
	upsertFact(t, server, projectID, "target:example.com", `{
		"category":"target",
		"summary":"updated summary",
		"body":"",
		"confidence":"confirmed"
	}`)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/facts/target:example.com", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected get fact status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var fact struct {
		Summary    string `json:"summary"`
		Body       string `json:"body"`
		Confidence string `json:"confidence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fact); err != nil {
		t.Fatalf("decode fact: %v", err)
	}
	if fact.Summary != "updated summary" {
		t.Fatalf("expected updated summary, got %q", fact.Summary)
	}
	if fact.Body != "Detailed reproduction and command notes" {
		t.Fatalf("expected body preserved, got %q", fact.Body)
	}
	if fact.Confidence != "confirmed" {
		t.Fatalf("expected confidence updated, got %q", fact.Confidence)
	}
}

func TestProjectFactVersionsArePreservedAcrossUpserts(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	upsertFact(t, server, projectID, "target:example.com", `{
		"category":"target",
		"summary":"initial summary",
		"body":"initial body",
		"confidence":"tentative"
	}`)
	upsertFact(t, server, projectID, "target:example.com", `{
		"category":"target",
		"summary":"confirmed summary",
		"body":"confirmed body",
		"confidence":"confirmed"
	}`)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/facts/target:example.com/versions", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected versions status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var body struct {
		Versions []struct {
			Version    int    `json:"version"`
			Summary    string `json:"summary"`
			Body       string `json:"body"`
			Confidence string `json:"confidence"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode versions: %v", err)
	}
	if len(body.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(body.Versions))
	}
	if body.Versions[0].Version != 1 || body.Versions[0].Summary != "initial summary" {
		t.Fatalf("expected initial version first, got %#v", body.Versions[0])
	}
	if body.Versions[1].Version != 2 || body.Versions[1].Summary != "confirmed summary" {
		t.Fatalf("expected confirmed version second, got %#v", body.Versions[1])
	}
}

func TestProjectFactRelationsConnectExistingFacts(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	upsertFact(t, server, projectID, "target:example.com", `{
		"category":"target",
		"summary":"example.com is in scope",
		"confidence":"confirmed"
	}`)
	upsertFact(t, server, projectID, "service:https:example.com", `{
		"category":"service",
		"summary":"HTTPS responds on example.com",
		"confidence":"confirmed"
	}`)

	body := []byte(`{
		"target_fact_key":"service:https:example.com",
		"relation":"supports",
		"summary":"The HTTPS service supports the target domain fact."
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/facts/target:example.com/relations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected relation status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var created struct {
		SourceFactKey string `json:"source_fact_key"`
		TargetFactKey string `json:"target_fact_key"`
		Relation      string `json:"relation"`
		Summary       string `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode relation: %v", err)
	}
	if created.SourceFactKey != "target:example.com" || created.TargetFactKey != "service:https:example.com" {
		t.Fatalf("expected source/target fact keys, got %#v", created)
	}
	if created.Relation != "supports" {
		t.Fatalf("expected relation supports, got %q", created.Relation)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/facts/target:example.com/relations", nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected list relations status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var listed struct {
		Relations []struct {
			TargetFactKey string `json:"target_fact_key"`
			Relation      string `json:"relation"`
		} `json:"relations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode relations: %v", err)
	}
	if len(listed.Relations) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(listed.Relations))
	}
	if listed.Relations[0].TargetFactKey != "service:https:example.com" {
		t.Fatalf("expected target fact key, got %#v", listed.Relations[0])
	}
}

func TestProjectFactRelationRejectsMissingTargetFact(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	upsertFact(t, server, projectID, "target:example.com", `{
		"category":"target",
		"summary":"example.com is in scope",
		"confidence":"confirmed"
	}`)

	body := []byte(`{
		"target_fact_key":"service:https:example.com",
		"relation":"supports",
		"summary":"Missing target should fail."
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/facts/target:example.com/relations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected relation status 404, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func TestProjectFindingCanMoveFromCVSSPendingToConfirmed(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	body := []byte(`{
		"title":"Exposed admin panel",
		"description":"The admin panel is reachable without network filtering.",
		"target":"https://example.com/admin",
		"impact":"Potential administrative access if authentication is weak.",
		"status":"unconfirmed"
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/findings/web-admin-exposed", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected pending finding status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var pending struct {
		FindingKey  string `json:"finding_key"`
		Status      string `json:"status"`
		CVSSPending bool   `json:"cvss_pending"`
		Severity    string `json:"severity"`
		Version     int    `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pending); err != nil {
		t.Fatalf("decode pending finding: %v", err)
	}
	if pending.FindingKey != "web-admin-exposed" {
		t.Fatalf("expected finding key, got %q", pending.FindingKey)
	}
	if !pending.CVSSPending {
		t.Fatal("expected CVSS pending")
	}
	if pending.Severity != "pending" {
		t.Fatalf("expected pending severity, got %q", pending.Severity)
	}
	if pending.Version != 1 {
		t.Fatalf("expected version 1, got %d", pending.Version)
	}

	body = []byte(`{
		"title":"Exposed admin panel",
		"description":"The admin panel is reachable without network filtering.",
		"target":"https://example.com/admin",
		"proof":"GET /admin returns the login panel from the public internet.",
		"impact":"Attackers can target administrative authentication directly.",
		"recommendation":"Restrict /admin to VPN or trusted source networks.",
		"status":"confirmed",
		"cvss_version":"4.0",
		"cvss_vector":"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N"
	}`)
	req = httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/findings/web-admin-exposed", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected confirmed finding status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var confirmed struct {
		Status      string `json:"status"`
		CVSSPending bool   `json:"cvss_pending"`
		Severity    string `json:"severity"`
		Version     int    `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&confirmed); err != nil {
		t.Fatalf("decode confirmed finding: %v", err)
	}
	if confirmed.Status != "confirmed" {
		t.Fatalf("expected confirmed status, got %q", confirmed.Status)
	}
	if confirmed.CVSSPending {
		t.Fatal("expected CVSS complete")
	}
	if confirmed.Severity != "critical" {
		t.Fatalf("expected derived critical severity, got %q", confirmed.Severity)
	}
	if confirmed.Version != 2 {
		t.Fatalf("expected version 2, got %d", confirmed.Version)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/findings/web-admin-exposed/versions", nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected finding versions status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var versions struct {
		Versions []struct {
			Version     int    `json:"version"`
			Severity    string `json:"severity"`
			CVSSPending bool   `json:"cvss_pending"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		t.Fatalf("decode finding versions: %v", err)
	}
	if len(versions.Versions) != 2 {
		t.Fatalf("expected 2 finding versions, got %d", len(versions.Versions))
	}
	if versions.Versions[0].Severity != "pending" || versions.Versions[1].Severity != "critical" {
		t.Fatalf("expected pending then critical versions, got %#v", versions.Versions)
	}
}

func TestConfirmedFindingRequiresCVSSAndCoreFields(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	body := []byte(`{
		"title":"Exposed admin panel",
		"target":"https://example.com/admin",
		"status":"confirmed"
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/findings/web-admin-exposed", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected confirmed validation status 400, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func TestEvidenceAttachReferencesManagedArtifactPath(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	upsertFact(t, server, projectID, "target:example.com", `{
		"category":"target",
		"summary":"example.com is in scope",
		"confidence":"confirmed"
	}`)

	body := []byte(`{
		"evidence_key":"admin-login-screenshot",
		"attach_to_type":"fact",
		"attach_to_key":"target:example.com",
		"artifact_type":"screenshot",
		"source_path":"task-123/artifacts/screenshot.png",
		"sha256":"abc123",
		"summary":"Screenshot of the exposed admin login."
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/evidence", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected evidence attach status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var evidence struct {
		EvidenceKey  string `json:"evidence_key"`
		AttachToType string `json:"attach_to_type"`
		AttachToKey  string `json:"attach_to_key"`
		ManagedPath  string `json:"managed_path"`
		ArtifactType string `json:"artifact_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence.EvidenceKey != "admin-login-screenshot" {
		t.Fatalf("expected evidence key, got %q", evidence.EvidenceKey)
	}
	if evidence.AttachToType != "fact" || evidence.AttachToKey != "target:example.com" {
		t.Fatalf("expected fact attachment, got %#v", evidence)
	}
	if evidence.ManagedPath != "artifacts/admin-login-screenshot/screenshot.png" {
		t.Fatalf("expected managed artifact path, got %q", evidence.ManagedPath)
	}
}

func TestEvidenceAttachRejectsMissingTarget(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	body := []byte(`{
		"evidence_key":"missing-target-proof",
		"attach_to_type":"fact",
		"attach_to_key":"target:example.com",
		"artifact_type":"log",
		"source_path":"task-123/artifacts/output.log",
		"summary":"Should not attach without a target fact."
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/evidence", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected evidence target status 404, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func upsertFact(t *testing.T, server interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, projectID, factKey, body string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/facts/"+factKey, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected upsert status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
}
