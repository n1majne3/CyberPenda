package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthDoesNotAdvertiseBuiltinMCP(t *testing.T) {
	server := newDaemon(t)
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d", response.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["mcp"]; exists {
		t.Fatalf("health still advertises retired built-in MCP: %#v", payload["mcp"])
	}
}

func TestBuiltinMCPEndpointIsNotRegistered(t *testing.T) {
	server := newDaemon(t)
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("retired /mcp status = %d, want 404", response.Code)
	}
}
