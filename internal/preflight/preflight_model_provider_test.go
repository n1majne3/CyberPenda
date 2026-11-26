package preflight_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeprofile"
)

func TestRunAcceptsCapturedModelProviderSnapshotWithGlobalCredentialBinding(t *testing.T) {
	svc := newTestServices(t)
	const apiKeyEnv = "PREFLIGHT_CAPTURED_MODEL_API_KEY"
	previous, existed := os.LookupEnv(apiKeyEnv)
	if err := os.Unsetenv(apiKeyEnv); err != nil {
		t.Fatalf("unset captured model API key env: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(apiKeyEnv, previous)
			return
		}
		_ = os.Unsetenv(apiKeyEnv)
	})
	if _, err := svc.creds.Upsert(apiKeyEnv, credential.ScopeGlobal, "", credential.Source{
		Kind: credential.SourceLiteral, Value: "sk-captured",
	}, false); err != nil {
		t.Fatalf("bind captured model API key: %v", err)
	}
	profile := runtimeprofile.Profile{
		ID: "captured-codex", Name: "Captured Codex", Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{BinaryPath: "/bin/true"},
	}
	snapshot := modelprovider.Snapshot{
		ModelProviderID: "captured-provider", ModelProviderName: "Captured Provider",
		EndpointBaseURL: "https://api.example.test/v1",
		Protocol:        modelprovider.ProtocolOpenAIResponses,
		Model:           "captured-model", APIKeyEnv: apiKeyEnv,
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		Profile: &profile, Runner: "host", HostActivated: true,
		ModelProviderSnapshot: &snapshot,
	})

	if !result.Pass {
		t.Fatalf("expected captured Model Provider Snapshot to use the global Credential Binding, got %#v", result.Checks)
	}
	if !checkPassed(result, "model_provider") {
		t.Fatalf("expected model_provider check to pass, got %#v", result.Checks)
	}
}

func TestRunUsesLaunchModelOverrideWithoutMutatingProfile(t *testing.T) {
	svc := newTestServices(t)
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
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
	profile, err := svc.profiles.Create("codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: provider.ID,
		ModelOverride:   "mimo-v2-flash",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID:    profile.ID,
		LaunchModelOverride: "mimo-v2-pro",
		ProjectID:           "p1",
	})
	if !result.Pass {
		t.Fatalf("expected preflight to pass, got %#v", result.Checks)
	}
	if result.ModelProvider == nil || result.ModelProvider.Model != "mimo-v2-pro" {
		t.Fatalf("expected launch override model preview, got %#v", result.ModelProvider)
	}

	stored, err := svc.profiles.Get(profile.ID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if stored.Fields.ModelOverride != "mimo-v2-flash" {
		t.Fatalf("profile model_override mutated to %q", stored.Fields.ModelOverride)
	}
}

func TestRunPassesWhenModelProviderConfigured(t *testing.T) {
	svc := newTestServices(t)
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name:      "MiMo",
		BaseURL:   "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"mimo"}, DefaultModel: "mimo"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	profile, err := svc.profiles.Create("codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
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
		t.Fatalf("expected preflight to pass with model provider, got %#v", result.Checks)
	}
	if !checkPassed(result, "model_provider") {
		t.Fatalf("expected model_provider check to pass, got %#v", result.Checks)
	}
	if result.ModelProvider == nil || result.ModelProvider.ModelProviderID != provider.ID {
		t.Fatalf("expected model provider preview, got %#v", result.ModelProvider)
	}
}

