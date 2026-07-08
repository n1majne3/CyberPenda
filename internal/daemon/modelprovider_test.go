package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
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

// roundTripFunc is a minimal http.RoundTripper that records the outgoing
// request and returns a canned response, so the daemon refresh API can be
// exercised end to end without real network traffic.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newRefreshServer(t *testing.T, transport http.RoundTripper) *daemon.Server {
	t.Helper()
	server, err := daemon.NewServer(daemon.Config{
		Version:              "test-version",
		DBPath:               t.TempDir() + "/pentest.db",
		DisableBuiltinSkills: true,
		ModelRefreshClient:   &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func createRefreshProvider(t *testing.T, server *daemon.Server, body string) (id, apiKeyEnv string) {
	t.Helper()
	create := httptest.NewRequest(http.MethodPost, "/api/model-providers", bytes.NewReader([]byte(body)))
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create provider status %d body %s", resp.Code, resp.Body.String())
	}
	var provider struct {
		ID        string `json:"id"`
		APIKeyEnv string `json:"api_key_env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	return provider.ID, provider.APIKeyEnv
}

// TestModelProviderAPIRefreshUsesChatCompletionsOrigin locks the daemon
// API contract for Model Catalog Refresh: it derives the refresh URL from
// the OpenAI Chat Completions endpoint origin, drops runtime path prefixes,
// and always requests /v1/models, then merges refreshed identifiers into
// the provider-level catalog.
func TestModelProviderAPIRefreshUsesChatCompletionsOrigin(t *testing.T) {
	var refreshURL string
	var authHeader string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		refreshURL = r.URL.String()
		authHeader = r.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"id":"gpt-5"},{"id":"shared"}]}`)),
			Header:     http.Header{},
		}, nil
	})
	server := newRefreshServer(t, transport)

	id, apiKeyEnv := createRefreshProvider(t, server, `{
		"name":"Split Path",
		"endpoints":[
			{"protocol":"openai_responses","base_url":"https://responses.example.test/api/coding/paas/v4/"},
			{"protocol":"openai_chat_completions","base_url":"https://chat.example.test/openai/v1"},
			{"protocol":"anthropic_messages","base_url":"https://api.example.test/anthropic"}
		],
		"catalog":{"manual":["shared","manual-only"],"default_model":"shared"}
	}`)
	t.Setenv(apiKeyEnv, "sk-test")

	refresh := httptest.NewRequest(http.MethodPost, "/api/model-providers/"+id+"/refresh-models", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, refresh)
	if resp.Code != http.StatusOK {
		t.Fatalf("refresh status %d body %s", resp.Code, resp.Body.String())
	}
	if refreshURL != "https://chat.example.test/v1/models" {
		t.Fatalf("refresh URL = %q, want chat-completions origin plus /v1/models", refreshURL)
	}
	if authHeader != "Bearer sk-test" {
		t.Fatalf("authorization = %q", authHeader)
	}
	var refreshed struct {
		Catalog struct {
			Manual    []string `json:"manual"`
			Refreshed []string `json:"refreshed"`
		} `json:"catalog"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refreshed); err != nil {
		t.Fatalf("decode refreshed provider: %v", err)
	}
	if len(refreshed.Catalog.Refreshed) != 2 || refreshed.Catalog.Refreshed[0] != "gpt-5" || refreshed.Catalog.Refreshed[1] != "shared" {
		t.Fatalf("refreshed catalog = %#v", refreshed.Catalog.Refreshed)
	}
	if len(refreshed.Catalog.Manual) != 1 || refreshed.Catalog.Manual[0] != "manual-only" {
		t.Fatalf("manual catalog = %#v, want shared merged into refreshed", refreshed.Catalog.Manual)
	}
}

// TestModelProviderAPIRefreshFallsBackToResponsesOrigin locks the daemon
// contract that Model Catalog Refresh falls back to the OpenAI Responses
// endpoint origin when Chat Completions is absent.
func TestModelProviderAPIRefreshFallsBackToResponsesOrigin(t *testing.T) {
	var refreshURL string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		refreshURL = r.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"id":"gpt-5"}]}`)),
			Header:     http.Header{},
		}, nil
	})
	server := newRefreshServer(t, transport)

	id, apiKeyEnv := createRefreshProvider(t, server, `{
		"name":"Responses Only",
		"endpoints":[
			{"protocol":"openai_responses","base_url":"https://responses.example.test/api/coding/v4"},
			{"protocol":"anthropic_messages","base_url":"https://api.example.test/anthropic"}
		],
		"catalog":{"manual":["manual"],"default_model":"manual"}
	}`)
	t.Setenv(apiKeyEnv, "sk-test")

	refresh := httptest.NewRequest(http.MethodPost, "/api/model-providers/"+id+"/refresh-models", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, refresh)
	if resp.Code != http.StatusOK {
		t.Fatalf("refresh status %d body %s", resp.Code, resp.Body.String())
	}
	if refreshURL != "https://responses.example.test/v1/models" {
		t.Fatalf("refresh URL = %q, want responses origin plus /v1/models", refreshURL)
	}
}

