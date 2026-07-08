package modelprovider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestCreateGeneratesStableIDAndAPIKeyEnv(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))

	first, err := svc.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://token-plan-cn.xiaomimimo.com/v1/",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.Create(modelprovider.CreateRequest{Name: "MiMo", BaseURL: "https://example.test/v1"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	if first.ID != "mimo" || first.APIKeyEnv != "MIMO_API_KEY" {
		t.Fatalf("first id/env = %q/%q", first.ID, first.APIKeyEnv)
	}
	if first.BaseURL != "https://token-plan-cn.xiaomimimo.com/v1" {
		t.Fatalf("base URL was not normalized: %q", first.BaseURL)
	}
	if second.ID != "mimo-2" || second.APIKeyEnv != "MIMO_2_API_KEY" {
		t.Fatalf("second id/env = %q/%q", second.ID, second.APIKeyEnv)
	}

	renamed, err := svc.Update(first.ID, modelprovider.UpdateRequest{Name: ptr("MiMo CN")})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.ID != "mimo" || renamed.APIKeyEnv != "MIMO_API_KEY" {
		t.Fatalf("rename changed id/env: %q/%q", renamed.ID, renamed.APIKeyEnv)
	}
}

func TestCreateUpdateAndFetchEndpointBackedProvider(t *testing.T) {
	db := newStore(t)
	svc := modelprovider.NewService(db)

	created, err := svc.Create(modelprovider.CreateRequest{
		Name: "Endpoint Provider",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/v1/"},
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/anthropic/"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{"gpt"}, DefaultModel: "gpt"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.BaseURL != "https://api.example.test/v1" {
		t.Fatalf("compat base URL = %q", created.BaseURL)
	}
	if !reflect.DeepEqual(created.Protocols, []modelprovider.Protocol{
		modelprovider.ProtocolOpenAIResponses,
		modelprovider.ProtocolAnthropicMessages,
	}) {
		t.Fatalf("protocols = %#v", created.Protocols)
	}
	if !reflect.DeepEqual(created.Endpoints, []modelprovider.Endpoint{
		{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/v1"},
		{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/anthropic"},
	}) {
		t.Fatalf("endpoints = %#v", created.Endpoints)
	}
	var storedProtocols string
	if err := db.QueryRow(`SELECT protocols_json FROM model_providers WHERE id = ?`, created.ID).Scan(&storedProtocols); err != nil {
		t.Fatalf("query stored protocols: %v", err)
	}
	if storedProtocols != "[]" {
		t.Fatalf("stored protocols_json = %q, want endpoint-backed storage without canonical protocols", storedProtocols)
	}

	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(fetched.Endpoints, created.Endpoints) {
		t.Fatalf("fetched endpoints = %#v", fetched.Endpoints)
	}

	updated, err := svc.Update(created.ID, modelprovider.UpdateRequest{
		Endpoints: &[]modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIChatCompletions, BaseURL: "https://chat.example.test/openai/v1/"},
		},
	})
	if err != nil {
		t.Fatalf("update endpoints: %v", err)
	}
	if !reflect.DeepEqual(updated.Protocols, []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions}) {
		t.Fatalf("updated protocols = %#v", updated.Protocols)
	}
	if updated.BaseURL != "https://chat.example.test/openai/v1" {
		t.Fatalf("updated base URL = %q", updated.BaseURL)
	}
}

func TestEndpointValidationRejectsDuplicatesAndOperationSuffixes(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))

	if _, err := svc.Create(modelprovider.CreateRequest{
		Name: "Duplicate",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/v1"},
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://other.example.test/v1"},
		},
	}); err == nil {
		t.Fatal("expected duplicate endpoint protocol to fail")
	}

	cases := []struct {
		name     string
		protocol modelprovider.Protocol
		baseURL  string
	}{
		{"versioned messages", modelprovider.ProtocolAnthropicMessages, "https://api.example.test/v1/messages/"},
		{"unversioned messages", modelprovider.ProtocolAnthropicMessages, "https://api.example.test/messages"},
		{"versioned responses", modelprovider.ProtocolOpenAIResponses, "https://api.example.test/v1/responses"},
		{"unversioned responses", modelprovider.ProtocolOpenAIResponses, "https://api.example.test/responses/"},
		{"versioned chat completions", modelprovider.ProtocolOpenAIChatCompletions, "https://api.example.test/v1/chat/completions"},
		{"unversioned chat completions", modelprovider.ProtocolOpenAIChatCompletions, "https://api.example.test/chat/completions/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(modelprovider.CreateRequest{
				Name: tc.name,
				Endpoints: []modelprovider.Endpoint{
					{Protocol: tc.protocol, BaseURL: tc.baseURL},
				},
			}); err == nil {
				t.Fatalf("expected operation URL %q to fail", tc.baseURL)
			}
		})
	}
}