func TestRunPassesCatalogRuntimeExtensionForPi(t *testing.T) {
	svc := newTestServices(t)
	enabled := true
	profile, err := svc.profiles.Create("Pi Catalog", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		Model:    "claude-sonnet-4",
		Endpoint: "https://api.example.test/anthropic",
		APIKeys:  map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{
			{
				ID:      "npm:pi-mcp-adapter",
				Enabled: &enabled,
				Config: map[string]string{
					"install_ref": "npm:pi-mcp-adapter",
					"registry":    "pi.dev/packages",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if !result.Pass {
		t.Fatalf("expected preflight to pass for catalog extension, got %#v", result.Checks)
	}
	if !checkPassed(result, "runtime_extensions") {
		t.Fatalf("expected runtime_extensions check to pass, got %#v", result.Checks)
	}
	if len(result.RuntimeExtensions) != 1 || result.RuntimeExtensions[0].ID != "npm:pi-mcp-adapter" {
		t.Fatalf("expected runtime extension preview, got %#v", result.RuntimeExtensions)
	}
	if result.RuntimeExtensions[0].Source != "catalog" {
		t.Fatalf("expected catalog source, got %q", result.RuntimeExtensions[0].Source)
	}
}

func TestRunFailsUnresolvedManualRuntimeExtension(t *testing.T) {
	svc := newTestServices(t)
	enabled := true
	profile, err := svc.profiles.Create("Pi Manual", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		Model:    "claude-sonnet-4",
		Endpoint: "https://api.example.test/anthropic",
		APIKeys:  map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{
			{ID: "missing_extension", Enabled: &enabled},
		},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if result.Pass {
		t.Fatal("expected preflight to fail for unresolved manual extension")
	}
	if !checkFailed(result, "runtime_extensions") {
		t.Fatalf("expected runtime_extensions check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsIncompatibleRegistryRuntimeExtension(t *testing.T) {
	svc := newTestServices(t)
	source := t.TempDir()
	registry := newPiExtensionRegistry(t, source)
	svc.preflight = svc.preflight.WithRuntimeExtensions(registry)

	enabled := true
	profile, err := svc.profiles.Create("Codex Incompatible", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{
			{ID: "pi_browser_tools", Enabled: &enabled},
		},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if result.Pass {
		t.Fatal("expected preflight to fail for incompatible extension")
	}
	if !checkFailed(result, "runtime_extensions") {
		t.Fatalf("expected runtime_extensions check to fail, got %#v", result.Checks)
	}
}

func TestRunPassesRegistryRuntimeExtension(t *testing.T) {
	svc := newTestServices(t)
	source := t.TempDir()
	registry := newPiExtensionRegistry(t, source)
	svc.preflight = svc.preflight.WithRuntimeExtensions(registry)

	enabled := true
	profile, err := svc.profiles.Create("Pi Registry", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		Model:    "claude-sonnet-4",
		Endpoint: "https://api.example.test/anthropic",
		APIKeys:  map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{
			{ID: "pi_browser_tools", Enabled: &enabled},
		},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if !result.Pass {
		t.Fatalf("expected preflight to pass for registry extension, got %#v", result.Checks)
	}
	if len(result.RuntimeExtensions) != 1 || result.RuntimeExtensions[0].Source != "registry" {
		t.Fatalf("expected registry extension preview, got %#v", result.RuntimeExtensions)
	}
}

func TestRunEnforcesRuntimeExtensionProjectAndScopeRequirements(t *testing.T) {
	svc := newTestServices(t)
	source := t.TempDir()
	enabled := true
	extension := runtimeextension.Extension{
		SchemaVersion:            runtimeextension.SchemaVersion,
		ID:                       "ctf_platform",
		Name:                     "CTF Platform",
		CompatibleRuntimePlugins: []string{"pi"},
		Source:                   runtimeextension.Source{Type: "local_dir", Path: source},
		Projection:               runtimeextension.Projection{Location: "provider_home", Path: "extensions/ctf-platform"},
		Requirements: runtimeextension.Requirements{
			ProjectKinds:      []string{"ctf_challenge"},
			ScopeCapabilities: []string{"challenge_platform"},
		},
	}
	registry, err := runtimeextension.NewRegistry([]runtimeextension.Extension{extension})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	svc.preflight = svc.preflight.WithRuntimeExtensions(registry)
	profile, err := svc.profiles.Create("CTF", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		Model: "claude-sonnet-4", Endpoint: "https://api.example.test/anthropic",
		APIKeys:           map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{{ID: extension.ID, Enabled: &enabled}},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	wrongKind := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID:  profile.ID,
		ProjectID:         "p1",
		ProjectKind:       "pentest",
		ScopeCapabilities: []string{"challenge_platform"},
	})
	if wrongKind.Pass || !checkFailed(wrongKind, "runtime_extension_requirements") {
		t.Fatalf("expected Project Kind requirement failure, got %#v", wrongKind.Checks)
	}

	missingCapability := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
		ProjectKind:      "ctf_challenge",
	})
	if missingCapability.Pass || !checkFailed(missingCapability, "runtime_extension_requirements") {
		t.Fatalf("expected Scope capability requirement failure, got %#v", missingCapability.Checks)
	}

	ready := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID:  profile.ID,
		ProjectID:         "p1",
		ProjectKind:       "ctf_challenge",
		ScopeCapabilities: []string{"challenge_platform"},
	})
	if !ready.Pass {
		t.Fatalf("expected requirements to pass, got %#v", ready.Checks)
	}
}

func TestRunSkipsModelProviderCheckForLegacyConfig(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex-inline",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{
			Model: "gpt-5",
			APIKeys: map[string]string{
				"OPENAI_API_KEY": "sk-inline",
			},
		},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected legacy inline config to pass without model provider check, got %#v", result.Checks)
	}
	for _, check := range result.Checks {
		if check.Name == "model_provider" {
			t.Fatalf("expected no model_provider check for legacy config, got %#v", check)
		}
	}
}

func TestRunPreviewsSelectedEndpointBaseURLWithoutSecrets(t *testing.T) {
	svc := newTestServices(t)
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Split Path",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/api/coding/paas/v4"},
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/api/anthropic"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{"glm"}, DefaultModel: "glm"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-preview-secret-value")
	profile, err := svc.profiles.Create("claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
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
		t.Fatalf("expected preflight to pass, got %#v", result.Checks)
	}
	if result.ModelProvider == nil {
		t.Fatalf("expected model provider preview")
	}
	preview := result.ModelProvider
	if preview.EndpointBaseURL != "https://api.example.test/api/anthropic" {
		t.Fatalf("endpoint_base_url = %q", preview.EndpointBaseURL)
	}
	if preview.BaseURL != preview.EndpointBaseURL {
		t.Fatalf("base_url alias = %q, want endpoint_base_url %q", preview.BaseURL, preview.EndpointBaseURL)
	}
	if preview.Protocol != string(modelprovider.ProtocolAnthropicMessages) {
		t.Fatalf("protocol = %q", preview.Protocol)
	}
	if preview.Model != "glm" {
		t.Fatalf("model = %q", preview.Model)
	}
	if preview.APIKeyEnv != provider.APIKeyEnv || preview.APIKeySource == "" {
		t.Fatalf("api key source provenance missing: env=%q source=%q", preview.APIKeyEnv, preview.APIKeySource)
	}
	if preview.ProjectionTarget != "claude_settings" {
		t.Fatalf("projection_target = %q", preview.ProjectionTarget)
	}
	// The preview is non-secret: the materialized API key value must never
	// appear anywhere in the encoded result.
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if strings.Contains(string(encoded), "sk-preview-secret-value") {
		t.Fatalf("preflight preview leaked API key value: %s", encoded)
	}
}

