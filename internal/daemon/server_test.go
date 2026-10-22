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
	"pentest/internal/project"
)

func TestHealthEndpointReportsVersionAndDatabaseStatus(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}

	var body struct {
		Version  string `json:"version"`
		Database struct {
			Status string `json:"status"`
		} `json:"database"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if body.Version != "test-version" {
		t.Fatalf("expected version test-version, got %q", body.Version)
	}
	if body.Database.Status != "ok" {
		t.Fatalf("expected database status ok, got %q", body.Database.Status)
	}
}

func TestProjectCanBeCreatedAndReadWithScope(t *testing.T) {
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

	createBody := []byte(`{
		"name": "Acme External",
		"description": "External perimeter test",
		"kind": "ctf_challenge",
		"scope": {
			"domains": ["example.com"],
			"urls": ["https://example.com"],
			"excluded": ["admin.example.com"],
			"testing_limits": ["no destructive payloads"]
		}
	}`)

	createRequest := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(createBody))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()

	server.ServeHTTP(createResponse, createRequest)

	if createResponse.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", createResponse.Code, createResponse.Body.String())
	}

	var created struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
		Scope       struct {
			Domains       []string `json:"domains"`
			URLs          []string `json:"urls"`
			Excluded      []string `json:"excluded"`
			TestingLimits []string `json:"testing_limits"`
		} `json:"scope"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected project id")
	}
	if created.Name != "Acme External" {
		t.Fatalf("expected project name Acme External, got %q", created.Name)
	}
	if created.Kind != project.KindCTFChallenge {
		t.Fatalf("expected CTF Challenge Project, got %q", created.Kind)
	}
	if got := created.Scope.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope domain example.com, got %#v", got)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+created.ID, nil)
	getResponse := httptest.NewRecorder()

	server.ServeHTTP(getResponse, getRequest)

	if getResponse.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", getResponse.Code, getResponse.Body.String())
	}

	var fetched struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       struct {
			Domains       []string `json:"domains"`
			URLs          []string `json:"urls"`
			Excluded      []string `json:"excluded"`
			TestingLimits []string `json:"testing_limits"`
		} `json:"scope"`
	}
	if err := json.NewDecoder(getResponse.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}

	if fetched.ID != created.ID {
		t.Fatalf("expected fetched id %q, got %q", created.ID, fetched.ID)
	}
	if fetched.Description != "External perimeter test" {
		t.Fatalf("expected description External perimeter test, got %q", fetched.Description)
	}
	if got := fetched.Scope.TestingLimits; len(got) != 1 || got[0] != "no destructive payloads" {
		t.Fatalf("expected testing limit, got %#v", got)
	}
}

func TestCreateProjectRequiresExplicitKind(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{Version: "test-version", DBPath: filepath.Join(t.TempDir(), "pentest.db")})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	for name, body := range map[string]string{
		"missing": `{"name":"Implicit"}`,
		"unknown": `{"name":"Unknown","kind":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d with body %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), project.ErrInvalidKind.Error()) {
				t.Fatalf("expected invalid kind error, got %s", response.Body.String())
			}
		})
	}
}

func TestProjectKindConversionPreviewAndConfirmUseExplicitOperatorFlow(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{Version: "test-version", DBPath: filepath.Join(t.TempDir(), "pentest.db")})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectID := createProject(t, server, `{"name":"Arena","kind":"pentest"}`)

	previewRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/kind-conversion/preview", strings.NewReader(`{"target_kind":"ctf_challenge"}`))
	previewResponse := httptest.NewRecorder()
	server.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK || !strings.Contains(previewResponse.Body.String(), `"ready":true`) {
		t.Fatalf("unexpected preview response %d: %s", previewResponse.Code, previewResponse.Body.String())
	}

	confirmRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/kind-conversion", strings.NewReader(`{"target_kind":"ctf_challenge","confirm":true}`))
	confirmResponse := httptest.NewRecorder()
	server.ServeHTTP(confirmResponse, confirmRequest)
	if confirmResponse.Code != http.StatusOK || !strings.Contains(confirmResponse.Body.String(), `"kind":"ctf_challenge"`) {
		t.Fatalf("unexpected conversion response %d: %s", confirmResponse.Code, confirmResponse.Body.String())
	}
}

func TestProjectsCanBeListedForDashboard(t *testing.T) {
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

	createProject(t, server, `{"name":"First","scope":{"domains":["first.example"]}}`)
	createProject(t, server, `{"name":"Second","scope":{"domains":["second.example"]}}`)

	request := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d with body %s", response.Code, response.Body.String())
	}

	var body struct {
		Projects []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Scope struct {
				Domains []string `json:"domains"`
			} `json:"scope"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode list response: %v", err)
	}

	if len(body.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(body.Projects))
	}
	if body.Projects[0].Name != "First" {
		t.Fatalf("expected first project First, got %q", body.Projects[0].Name)
	}
	if body.Projects[1].Name != "Second" {
		t.Fatalf("expected second project Second, got %q", body.Projects[1].Name)
	}
}