func TestLegacyProviderBackfillsEndpointsFromBaseURLAndProtocols(t *testing.T) {
	db := newStore(t)
	if err := seedLegacyProvider(db, "legacy", "https://hub.example.test/v1/",
		[]modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses, modelprovider.ProtocolAnthropicMessages},
		modelprovider.Catalog{}); err != nil {
		t.Fatalf("insert legacy provider: %v", err)
	}

	provider, err := modelprovider.NewService(db).Get("legacy")
	if err != nil {
		t.Fatalf("get legacy provider: %v", err)
	}
	if !reflect.DeepEqual(provider.Endpoints, []modelprovider.Endpoint{
		{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://hub.example.test/v1"},
		{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://hub.example.test"},
	}) {
		t.Fatalf("legacy endpoints = %#v", provider.Endpoints)
	}
	if !provider.Supports(modelprovider.ProtocolAnthropicMessages) {
		t.Fatalf("legacy protocols were not derived from endpoints: %#v", provider.Protocols)
	}
}

// TestLegacyProviderBackfillEndpointMatrix locks the endpoint-backfill
// contract across the URL shapes called out for migration: /v1, /v2,
// host-only, and deeper path prefixes. Non-Anthropic protocols copy the
// normalized legacy base URL; anthropic_messages removes only the final
// non-empty path segment with no other semantic URL repair.
func TestLegacyProviderBackfillEndpointMatrix(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		protocol modelprovider.Protocol
		want     string
	}{
		{"openai responses v1", "https://hub.example.test/v1/", modelprovider.ProtocolOpenAIResponses, "https://hub.example.test/v1"},
		{"anthropic messages v1", "https://hub.example.test/v1/", modelprovider.ProtocolAnthropicMessages, "https://hub.example.test"},
		{"openai responses v2", "https://provider.example.test/v2", modelprovider.ProtocolOpenAIResponses, "https://provider.example.test/v2"},
		{"anthropic messages v2", "https://provider.example.test/v2", modelprovider.ProtocolAnthropicMessages, "https://provider.example.test"},
		{"openai responses host only", "https://host-only.example.test", modelprovider.ProtocolOpenAIResponses, "https://host-only.example.test"},
		{"anthropic messages host only unchanged", "https://host-only.example.test", modelprovider.ProtocolAnthropicMessages, "https://host-only.example.test"},
		{"openai chat completions deeper path", "https://open.example.test/api/coding/paas/v4", modelprovider.ProtocolOpenAIChatCompletions, "https://open.example.test/api/coding/paas/v4"},
		{"anthropic messages deeper path drops only final segment", "https://open.example.test/api/coding/paas/v4", modelprovider.ProtocolAnthropicMessages, "https://open.example.test/api/coding/paas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newStore(t)
			if err := seedLegacyProvider(db, "legacy", tc.baseURL, []modelprovider.Protocol{tc.protocol}, modelprovider.Catalog{}); err != nil {
				t.Fatalf("insert legacy provider: %v", err)
			}
			provider, err := modelprovider.NewService(db).Get("legacy")
			if err != nil {
				t.Fatalf("get legacy provider: %v", err)
			}
			endpoint, ok := provider.EndpointFor(tc.protocol)
			if !ok {
				t.Fatalf("expected backfilled endpoint for %s, got %#v", tc.protocol, provider.Endpoints)
			}
			if endpoint.BaseURL != tc.want {
				t.Fatalf("endpoint base URL = %q, want %q", endpoint.BaseURL, tc.want)
			}
		})
	}
}

