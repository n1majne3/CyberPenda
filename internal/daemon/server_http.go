package daemon

import (
	"net/http"
	"net/url"
	"time"
)

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	// Reject DNS-rebinding before anything else. A malicious web page can rebind
	// its own domain to 127.0.0.1 and then send same-origin-looking requests to
	// the unauthenticated loopback daemon, but the browser still stamps those
	// requests with the page's real (foreign) Origin. Any request carrying a
	// non-loopback Origin is therefore a cross-site/rebinding attempt and is
	// refused before routing. Requests with no Origin (the local UI's same-origin
	// GETs, the sandbox runtime, CLI clients, synthetic test requests) are
	// unaffected, so this guard is invisible to legitimate local callers.
	if !server.allowedOrigin(request) {
		writeError(response, http.StatusForbidden, "forbidden origin")
		return
	}
	if server.authToken != "" && !server.publicPath(request) {
		if !server.authorized(request) {
			// Blackboard v2 handlers own their narrower Continuation capability
			// checks. Every other API remains behind this middleware.
			handlerOwnsCapability := server.blackboardV2 != nil && isBlackboardV2HTTPTransport(request)
			if !handlerOwnsCapability {
				writeError(response, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
	}
	start := time.Now()
	recorder := newStatusRecorder(response)
	server.mux.ServeHTTP(recorder, request)
	server.logRequest(start, request, recorder.status)
}

func (server *Server) ListenAddr() string {
	return server.listenAddr
}

// GeneratedOperatorToken exposes only the startup-generated credential, never a configured secret.
func (server *Server) GeneratedOperatorToken() string {
	if !server.generatedOperatorToken {
		return ""
	}
	return server.operatorToken
}

// GeneratedOperatorAccessURL returns the one startup URL that transfers a
// generated operator bearer capability to the browser. An explicitly
// configured daemon token is never returned or logged by this seam.
func (server *Server) GeneratedOperatorAccessURL() string {
	token := server.GeneratedOperatorToken()
	if token == "" {
		return ""
	}
	return "http://" + server.listenAddr + "/?token=" + url.QueryEscape(token)
}
