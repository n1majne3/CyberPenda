package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runtimeextension"
)

func TestRuntimeExtensionDirectoryExtendsRegistry(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("create source: %v", err)
	}
	extension := runtimeextension.Extension{
		SchemaVersion: runtimeextension.SchemaVersion,
		ID:            "pi_browser_tools",
		Name:          "Pi Browser Tools",
		CompatibleRuntimePlugins: []string{
			"pi",
		},
		Source:     runtimeextension.Source{Type: "local_dir", Path: source},
		Projection: runtimeextension.Projection{Location: "provider_home", Path: "extensions/browser-tools"},
	}
	raw, err := json.Marshal(extension)
	if err != nil {
		t.Fatalf("marshal extension: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi_browser_tools.json"), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	server, err := daemon.NewServer(daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeExtensionDirs: []string{dir},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime-extensions", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Extensions []runtimeextension.Extension `json:"extensions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode extensions response: %v", err)
	}
	if len(body.Extensions) != 1 || body.Extensions[0].ID != "pi_browser_tools" {
		t.Fatalf("unexpected extensions response: %#v", body.Extensions)
	}
}
