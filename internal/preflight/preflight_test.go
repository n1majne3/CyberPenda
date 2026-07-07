package preflight_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/store"
)

type services struct {
	preflight      *preflight.Service
	db             *store.DB
	profiles       *runtimeprofile.Service
	creds          *credential.Service
	modelProviders *modelprovider.Service
}

func newTestServices(t *testing.T) services {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	profiles := runtimeprofile.NewService(db)
	creds := credential.NewService(db)
	providers := modelprovider.NewService(db)
	plugins := runtimeplugin.MustBuiltinRegistry()
	return services{
		preflight:      preflight.NewService(profiles, creds).WithModelProviders(providers, plugins),
		db:             db,
		profiles:       profiles,
		creds:          creds,
		modelProviders: providers,
	}
}

func TestRunFailsWhenProfileMissing(t *testing.T) {
	svc := newTestServices(t)

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: "missing",
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when profile is missing")
	}
	if !checkFailed(result, "runtime_profile") {
		t.Fatalf("expected runtime_profile check to fail, got %#v", result.Checks)
	}
}

func TestRunPassesWhenProfileHasNoCredentials(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
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
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass with no refs, got %#v", result.Checks)
	}
}

func TestRunPassesWithInlineProfileAPIKeys(t *testing.T) {
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
		t.Fatalf("expected preflight to pass with inline api keys, got %#v", result.Checks)
	}
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass, got %#v", result.Checks)
	}
}

func TestRunFailsWhenCredentialReferenceUnresolved(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when credential has no binding")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunPassesWhenCredentialResolvedGlobally(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	t.Setenv("CODEX_API_KEY", "configured-secret")
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass with global binding, got %#v", result.Checks)
	}
}

func TestRunFailsWhenEnvCredentialNotSet(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	os.Unsetenv("PREFLIGHT_MISSING_API_KEY")
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "PREFLIGHT_MISSING_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when env credential is not set")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsWhenFileCredentialUnreadable(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	source := credential.Source{Kind: credential.SourceFile, Value: filepath.Join(t.TempDir(), "does-not-exist.txt"), DestinationEnv: "CODEX_API_KEY"}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when file credential is unreadable")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsWhenCommandCredentialExitsNonZero(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	source := credential.Source{Kind: credential.SourceCommand, Value: "exit 42", DestinationEnv: "CODEX_API_KEY"}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when command credential exits non-zero")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunPassesWhenAllCredentialsMaterialize(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key", "extra-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	t.Setenv("CODEX_API_KEY", "configured-secret")
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}
	tokenPath := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenPath, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fileSource := credential.Source{Kind: credential.SourceFile, Value: tokenPath, DestinationEnv: "EXTRA_TOKEN"}
	if _, err := svc.creds.Upsert("extra-key", credential.ScopeGlobal, "", fileSource, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if !result.Pass {
		t.Fatalf("expected preflight to pass when all credentials materialize, got %#v", result.Checks)
	}
	if !checkPassed(result, "credentials") {
		t.Fatalf("expected credentials check to pass, got %#v", result.Checks)
	}
}

func TestRunFailsWhenFileCredentialHasNoDestinationEnv(t *testing.T) {
	// A file source without destination_env materializes fine, but projection
	// errors because there is no env var name to project under. Preflight must
	// catch this so the task fails before launch, not during it.
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	tokenPath := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(tokenPath, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	// File source with a readable file but no DestinationEnv: Materialize would
	// succeed, but destinationEnv (and thus projection) would fail.
	source := credential.Source{Kind: credential.SourceFile, Value: tokenPath}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", source, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when file source has no destination_env")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsWhenCredentialDisabledForProject(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create(
		"codex",
		runtimeprofile.ProviderCodex,
		runtimeprofile.Fields{CredentialRefs: []string{"codex-api-key"}},
	)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{Kind: credential.SourceEnv, Value: "CODEX_API_KEY"}, false); err != nil {
		t.Fatalf("upsert global binding: %v", err)
	}
	if _, err := svc.creds.Upsert("codex-api-key", credential.ScopeProject, "p1", credential.Source{}, true); err != nil {
		t.Fatalf("upsert project disabled binding: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when project disabled the binding")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunRejectsUnsupportedRunner(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
		Runner:           "kali-magic",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail for unsupported runner")
	}
	if !checkFailed(result, "runner") {
		t.Fatalf("expected runner check to fail, got %#v", result.Checks)
	}
}

func TestRunAcceptsSandboxAndHostRunners(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	for _, runner := range []string{"sandbox", "host", ""} {
		req := preflight.Request{
			RuntimeProfileID: profile.ID,
			ProjectID:        "p1",
			Runner:           runner,
		}
		if runner == "host" {
			req.HostActivated = true
		}
		result := svc.preflight.Run(context.Background(), req)
		if !result.Pass {
			t.Fatalf("expected preflight to pass for runner %q, got %#v", runner, result.Checks)
		}
	}
}

func TestRunIncludesExtraRefsFromRequest(t *testing.T) {
	svc := newTestServices(t)
	// Profile declares no credential refs; the launch request adds one.
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID:        profile.ID,
		ProjectID:               "p1",
		CredentialRefsToResolve: []string{"extra-key"},
	})

	if result.Pass {
		t.Fatal("expected preflight to fail for unresolved extra ref")
	}
	if !checkFailed(result, "credentials") {
		t.Fatalf("expected credentials check to fail, got %#v", result.Checks)
	}
}

