package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
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

func TestCreateRuntimeProfilePersistsProviderAndFields(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{
		"name": "Codex Default",
		"provider": "codex",
		"fields": {
			"binary_path": "/usr/local/bin/codex",
			"model": "gpt-5",
			"credential_refs": ["codex-api-key"]
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
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Fields   struct {
			BinaryPath     string   `json:"binary_path"`
			Model          string   `json:"model"`
			CredentialRefs []string `json:"credential_refs"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected profile id")
	}
	if created.Provider != "codex" {
		t.Fatalf("expected provider codex, got %q", created.Provider)
	}
	if created.Fields.Model != "gpt-5" {
		t.Fatalf("expected model gpt-5, got %q", created.Fields.Model)
	}
	if got := created.Fields.CredentialRefs; len(got) != 1 || got[0] != "codex-api-key" {
		t.Fatalf("expected credential ref preserved, got %#v", got)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+created.ID, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)

	if getResp.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", getResp.Code, getResp.Body.String())
	}

	var fetched struct {
		Provider string `json:"provider"`
		Fields   struct {
			BinaryPath string `json:"binary_path"`
			Model      string `json:"model"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.Provider != "codex" {
		t.Fatalf("expected fetched provider codex, got %q", fetched.Provider)
	}
	if fetched.Fields.BinaryPath != "/usr/local/bin/codex" {
		t.Fatalf("expected fetched binary path, got %q", fetched.Fields.BinaryPath)
	}
}

func TestCreateRuntimeProfileRejectsBlankName(t *testing.T) {
	server := newDaemon(t)

	body := []byte(`{"name":"   ","provider":"codex"}`)
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

func TestListRuntimeProfilesReturnsArrayInCreationOrder(t *testing.T) {
	server := newDaemon(t)

	createRuntimeProfile(t, server, `{"name":"First","provider":"fake"}`)
	createRuntimeProfile(t, server, `{"name":"Second","provider":"codex"}`)

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
	if body.Profiles == nil {
		t.Fatal("expected profiles array, got null")
	}
	if len(body.Profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(body.Profiles))
	}
	if body.Profiles[0].Name != "First" || body.Profiles[1].Name != "Second" {
		t.Fatalf("expected creation order First then Second, got %q then %q", body.Profiles[0].Name, body.Profiles[1].Name)
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

func getRuntimeProfile(t *testing.T, server *daemon.Server, id string, target any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+id, nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", resp.Code, resp.Body.String())
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
}

func TestPatchRuntimeProfilePreservesUntouchedFields(t *testing.T) {
	server := newDaemon(t)
	id := createRuntimeProfile(t, server, `{
		"name":"Codex",
		"provider":"codex",
		"fields":{"model":"gpt-5","binary_path":"/bin/codex"}
	}`)

	// Patch only the name; fields omitted must be preserved.
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
			Model      string `json:"model"`
			BinaryPath string `json:"binary_path"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(patchResp.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patched.Name != "Codex Renamed" {
		t.Fatalf("expected renamed profile, got %q", patched.Name)
	}
	if patched.Fields.Model != "gpt-5" {
		t.Fatalf("expected model preserved, got %q", patched.Fields.Model)
	}
	if patched.Fields.BinaryPath != "/bin/codex" {
		t.Fatalf("expected binary path preserved, got %q", patched.Fields.BinaryPath)
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

	// Subsequent get must 404.
	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+id, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusNotFound {
		t.Fatalf("expected get after delete to 404, got %d", getResp.Code)
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
