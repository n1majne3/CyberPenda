package runner_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectPiConfigWritesModelsAndAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")

	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	creds := credential.NewService(db)
	if _, err := creds.Upsert("anthropic-key", credential.ScopeGlobal, "", credential.Source{
		Kind:  credential.SourceEnv,
		Value: "ANTHROPIC_API_KEY",
	}, false); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Model:          "claude-sonnet-4",
			Endpoint:       "https://proxy.example.test/anthropic",
			CredentialRefs: []string{"anthropic-key"},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:   "project-1",
		Credentials: creds,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	agentDir := filepath.Join(layout.ProviderHome, "agent")
	modelsPath := filepath.Join(agentDir, "models.json")
	authPath := filepath.Join(agentDir, "auth.json")
	if projection.ConfigPath != modelsPath {
		t.Fatalf("expected models.json path, got %q", projection.ConfigPath)
	}

	modelsRaw, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var models struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(modelsRaw, &models); err != nil {
		t.Fatalf("decode models.json: %v", err)
	}
	custom, ok := models.Providers["custom"]
	if !ok {
		t.Fatalf("expected custom provider, got %#v", models.Providers)
	}
	if custom.BaseURL != "https://proxy.example.test/anthropic" {
		t.Fatalf("unexpected baseUrl: %q", custom.BaseURL)
	}
	if custom.API != "anthropic-messages" {
		t.Fatalf("unexpected api: %q", custom.API)
	}
	if custom.APIKey != "$ANTHROPIC_API_KEY" {
		t.Fatalf("unexpected apiKey ref: %q", custom.APIKey)
	}
	if len(custom.Models) != 1 || custom.Models[0].ID != "claude-sonnet-4" {
		t.Fatalf("unexpected models: %#v", custom.Models)
	}

	authRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil {
		t.Fatalf("decode auth.json: %v", err)
	}
	entry, ok := auth["custom"]
	if !ok {
		t.Fatalf("expected custom provider auth entry, got %#v", auth)
	}
	if entry.Type != "api_key" || entry.Key != "sk-ant-test-key" {
		t.Fatalf("unexpected custom provider auth: %#v", entry)
	}

	authPreview, ok := projection.Config["auth_json"].(map[string]any)
	if !ok {
		t.Fatalf("expected auth_json preview, got %#v", projection.Config["auth_json"])
	}
	customPreview, ok := authPreview["custom"].(map[string]any)
	if !ok {
		t.Fatalf("expected custom provider preview entry, got %#v", authPreview["custom"])
	}
	if customPreview["key"] != "[REDACTED]" {
		t.Fatalf("expected redacted key preview, got %#v", customPreview["key"])
	}
}

// TestProjectPiConfigWritesCatalogExtensionPackages proves that catalog-sourced
// runtime extensions (npm: install refs selected from the catalog) are written
// into the pi agent settings.json packages field, so pi installs them on launch.
func TestProjectPiConfigWritesCatalogExtensionPackages(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")

	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-ext", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	enabled := true
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Model: "DeepSeek-V4-Pro",
			RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{
				{
					ID:      "npm:pi-mcp-adapter",
					Enabled: &enabled,
					Config: map[string]string{
						"install_ref": "npm:pi-mcp-adapter",
						"registry":    "pi.dev/packages",
						"source_url":  "https://pi.dev/packages/pi-mcp-adapter",
					},
				},
				{
					ID:      "npm:pi-subagents",
					Enabled: &enabled,
					Config: map[string]string{
						"install_ref": "npm:pi-subagents",
						"registry":    "pi.dev/packages",
					},
				},
			},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID: "project-1",
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	settingsPath := filepath.Join(layout.ProviderHome, "agent", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode settings.json: %v", err)
	}
	want := map[string]bool{"npm:pi-mcp-adapter": true, "npm:pi-subagents": true}
	got := map[string]bool{}
	for _, p := range settings.Packages {
		got[p] = true
	}
	for ref := range want {
		if !got[ref] {
			t.Fatalf("expected packages to contain %q, got %#v", ref, settings.Packages)
		}
	}
	if preview, ok := projection.Config["packages"].([]string); !ok || len(preview) != 2 {
		t.Fatalf("expected packages preview with 2 entries, got %#v", projection.Config["packages"])
	}
}