func TestRunFailsWhenEnabledSkillBundleIsMissing(t *testing.T) {
	svc := newTestServices(t)
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	skills := skill.NewService(svc.db, skillsRoot)
	svc.preflight = preflight.NewService(svc.profiles, svc.creds, skills).
		WithModelProviders(svc.modelProviders, runtimeplugin.MustBuiltinRegistry())
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	published, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:   "recon-helper",
			Name: "Recon Helper",
		},
		Files: map[string]string{"SKILL.md": "# Recon"},
	})
	if err != nil {
		t.Fatalf("publish skill: %v", err)
	}
	if err := os.RemoveAll(published.BundlePath); err != nil {
		t.Fatalf("remove bundle: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if result.Pass {
		t.Fatal("expected preflight to fail when enabled skill bundle is missing")
	}
	if !checkFailed(result, "skills") {
		t.Fatalf("expected skills check to fail, got %#v", result.Checks)
	}
}

func TestRunListsEnabledSkillsWithoutAddingCredentialRequirements(t *testing.T) {
	svc := newTestServices(t)
	skills := skill.NewService(svc.db, filepath.Join(t.TempDir(), "skills"))
	svc.preflight = preflight.NewService(svc.profiles, svc.creds, skills).
		WithModelProviders(svc.modelProviders, runtimeplugin.MustBuiltinRegistry())
	profile, err := svc.profiles.Create("fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:   "recon-helper",
			Name: "Recon Helper",
		},
		Files: map[string]string{"SKILL.md": "# Recon"},
	}); err != nil {
		t.Fatalf("publish skill: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})
	if !result.Pass {
		t.Fatalf("expected enabled skill without profile credentials to pass preflight, got %#v", result.Checks)
	}
	if len(result.Skills) != 1 || result.Skills[0].ID != "recon-helper" {
		t.Fatalf("expected enabled skill preview, got %#v", result.Skills)
	}
}

func TestRunFailsWhenRequiredRuntimeLacksModelProvider(t *testing.T) {
	svc := newTestServices(t)
	profile, err := svc.profiles.Create("codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	result := svc.preflight.Run(context.Background(), preflight.Request{
		RuntimeProfileID: profile.ID,
		ProjectID:        "p1",
	})

	if result.Pass {
		t.Fatal("expected preflight to fail when required runtime has no model provider")
	}
	if !checkFailed(result, "model_provider") {
		t.Fatalf("expected model_provider check to fail, got %#v", result.Checks)
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

func newPiExtensionRegistry(t *testing.T, source string) *runtimeextension.Registry {
	t.Helper()
	loaded, errs := runtimeextension.LoadDirectory(writePiExtensionManifest(t, source))
	if len(errs) > 0 {
		t.Fatalf("load extensions: %v", errs)
	}
	registry, err := runtimeextension.NewRegistry(loaded)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func writePiExtensionManifest(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	extension := runtimeextension.Extension{
		SchemaVersion: runtimeextension.SchemaVersion,
		ID:            "pi_browser_tools",
		Name:          "Pi Browser Tools",
		CompatibleRuntimePlugins: []string{
			"pi",
		},
		Source:     runtimeextension.Source{Type: "local_dir", Path: source},
		Projection: runtimeextension.Projection{Location: "provider_home", Path: "extensions/browser-tools"},
	}
	raw, err := json.Marshal(extension)
	if err != nil {
		t.Fatalf("marshal extension: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi_browser_tools.json"), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
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

func checkPassed(result preflight.Result, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status == preflight.CheckPass
		}
	}
	return false
}

func checkFailed(result preflight.Result, name string) bool {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status == preflight.CheckFail
		}
	}
	return false
}
