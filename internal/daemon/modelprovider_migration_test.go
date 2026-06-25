package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runtimeprofile"
)

func TestModelProviderMigrationPreviewAndApply(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{Version: "test-version", DBPath: t.TempDir() + "/pentest.db", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	profile, err := server.CreateLocalRuntimeProfile("Codex CN", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Endpoint: "https://api.example.test/v1",
		Model:    "gpt-5",
		APIKeys:  map[string]string{"OPENAI_API_KEY": "sk-test"},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	previewReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profile.ID+"/model-provider-migration-preview", nil)
	previewResp := httptest.NewRecorder()
	server.ServeHTTP(previewResp, previewReq)
	if previewResp.Code != http.StatusOK {
		t.Fatalf("preview status %d body %s", previewResp.Code, previewResp.Body.String())
	}
	var preview struct {
		Eligible      bool `json:"eligible"`
		Matches       []any `json:"matches"`
		APIKeySources []any `json:"api_key_sources"`
		Proposed      struct {
			BaseURL string `json:"base_url"`
			Model   string `json:"model"`
		} `json:"proposed"`
	}
	if err := json.NewDecoder(previewResp.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if !preview.Eligible || preview.Proposed.BaseURL != "https://api.example.test/v1" || preview.Proposed.Model != "gpt-5" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if preview.Matches == nil || preview.APIKeySources == nil {
		t.Fatalf("expected non-nil slices in preview, got matches=%#v api_key_sources=%#v", preview.Matches, preview.APIKeySources)
	}

	applyReq := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles/"+profile.ID+"/model-provider-migration", bytes.NewReader([]byte(`{
		"action":"create",
		"migrate_api_key":true
	}`)))
	applyResp := httptest.NewRecorder()
	server.ServeHTTP(applyResp, applyReq)
	if applyResp.Code != http.StatusOK {
		t.Fatalf("apply status %d body %s", applyResp.Code, applyResp.Body.String())
	}
	var applied struct {
		Profile struct {
			Fields struct {
				ModelProviderID string `json:"model_provider_id"`
				Endpoint        string `json:"endpoint"`
				Model           string `json:"model"`
			} `json:"fields"`
		} `json:"profile"`
		Provider struct {
			ID        string `json:"id"`
			APIKeyEnv string `json:"api_key_env"`
		} `json:"provider"`
	}
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if applied.Profile.Fields.ModelProviderID == "" || applied.Profile.Fields.Endpoint != "" || applied.Profile.Fields.Model != "" {
		t.Fatalf("unexpected migrated profile: %#v", applied.Profile.Fields)
	}
	if applied.Provider.ID == "" || applied.Provider.APIKeyEnv == "" {
		t.Fatalf("unexpected provider: %#v", applied.Provider)
	}
}