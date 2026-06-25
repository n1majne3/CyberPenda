package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pentest/internal/daemon"
)

func TestModelProviderAPIManagesProvidersAndBlocksReferencedDelete(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{Version: "test-version", DBPath: t.TempDir() + "/pentest.db", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	create := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(`{
		"name":"MiMo",
		"base_url":"https://api.example.test/v1/",
		"protocols":["openai_responses"],
		"catalog":{"manual":["mimo"],"default_model":"mimo"}
	}`)))
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create status %d body %s", resp.Code, resp.Body.String())
	}
	var provider struct {
		ID        string `json:"id"`
		BaseURL   string `json:"base_url"`
		APIKeyEnv string `json:"api_key_env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if provider.ID != "mimo" || provider.BaseURL != "https://api.example.test/v1" || provider.APIKeyEnv != "MIMO_API_KEY" {
		t.Fatalf("unexpected provider: %#v", provider)
	}

	createProfile := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader([]byte(`{
		"name":"Pi",
		"provider":"pi",
		"fields":{"model_provider_id":"mimo"}
	}`)))
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, createProfile)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create profile status %d body %s", resp.Code, resp.Body.String())
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/model-providers/mimo", bytes.NewReader([]byte(`{
		"catalog":{"manual":["mimo","mimo-v2"],"default_model":"mimo-v2"}
	}`)))
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, patch)
	if resp.Code != http.StatusOK {
		t.Fatalf("patch status %d body %s", resp.Code, resp.Body.String())
	}
	var patched struct {
		Catalog struct {
			Manual       []string `json:"manual"`
			DefaultModel string   `json:"default_model"`
		} `json:"catalog"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched provider: %v", err)
	}
	if patched.Catalog.DefaultModel != "mimo-v2" {
		t.Fatalf("patched default model = %q", patched.Catalog.DefaultModel)
	}
	if len(patched.Catalog.Manual) != 2 {
		t.Fatalf("patched manual catalog = %#v", patched.Catalog.Manual)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/model-providers/mimo", nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, deleteReq)
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected conflict deleting referenced provider, got %d body %s", resp.Code, resp.Body.String())
	}
}
