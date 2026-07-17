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

func TestMCPEndpointAcceptsDockerInternalHostHeader(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Host = "host.docker.internal:8787"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("initialize with docker host expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestMCPEndpointInitializesWithNoLegacyV1Tools(t *testing.T) {
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
	var listed struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	want := map[string]bool{
		"blackboard_change": true, "blackboard_read": true, "blackboard_history": true,
		"blackboard_retain_evidence": true, "blackboard_checkpoint_attempt": true, "blackboard_finish": true,
	}
	if len(listed.Result.Tools) != len(want) {
		t.Fatalf("blackboard_v2 tools/list = %#v, want exactly the six trusted v2 tools", listed.Result.Tools)
	}
	for _, raw := range listed.Result.Tools {
		var tool struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			t.Fatalf("decode tool: %v", err)
		}
		if !want[tool.Name] {
			t.Fatalf("unexpected trusted tool %q in %#v", tool.Name, listed.Result.Tools)
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing trusted tools %#v", want)
	}
}
