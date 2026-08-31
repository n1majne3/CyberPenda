package hostedcontroller

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/runner"
)

func TestStartHostedLoopbackUsesBoundListenAddrForRuntimeMCP(t *testing.T) {
	server, listener, err := startHostedLoopback(t.TempDir(), nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	bound := listener.Addr().String()
	_, port, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatal(err)
	}
	if port == "0" {
		t.Fatalf("listener did not bind a concrete port: %q", bound)
	}
	if server.ListenAddr() != bound {
		t.Fatalf("daemon listenAddr = %q, want bound %q so Runtime MCP does not use port 0", server.ListenAddr(), bound)
	}
	mcp := runner.MCPEndpointURL(server.ListenAddr(), false)
	if !strings.Contains(mcp, ":"+port+"/") {
		t.Fatalf("MCP URL = %q, want bound port %s", mcp, port)
	}
}

func TestStartHostedLoopbackDoesNotSeedBuiltinSkills(t *testing.T) {
	server, listener, err := startHostedLoopback(t.TempDir(), nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	request := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list hosted Skills status = %d, body = %s", response.Code, response.Body.String())
	}
	var listed struct {
		Skills []struct {
			ID string `json:"id"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Skills) != 0 {
		t.Fatalf("hosted daemon seeded Built-in Skills: %#v", listed.Skills)
	}
}
