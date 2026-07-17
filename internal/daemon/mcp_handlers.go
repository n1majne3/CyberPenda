package daemon

import (
	"crypto/subtle"
	"net/http"

	"pentest/internal/blackboardv2"
	"pentest/internal/mcpserver"
	"pentest/internal/projectinterface"
	"pentest/internal/store"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (server *Server) registerMCP() {
	handler := sdkmcp.NewStreamableHTTPHandler(func(request *http.Request) *sdkmcp.Server {
		if server.canonicalStore == store.CanonicalStoreBlackboardV2 {
			deps := mcpserver.V2Deps{BlackboardV2: server.blackboardV2}
			// Resolve the Continuation Interface capability for Runtime MCP.
			// The endpoint has no path Project; the grant's bound Project is the
			// sole authority. A presented-but-invalid token is captured so tools
			// report authority_denied rather than conflating it with "no grant".
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
			return mcpserver.NewV2(deps)
		}
		deps := mcpserver.Deps{
			Projects:      server.projects,
			Facts:         server.facts,
			Tasks:         server.tasks,
			Reads:         server.reads,
			Compatibility: server.compatibility,
		}
		// Resolve the Continuation Interface Grant when the graph project-interface
		// module is active and the request carries a grant token (runtime protocol
		// §12.2). The MCP endpoint has no path Project, so the grant's bound
		// Project is the sole authority. A presented-but-invalid token is captured
		// as PrincipalError so the tools report grant_not_found rather than
		// conflating it with an operator request.
		if server.projectInterface != nil {
			deps.ProjectInterface = server.projectInterface
			if token := projectinterface.BearerToken(request); token != "" &&
				(server.authToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(server.authToken)) != 1) {
				principal, err := server.projectInterface.Authenticate(request.Context(), token, "")
				switch {
				case err == nil:
					deps.Principal = &principal
				default:
					deps.PrincipalError = projectinterface.AsError(err)
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
