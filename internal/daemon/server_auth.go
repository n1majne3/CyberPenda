package daemon

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"pentest/internal/projectinterface"
)

// authorized reports whether the request carries the configured auth token.
// The token is accepted as either an "Authorization: Bearer <token>" header or
// a "?token=<token>" query parameter; the query form exists so sandbox MCP
// transports that cannot attach per-request headers still authenticate.
func (server *Server) authorized(request *http.Request) bool {
	if server.authToken == "" {
		return true
	}
	if server.operatorRequest(request) {
		return true
	}
	if header := strings.TrimSpace(request.Header.Get("Authorization")); header != "" {
		if scheme, token, ok := strings.Cut(header, " "); ok && strings.EqualFold(scheme, "Bearer") {
			if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(server.authToken)) == 1 {
				return true
			}
		}
	}
	if queryToken := request.URL.Query().Get("token"); queryToken != "" {
		if subtle.ConstantTimeCompare([]byte(queryToken), []byte(server.authToken)) == 1 {
			return true
		}
	}
	// A Continuation Interface Grant is a separate, narrower credential from
	// the daemon operator token. Accept it only on Blackboard v2 HTTP.
	if token := projectinterface.BearerToken(request); token != "" {
		if server.blackboardV2 != nil && server.projectInterfaceGrants != nil &&
			isBlackboardV2HTTPTransport(request) {
			_, err := server.projectInterfaceGrants.Resolve(request.Context(), token)
			return err == nil
		}
	}
	return false
}

// requireOperatorAuthority protects local operator workflows that can mutate
// Blackboard outside the versioned Project Interface. The Actor header is
// provenance only; it never authenticates the caller.
func (server *Server) requireOperatorAuthority(response http.ResponseWriter, request *http.Request) bool {
	if !server.operatorRequest(request) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// publicPath reports whether the request targets a route that stays reachable
// without the auth token: the health probe, CORS preflight, and the SPA's static
// assets (which a browser loads before it can attach a header). API and MCP
// routes are never public.
func (server *Server) publicPath(request *http.Request) bool {
	if request.Method == http.MethodOptions {
		return true
	}
	if request.Method == http.MethodGet && request.URL.Path == "/health" {
		return true
	}
	// Every public path below is a static asset the SPA file server serves via
	// GET only, so non-GET requests must go through auth (and then 404/405).
	if request.Method != http.MethodGet {
		return false
	}
	clean := path.Clean(request.URL.Path)
	if strings.HasPrefix(clean, "/assets/") {
		return true
	}
	switch clean {
	case "/", "/index.html":
		return true
	}
	if isStaticAssetPath(clean) {
		return true
	}
	if clean == "/api" || strings.HasPrefix(clean, "/api/") || clean == "/mcp" || strings.HasPrefix(clean, "/mcp/") {
		return false
	}
	_, pattern := server.mux.Handler(request)
	return pattern == "/"
}

// isStaticAssetPath reports whether the cleaned path is a static asset served
// by the SPA file server (favicon, logos, manifest, etc.).
func isStaticAssetPath(clean string) bool {
	ext := strings.ToLower(path.Ext(clean))
	switch ext {
	case ".svg", ".png", ".ico", ".webp", ".woff", ".woff2", ".css", ".js", ".json":
		// Exclude API-shaped JSON (/api/...) and the MCP route: only top-level
		// asset files count as static SPA assets.
		return !strings.HasPrefix(clean, "/api/") && clean != "/mcp"
	}
	return false
}

// dockerInternalHost is the host gateway the sandbox runtime uses to reach the
// daemon from inside its container. It is a fixed, non-attacker-controllable
// name (a browser stamps Origin/Host from the page origin, so a rebinding page
// can never forge it), which is why the MCP SDK's built-in localhost protection
// is disabled and this allowlist admits it explicitly.
const dockerInternalHost = "host.docker.internal"

// allowedOrigin reports whether the request's Origin header is acceptable. The
// daemon serves only the local operator's UI (same-origin, loopback) plus the
// sandbox runtime and CLI clients, which send no Origin at all. A present Origin
// that is not a loopback origin signals a DNS-rebinding or cross-site request: a
// malicious page rebinds its domain to 127.0.0.1, but the browser still stamps
// the request with the page's real foreign Origin. An absent Origin is allowed
// because browsers always attach one to the cross-site/rebinding requests this
// guard targets, while local non-browser clients and same-origin GETs omit it.
func (server *Server) allowedOrigin(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	host := originHost(origin)
	if host == "" {
		return false
	}
	if isLoopback(host) {
		return true
	}
	if strings.EqualFold(hostOnly(host), dockerInternalHost) {
		return true
	}
	return hostsSame(host, server.listenAddr)
}

// originHost extracts the host[:port] from an Origin header value such as
// "http://127.0.0.1:8787" or "http://[::1]:8787". It returns "" for the opaque
// "null" origin or any value that cannot be parsed, both of which are rejected.
func originHost(origin string) string {
	if strings.EqualFold(origin, "null") {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return strings.TrimSpace(strings.Trim(host, "[]"))
}

// hostsSame reports whether two host[:port] strings name the same host,
// ignoring port and case.
func hostsSame(a, b string) bool {
	a, b = hostOnly(a), hostOnly(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

// isLoopback reports whether the listen address binds only to the local host.
// An empty address defaults to loopback. IPv6 any-addresses ("[::]", "::")
// count as non-loopback so they require an auth token like 0.0.0.0.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = strings.TrimSpace(addr)
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return strings.TrimSpace(addr) == ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}
