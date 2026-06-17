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
