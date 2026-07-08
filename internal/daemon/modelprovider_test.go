package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/daemon"
)

func TestModelProviderAPIManagesEndpointBackedProviders(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{Version: "test-version", DBPath: t.TempDir() + "/pentest.db", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	create := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(`{
		"name":"Split Provider",
		"endpoints":[
			{"protocol":"openai_responses","base_url":"https://api.example.test/api/coding/paas/v4/"},
			{"protocol":"anthropic_messages","base_url":"https://api.example.test/api/anthropic/"}
		],
		"catalog":{"manual":["gpt"],"default_model":"gpt"}
	}`)))
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create status %d body %s", resp.Code, resp.Body.String())
	}
	var provider struct {
		ID        string   `json:"id"`
		BaseURL   string   `json:"base_url"`
		Protocols []string `json:"protocols"`
		Endpoints []struct {
			Protocol string `json:"protocol"`
			BaseURL  string `json:"base_url"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if provider.BaseURL != "https://api.example.test/api/coding/paas/v4" {
		t.Fatalf("base_url = %q", provider.BaseURL)
	}
	if len(provider.Protocols) != 2 || provider.Protocols[0] != "openai_responses" || provider.Protocols[1] != "anthropic_messages" {
		t.Fatalf("protocols = %#v", provider.Protocols)
	}
	if len(provider.Endpoints) != 2 || provider.Endpoints[0].BaseURL != "https://api.example.test/api/coding/paas/v4" {
		t.Fatalf("endpoints = %#v", provider.Endpoints)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/model-providers/"+provider.ID, nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, get)
	if resp.Code != http.StatusOK {
		t.Fatalf("get status %d body %s", resp.Code, resp.Body.String())
	}
	var fetched struct {
		Endpoints []struct {
			Protocol string `json:"protocol"`
			BaseURL  string `json:"base_url"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched provider: %v", err)
	}
	if len(fetched.Endpoints) != 2 || fetched.Endpoints[1].BaseURL != "https://api.example.test/api/anthropic" {
		t.Fatalf("fetched endpoints = %#v", fetched.Endpoints)
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/model-providers/"+provider.ID, bytes.NewReader([]byte(`{
		"endpoints":[{"protocol":"openai_chat_completions","base_url":"https://chat.example.test/v1/"}]
	}`)))
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, patch)
	if resp.Code != http.StatusOK {
		t.Fatalf("patch status %d body %s", resp.Code, resp.Body.String())
	}
	var patched struct {
		Protocols []string `json:"protocols"`
		Endpoints []struct {
			Protocol string `json:"protocol"`
			BaseURL  string `json:"base_url"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched provider: %v", err)
	}
	if len(patched.Protocols) != 1 || patched.Protocols[0] != "openai_chat_completions" {
		t.Fatalf("patched protocols = %#v", patched.Protocols)
	}
	if len(patched.Endpoints) != 1 || patched.Endpoints[0].BaseURL != "https://chat.example.test/v1" {
		t.Fatalf("patched endpoints = %#v", patched.Endpoints)
	}

	duplicate := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(`{
		"name":"Duplicate",
		"endpoints":[
			{"protocol":"openai_responses","base_url":"https://api.example.test/v1"},
			{"protocol":"openai_responses","base_url":"https://other.example.test/v1"}
		]
	}`)))
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, duplicate)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "duplicate") {
		t.Fatalf("expected duplicate validation error, got %d body %s", resp.Code, resp.Body.String())
	}
}

func TestModelProviderAPIManagesProvidersAndBlocksReferencedDelete(t *testing.T) {
	server, err := daemon.NewServer(daemon.Config{Version: "test-version", DBPath: t.TempDir() + "/pentest.db", DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	create := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(`{
		"name":"MiMo",
		"base_url":"https://api.example.test/v1/",
		"protocols":["openai_responses"],
		"catalog":{"manual":["mimo"],"default_model":"mimo"}
	}`)))
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create status %d body %s", resp.Code, resp.Body.String())
	}
	var provider struct {
		ID        string `json:"id"`
		BaseURL   string `json:"base_url"`
		APIKeyEnv string `json:"api_key_env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if provider.ID != "mimo" || provider.BaseURL != "https://api.example.test/v1" || provider.APIKeyEnv != "MIMO_API_KEY" {
		t.Fatalf("unexpected provider: %#v", provider)
	}

	createProfile := httptest.NewRequest(http.MethodPost, "/api/runtime-profiles", bytes.NewReader([]byte(`{
		"name":"Pi",
		"provider":"pi",
		"fields":{"model_provider_id":"mimo"}
	}`)))
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, createProfile)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create profile status %d body %s", resp.Code, resp.Body.String())
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/model-providers/mimo", bytes.NewReader([]byte(`{
		"catalog":{"manual":["mimo","mimo-v2"],"default_model":"mimo-v2"}
	}`)))
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, patch)
	if resp.Code != http.StatusOK {
		t.Fatalf("patch status %d body %s", resp.Code, resp.Body.String())
	}
	var patched struct {
		Catalog struct {
			Manual       []string `json:"manual"`
			DefaultModel string   `json:"default_model"`
		} `json:"catalog"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched provider: %v", err)
	}
	if patched.Catalog.DefaultModel != "mimo-v2" {
		t.Fatalf("patched default model = %q", patched.Catalog.DefaultModel)
	}
	if len(patched.Catalog.Manual) != 2 {
		t.Fatalf("patched manual catalog = %#v", patched.Catalog.Manual)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/model-providers/mimo", nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, deleteReq)
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected conflict deleting referenced provider, got %d body %s", resp.Code, resp.Body.String())
	}
}
