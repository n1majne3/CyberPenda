package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateTaskRefusesLegacyModelOverrideAlias(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Legacy refusal","scope":{"domains":["example.test"]}}`)
	body := `{"type":"pentest","goal":"inspect example.test","runtime_plugin_id":"codex","model_provider_id":"provider-1","model_override":"gpt-test"}`
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", strings.NewReader(body)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model_override is no longer accepted") {
		t.Fatalf("legacy model_override status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLegacyRuntimeProfileNativeFieldsFailClosed(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Legacy profile refusal","scope":{"domains":["example.test"]}}`)
	profileBody := `{"name":"Legacy Codex","provider":"codex","fields":{"model":"gpt-test","endpoint":"https://legacy.example.test","api_keys":{"OPENAI_API_KEY":"sk-test"}}}`
	profileResponse := httptest.NewRecorder()
	server.ServeHTTP(profileResponse, httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader([]byte(profileBody))))
	if profileResponse.Code != http.StatusCreated {
		t.Fatalf("create legacy fixture profile: %d %s", profileResponse.Code, profileResponse.Body.String())
	}
	var profile struct{ ID string }
	if err := json.NewDecoder(profileResponse.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	launch := `{"type":"pentest","goal":"inspect example.test","runtime_profile_id":` + quoteJSON(profile.ID) + `,"runner":"host","run_controls":{"host_activated":true}}`
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", strings.NewReader(launch)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model provider is required") {
		t.Fatalf("legacy native fields launch status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestQueuedSteerRefusesNestedLegacyModelOverrideAlias(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Nested legacy refusal","scope":{"domains":["example.test"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
	taskID := createTask(t, server, projectID, `{"type":"pentest","goal":"inspect example.test","runtime_profile_id":`+quoteJSON(profileID)+`,"runner":"host","run_controls":{"host_activated":true}}`)
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/steer/queue", strings.NewReader(`{"directive":"continue","model_override":"gpt-test"}`))
	server.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model_override is no longer accepted") {
		t.Fatalf("nested legacy model_override status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionPreflightRefusesLegacyModelOverrideAlias(t *testing.T) {
	server := newDaemon(t)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/preflight", strings.NewReader(`{"model_override":"gpt-test"}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model_override is no longer accepted") {
		t.Fatalf("session preflight legacy model_override status=%d body=%s", response.Code, response.Body.String())
	}
}
