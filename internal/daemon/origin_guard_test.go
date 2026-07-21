package daemon_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestServeHTTPRejectsForeignOrigin pins the DNS-rebinding mitigation for issue
// #158. A malicious web page rebinds its own domain to 127.0.0.1 and then sends
// same-origin-looking requests to the unauthenticated loopback daemon, but the
// browser still stamps those requests with the page's real foreign Origin. Any
// request carrying a non-loopback Origin must be refused before routing, which
// breaks the credential-binding + model-refresh remote-code-execution chain at
// its first mutating request.
func TestServeHTTPRejectsForeignOrigin(t *testing.T) {
	server := newDaemon(t)

	foreign := []string{
		"http://evil.com",
		"https://evil.com",
		"http://evil.com:8787",
		"http://127.0.0.1.evil.com",
		"http://attacker.example:9999",
		"null", // opaque origin from sandboxed iframes / file:// must not pass
	}
	for _, origin := range foreign {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost} {
			req := httptest.NewRequest(method, "/health", nil)
			req.Header.Set("Origin", origin)
			resp := httptest.NewRecorder()
			server.ServeHTTP(resp, req)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("%s with Origin %q: status %d, want 403", method, origin, resp.Code)
			}
		}
	}
}

// TestServeHTTPAcceptsLocalAndSandboxOrigins confirms the guard admits every
// legitimate caller: loopback origins (the local UI), the Docker host gateway
// the sandbox uses to reach the daemon, and requests that carry no Origin header
// at all (CLI clients, the sandbox runtime, same-origin GETs, and the synthetic
// requests the rest of this test suite builds with httptest.NewRequest).
func TestServeHTTPAcceptsLocalAndSandboxOrigins(t *testing.T) {
	server := newDaemon(t)

	allowed := []string{
		"", // no Origin header
		"http://127.0.0.1:8787",
		"http://localhost:8787",
		"http://[::1]:8787",
		"http://host.docker.internal:8787", // sandbox runtime gateway
	}
	for _, origin := range allowed {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("Origin %q: status %d body %s, want 200", origin, resp.Code, resp.Body.String())
		}
	}
}
