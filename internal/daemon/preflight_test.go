package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	// Profile declares a credential ref; a global binding resolves it.
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
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex"}`)
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

func TestPreflightBuiltinSkillPreviewUsesSourceFreeID(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex"}`)
	putSkill(t, server, "cyberstrikeai-api-security-testing", `{
		"name":"cyberstrikeai-api-security-testing",
		"source_provenance":{"kind":"builtin"},
		"files":{"SKILL.md":"# API Security Testing"}
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
	if len(result.Skills) != 1 || result.Skills[0].ID != "api-security-testing" || result.Skills[0].Name != "api-security-testing" {
		t.Fatalf("expected source-free builtin skill preview, got %#v", result.Skills)
	}
}