// TestModelProviderAPIRefreshUnavailableForAnthropicOnlyProvider locks the
// daemon contract that Model Catalog Refresh is unavailable for a provider
// with no OpenAI-family endpoint. The API must surface the failure rather
// than guessing from Anthropic-only configuration, while preserving the
// existing catalog.
func TestModelProviderAPIRefreshUnavailableForAnthropicOnlyProvider(t *testing.T) {
	called := false
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: http.Header{}}, nil
	})
	server := newRefreshServer(t, transport)

	id, apiKeyEnv := createRefreshProvider(t, server, `{
		"name":"Anthropic Only",
		"endpoints":[{"protocol":"anthropic_messages","base_url":"https://api.anthropic.example.test"}],
		"catalog":{"manual":["claude-sonnet"],"default_model":"claude-sonnet"}
	}`)
	t.Setenv(apiKeyEnv, "sk-test")

	refresh := httptest.NewRequest(http.MethodPost, "/api/model-providers/"+id+"/refresh-models", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, refresh)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected refresh unavailable status, got %d body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "OpenAI-family endpoint") {
		t.Fatalf("expected refresh unavailable error to explain the missing OpenAI-family endpoint, got %q", resp.Body.String())
	}
	if called {
		t.Fatal("refresh must not call upstream when no OpenAI-family endpoint exists")
	}

	// The existing catalog must survive the unavailable refresh.
	get := httptest.NewRequest(http.MethodGet, "/api/model-providers/"+id, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, get)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status %d body %s", getResp.Code, getResp.Body.String())
	}
	var fetched struct {
		Catalog struct {
			Manual []string `json:"manual"`
		} `json:"catalog"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched provider: %v", err)
	}
	if len(fetched.Catalog.Manual) != 1 || fetched.Catalog.Manual[0] != "claude-sonnet" {
		t.Fatalf("manual catalog changed after unavailable refresh: %#v", fetched.Catalog.Manual)
	}
}

// TestModelProviderAPIRefreshPreservesCatalogOnUpstreamError locks the
// daemon contract that a temporary upstream failure during Model Catalog
// Refresh preserves the previous catalog and surfaces the error.
func TestModelProviderAPIRefreshPreservesCatalogOnUpstreamError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"upstream"}`)),
			Header:     http.Header{},
		}, nil
	})
	server := newRefreshServer(t, transport)

	id, apiKeyEnv := createRefreshProvider(t, server, `{
		"name":"MiMo",
		"base_url":"https://api.example.test/v1",
		"protocols":["openai_chat_completions"],
		"catalog":{"manual":["manual"],"refreshed":["prior-refreshed"],"default_model":"manual"}
	}`)
	t.Setenv(apiKeyEnv, "sk-test")

	refresh := httptest.NewRequest(http.MethodPost, "/api/model-providers/"+id+"/refresh-models", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, refresh)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected upstream failure status, got %d body %s", resp.Code, resp.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api/model-providers/"+id, nil)
	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, get)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status %d body %s", getResp.Code, getResp.Body.String())
	}
	var fetched struct {
		Catalog struct {
			Manual    []string `json:"manual"`
			Refreshed []string `json:"refreshed"`
		} `json:"catalog"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched provider: %v", err)
	}
	if len(fetched.Catalog.Manual) != 1 || fetched.Catalog.Manual[0] != "manual" {
		t.Fatalf("manual catalog changed after upstream failure: %#v", fetched.Catalog.Manual)
	}
	if len(fetched.Catalog.Refreshed) != 1 || fetched.Catalog.Refreshed[0] != "prior-refreshed" {
		t.Fatalf("refreshed catalog changed after upstream failure: %#v", fetched.Catalog.Refreshed)
	}
}
