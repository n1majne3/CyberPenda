package daemon_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"pentest/internal/daemon"
)

// newAuthTestServer builds a daemon on an in-memory DB for auth tests. It uses
// a loopback listen address by default so a missing token stays valid.
func newAuthTestServer(t *testing.T, listenAddr string, authToken string) *daemon.Server {
	t.Helper()
	server, err := daemon.NewServer(daemon.Config{
		Version:    "test",
		DBPath:     ":memory:",
		ListenAddr: listenAddr,
		AuthToken:  authToken,
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

func TestNewServerRefusesNonLoopbackWithoutToken(t *testing.T) {
	_, err := daemon.NewServer(daemon.Config{
		DBPath:     ":memory:",
		ListenAddr: "0.0.0.0:8787",
		AuthToken:  "",
	})
	if err == nil {
		t.Fatal("expected error when binding non-loopback without an auth token")
	}
}

func TestNewServerAcceptsNonLoopbackWithToken(t *testing.T) {
	if _, err := daemon.NewServer(daemon.Config{
		DBPath:     ":memory:",
		ListenAddr: "0.0.0.0:8787",
		AuthToken:  "a-secret-token",
	}); err != nil {
		t.Fatalf("expected non-loopback bind with token to succeed, got %v", err)
	}
}

func TestNewServerAcceptsLoopbackWithoutToken(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8787", "localhost:8787", "[::1]:8787", ""} {
		if _, err := daemon.NewServer(daemon.Config{
			DBPath:     ":memory:",
			ListenAddr: addr,
			AuthToken:  "",
		}); err != nil {
			t.Errorf("expected loopback bind %q without token to succeed, got %v", addr, err)
		}
	}
}

func TestServeHTTPRejectsAPIWithoutBearer(t *testing.T) {
	server := newAuthTestServer(t, "0.0.0.0:8787", "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for API request without token, got %d", resp.Code)
	}
}

func TestServeHTTPAcceptsAPIWithBearer(t *testing.T) {
	server := newAuthTestServer(t, "0.0.0.0:8787", "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code == http.StatusUnauthorized {
		t.Fatalf("expected non-401 for API request with valid bearer, got %d", resp.Code)
	}
}

func TestServeHTTPRejectsAPIWithWrongBearer(t *testing.T) {
	server := newAuthTestServer(t, "0.0.0.0:8787", "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for API request with wrong bearer, got %d", resp.Code)
	}
}

func TestServeHTTPAcceptsQueryToken(t *testing.T) {
	server := newAuthTestServer(t, "0.0.0.0:8787", "secret")

	// The sandbox MCP transport reaches /mcp with the token as a query param,
	// since per-runtime Authorization-header support is not guaranteed.
	req := httptest.NewRequest(http.MethodPost, "/mcp?token=secret", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code == http.StatusUnauthorized {
		t.Fatalf("expected non-401 for /mcp with valid query token, got %d", resp.Code)
	}
}

func TestServeHTTPHealthOpenWithoutToken(t *testing.T) {
	server := newAuthTestServer(t, "0.0.0.0:8787", "secret")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected /health to be open without token, got %d", resp.Code)
	}
}

func TestServeHTTPAcceptsSPABrowserAssets(t *testing.T) {
	server := newAuthTestServer(t, "0.0.0.0:8787", "secret")

	// The SPA entry and its static assets must load in a browser, which cannot
	// attach a bearer header to the initial document request.
	for _, path := range []string{"/", "/favicon.svg", "/assets/index-DoIK4l0W.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code == http.StatusUnauthorized {
			t.Fatalf("expected %q to be open without token, got 401", path)
		}
	}
}

func TestServeHTTPRejectsNonGetAssetsWithoutToken(t *testing.T) {
	server := newAuthTestServer(t, "0.0.0.0:8787", "secret")

	// Static assets are served GET-only; a POST to /assets/... must not bypass
	// auth by riding the public-asset path. (No such route is registered, so the
	// request 404s once authed — but it must require a token first.)
	req := httptest.NewRequest(http.MethodPost, "/assets/index-DoIK4l0W.js", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected POST /assets/... to require a token, got %d", resp.Code)
	}
}

func TestServeHTTPEmptyTokenKeepsAllOpen(t *testing.T) {
	// Loopback default path: no token configured means no enforcement, so an
	// existing unauthenticated caller (make dev, pentestctl-via-API) still works.
	server := newAuthTestServer(t, "127.0.0.1:8787", "")

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code == http.StatusUnauthorized {
		t.Fatalf("expected no auth enforcement when token is unset, got 401")
	}
}
