package daemon_test

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

// End-to-end journey for the Custom Config File feature (issue #226) through
// the real daemon graph: real handlers, real store, real service, real
// Config Projection. Only the filesystem layout is a temp dir, exactly as in
// production. The warp use case drives it: a non-catalog plugin and an extra
// env var imported from an edited Claude settings.json must survive round
// trip storage and reach the projected file.
func TestProfileConfigImportEndToEndJourney(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
		Logger:  log.New(&bytes.Buffer{}, "", 0),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// 1. Create the profile over HTTP.
	createBody := `{"name":"E2E Claude","provider":"claude_code","fields":{"model":"claude-opus-4-6"}}`
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", strings.NewReader(createBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create profile: status %d body %s", rec.Code, rec.Body.String())
	}
	var created runtimeprofile.Profile
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("parse create response: %v", err)
	}
	profileID := created.ID

	// 2. The editor seed is a complete provider-native settings.json.
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profileID+"/projected-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("projected-config: status %d body %s", rec.Code, rec.Body.String())
	}
	var seed struct {
		Provider      string `json:"provider"`
		Format        string `json:"format"`
		Text          string `json:"text"`
		ProjectedText string `json:"projected_text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &seed); err != nil {
		t.Fatalf("parse projected-config: %v", err)
	}
	if seed.Format != "json" || !strings.Contains(seed.Text, "ANTHROPIC_MODEL") {
		t.Fatalf("seed must be a native Claude settings.json, got format=%q text=%q", seed.Format, seed.Text)
	}

	// 3. Import the projected seed plus an overlay-only plugin and extra env.
	var seedDoc map[string]any
	if err := json.Unmarshal([]byte(seed.Text), &seedDoc); err != nil {
		t.Fatalf("parse seed text: %v", err)
	}
	env, _ := seedDoc["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
		seedDoc["env"] = env
	}
	env["EXTRA_TOOL_FLAG"] = "1"
	seedDoc["enabledPlugins"] = map[string]any{"warp@claude-code-warp": true}
	editedRaw, err := json.Marshal(seedDoc)
	if err != nil {
		t.Fatalf("encode edited seed: %v", err)
	}
	rec = httptest.NewRecorder()
	importBody, err := json.Marshal(map[string]string{
		"config_text":    string(editedRaw),
		"projected_text": seed.ProjectedText,
	})
	if err != nil {
		t.Fatalf("encode import body: %v", err)
	}
	importReq := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+profileID+"/import-config", bytes.NewReader(importBody))
	server.ServeHTTP(rec, importReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("import-config: status %d body %s", rec.Code, rec.Body.String())
	}
	var imported struct {
		Profile    runtimeprofile.Profile `json:"profile"`
		MappedKeys []string               `json:"mapped_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatalf("parse import response: %v", err)
	}

	// 4. Structured keys mapped back; the remainder persisted verbatim.
	if imported.Profile.Fields.Env["EXTRA_TOOL_FLAG"] != "1" {
		t.Fatalf("env must map into the structured field, got %#v", imported.Profile.Fields.Env)
	}
	if !strings.Contains(imported.Profile.Fields.CustomConfigFile, "warp@claude-code-warp") {
		t.Fatalf("plugin remainder must persist on the Custom Config File, got %q", imported.Profile.Fields.CustomConfigFile)
	}

	// 5. The merged preview shows the final result the runtime receives.
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profileID+"/merged-config-preview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("merged-config-preview: status %d body %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Merged map[string]any `json:"merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("parse merged preview: %v", err)
	}
	previewEnv, _ := preview.Merged["env"].(map[string]any)
	plugins, _ := preview.Merged["enabledPlugins"].(map[string]any)
	if previewEnv["EXTRA_TOOL_FLAG"] != "1" || plugins["warp@claude-code-warp"] != true {
		t.Fatalf("merged preview must show structured env plus overlay plugin, got env=%#v plugins=%#v", previewEnv, plugins)
	}

	// 6. The real Config Projection deep-merges the same overlay onto disk.
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-e2e", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	projection, err := runner.ProjectRuntimeConfig(layout, imported.Profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	settings := readE2EJSON(t, projection.ConfigPath)
	projectedEnv, _ := settings["env"].(map[string]any)
	projectedPlugins, _ := settings["enabledPlugins"].(map[string]any)
	if projectedEnv["ANTHROPIC_MODEL"] != "claude-opus-4-6" || projectedEnv["EXTRA_TOOL_FLAG"] != "1" {
		t.Fatalf("projected env must combine structured fields and overlay, got %#v", projectedEnv)
	}
	if projectedPlugins["warp@claude-code-warp"] != true {
		t.Fatalf("warp plugin must reach the projected settings.json, got %#v", projectedPlugins)
	}

	// 7. Re-opening the editor shows the merged file, including the remainder.
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profileID+"/projected-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reopen projected-config: status %d body %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &seed); err != nil {
		t.Fatalf("parse reopen seed: %v", err)
	}
	if !strings.Contains(seed.Text, "warp@claude-code-warp") {
		t.Fatalf("reopened editor must show the Custom Config File remainder, got %q", seed.Text)
	}
}

func readE2EJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}
