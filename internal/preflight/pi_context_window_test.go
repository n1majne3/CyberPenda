package preflight_test

import (
	"context"
	"strings"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/runtimeprofile"
)

func TestRunFailsWhenPiModelContextWindowTooSmall(t *testing.T) {
	svc := newTestServices(t)
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name:    "Local llama.cpp",
		BaseURL: "http://127.0.0.1:8080/v1",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIChatCompletions, BaseURL: "http://127.0.0.1:8080/v1"},
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"qwen2.5-1.5b"},
			DefaultModel: "qwen2.5-1.5b",
			Limits:       map[string]modelprovider.CatalogLimits{"qwen2.5-1.5b": {ContextWindow: 8192}},
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-local")
	profile, err := svc.profiles.Create("Pi Local", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: provider.ID,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatalf("expected preflight to fail for 8192 Pi context window, got %#v", result.Checks)
	}
	var found *preflight.Check
	for index := range result.Checks {
		if result.Checks[index].Name == "model_context_window" {
			found = &result.Checks[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected model_context_window check, got %#v", result.Checks)
	}
	if found.Status != preflight.CheckFail {
		t.Fatalf("expected model_context_window to fail, got %#v", found)
	}
	if !strings.Contains(found.Detail, "8192") || !strings.Contains(found.Detail, "16384") {
		t.Fatalf("expected detail to mention actual and minimum context window, got %q", found.Detail)
	}
}

func TestRunPassesWhenPiModelContextWindowSufficient(t *testing.T) {
	svc := newTestServices(t)
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name:    "Local llama.cpp 32K",
		BaseURL: "http://127.0.0.1:8080/v1",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIChatCompletions, BaseURL: "http://127.0.0.1:8080/v1"},
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"qwen2.5-1.5b"},
			DefaultModel: "qwen2.5-1.5b",
			Limits:       map[string]modelprovider.CatalogLimits{"qwen2.5-1.5b": {ContextWindow: 32768}},
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-local")
	profile, err := svc.profiles.Create("Pi Local 32K", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: provider.ID,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass for 32768 Pi context window, got %#v", result.Checks)
	}
	if !checkPassed(result, "model_context_window") {
		t.Fatalf("expected model_context_window check to pass, got %#v", result.Checks)
	}
}

func TestRunSkipsPiContextWindowCheckForNonPiRuntime(t *testing.T) {
	svc := newTestServices(t)
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name:    "Small Context Provider",
		BaseURL: "http://127.0.0.1:8080/v1",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "http://127.0.0.1:8080/v1"},
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"small-model"},
			DefaultModel: "small-model",
			Limits:       map[string]modelprovider.CatalogLimits{"small-model": {ContextWindow: 8192}},
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-local")
	profile, err := svc.profiles.Create("Codex Small", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass for non-Pi runtime, got %#v", result.Checks)
	}
}
