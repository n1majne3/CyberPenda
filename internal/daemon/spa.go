package daemon

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"pentest/internal/daemon/webfs"
)

// registerSPA serves the embedded React build for any non-API, non-health path.
// During development (Vite), the embedded dist is still present but unused; the
// proxy in vite.config.ts routes /api to the daemon instead.
func (server *Server) registerSPA() {
	assets, err := fs.Sub(webfs.Dist, "dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(assets))
	server.mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") || request.URL.Path == "/mcp" {
			http.NotFound(response, request)
			return
		}
		// Serve static assets directly; everything else falls back to
		// index.html so client-side routing works on refresh.
		clean := path.Clean(request.URL.Path)
		if strings.HasPrefix(clean, "/assets/") {
			fileServer.ServeHTTP(response, request)
			return
		}
		if f, err := assets.Open(strings.TrimPrefix(clean, "/")); err == nil {
			f.Close()
			fileServer.ServeHTTP(response, request)
			return
		}
		request.URL.Path = "/"
		fileServer.ServeHTTP(response, request)
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, struct {
		Error string `json:"error"`
	}{
		Error: message,
	})
}
