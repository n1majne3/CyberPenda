package modelprovider_test

import (
	"bytes"
	"context"
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

func ptr(s string) *string { return &s }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
