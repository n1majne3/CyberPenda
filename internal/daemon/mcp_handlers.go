package daemon

import (
	"crypto/subtle"
	"net/http"

	"pentest/internal/blackboardv2"
	"pentest/internal/mcpserver"
	"pentest/internal/projectinterface"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (server *Server) registerMCP() {
	handler := sdkmcp.NewStreamableHTTPHandler(func(request *http.Request) *sdkmcp.Server {
		deps := mcpserver.Deps{BlackboardV2: server.blackboardV2}
		// The endpoint has no path Project; the grant's bound Project is the sole
		// authority for every trusted v2 tool.
		if server.projectInterfaceGrants != nil {
			if token := projectinterface.BearerToken(request); token != "" &&
				(server.authToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(server.authToken)) != 1) {
				grant, err := server.projectInterfaceGrants.Resolve(request.Context(), token)
				switch {
				case err == nil:
					deps.Grant = &grant
				default:
					deps.GrantError = &blackboardv2.Error{
						Code: "authority_denied", Message: "Continuation Interface capability is invalid",
						Path: "authorization", Retryable: false,
					}
				}
			}
		}
		return mcpserver.New(deps)
	}, &sdkmcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Sandbox runtimes reach the daemon via host.docker.internal; the default
		// loopback Host-header check would reject those requests with 403.
		DisableLocalhostProtection: true,
	})
	server.mux.Handle("/mcp", handler)
}
