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

func createProject(t *testing.T, server *daemon.Server, body string) string {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d with body %s", response.Code, response.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created.ID
}
