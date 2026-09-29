package daemon

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"

	"pentest/internal/projectinterface"
)

const operatorSessionCookie = "pentest.operator-session"

// The browser session survives browser restarts so a tunneled or remote UI
// does not demand the token on every launch. Thirty days balances operator
// convenience against the cookie's operator authority.
const operatorSessionMaxAgeSeconds = 30 * 24 * 60 * 60

// Browser authority is separate from Runtime bearer grants. Fetch metadata
// and the origin check prevent other web origins from bootstrapping a session.
func sameOriginBrowserRequest(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			return false
		}
	}
	return true
}

func (server *Server) operatorRequest(r *http.Request) bool {
	if token := projectinterface.BearerToken(r); token != "" {
		return subtle.ConstantTimeCompare([]byte(token), []byte(server.operatorToken)) == 1
	}
	if !sameOriginBrowserRequest(r) {
		return false
	}
	cookie, err := r.Cookie(operatorSessionCookie)
	return err == nil && cookie.Value != "" && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(server.operatorToken)) == 1
}

func (server *Server) handleOperatorSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOriginBrowserRequest(r) {
		writeError(w, http.StatusForbidden, "browser session requires a same-origin request")
		return
	}
	// Only the default local UI can start without an explicit credential.
	// A container/remote caller cannot turn a Runtime grant into operator access.
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(peer)
	host := r.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	hostIP := net.ParseIP(strings.Trim(host, "[]"))
	local := err == nil && ip != nil && ip.IsLoopback() && (host == "localhost" || hostIP != nil && hostIP.IsLoopback())
	if !server.operatorRequest(r) && !(server.generatedOperatorToken && local && isLoopback(server.listenAddr)) {
		writeError(w, http.StatusUnauthorized, "operator sign-in is required")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: operatorSessionCookie, Value: server.operatorToken, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: operatorSessionMaxAgeSeconds})
	w.WriteHeader(http.StatusNoContent)
}

// Sign-out expires the browser cookie. Only a same-origin browser request may
// end the session; the operator token itself is unaffected.
func (server *Server) handleOperatorSessionDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOriginBrowserRequest(r) {
		writeError(w, http.StatusForbidden, "browser session sign-out requires a same-origin request")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: operatorSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