// TestProjectPiConfigProjectsAllLaunchReadyGlobalProviders proves ADR 0015:
// every launch-ready global Model Provider is projected into models.json and
// auth.json for a Pi runtime. Drafts are skipped without blocking the launch.
func TestProjectPiConfigProjectsAllLaunchReadyGlobalProviders(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)

	primary, err := providers.Create(modelprovider.CreateRequest{
		Name:    "Primary OpenAI",
		BaseURL: "https://primary.example.test/v1",
		Protocols: []modelprovider.Protocol{
			modelprovider.ProtocolOpenAIChatCompletions,
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"gpt-primary", "gpt-strong"},
			Refreshed:    []string{"gpt-refreshed"},
			DefaultModel: "gpt-primary",
		},
	})
	if err != nil {
		t.Fatalf("create primary: %v", err)
	}
	alternate, err := providers.Create(modelprovider.CreateRequest{
		Name:    "Alternate Anthropic",
		BaseURL: "https://alternate.example.test/anthropic",
		Protocols: []modelprovider.Protocol{
			modelprovider.ProtocolAnthropicMessages,
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"claude-alt"},
			DefaultModel: "claude-alt",
		},
	})
	if err != nil {
		t.Fatalf("create alternate: %v", err)
	}
	// Draft: no models in catalog — must be skipped.
	if _, err := providers.Create(modelprovider.CreateRequest{
		Name:      "Draft Empty Catalog",
		BaseURL:   "https://draft.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{},
	}); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	// No API key configured — must be skipped.
	if _, err := providers.Create(modelprovider.CreateRequest{
		Name:      "No Key Provider",
		BaseURL:   "https://nokey.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1"}, DefaultModel: "m1"},
	}); err != nil {
		t.Fatalf("create no-key: %v", err)
	}

	t.Setenv(primary.APIKeyEnv, "sk-primary-secret")
	t.Setenv(alternate.APIKeyEnv, "sk-alternate-secret")

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-multi", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			ModelProviderID: primary.ID,
		},
	}

	// Seed a host models.json that must NOT overwrite CyberPenda projection.
	hostPi := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", hostPi)
	if err := os.MkdirAll(filepath.Join(hostPi, ".pi", "agent"), 0o700); err != nil {
		t.Fatalf("host pi dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostPi, ".pi", "agent", "models.json"), []byte(`{"providers":{"host-only":{}}}`), 0o600); err != nil {
		t.Fatalf("write host models: %v", err)
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ModelProviders: providers,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	modelsRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	if strings.Contains(string(modelsRaw), "host-only") {
		t.Fatalf("host models.json overwrote multi-provider projection: %s", modelsRaw)
	}
	var models struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(modelsRaw, &models); err != nil {
		t.Fatalf("decode models.json: %v", err)
	}
	if _, ok := models.Providers[primary.ID]; !ok {
		t.Fatalf("missing primary provider %q in %#v", primary.ID, models.Providers)
	}
	if _, ok := models.Providers[alternate.ID]; !ok {
		t.Fatalf("missing alternate provider %q in %#v", alternate.ID, models.Providers)
	}
	if len(models.Providers) != 2 {
		t.Fatalf("expected only launch-ready providers, got %#v", models.Providers)
	}
	primaryDoc := models.Providers[primary.ID]
	if primaryDoc.BaseURL != "https://primary.example.test/v1" {
		t.Fatalf("primary baseUrl = %q", primaryDoc.BaseURL)
	}
	if primaryDoc.API != "openai-completions" {
		t.Fatalf("primary api = %q", primaryDoc.API)
	}
	if primaryDoc.APIKey != "$"+primary.APIKeyEnv {
		t.Fatalf("primary apiKey ref = %q", primaryDoc.APIKey)
	}
	gotModels := map[string]bool{}
	for _, m := range primaryDoc.Models {
		gotModels[m.ID] = true
	}
	for _, want := range []string{"gpt-primary", "gpt-strong", "gpt-refreshed"} {
		if !gotModels[want] {
			t.Fatalf("primary missing model %q: %#v", want, primaryDoc.Models)
		}
	}
	altDoc := models.Providers[alternate.ID]
	if altDoc.API != "anthropic-messages" {
		t.Fatalf("alternate api = %q", altDoc.API)
	}
	if len(altDoc.Models) != 1 || altDoc.Models[0].ID != "claude-alt" {
		t.Fatalf("alternate models = %#v", altDoc.Models)
	}

	authRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "auth.json"))
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	if strings.Contains(string(string(authRaw)), "sk-primary-secret") {
		// auth.json intentionally holds keys for Pi — that is correct.
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil {
		t.Fatalf("decode auth.json: %v", err)
	}
	if auth[primary.ID].Key != "sk-primary-secret" || auth[alternate.ID].Key != "sk-alternate-secret" {
		t.Fatalf("auth keys incomplete: %#v", auth)
	}
	if len(auth) != 2 {
		t.Fatalf("auth should only cover projected providers: %#v", auth)
	}

	// Non-secret preview/snapshot must redact credential values.
	authPreview, ok := projection.Config["auth_json"].(map[string]any)
	if !ok {
		t.Fatalf("expected auth_json preview, got %#v", projection.Config["auth_json"])
	}
	for id, entry := range authPreview {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("auth preview entry %q: %#v", id, entry)
		}
		if m["key"] != "[REDACTED]" {
			t.Fatalf("auth preview leaked key for %q: %#v", id, m["key"])
		}
	}
	if strings.Contains(fmt.Sprint(projection.Config), "sk-primary-secret") ||
		strings.Contains(fmt.Sprint(projection.Config), "sk-alternate-secret") {
		t.Fatalf("config preview leaked secrets: %#v", projection.Config)
	}

	// Initial selection remains the starting provider (PI_PROVIDER_ID).
	if projection.ResolvedProfile.Fields.Env["PI_PROVIDER_ID"] != primary.ID {
		t.Fatalf("PI_PROVIDER_ID = %q, want initial %q", projection.ResolvedProfile.Fields.Env["PI_PROVIDER_ID"], primary.ID)
	}
	if projection.ModelSnapshot == nil || projection.ModelSnapshot.ModelProviderID != primary.ID {
		t.Fatalf("snapshot initial provider = %#v", projection.ModelSnapshot)
	}

	projectedIDs, ok := projection.Config["projected_model_provider_ids"].([]string)
	if !ok {
		// allow []any from JSON-shaped maps
		if raw, ok := projection.Config["projected_model_provider_ids"].([]any); ok {
			for _, v := range raw {
				projectedIDs = append(projectedIDs, fmt.Sprint(v))
			}
		}
	}
	if len(projectedIDs) != 2 {
		t.Fatalf("projected_model_provider_ids = %#v", projection.Config["projected_model_provider_ids"])
	}
}

