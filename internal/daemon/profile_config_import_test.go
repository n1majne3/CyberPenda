package daemon_test

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
)

func newImportTestServer(t *testing.T) *daemon.Server {
	t.Helper()
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
		Logger:  log.New(&bytes.Buffer{}, "", 0),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func createProfileForImport(t *testing.T, server *daemon.Server) string {
	t.Helper()
	body := `{"name":"Claude Import","provider":"claude_code","fields":{}}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create profile status %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	return created.ID
}

func TestImportRuntimeProfileConfigEndpointSucceeds(t *testing.T) {
	server := newImportTestServer(t)
	id := createProfileForImport(t, server)

	body := `{"config_text":"{\n  \"env\": {\"MY_TOOL_TAG\": \"abc\"},\n  \"enabledPlugins\": {\"warp@claude-code-warp\": true}\n}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+id+"/import-config", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status %d body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Profile struct {
			Fields struct {
				Env              map[string]string `json:"env"`
				CustomConfigFile string            `json:"custom_config_file"`
			} `json:"fields"`
		} `json:"profile"`
		MappedKeys []string `json:"mapped_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body %s", err, rec.Body.String())
	}
	if payload.Profile.Fields.Env["MY_TOOL_TAG"] != "abc" {
		t.Fatalf("env not mapped: %#v", payload.Profile.Fields.Env)
	}
	if !strings.Contains(payload.Profile.Fields.CustomConfigFile, "warp@claude-code-warp") {
		t.Fatalf("remainder missing: %q", payload.Profile.Fields.CustomConfigFile)
	}
	if len(payload.MappedKeys) == 0 {
		t.Fatalf("mapped keys missing: %#v", payload.MappedKeys)
	}
}

func TestImportRuntimeProfileConfigEndpointRefusesPerKey(t *testing.T) {
	server := newImportTestServer(t)
	id := createProfileForImport(t, server)

	// claude_code with model_provider unset: env.ANTHROPIC_BASE_URL is
	// conditionally managed, so only secret keys refuse here.
	body := `{"config_text":"{\n  \"env\": {\"MY_API_TOKEN\": \"sk-live\"}\n}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+id+"/import-config", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import status %d body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
		Keys  []struct {
			Key     string `json:"key"`
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body %s", err, rec.Body.String())
	}
	if !strings.Contains(payload.Error, "MY_API_TOKEN") {
		t.Fatalf("error must name the refused key, got %q", payload.Error)
	}
	if len(payload.Keys) == 0 {
		t.Fatalf("per-key errors missing: %#v", payload.Keys)
	}
	if strings.Contains(rec.Body.String(), "sk-live") {
		t.Fatalf("response must not echo secret value: %s", rec.Body.String())
	}
}

func TestImportRuntimeProfileConfigEndpointUnknownProfile(t *testing.T) {
	server := newImportTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/nope/import-config", bytes.NewReader([]byte(`{"config_text":"{}"}`)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateRuntimeProfileProviderSwitchReturnsConflict(t *testing.T) {
	server := newImportTestServer(t)
	id := createProfileForImport(t, server)

	// Seed a non-empty Custom Config File through the import endpoint.
	importBody := `{"config_text":"{\"enabledPlugins\":{\"warp@claude-code-warp\":true}}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+id+"/import-config", bytes.NewReader([]byte(importBody)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status %d body %s", rec.Code, rec.Body.String())
	}

	// A provider switch without confirmation must surface the dedicated
	// conflict code, not a generic 500.
	switchBody := `{"provider":"codex","confirm_provider_switch_clears_overlay":false}`
	req = httptest.NewRequest(http.MethodPatch, "/api/runtime-profiles/"+id, bytes.NewReader([]byte(switchBody)))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("switch status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "provider_switch_needs_overlay_clear") {
		t.Fatalf("body must carry the conflict code: %s", rec.Body.String())
	}
}

func TestUpdateRuntimeProfileProviderSwitchConfirmClearsOverlay(t *testing.T) {
	server := newImportTestServer(t)
	id := createProfileForImport(t, server)

	importBody := `{"config_text":"{\"enabledPlugins\":{\"warp@claude-code-warp\":true}}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+id+"/import-config", bytes.NewReader([]byte(importBody)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status %d body %s", rec.Code, rec.Body.String())
	}

	// With confirmation the switch succeeds and drops the overlay.
	switchBody := `{"provider":"codex","confirm_provider_switch_clears_overlay":true}`
	req = httptest.NewRequest(http.MethodPatch, "/api/runtime-profiles/"+id, bytes.NewReader([]byte(switchBody)))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed switch status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "warp@claude-code-warp") {
		t.Fatalf("overlay must be cleared on confirmed switch: %s", rec.Body.String())
	}
}

func TestUpdateRuntimeProfileProviderSwitchConfirmClearsOverlayWithFields(t *testing.T) {
	server := newImportTestServer(t)
	id := createProfileForImport(t, server)

	importBody := `{"config_text":"{\"enabledPlugins\":{\"warp@claude-code-warp\":true}}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+id+"/import-config", bytes.NewReader([]byte(importBody)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status %d body %s", rec.Code, rec.Body.String())
	}

	// The UI form save resubmits the previous overlay alongside the
	// confirmation flag; the service must still drop it.
	switchBody := `{"provider":"codex","fields":{"custom_config_file":"{\"enabledPlugins\":{\"warp@claude-code-warp\":true}}"},"confirm_provider_switch_clears_overlay":true}`
	req = httptest.NewRequest(http.MethodPatch, "/api/runtime-profiles/"+id, bytes.NewReader([]byte(switchBody)))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed switch with fields status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "warp@claude-code-warp") {
		t.Fatalf("overlay must be cleared even when fields resubmit it: %s", rec.Body.String())
	}
}

// Story 16: the generated config preview shows the final merged result.
func TestMergedConfigPreviewEndpointCombinesOverlay(t *testing.T) {
	server := newImportTestServer(t)
	id := createProfileForImport(t, server)

	importBody := `{"config_text":"{\"enabledPlugins\":{\"warp@claude-code-warp\":true}}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+id+"/import-config", bytes.NewReader([]byte(importBody)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status %d body %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+id+"/merged-config-preview", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status %d body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Provider string         `json:"provider"`
		Merged   map[string]any `json:"merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if payload.Provider != "claude_code" {
		t.Fatalf("provider = %q", payload.Provider)
	}
	plugins, ok := payload.Merged["enabledPlugins"].(map[string]any)
	if !ok || plugins["warp@claude-code-warp"] != true {
		t.Fatalf("merged preview must include the overlay plugin, got %#v", payload.Merged["enabledPlugins"])
	}

	// Unknown profile id answers 404.
	req = httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/nope/merged-config-preview", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status %d", rec.Code)
	}
}

// Story 2: the editor seed is the provider-native projected config text.
func TestProjectedConfigEndpointSeedsProviderNativeText(t *testing.T) {
	server := newImportTestServer(t)
	id := createProfileForImport(t, server)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+id+"/projected-config", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("projected config status %d body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Provider string `json:"provider"`
		Format   string `json:"format"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Provider != "claude_code" || payload.Format != "json" {
		t.Fatalf("provider=%q format=%q", payload.Provider, payload.Format)
	}
	if !strings.Contains(payload.Text, `"env"`) {
		t.Fatalf("seed must be provider-native settings.json shape, got %s", payload.Text)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/none/projected-config", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status %d", rec.Code)
	}
}
