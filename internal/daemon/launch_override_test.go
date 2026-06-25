package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreflightUsesLaunchModelOverrideForPresetProfile(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	createProvider := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(`{
		"name":"MiMo",
		"base_url":"https://api.example.test/v1",
		"protocols":["openai_responses"],
		"catalog":{"manual":["mimo-v2-flash","mimo-v2-pro"],"default_model":"mimo-v2-flash"}
	}`)))
	createProvider.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, createProvider)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create provider status %d body %s", resp.Code, resp.Body.String())
	}
	var provider struct {
		ID        string `json:"id"`
		APIKeyEnv string `json:"api_key_env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	profileID := createRuntimeProfile(t, server, `{
		"name":"Codex MCP",
		"provider":"codex",
		"fields":{"model_provider_id":"`+provider.ID+`","model_override":"mimo-v2-flash"}
	}`)

	body := []byte(`{
		"runtime_profile_id":"` + profileID + `",
		"model_override":"mimo-v2-pro",
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("preflight status %d body %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Pass          bool `json:"pass"`
		ModelProvider *struct {
			Model string `json:"model"`
		} `json:"model_provider"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode preflight: %v", err)
	}
	if !result.Pass {
		t.Fatal("expected preflight to pass")
	}
	if result.ModelProvider == nil || result.ModelProvider.Model != "mimo-v2-pro" {
		t.Fatalf("expected launch override preview, got %#v", result.ModelProvider)
	}

	getProfile := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profileID, nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, getProfile)
	if resp.Code != http.StatusOK {
		t.Fatalf("get profile status %d body %s", resp.Code, resp.Body.String())
	}
	var stored struct {
		Fields struct {
			ModelOverride string `json:"model_override"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if stored.Fields.ModelOverride != "mimo-v2-flash" {
		t.Fatalf("profile model_override mutated to %q", stored.Fields.ModelOverride)
	}
}