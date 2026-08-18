package hostedcontroller

import (
	"io"
	"log"
	"net"
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
