package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/session"
)

func TestLocalBrowserSessionReadsTaskAndSessionBlackboards(t *testing.T) {
	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), SessionRoot: filepath.Join(root, "sessions"), DisableBuiltinSkills: true}
	s, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.projects.Create("Browser", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.sessions.Create(session.CreateRequest{Input: "Browser", BlackboardMode: session.BlackboardModeWorkingGraph})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, cookie *http.Cookie, browser bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if browser {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Origin", "http://127.0.0.1:8787")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodPost, "/api/operator-session", nil, true)
	if w.Code != http.StatusNoContent {
		t.Fatalf("browser session: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("missing protected browser cookie")
	}
	for _, path := range []string{"/api/v2/projects/" + p.ID + "/fgs", "/api/v2/sessions/" + a.ID + "/fgs"} {
		if w := request(http.MethodGet, path, cookies[0], true); w.Code != http.StatusOK {
			t.Fatalf("browser read: %d %s", w.Code, w.Body.String())
		}
		if w := request(http.MethodGet, path, nil, false); w.Code == http.StatusOK {
			t.Fatal("tokenless Runtime gained Blackboard access")
		}
		if w := request(http.MethodGet, path, cookies[0], false); w.Code == http.StatusOK {
			t.Fatal("Runtime used browser-only authority")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v2/projects/" + p.ID + "/fgs"
	if w := request(http.MethodGet, path, cookies[0], true); w.Code == http.StatusOK {
		t.Fatal("old daemon cookie remained valid")
	}
	w = request(http.MethodPost, "/api/operator-session", cookies[0], true)
	if w.Code != http.StatusNoContent {
		t.Fatalf("refresh after restart: %d", w.Code)
	}
	if w := request(http.MethodGet, path, w.Result().Cookies()[0], true); w.Code != http.StatusOK {
		t.Fatalf("read after restart: %d", w.Code)
	}
}

func TestBrowserSessionDoesNotBootstrapRemoteOrRuntimeAuthority(t *testing.T) {
	for _, tc := range []struct{ name, token, peer, host, site, origin string }{
		{"Runtime", "", "127.0.0.1:1234", "127.0.0.1:8787", "", ""},
		{"container", "", "172.18.0.2:1234", "127.0.0.1:8787", "same-origin", ""},
		{"foreign-origin", "", "127.0.0.1:1234", "127.0.0.1:8787", "same-origin", "https://example.test"},
		{"cross-site", "", "127.0.0.1:1234", "127.0.0.1:8787", "cross-site", ""},
		{"foreign-host", "", "127.0.0.1:1234", "example.test", "same-origin", ""},
		{"configured-auth", "configured-secret", "127.0.0.1:1234", "127.0.0.1:8787", "same-origin", ""},
	} {		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s, err := NewServer(Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), AuthToken: tc.token, DisableBuiltinSkills: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			r := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/api/operator-session", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code < 400 || len(w.Result().Cookies()) != 0 {
				t.Fatalf("untrusted bootstrap: %d", w.Code)
			}
		})
	}
}

// A browser that closes must not lose the operator session: the cookie carries
// a persistence lifetime instead of dying with the browser session.
func TestOperatorSessionCookiePersistsBeyondBrowserSession(t *testing.T) {
	root := t.TempDir()
	s, err := NewServer(Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/operator-session", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("browser session: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one session cookie, got %d", len(cookies))
	}
	if cookies[0].MaxAge <= 0 {
		t.Fatalf("operator session cookie is browser-scoped (MaxAge=%d)", cookies[0].MaxAge)
	}
}

// An operator with the configured token signs in from a non-loopback peer
// (SSH tunnel, container gateway) by presenting the bearer credential; the
// daemon answers with the persistent browser cookie.
func TestConfiguredTokenBrowserSignInFromRemotePeer(t *testing.T) {
	root := t.TempDir()
	s, err := NewServer(Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), ListenAddr: "0.0.0.0:8787", AuthToken: "configured-secret", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r := httptest.NewRequest(http.MethodPost, "http://172.17.0.1:8787/api/operator-session", nil)
	r.RemoteAddr = "172.17.0.2:12345"
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Authorization", "Bearer configured-secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("bearer sign-in: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge <= 0 {
		t.Fatalf("bearer sign-in must issue a persistent cookie: %+v", cookies)
	}
}

// Sign-out expires the browser cookie so the session does not survive on a
// shared machine. Only a same-origin browser request may expire it.
func TestOperatorSessionDeleteExpiresBrowserCookie(t *testing.T) {
	root := t.TempDir()
	s, err := NewServer(Config{DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), AuthToken: "configured-secret", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	signIn := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/operator-session", nil)
	signIn.RemoteAddr = "127.0.0.1:12345"
	signIn.Header.Set("Sec-Fetch-Site", "same-origin")
	signIn.Header.Set("Authorization", "Bearer configured-secret")
	signInRecorder := httptest.NewRecorder()
	s.ServeHTTP(signInRecorder, signIn)
	if signInRecorder.Code != http.StatusNoContent || len(signInRecorder.Result().Cookies()) != 1 {
		t.Fatalf("bearer sign-in: %d", signInRecorder.Code)
	}
	sessionCookie := signInRecorder.Result().Cookies()[0]
	deleteSession := func(site string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:8787/api/operator-session", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := deleteSession("cross-site", sessionCookie); w.Code == http.StatusNoContent {
		t.Fatal("cross-site request expired the browser session")
	}
	w := deleteSession("same-origin", sessionCookie)
	if w.Code != http.StatusNoContent {
		t.Fatalf("sign-out: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != operatorSessionCookie || cookies[0].MaxAge >= 0 {
		t.Fatalf("sign-out must expire the operator cookie: %+v", cookies)
	}
}