func TestRefreshModelsPreservesManualCatalogAndUsesGeneratedEnv(t *testing.T) {
	var auth string
	var path string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		path = r.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"id":"refreshed"},{"id":"manual"}]}`)),
			Header:     http.Header{},
		}, nil
	})}

	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://upstream.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"manual", "local-only"}, DefaultModel: "manual"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	refreshed, err := svc.RefreshModels(context.Background(), provider.ID, client)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("authorization = %q", auth)
	}
	if path != "/v1/models" {
		t.Fatalf("refresh path = %q", path)
	}
	if !reflect.DeepEqual(refreshed.Catalog.Manual, []string{"local-only"}) {
		t.Fatalf("manual catalog = %#v", refreshed.Catalog.Manual)
	}
	if !reflect.DeepEqual(refreshed.Catalog.Refreshed, []string{"manual", "refreshed"}) {
		t.Fatalf("refreshed catalog = %#v", refreshed.Catalog.Refreshed)
	}
}

func TestRefreshModelsUsesOpenAIFamilyEndpointOrigin(t *testing.T) {
	var refreshURL string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		refreshURL = r.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"id":"gpt-5"}]}`)),
			Header:     http.Header{},
		}, nil
	})}

	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name: "Path Prefix Provider",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/api/anthropic"},
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/api/coding/paas/v4"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{"manual"}, DefaultModel: "manual"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	if _, err := svc.RefreshModels(context.Background(), provider.ID, client); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshURL != "https://api.example.test/v1/models" {
		t.Fatalf("refresh URL = %q", refreshURL)
	}
}

// TestRefreshModelsPrefersChatCompletionsOrigin locks the Model Catalog
// Refresh URL preference order: when both OpenAI-family endpoints exist,
// the refresh URL derives from the Chat Completions endpoint origin, never
// the Responses endpoint. The derived path is always /v1/models regardless
// of the runtime path prefix on the selected endpoint.
func TestRefreshModelsPrefersChatCompletionsOrigin(t *testing.T) {
	var refreshURL string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		refreshURL = r.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"id":"gpt-5"}]}`)),
			Header:     http.Header{},
		}, nil
	})}

	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name: "Preference Order Provider",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://responses.example.test/api/coding/paas/v4"},
			{Protocol: modelprovider.ProtocolOpenAIChatCompletions, BaseURL: "https://chat.example.test/openai/v1"},
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/anthropic"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{"manual"}, DefaultModel: "manual"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	if _, err := svc.RefreshModels(context.Background(), provider.ID, client); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshURL != "https://chat.example.test/v1/models" {
		t.Fatalf("refresh URL = %q, want chat-completions origin plus /v1/models", refreshURL)
	}
}

// TestRefreshModelsUnavailableForAnthropicOnlyProvider locks the contract
// that Model Catalog Refresh is unavailable when a provider has no
// OpenAI-family endpoint. Refresh must fail clearly rather than guessing
// from an Anthropic-only configuration, while manual catalog maintenance
// remains possible.
func TestRefreshModelsUnavailableForAnthropicOnlyProvider(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name: "Anthropic Only",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.anthropic.example.test"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{"claude-sonnet"}, DefaultModel: "claude-sonnet"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	if _, err := svc.RefreshModels(context.Background(), provider.ID, http.DefaultClient); err == nil {
		t.Fatal("expected refresh to be unavailable for an Anthropic-only provider")
	}
	after, err := svc.Get(provider.ID)
	if err != nil {
		t.Fatalf("get after unavailable refresh: %v", err)
	}
	if !reflect.DeepEqual(after.Catalog, provider.Catalog) {
		t.Fatalf("manual catalog changed when refresh was unavailable: %#v", after.Catalog)
	}
}

// TestRefreshModelsFailurePreservesCatalogOnUpstreamError locks the
// contract that a temporary provider error during refresh does not erase
// the existing Model Catalog. The previous manual and refreshed model
// choices must survive an upstream HTTP failure.
func TestRefreshModelsFailurePreservesCatalogOnUpstreamError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":"upstream"}`)),
			Header:     http.Header{},
		}, nil
	})}

	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:    "MiMo",
		BaseURL: "https://api.example.test/v1",
		Catalog: modelprovider.Catalog{
			Manual:       []string{"manual"},
			Refreshed:    []string{"refreshed-model"},
			DefaultModel: "manual",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	if _, err := svc.RefreshModels(context.Background(), provider.ID, client); err == nil {
		t.Fatal("expected refresh to fail on upstream error")
	}
	after, err := svc.Get(provider.ID)
	if err != nil {
		t.Fatalf("get after upstream failure: %v", err)
	}
	if !reflect.DeepEqual(after.Catalog, provider.Catalog) {
		t.Fatalf("catalog changed on upstream failure: %#v", after.Catalog)
	}
}