// TestProjectPiConfigHostModelsIgnoredWhenGlobalProjectionAvailable locks ADR
// 0015: with a global ModelProviders lister, host ~/.pi/agent/models.json must
// not inject non-global / non-launch-ready providers—even when zero globals are
// launch-ready.
func TestProjectPiConfigHostModelsIgnoredWhenGlobalProjectionAvailable(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	// Draft only: catalog empty → not launch-ready. Global projection still runs.
	if _, err := providers.Create(modelprovider.CreateRequest{
		Name:      "Draft Only",
		BaseURL:   "https://draft.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{},
	}); err != nil {
		t.Fatalf("create draft: %v", err)
	}

	hostPi := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", hostPi)
	if err := os.MkdirAll(filepath.Join(hostPi, ".pi", "agent"), 0o700); err != nil {
		t.Fatalf("host pi dir: %v", err)
	}
	hostModels := `{"providers":{"host-leaked":{"baseUrl":"https://host.local","models":[{"id":"host-model"}]}}}`
	if err := os.WriteFile(filepath.Join(hostPi, ".pi", "agent", "models.json"), []byte(hostModels), 0o600); err != nil {
		t.Fatalf("write host models: %v", err)
	}
	hostAuth := `{"host-leaked":{"type":"api_key","key":"sk-host-secret"}}`
	if err := os.WriteFile(filepath.Join(hostPi, ".pi", "agent", "auth.json"), []byte(hostAuth), 0o600); err != nil {
		t.Fatalf("write host auth: %v", err)
	}

	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-no-ready", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	// Profile uses legacy fields so single-provider fallback still writes a
	// CyberPenda-owned models.json without needing a ready global provider.
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Model:    "profile-model",
			Endpoint: "https://profile.example/v1",
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ModelProviders: providers,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	modelsRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	if strings.Contains(string(modelsRaw), "host-leaked") || strings.Contains(string(modelsRaw), "host-model") {
		t.Fatalf("host models leaked into global-projection runtime: %s", modelsRaw)
	}
	if !strings.Contains(string(modelsRaw), "profile-model") {
		t.Fatalf("expected profile single-provider fallback models, got %s", modelsRaw)
	}
	authPath := filepath.Join(layout.ProviderHome, "agent", "auth.json")
	if raw, err := os.ReadFile(authPath); err == nil {
		if strings.Contains(string(raw), "host-leaked") || strings.Contains(string(raw), "sk-host-secret") {
			t.Fatalf("host auth leaked into global-projection runtime: %s", raw)
		}
	}
	// Empty projected set still recorded so daemon fails closed on cross-provider.
	ids, ok := projection.Config["projected_model_provider_ids"].([]string)
	if !ok {
		if raw, ok := projection.Config["projected_model_provider_ids"].([]any); ok {
			ids = make([]string, 0, len(raw))
			for _, v := range raw {
				ids = append(ids, fmt.Sprint(v))
			}
		}
	}
	if ids == nil {
		t.Fatalf("expected projected_model_provider_ids when global projection ran, got %#v", projection.Config["projected_model_provider_ids"])
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty projected set, got %#v", ids)
	}
}

