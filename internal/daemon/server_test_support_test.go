package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pentest/internal/daemon"
	"pentest/internal/project"
)

func getProject(t *testing.T, server *daemon.Server, id string, target any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/projects/"+id, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected get status 200, got %d with body %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
}

func createProject(t *testing.T, server *daemon.Server, body string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode project fixture: %v", err)
	}
	if _, ok := payload["kind"]; !ok {
		payload["kind"] = project.KindPentest
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode project fixture: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(bodyBytes))
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