func TestCreateProjectPersistsRunnerAndDropsRemovedRuntimeProfileDefault(t *testing.T) {
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

	id := createProject(t, server, `{
		"name": "Acme",
		"defaults": {"runner": "sandbox", "runtime_profile": "codex-default"}
	}`)

	var fetched struct {
		Defaults map[string]any `json:"defaults"`
	}
	getProject(t, server, id, &fetched)

	if fetched.Defaults["runner"] != "sandbox" {
		t.Fatalf("expected default runner sandbox, got %#v", fetched.Defaults["runner"])
	}
	if _, exists := fetched.Defaults["runtime_profile"]; exists {
		t.Fatalf("removed runtime_profile default was persisted: %#v", fetched.Defaults)
	}
}

func TestPatchProjectUpdatesNameAndScope(t *testing.T) {
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

	id := createProject(t, server, `{"name":"Original","scope":{"domains":["example.com"],"notes":"original"}}`)

	patchBody := []byte(`{
		"name": "Renamed",
		"scope": {"domains": ["acme.example"], "testing_limits": ["business hours"]}
	}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusOK {
		t.Fatalf("expected patch status 200, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}

	var patched struct {
		Name  string `json:"name"`
		Scope struct {
			Domains       []string `json:"domains"`
			Notes         string   `json:"notes"`
			TestingLimits []string `json:"testing_limits"`
		} `json:"scope"`
	}
	if err := json.NewDecoder(patchResponse.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patched.Name != "Renamed" {
		t.Fatalf("expected renamed project, got %q", patched.Name)
	}
	if got := patched.Scope.Domains; len(got) != 1 || got[0] != "acme.example" {
		t.Fatalf("expected scope domains overwritten, got %#v", got)
	}
	if patched.Scope.Notes != "" {
		t.Fatalf("expected scope notes cleared by full-scope overwrite, got %q", patched.Scope.Notes)
	}
	if got := patched.Scope.TestingLimits; len(got) != 1 || got[0] != "business hours" {
		t.Fatalf("expected new testing limits, got %#v", got)
	}

	// The update must persist across reloads.
	var fetched struct {
		Name  string `json:"name"`
		Scope struct {
			Domains []string `json:"domains"`
		} `json:"scope"`
	}
	getProject(t, server, id, &fetched)
	if fetched.Name != "Renamed" {
		t.Fatalf("expected persisted rename, got %q", fetched.Name)
	}
	if got := fetched.Scope.Domains; len(got) != 1 || got[0] != "acme.example" {
		t.Fatalf("expected persisted scope, got %#v", got)
	}
}

func TestPatchProjectPreservesUntouchedScope(t *testing.T) {
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

	id := createProject(t, server, `{"name":"Original","scope":{"domains":["example.com"],"notes":"keep me"}}`)

	patchBody := []byte(`{"description":"only description changes"}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusOK {
		t.Fatalf("expected patch status 200, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}

	var fetched struct {
		Scope struct {
			Domains []string `json:"domains"`
			Notes   string   `json:"notes"`
		} `json:"scope"`
	}
	getProject(t, server, id, &fetched)
	if got := fetched.Scope.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected untouched scope preserved, got %#v", got)
	}
	if fetched.Scope.Notes != "keep me" {
		t.Fatalf("expected untouched notes preserved, got %q", fetched.Scope.Notes)
	}
}

func TestPatchProjectRejectsBlankName(t *testing.T) {
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

	id := createProject(t, server, `{"name":"Original"}`)

	patchBody := []byte(`{"name":"   "}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected blank-name patch to fail with 400, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}
}

func TestPatchProjectReturnsNotFoundForUnknownId(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	patchBody := []byte(`{"name":"Anything"}`)
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/projects/missing", bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	server.ServeHTTP(patchResponse, patchRequest)

	if patchResponse.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown id, got %d with body %s", patchResponse.Code, patchResponse.Body.String())
	}
}

func TestListProjectsReturnsArrayShape(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{
		Version: "test-version",
		DBPath:  ":memory:",
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	request := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", response.Code)
	}
	var body struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode empty list response: %v", err)
	}
	if body.Projects == nil {
		t.Fatal("expected projects array, got null")
	}
}
