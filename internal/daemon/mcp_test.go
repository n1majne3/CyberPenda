package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthReportsMCPEndpoint(t *testing.T) {
	server := newDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	var payload struct {
		MCP struct {
			Status string `json:"status"`
			Path   string `json:"path"`
		} `json:"mcp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if payload.MCP.Status != "ok" || payload.MCP.Path != "/mcp" {
		t.Fatalf("unexpected mcp health: %#v", payload.MCP)
	}
}

func TestMCPEndpointInitializesAndListsTools(t *testing.T) {
	server := newDaemon(t)

	initBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("initialize expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	listBody := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(listBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, tool := range []string{
		"upsert_project_fact",
		"get_project_fact",
		"list_project_facts",
		"search_project_facts",
		"deprecate_project_fact",
		"upsert_fact_relation",
		"record_vulnerability",
		"list_vulnerabilities",
		"attach_evidence",
		"generate_report",
		"request_approval",
		"request_scope_expansion",
	} {
		if !bytes.Contains([]byte(body), []byte(tool)) {
			t.Fatalf("tools/list missing %q in %s", tool, body)
		}
	}
}
