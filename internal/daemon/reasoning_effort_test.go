package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeProfileHTTPRoundTripsReasoningEffort(t *testing.T) {
	server := newDaemon(t)

	createBody := []byte(`{
		"name":"Codex Effort",
		"provider":"codex",
		"fields":{"model":"gpt-test","reasoning_effort":"xhigh"}
	}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status %d body %s", createResp.Code, createResp.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Fields struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Fields.ReasoningEffort != "xhigh" {
		t.Fatalf("created effort = %q, want xhigh", created.Fields.ReasoningEffort)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+created.ID, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status %d body %s", getResp.Code, getResp.Body.String())
	}
	var fetched struct {
		Fields struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if fetched.Fields.ReasoningEffort != "xhigh" {
		t.Fatalf("fetched effort = %q, want xhigh", fetched.Fields.ReasoningEffort)
	}

	// Missing stored value stays empty (resolves to high at request time only).
	missingID := createRuntimeProfile(t, server, `{"name":"No Effort","provider":"codex","fields":{"model":"gpt-test"}}`)
	missingReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+missingID, nil)
	missingResp := httptest.NewRecorder()
	server.ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusOK {
		t.Fatalf("missing get status %d body %s", missingResp.Code, missingResp.Body.String())
	}
	if strings.Contains(missingResp.Body.String(), `"reasoning_effort"`) {
		t.Fatalf("missing effort should stay omitted/empty, body %s", missingResp.Body.String())
	}
}

func TestRuntimeProfileHTTPRejectsInvalidReasoningEffort(t *testing.T) {
	server := newDaemon(t)
	body := []byte(`{
		"name":"Bad Effort",
		"provider":"codex",
		"fields":{"reasoning_effort":"auto"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s, want 400", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "reasoning effort") {
		t.Fatalf("expected reasoning effort error, body %s", resp.Body.String())
	}
}

func TestRuntimeProfileMissingReasoningEffortResolvesToHighOnLaunchCapture(t *testing.T) {
	// Storage stays empty; resolution yields high for the initial request.
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"fake","fields":{"model":"gpt-test"}}`)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profileID, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get profile status %d body %s", getResp.Code, getResp.Body.String())
	}
	if strings.Contains(getResp.Body.String(), `"reasoning_effort"`) {
		t.Fatalf("missing stored effort must stay omitted, body %s", getResp.Body.String())
	}

	// Fake provider launch still captures requested_reasoning_effort=high.
	body := []byte(`{
		"goal":"inspect example.com",
		"runtime_profile_id":` + quoteJSON(profileID) + `,
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create task status %d body %s", resp.Code, resp.Body.String())
	}
}

func TestTaskLaunchHTTPRejectsInvalidReasoningEffortOverride(t *testing.T) {
	server := newDaemon(t)
	projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
	profileID := createRuntimeProfile(t, server, `{"name":"Codex","provider":"codex","fields":{"model":"gpt-test","reasoning_effort":"medium"}}`)

	body := []byte(`{
		"goal":"inspect example.com",
		"runtime_profile_id":` + quoteJSON(profileID) + `,
		"reasoning_effort":"auto",
		"runner":"sandbox"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s, want 400", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "reasoning effort") {
		t.Fatalf("expected reasoning effort error, body %s", resp.Body.String())
	}

	// Profile must remain unchanged after a rejected launch override.
	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime-profiles/"+profileID, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get profile status %d body %s", getResp.Code, getResp.Body.String())
	}
	var stored struct {
		Fields struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&stored); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if stored.Fields.ReasoningEffort != "medium" {
		t.Fatalf("profile effort mutated to %q", stored.Fields.ReasoningEffort)
	}
}
