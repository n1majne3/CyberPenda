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
	req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/preflight", bytes.NewReader(body))
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

	body := []byte(`{"runtime_profile_id":"missing"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/preflight", bytes.NewReader(body))
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
