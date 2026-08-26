package modelprovider_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/modelprovider"
)

func TestCapabilityCacheLookupExactID(t *testing.T) {
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"deepseek/deepseek-v4-flash": {ContextWindow: 1048576, MaxOutputTokens: 128000},
	}, "", nil)

	got, ok := cache.Lookup("deepseek/deepseek-v4-flash")
	if !ok {
		t.Fatal("expected exact id hit")
	}
	if got.ContextWindow != 1048576 || got.MaxOutputTokens != 128000 {
		t.Fatalf("limits = %#v", got)
	}
}

func TestCapabilityCacheLookupUniqueSuffix(t *testing.T) {
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"moonshotai/kimi-k2.7-code": {ContextWindow: 256000, MaxOutputTokens: 256000},
	}, "", nil)

	got, ok := cache.Lookup("kimi-k2.7-code")
	if !ok {
		t.Fatal("expected unique suffix hit")
	}
	if got.ContextWindow != 256000 || got.MaxOutputTokens != 256000 {
		t.Fatalf("limits = %#v", got)
	}
}

func TestCapabilityCacheLookupAmbiguousSuffixMissesUnlessLimitsAgree(t *testing.T) {
	disagree := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"deepseek/deepseek-v4-flash":  {ContextWindow: 1048576, MaxOutputTokens: 128000},
		"fireworks/deepseek-v4-flash": {ContextWindow: 1000000, MaxOutputTokens: 32768},
	}, "", nil)
	if _, ok := disagree.Lookup("deepseek-v4-flash"); ok {
		t.Fatal("ambiguous suffix with different limits must miss")
	}

	agree := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"deepseek/deepseek-v4-flash":  {ContextWindow: 1048576, MaxOutputTokens: 128000},
		"fireworks/deepseek-v4-flash": {ContextWindow: 1048576, MaxOutputTokens: 128000},
	}, "", nil)
	got, ok := agree.Lookup("deepseek-v4-flash")
	if !ok {
		t.Fatal("ambiguous suffix with identical limits must hit")
	}
	if got.ContextWindow != 1048576 || got.MaxOutputTokens != 128000 {
		t.Fatalf("limits = %#v", got)
	}
}

func TestCapabilityCacheLookupMiss(t *testing.T) {
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"gpt-test": {ContextWindow: 128000, MaxOutputTokens: 16384},
	}, "", nil)
	if _, ok := cache.Lookup("unknown-model"); ok {
		t.Fatal("unknown model must miss")
	}
}

func TestFlattenModelsDevResolvesDuplicateIDsByMajorityThenLargerWindow(t *testing.T) {
	raw := []byte(`{
		"alpha": {"models": {
			"deepseek/deepseek-v4-flash": {"id": "deepseek/deepseek-v4-flash", "limit": {"context": 1048576, "output": 128000}},
			"alias": {"id": "shared-id", "limit": {"context": 200000, "output": 32000}}
		}},
		"beta": {"models": {
			"deepseek/deepseek-v4-flash": {"id": "deepseek/deepseek-v4-flash", "limit": {"context": 1048576, "output": 128000}},
			"other": {"id": "shared-id", "limit": {"context": 1000000, "output": 128000}}
		}}
	}`)
	flat, err := modelprovider.FlattenModelsDev(raw)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	got := flat["deepseek/deepseek-v4-flash"]
	if got.ContextWindow != 1048576 || got.MaxOutputTokens != 128000 {
		t.Fatalf("majority id = %#v", got)
	}
	// shared-id appears twice with different limits: tie, pick larger context.
	shared := flat["shared-id"]
	if shared.ContextWindow != 1000000 || shared.MaxOutputTokens != 128000 {
		t.Fatalf("tie-break id = %#v", shared)
	}
}

func TestCapabilityCacheRefreshReplacesOverlayAndPreservesPreviousOnFailure(t *testing.T) {
	dir := t.TempDir()
	bundled := map[string]modelprovider.CatalogLimits{
		"old/alpha": {ContextWindow: 128000, MaxOutputTokens: 4096},
	}
	success := roundTripCache(t, bundled, dir, &http.Response{
		StatusCode: 200,
		Body: io.NopCloser(bytes.NewReader([]byte(`{
			"p": {"models": {"new/beta": {"id": "new/beta", "limit": {"context": 256000, "output": 32000}}}}
		}`))),
	})
	got, ok := success.Lookup("new/beta")
	if !ok || got.ContextWindow != 256000 {
		t.Fatalf("refreshed lookup = ok %v %#v", ok, got)
	}
	if _, ok := success.Lookup("old/alpha"); ok {
		t.Fatal("complete overlay must replace bundled entries")
	}

	failed := modelprovider.NewCapabilityCache(bundled, dir, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})})
	if err := os.WriteFile(filepath.Join(dir, modelprovider.CapabilityCacheOverlayFile), []byte(`{"new/model":{"context_window":256000,"max_output_tokens":32000}}`), 0o600); err != nil {
		t.Fatalf("seed overlay: %v", err)
	}
	if err := failed.LoadOverlay(); err != nil {
		t.Fatalf("load overlay: %v", err)
	}
	if err := failed.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh failure")
	}
	got, ok = failed.Lookup("new/model")
	if !ok || got.ContextWindow != 256000 {
		t.Fatalf("failed refresh must keep overlay, got ok %v %#v", ok, got)
	}
}

func TestResolveLimitsPrefersCatalogOverrideThenCache(t *testing.T) {
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"gpt-reasoning": {ContextWindow: 200000, MaxOutputTokens: 32000},
	}, "", nil)
	catalog := modelprovider.Catalog{
		Manual: []string{"gpt-reasoning"},
		Limits: map[string]modelprovider.CatalogLimits{
			"gpt-reasoning": {ContextWindow: 1048576},
		},
	}
	resolved := modelprovider.ResolveLimits("gpt-reasoning", catalog, cache)
	if resolved.ContextWindow != 1048576 || resolved.ContextWindowSource != modelprovider.LimitSourceCatalog {
		t.Fatalf("window = %d %s", resolved.ContextWindow, resolved.ContextWindowSource)
	}
	if resolved.MaxOutputTokens != 32000 || resolved.MaxOutputTokensSource != modelprovider.LimitSourceCapabilityCache {
		t.Fatalf("output = %d %s", resolved.MaxOutputTokens, resolved.MaxOutputTokensSource)
	}

	unknown := modelprovider.ResolveLimits("missing", modelprovider.Catalog{}, cache)
	if unknown.ContextWindow != 0 || unknown.ContextWindowSource != modelprovider.LimitSourceRuntimeDefault {
		t.Fatalf("miss = %#v", unknown)
	}
}

func roundTripCache(t *testing.T, bundled map[string]modelprovider.CatalogLimits, dir string, resp *http.Response) *modelprovider.CapabilityCache {
	t.Helper()
	cache := modelprovider.NewCapabilityCache(bundled, dir, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return resp, nil
	})})
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return cache
}