func TestRunReportsCatalogAndCacheLimitSourcesWithoutFailing(t *testing.T) {
	svc := newTestServices(t)
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"glm": {ContextWindow: 200000, MaxOutputTokens: 32000},
	}, "", nil)
	svc.preflight.WithCapabilityCache(cache)
	t.Setenv("GLM_API_KEY", "sk-test")
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name:    "GLM",
		BaseURL: "https://api.example.test/v1",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/api/anthropic"},
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"glm"},
			DefaultModel: "glm",
			Limits:       map[string]modelprovider.CatalogLimits{"glm": {ContextWindow: 1048576}},
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	profile, err := svc.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: provider.ID,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	result := svc.preflight.Run(context.Background(), preflight.Request{RuntimeProfileID: profile.ID, ProjectID: "p1"})
	if !result.Pass {
		t.Fatalf("expected pass, got %#v", result.Checks)
	}
	if result.ModelProvider.ContextWindow != 1048576 || result.ModelProvider.ContextWindowSource != modelprovider.LimitSourceCatalog {
		t.Fatalf("window preview = %#v", result.ModelProvider)
	}
	if result.ModelProvider.MaxOutputTokens != 32000 || result.ModelProvider.MaxOutputTokensSource != modelprovider.LimitSourceCapabilityCache {
		t.Fatalf("output preview = %#v", result.ModelProvider)
	}
	// The preview is non-secret: the materialized API key value must never
	// appear anywhere in the encoded result.
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if strings.Contains(string(encoded), "sk-preview-secret-value") {
		t.Fatalf("preflight preview leaked API key value: %s", encoded)
	}
}

func TestRunFailsWhenProtocolPinHasNoCompatibleEndpoint(t *testing.T) {
	svc := newTestServices(t)
	provider, err := svc.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Anthropic Only",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/api/anthropic"},
		},
		Catalog: modelprovider.Catalog{Manual: []string{"glm"}, DefaultModel: "glm"},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	profile, err := svc.profiles.Create("claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID:       provider.ID,
		ModelProviderProtocol: string(modelprovider.ProtocolOpenAIResponses),
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if result.Pass {
		t.Fatal("expected preflight to fail for an incompatible protocol pin")
	}
	if !checkFailed(result, "model_provider") {
		t.Fatalf("expected model_provider check to fail, got %#v", result.Checks)
	}
}
