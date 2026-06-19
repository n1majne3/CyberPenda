package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
)

func TestListRuntimePluginsReturnsBuiltIns(t *testing.T) {
	server := newDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime-plugins", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	if containsSecretLikeValue(resp.Body.String()) {
		t.Fatalf("runtime plugin response contains a secret-like value: %s", resp.Body.String())
	}
	var body struct {
		Plugins []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			CredentialEnv []string `json:"credential_env"`
		} `json:"plugins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode plugins response: %v", err)
	}
	ids := map[string]bool{}
	for _, plugin := range body.Plugins {
		ids[plugin.ID] = true
		for _, env := range plugin.CredentialEnv {
			if env == "" || env == "[REDACTED]" {
				t.Fatalf("expected public env var names, got %#v", plugin.CredentialEnv)
			}
		}
	}
	for _, want := range []string{"fake", "codex", "claude_code", "pi"} {
		if !ids[want] {
			t.Fatalf("expected builtin plugin %q in %#v", want, ids)
		}
	}
}

func TestGetRuntimePluginReturnsOnePlugin(t *testing.T) {
	server := newDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime-plugins/claude_code", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode plugin response: %v", err)
	}
	if body.ID != "claude_code" || body.Name != "Claude Code" {
		t.Fatalf("unexpected plugin response: %#v", body)
	}
}

func TestGetRuntimePluginMissingReturnsNotFound(t *testing.T) {
	server := newDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime-plugins/missing", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func TestRuntimePluginDirectoryExtendsRegistry(t *testing.T) {
	dir := t.TempDir()
	manifest := `{
  "schema_version": 1,
  "id": "external_runtime",
  "name": "External Runtime",
  "binary": {"default": "external"},
  "profile_schema": {"fields": [{"name": "model", "type": "string", "label": "Model"}]},
  "config_projection": {"primitive": "generic_config"},
  "launch": {"args": ["{{binary}}", "{{goal}}"]},
  "transcript": {"parser": "plain_runtime_output"}
}`
	if err := os.WriteFile(filepath.Join(dir, "external.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	server, err := daemon.NewServer(daemon.Config{
		Version:           "test-version",
		DBPath:            filepath.Join(t.TempDir(), "pentest.db"),
		RuntimePluginDirs: []string{dir},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime-plugins/external_runtime", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func containsSecretLikeValue(value string) bool {
	markers := []string{"sk-", "bearer ", "[REDACTED", "secret-value"}
	lower := strings.ToLower(value)
	for _, marker := range markers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}
