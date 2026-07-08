package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func checkNamed(checks []struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}, name, status string) bool {
	for _, c := range checks {
		if c.Name == name {
			return c.Status == status
		}
	}
	return false
}

func TestPreflightPassesWhenCredentialsResolveGlobally(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	// Profile declares a credential ref; a global binding resolves it. Preflight
	// also materializes the binding, so the env var must actually be set.
	t.Setenv("CODEX_API_KEY", "configured-secret")
	profileID := createRuntimeProfile(t, server, `{
		"name":"Codex",
		"provider":"codex",
		"fields":{"credential_refs":["codex-api-key"]}
	}`)
	putBinding(t, server, "/api/credential-bindings", `{
		"credential_ref":"codex-api-key",
		"source":{"kind":"env","value":"CODEX_API_KEY"}
	}`)

	body := []byte(`{"runtime_profile_id":"` + profileID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected preflight status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Pass {
		t.Fatalf("expected preflight to pass with resolved credentials, got %#v", result.Checks)
	}
	if !checkNamed(result.Checks, "credentials", "pass") {
		t.Fatalf("expected credentials check to pass, got %#v", result.Checks)
	}
}

func TestPreflightPreviewsPiCatalogRuntimeExtension(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	profileID := createRuntimeProfile(t, server, `{
		"name":"Pi Catalog",
		"provider":"pi",
		"fields":{
			"model":"claude-sonnet-4",
			"endpoint":"https://api.example.test/anthropic",
			"api_keys":{"ANTHROPIC_API_KEY":"sk-test"},
			"runtime_extensions":[{
				"id":"npm:pi-mcp-adapter",
				"enabled":true,
				"config":{
					"install_ref":"npm:pi-mcp-adapter",
					"registry":"pi.dev/packages"
				}
			}]
		}
	}`)

	body := []byte(`{"runtime_profile_id":"` + profileID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected preflight status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var result struct {
		Pass              bool `json:"pass"`
		RuntimeExtensions []struct {
			ID         string `json:"id"`
			Source     string `json:"source"`
			InstallRef string `json:"install_ref"`
		} `json:"runtime_extensions"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Pass {
		t.Fatalf("expected preflight to pass, got %#v", result.Checks)
	}
	if !checkNamed(result.Checks, "runtime_extensions", "pass") {
		t.Fatalf("expected runtime_extensions check to pass, got %#v", result.Checks)
	}
	if len(result.RuntimeExtensions) != 1 || result.RuntimeExtensions[0].ID != "npm:pi-mcp-adapter" {
		t.Fatalf("expected runtime extension preview, got %#v", result.RuntimeExtensions)
	}
	if result.RuntimeExtensions[0].Source != "catalog" || result.RuntimeExtensions[0].InstallRef != "npm:pi-mcp-adapter" {
		t.Fatalf("unexpected catalog preview: %#v", result.RuntimeExtensions[0])
	}
}

func TestPreflightFailsWhenRuntimeProfileMissing(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	body := []byte(`{"runtime_profile_id":"missing"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected preflight status 200 (fail is still 200), got %d with body %s", resp.Code, resp.Body.String())
	}

	var result struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode preflight response: %v", err)
	}
	if result.Pass {
		t.Fatal("expected preflight to fail (pass=false) when profile is missing")
	}
	if !checkNamed(result.Checks, "runtime_profile", "fail") {
		t.Fatalf("expected runtime_profile check to fail, got %#v", result.Checks)
	}
}

func TestPreflightUsesProjectDefaultsWhenLaunchOmitsRuntimeControls(t *testing.T) {
	server := newDaemon(t)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	projectID := createProject(t, server, `{
		"name":"Acme",
		"scope":{"domains":["example.com"]},
		"defaults":{"runtime_profile":`+quoteJSON(profileID)+`,"runner":"sandbox"}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected preflight status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode preflight response: %v", err)
	}
	if !result.Pass {
		t.Fatalf("expected preflight to pass using project defaults, got %#v", result.Checks)
	}
	if !checkNamed(result.Checks, "runtime_profile", "pass") {
		t.Fatalf("expected runtime_profile check to pass, got %#v", result.Checks)
	}
}

func TestPreflightReturnsEnabledSkillPreviewWithoutCredentialBlockers(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	putSkill(t, server, "recon-helper", `{
		"name":"Recon Helper",
		"credential_refs":["recon-api-key"],
		"files":{"SKILL.md":"# Recon"}
	}`)

	body := []byte(`{"runtime_profile_id":"` + profileID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected preflight status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
		Skills []struct {
			ID string `json:"id"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode preflight response: %v", err)
	}
	if !result.Pass {
		t.Fatalf("expected skill credential_refs input to be ignored by preflight, got %#v", result.Checks)
	}
	if !checkNamed(result.Checks, "skills", "pass") || !checkNamed(result.Checks, "credentials", "pass") {
		t.Fatalf("expected skills and credentials checks to pass, got %#v", result.Checks)
	}
	if len(result.Skills) != 1 || result.Skills[0].ID != "recon-helper" {
		t.Fatalf("expected skill preview, got %#v", result.Skills)
	}
}

func TestPreflightFailsWhenRequiredRuntimeLacksModelProvider(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex"}`)

	body := []byte(`{"runtime_profile_id":"` + profileID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected preflight status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Pass   bool `json:"pass"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode preflight response: %v", err)
	}
	if result.Pass {
		t.Fatal("expected preflight to fail when codex profile has no model provider")
	}
	if !checkNamed(result.Checks, "model_provider", "fail") {
		t.Fatalf("expected model_provider check to fail, got %#v", result.Checks)
	}
}

func TestPreflightBuiltinSkillPreviewUsesSourceFreeID(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	putSkill(t, server, "cyberstrikeai-vulnerabilities-xss", `{
		"name":"cyberstrikeai-vulnerabilities-xss",
		"source_provenance":{"kind":"builtin"},
		"files":{"SKILL.md":"# XSS Testing"}
	}`)

	body := []byte(`{"runtime_profile_id":"` + profileID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected preflight status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Skills []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode preflight response: %v", err)
	}
	if len(result.Skills) != 1 || result.Skills[0].ID != "vulnerabilities-xss" || result.Skills[0].Name != "vulnerabilities-xss" {
		t.Fatalf("expected source-free builtin skill preview, got %#v", result.Skills)
	}
}

// TestPreflightPreviewsSelectedEndpointBaseURL locks the daemon Model Preflight
// Preview contract: the HTTP preflight preview surfaces the selected endpoint
// base URL, protocol, model, and API key source status for a provider with
// split protocol paths, without exposing API key values. Claude Code selects
// the Anthropic Messages endpoint via runtime plugin preference.
func TestPreflightPreviewsSelectedEndpointBaseURL(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)

	createProvider := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(`{
		"name":"Split Path",
		"endpoints":[
			{"protocol":"openai_responses","base_url":"https://api.example.test/api/coding/paas/v4"},
			{"protocol":"anthropic_messages","base_url":"https://api.example.test/api/anthropic"}
		],
		"catalog":{"manual":["glm"],"default_model":"glm"}
	}`)))
	createProvider.Header.Set("Content-Type", "application/json")
	providerResp := httptest.NewRecorder()
	server.ServeHTTP(providerResp, createProvider)
	if providerResp.Code != http.StatusCreated {
		t.Fatalf("create provider status %d body %s", providerResp.Code, providerResp.Body.String())
	}
	var provider struct {
		ID        string `json:"id"`
		APIKeyEnv string `json:"api_key_env"`
	}
	if err := json.NewDecoder(providerResp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-daemon-preview-secret")

	profileID := createRuntimeProfile(t, server, `{
		"name":"Claude",
		"provider":"claude_code",
		"fields":{"model_provider_id":"`+provider.ID+`"}
	}`)

	body := []byte(`{
		"runtime_profile_id":"` + profileID + `",
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("preflight status %d body %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Pass bool `json:"pass"`
		ModelProvider *struct {
			EndpointBaseURL  string `json:"endpoint_base_url"`
			BaseURL          string `json:"base_url"`
			Protocol         string `json:"protocol"`
			Model            string `json:"model"`
			APIKeyEnv        string `json:"api_key_env"`
			APIKeySource     string `json:"api_key_source"`
			ProjectionTarget string `json:"projection_target"`
		} `json:"model_provider"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode preflight: %v", err)
	}
	if !result.Pass {
		t.Fatal("expected preflight to pass")
	}
	if result.ModelProvider == nil {
		t.Fatal("expected model provider preview")
	}
	preview := result.ModelProvider
	if preview.EndpointBaseURL != "https://api.example.test/api/anthropic" {
		t.Fatalf("endpoint_base_url = %q", preview.EndpointBaseURL)
	}
	if preview.BaseURL != preview.EndpointBaseURL {
		t.Fatalf("base_url alias = %q, want endpoint_base_url %q", preview.BaseURL, preview.EndpointBaseURL)
	}
	if preview.Protocol != "anthropic_messages" {
		t.Fatalf("protocol = %q", preview.Protocol)
	}
	if preview.Model != "glm" {
		t.Fatalf("model = %q", preview.Model)
	}
	if preview.APIKeyEnv != provider.APIKeyEnv || preview.APIKeySource == "" {
		t.Fatalf("api key source provenance missing: env=%q source=%q", preview.APIKeyEnv, preview.APIKeySource)
	}
	if preview.ProjectionTarget != "claude_settings" {
		t.Fatalf("projection_target = %q", preview.ProjectionTarget)
	}
	// The HTTP preview must not expose the API key value.
	if strings.Contains(resp.Body.String(), "sk-daemon-preview-secret") {
		t.Fatalf("preflight response leaked API key value: %s", resp.Body.String())
	}
}
