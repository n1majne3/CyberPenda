package modelprovider_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

func TestResolveModelProviderUsesRuntimeProtocolPreference(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:      "Pi Shared",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses, modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"mimo"}, DefaultModel: "mimo"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields:   runtimeprofile.Fields{ModelProviderID: provider.ID},
		},
		Providers: svc,
		Plugins:   runtimeplugin.MustBuiltinRegistry(),
		CheckEnv:  true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snapshot.Protocol != modelprovider.ProtocolOpenAIChatCompletions {
		t.Fatalf("protocol = %q", snapshot.Protocol)
	}
	if snapshot.Model != "mimo" || snapshot.APIKeyEnv != "PI_SHARED_API_KEY" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestResolveModelProviderUsesSelectedEndpointBaseURL(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name: "Split Path",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/api/coding/paas/v4"},
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/api/anthropic"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{"gpt"}, DefaultModel: "gpt"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields: runtimeprofile.Fields{
				ModelProviderID:       provider.ID,
				ModelProviderProtocol: string(modelprovider.ProtocolAnthropicMessages),
			},
		},
		Providers: svc,
		Plugins:   runtimeplugin.MustBuiltinRegistry(),
		CheckEnv:  true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snapshot.EndpointBaseURL != "https://api.example.test/api/anthropic" || snapshot.BaseURL != snapshot.EndpointBaseURL {
		t.Fatalf("snapshot endpoint base URL = %#v", snapshot)
	}
}

func TestResolveModelProviderStrictPinDoesNotFallback(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:      "Responses Only",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt"}, DefaultModel: "gpt"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	_, err = modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				ModelProviderID:       provider.ID,
				ModelProviderProtocol: string(modelprovider.ProtocolAnthropicMessages),
			},
		},
		Providers: svc,
		Plugins:   runtimeplugin.MustBuiltinRegistry(),
		CheckEnv:  true,
	})
	if err == nil {
		t.Fatal("expected strict incompatible pin to fail")
	}
}

func TestResolveModelProviderRequiresCatalogAndEnv(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:      "Empty",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	_, err = modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields:   runtimeprofile.Fields{ModelProviderID: provider.ID},
		},
		Providers: svc,
		Plugins:   runtimeplugin.MustBuiltinRegistry(),
		CheckEnv:  true,
	})
	if err == nil {
		t.Fatal("expected empty catalog/env to fail")
	}
}

func TestResolveModelProviderUsesLaunchModelOverrideOverProfileField(t *testing.T) {
	svc := modelprovider.NewService(newStore(t))
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"mimo-v2-flash", "mimo-v2-pro"},
			DefaultModel: "mimo-v2-flash",
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")

	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields: runtimeprofile.Fields{
				ModelProviderID: provider.ID,
				ModelOverride:   "mimo-v2-flash",
			},
		},
		Providers:           svc,
		Plugins:             runtimeplugin.MustBuiltinRegistry(),
		CheckEnv:            true,
		LaunchModelOverride: "mimo-v2-pro",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snapshot.Model != "mimo-v2-pro" {
		t.Fatalf("model = %q", snapshot.Model)
	}
}

func TestResolveModelProviderAcceptsGeneratedCredentialBinding(t *testing.T) {
	db := newStore(t)
	svc := modelprovider.NewService(db)
	creds := credential.NewService(db)
	provider, err := svc.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt"}, DefaultModel: "gpt"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	secretPath := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(secretPath, []byte("sk-file-secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if _, err := creds.Upsert(provider.APIKeyEnv, credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceFile, Value: secretPath}, false); err != nil {
		t.Fatalf("upsert credential binding: %v", err)
	}

	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields:   runtimeprofile.Fields{ModelProviderID: provider.ID},
		},
		Providers:   svc,
		Plugins:     runtimeplugin.MustBuiltinRegistry(),
		Credentials: creds,
		CheckEnv:    true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if snapshot.APIKeyEnv != provider.APIKeyEnv {
		t.Fatalf("snapshot APIKeyEnv = %q", snapshot.APIKeyEnv)
	}
}

// TestResolveModelProviderBackfillsLegacyProviderRow verifies that an old
// provider record carrying only provider-level base_url and protocols still
// resolves as an endpoint-backed provider during the transition, with the
// Anthropic final-segment adaptation applied to the resolved endpoint.
func TestResolveModelProviderBackfillsLegacyProviderRow(t *testing.T) {
	db := newStore(t)
	if err := seedLegacyProvider(db, "legacy", "https://hub.example.test/v1/",
		[]modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses, modelprovider.ProtocolAnthropicMessages},
		modelprovider.Catalog{Manual: []string{"gpt-5"}, DefaultModel: "gpt-5"}); err != nil {
		t.Fatalf("insert legacy provider: %v", err)
	}
	svc := modelprovider.NewService(db)
	t.Setenv("LEGACY_API_KEY", "sk-test")

	// Claude Code resolves through the Anthropic Messages endpoint, which
	// must have the final /v1 segment dropped during backfill.
	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
			Fields:   runtimeprofile.Fields{ModelProviderID: "legacy"},
		},
		Providers: svc,
		Plugins:   runtimeplugin.MustBuiltinRegistry(),
		CheckEnv:  true,
	})
	if err != nil {
		t.Fatalf("resolve claude_code: %v", err)
	}
	if snapshot.EndpointBaseURL != "https://hub.example.test" || snapshot.Protocol != modelprovider.ProtocolAnthropicMessages {
		t.Fatalf("anthropic snapshot = %#v", snapshot)
	}

	// Codex resolves through the OpenAI Responses endpoint, which must copy
	// the normalized legacy base URL unchanged.
	snapshot, err = modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderCodex,
			Fields:   runtimeprofile.Fields{ModelProviderID: "legacy"},
		},
		Providers: svc,
		Plugins:   runtimeplugin.MustBuiltinRegistry(),
		CheckEnv:  true,
	})
	if err != nil {
		t.Fatalf("resolve codex: %v", err)
	}
	if snapshot.EndpointBaseURL != "https://hub.example.test/v1" || snapshot.Protocol != modelprovider.ProtocolOpenAIResponses {
		t.Fatalf("openai snapshot = %#v", snapshot)
	}
}
