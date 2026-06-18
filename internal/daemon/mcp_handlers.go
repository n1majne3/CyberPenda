package daemon

import (
	"net/http"
	"pentest/internal/mcpserver"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (server *Server) registerMCP() {
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return mcpserver.New(mcpserver.Deps{
			Projects:  server.projects,
			Facts:     server.facts,
			Tasks:     server.tasks,
			Approvals: server.approvals,
		})
	}, &sdkmcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	server.mux.Handle("/mcp", handler)
}