// TestRefreshModelsMergesManualAndRefreshedByIdentifier locks the
// contract that manual model entries and refreshed model identifiers
// coexist in one provider-level Model Catalog, with duplicate identifiers
// merged by identifier rather than shown twice.
func TestRefreshModelsMergesManualAndRefreshedByIdentifier(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"id":"shared"},{"id":"refreshed-only"}]}`)),
			Header:     http.Header{},
		}, nil
	})}

	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"shared", "manual-only"},
			DefaultModel: "shared",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	refreshed, err := svc.RefreshModels(context.Background(), provider.ID, client)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !reflect.DeepEqual(refreshed.Catalog.Refreshed, []string{"refreshed-only", "shared"}) {
		t.Fatalf("refreshed catalog = %#v", refreshed.Catalog.Refreshed)
	}
	if !reflect.DeepEqual(refreshed.Catalog.Manual, []string{"manual-only"}) {
		t.Fatalf("manual catalog = %#v, want shared merged into refreshed", refreshed.Catalog.Manual)
	}
	if !refreshed.Catalog.Contains("shared") {
		t.Fatalf("merged catalog missing shared identifier: %#v", refreshed.Catalog)
	}
}

func TestRefreshModelsFailurePreservesCatalog(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:    "OpenAI CN",
		BaseURL: "https://api.example.test/v1",
		Catalog: modelprovider.Catalog{Manual: []string{"manual"}, DefaultModel: "manual"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	os.Unsetenv(provider.APIKeyEnv)

	if _, err := svc.RefreshModels(context.Background(), provider.ID, http.DefaultClient); err == nil {
		t.Fatal("expected refresh to fail without generated env var")
	}
	after, err := svc.Get(provider.ID)
	if err != nil {
		t.Fatalf("get after refresh failure: %v", err)
	}
	if !reflect.DeepEqual(after.Catalog, provider.Catalog) {
		t.Fatalf("catalog changed on failure: %#v", after.Catalog)
	}
}

func TestUpdatePreservesRefreshedCatalogAndDefaultModel(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:    "MiMo",
		BaseURL: "https://api.example.test/v1",
		Catalog: modelprovider.Catalog{
			Manual:       []string{"manual-only"},
			Refreshed:    []string{"refreshed-model"},
			DefaultModel: "refreshed-model",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := svc.Update(provider.ID, modelprovider.UpdateRequest{
		Catalog: &modelprovider.Catalog{
			Manual:       []string{"manual-only"},
			DefaultModel: "refreshed-model",
		},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Catalog.DefaultModel != "refreshed-model" {
		t.Fatalf("default model = %q, want refreshed-model", updated.Catalog.DefaultModel)
	}
	if !reflect.DeepEqual(updated.Catalog.Refreshed, []string{"refreshed-model"}) {
		t.Fatalf("refreshed catalog = %#v", updated.Catalog.Refreshed)
	}
}

func TestDeleteProviderBlockedWhenRuntimeProfileReferencesIt(t *testing.T) {
	db := newStore(t)
	providers := modelprovider.NewService(db)
	profiles := runtimeprofile.NewService(db)
	provider, err := providers.Create(modelprovider.CreateRequest{Name: "MiMo", BaseURL: "https://api.example.test/v1"})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := profiles.Create("Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{ModelProviderID: provider.ID}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	if err := providers.Delete(provider.ID); err == nil {
		t.Fatal("expected delete to be blocked")
	}
}

func newStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedLegacyProvider inserts an old-shape provider row carrying only the
// provider-level base URL and protocols, without endpoints, so tests can
// assert compatibility-backfill behavior during the transition.
func seedLegacyProvider(db *store.DB, id, baseURL string, protocols []modelprovider.Protocol, catalog modelprovider.Catalog) error {
	protocolsJSON, err := json.Marshal(protocols)
	if err != nil {
		return fmt.Errorf("encode protocols: %w", err)
	}
	catalogJSON, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	now := "2026-07-08T00:00:00Z"
	_, err = db.Exec(
		`INSERT INTO model_providers (id, name, base_url, protocols_json, catalog_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, "Legacy", baseURL, string(protocolsJSON), string(catalogJSON), now, now,
	)
	return err
}

func ptr(s string) *string { return &s }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