// TestMaterializeLaunchCredentialsIncludesAllPiProjectedProviders ensures every
// projected provider's API key is available in the launch env so Pi can
// authenticate cross-provider turns without restart.
func TestMaterializeLaunchCredentialsIncludesAllPiProjectedProviders(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	a, err := providers.Create(modelprovider.CreateRequest{
		Name: "A", BaseURL: "https://a.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"a1"}, DefaultModel: "a1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := providers.Create(modelprovider.CreateRequest{
		Name: "B", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"b1"}, DefaultModel: "b1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(a.APIKeyEnv, "sk-a")
	t.Setenv(b.APIKeyEnv, "sk-b")

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields:   runtimeprofile.Fields{ModelProviderID: a.ID},
	}
	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: profile, Providers: providers, CheckEnv: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := runner.MaterializeLaunchCredentials(profile, runner.ProjectionRequest{
		ModelProviders: providers,
		ModelSnapshot:  &snapshot,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if creds[a.APIKeyEnv] != "sk-a" || creds[b.APIKeyEnv] != "sk-b" {
		t.Fatalf("materialized credentials incomplete: %#v", creds)
	}
}

// TestProjectPiConfigInvalidInitialProviderStillFails ensures a bad initial
// selection fails clearly while drafts of other providers are unrelated.
func TestProjectPiConfigInvalidInitialProviderStillFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	// Provider exists but has no models and no key — not launch-ready, and is
	// the *initial* selection so resolve/preflight must fail.
	broken, err := providers.Create(modelprovider.CreateRequest{
		Name:      "Broken Initial",
		BaseURL:   "https://broken.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-broken", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields:   runtimeprofile.Fields{ModelProviderID: broken.ID},
	}, runner.ProjectionRequest{ModelProviders: providers})
	if err == nil {
		t.Fatal("expected invalid initial provider to fail projection")
	}
}
