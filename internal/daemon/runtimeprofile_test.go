package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/runtimeprofile"
)

func newDaemon(t *testing.T) *daemon.Server {
	t.Helper()
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  filepath.Join(t.TempDir(), "pentest.db"),
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	return server
}

func TestCreateRuntimeProfilePersistsCodexProvider(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{
		"name": "Codex Default",
		"provider": "codex",
		"fields": {"model": "gpt-5"}
	}`)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, createReq)

	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", createResp.Code, createResp.Body.String())
	}

	var created struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Fields   struct {
			Model string `json:"model"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Provider != "codex" || created.Fields.Model != "gpt-5" {
		t.Fatalf("unexpected created profile: %#v", created)
	}
}

func TestCreateRuntimeProfilePersistsFakeProvider(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{
		"name": "Fake Harness",
		"provider": "fake",
		"fields": {"model": "demo"}
	}`)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, createReq)

	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", createResp.Code, createResp.Body.String())
	}
}

func TestCreateRuntimeProfileRejectsBlankName(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{"name":"   ","provider":"fake"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected blank-name create to fail with 400, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func TestCreateRuntimeProfileRejectsUnknownProvider(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{"name":"My Profile","provider":"not-a-real-provider"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown-provider create to fail with 400, got %d with body %s", resp.Code, resp.Body.String())
	}
}

func TestListRuntimeProfiles(t *testing.T) {
	server := newDaemon(t)
	createRuntimeProfile(t, server, `{"name":"Codex Default","provider":"codex"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d with body %s", resp.Code, resp.Body.String())
	}

	var body struct {
		Profiles []struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
		} `json:"profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(body.Profiles) != 1 || body.Profiles[0].Name != "Codex Default" {
		t.Fatalf("unexpected profiles: %#v", body.Profiles)
	}
}

func createRuntimeProfile(t *testing.T, server *daemon.Server, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", resp.Code, resp.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created.ID
}

func createLocalRuntimeProfile(t *testing.T, server *daemon.Server, name string, provider runtimeprofile.Provider, fields runtimeprofile.Fields) string {
	t.Helper()
	created, err := server.CreateLocalRuntimeProfile(name, provider, fields)
	if err != nil {
		t.Fatalf("create local runtime profile: %v", err)
	}
	return created.ID
}

func TestPatchRuntimeProfilePreservesUntouchedFields(t *testing.T) {
	server := newDaemon(t)
	id := createRuntimeProfile(t, server, `{
		"name":"Codex",
		"provider":"codex",
		"fields":{"model":"gpt-5"}
	}`)

	patchBody := []byte(`{"name":"Codex Renamed"}`)
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/runtime-profiles/"+id, bytes.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp := httptest.NewRecorder()
	server.ServeHTTP(patchResp, patchReq)

	if patchResp.Code != http.StatusOK {
		t.Fatalf("expected patch status 200, got %d with body %s", patchResp.Code, patchResp.Body.String())
	}

	var patched struct {
		Name   string `json:"name"`
		Fields struct {
			Model string `json:"model"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(patchResp.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patched.Name != "Codex Renamed" || patched.Fields.Model != "gpt-5" {
		t.Fatalf("unexpected patched profile: %#v", patched)
	}
}

func TestDeleteRuntimeProfileRemovesIt(t *testing.T) {
	server := newDaemon(t)
	id := createRuntimeProfile(t, server, `{"name":"Temp","provider":"fake"}`)

	delReq := httptest.NewRequest(http.MethodDelete, "/api/runtime-profiles/"+id, nil)
	delResp := httptest.NewRecorder()
	server.ServeHTTP(delResp, delReq)
	if delResp.Code != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d with body %s", delResp.Code, delResp.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+id, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusNotFound {
		t.Fatalf("expected get after delete to 404, got %d", getResp.Code)
	}
}

func TestRuntimeProfileAPIRedactsInlineAPIKeys(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{
		"name": "Codex With Key",
		"provider": "codex",
		"fields": {
			"model": "gpt-5",
			"api_keys": {"OPENAI_API_KEY": "sk-test-secret"}
		}
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", createResp.Code, createResp.Body.String())
	}

	var created struct {
		ID     string `json:"id"`
		Fields struct {
			APIKeys map[string]string `json:"api_keys"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Fields.APIKeys["OPENAI_API_KEY"] != runtimeprofile.ConfiguredAPIKeySentinel {
		t.Fatalf("expected redacted api key in response, got %#v", created.Fields.APIKeys)
	}
	if strings.Contains(createResp.Body.String(), "sk-test-secret") {
		t.Fatalf("response leaked secret: %s", createResp.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+created.ID, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d", getResp.Code)
	}
	if strings.Contains(getResp.Body.String(), "sk-test-secret") {
		t.Fatalf("get response leaked secret: %s", getResp.Body.String())
	}
}

func TestDeleteRuntimeProfileMissingReturnsNotFound(t *testing.T) {
	server := newDaemon(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/runtime-profiles/missing", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected delete missing to 404, got %d", resp.Code)
	}
}