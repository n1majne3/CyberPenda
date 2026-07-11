package daemon

import (
	"net/http"
	"pentest/internal/mcpserver"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (server *Server) registerMCP() {
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return mcpserver.New(mcpserver.Deps{
			Projects: server.projects,
			Facts:    server.facts,
			Tasks:    server.tasks,
			Reads:    server.reads,
		})
	}, &sdkmcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Sandbox runtimes reach the daemon via host.docker.internal; the default
		// loopback Host-header check would reject those requests with 403.
		DisableLocalhostProtection: true,
	})
	server.mux.Handle("/mcp", handler)
}
