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
	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectPiConfigWritesModelsAndAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
	// copyHostPiModels falls back to the host home when no Global Model
	// Projection exists.
	isolatePiHostHome(t)

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
		Owner:       owner.NewTaskContract("task-pi", "project-1", layout.Workdir),
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
	isolatePiHostHome(t)

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
		Owner: owner.NewTaskContract("task-pi", "project-1", layout.Workdir),
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

// TestProjectPiConfigMergesHostSettingsPackages proves that packages configured
// in host ~/.pi/agent/settings.json are preserved and merged into the task-local
// settings.json packages list.
func TestProjectPiConfigMergesHostSettingsPackages(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
	hostHome := isolatePiHostHome(t)

	hostPiDir := filepath.Join(hostHome, ".pi", "agent")
	if err := os.MkdirAll(hostPiDir, 0o700); err != nil {
		t.Fatalf("mkdir host pi agent dir: %v", err)
	}
	hostSettings := `{"theme":"dark","packages":["npm:pi-web-access","npm:pi-subagents"]}`
	if err := os.WriteFile(filepath.Join(hostPiDir, "settings.json"), []byte(hostSettings), 0o600); err != nil {
		t.Fatalf("write host settings.json: %v", err)
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-host-settings", runtimeprofile.ProviderPi)
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
					ID:      "npm:@tintinweb/pi-subagents",
					Enabled: &enabled,
					Config: map[string]string{
						"install_ref": "npm:@tintinweb/pi-subagents",
					},
				},
			},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		Owner: owner.NewTaskContract("task-pi", "project-1", layout.Workdir),
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	settingsPath := filepath.Join(layout.ProviderHome, "agent", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read projected settings.json: %v", err)
	}
	var settings struct {
		Theme    string   `json:"theme"`
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode settings.json: %v", err)
	}
	if settings.Theme != "dark" {
		t.Fatalf("expected theme 'dark' preserved from host, got %q", settings.Theme)
	}

	want := map[string]bool{
		"npm:pi-web-access":           true,
		"npm:pi-subagents":            true,
		"npm:@tintinweb/pi-subagents": true,
	}
	got := map[string]bool{}
	for _, p := range settings.Packages {
		got[p] = true
	}
	for ref := range want {
		if !got[ref] {
			t.Fatalf("expected merged packages to contain %q, got %#v", ref, settings.Packages)
		}
	}
	if preview, ok := projection.Config["packages"].([]string); !ok || len(preview) != 3 {
		t.Fatalf("expected packages preview with 3 entries, got %#v", projection.Config["packages"])
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
		t.Fatalf("expected projected_model_provider_ids when Global Model Projection ran, got %#v", projection.Config["projected_model_provider_ids"])
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

// Story 16: preview of the Pi main config must be semantic-equal to the
// launch-projected models.json, including every launch-ready global.
func TestProjectedConfigTextMatchesPiLaunchReadyGlobals(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	primary, err := providers.Create(modelprovider.CreateRequest{
		Name:      "Primary OpenAI",
		BaseURL:   "https://primary.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-primary"}, DefaultModel: "gpt-primary"},
	})
	if err != nil {
		t.Fatalf("create primary: %v", err)
	}
	alternate, err := providers.Create(modelprovider.CreateRequest{
		Name:      "Alternate Anthropic",
		BaseURL:   "https://alternate.example.test/anthropic",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-alt"}, DefaultModel: "claude-alt"},
	})
	if err != nil {
		t.Fatalf("create alternate: %v", err)
	}
	t.Setenv(primary.APIKeyEnv, "sk-test-not-a-real-key")
	t.Setenv(alternate.APIKeyEnv, "sk-test-not-a-real-key")

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields:   runtimeprofile.Fields{ModelProviderID: primary.ID},
	}
	req := runner.ProjectionRequest{ModelProviders: providers}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-preview", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, req); err != nil {
		t.Fatalf("project config: %v", err)
	}
	modelsRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}

	preview, err := runner.ProjectedConfigTextWith(runtimeprofile.ProviderPi, profile, req)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if strings.Contains(preview, "sk-test-not-a-real-key") {
		t.Fatalf("preview must redact credentials, got %s", preview)
	}

	var launched, shown struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(modelsRaw, &launched); err != nil {
		t.Fatalf("decode launched: %v", err)
	}
	if err := json.Unmarshal([]byte(preview), &shown); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if len(shown.Providers) != len(launched.Providers) {
		t.Fatalf("preview providers = %#v, launched = %#v", shown.Providers, launched.Providers)
	}
	for id, entry := range launched.Providers {
		got, ok := shown.Providers[id]
		if !ok || got.BaseURL != entry.BaseURL {
			t.Fatalf("preview missing launched provider %q: shown=%#v launched=%#v", id, shown.Providers, launched.Providers)
		}
	}
}

// TestProjectPiModelsDeclareReasoningCapability locks the Requested Reasoning
// Effort contract: Pi clamps set_thinking_level to "off" and omits
// reasoning_effort from provider requests when a models.json entry lacks
// reasoning metadata. Every projected model entry, in both the Global Model
// Projection path and the single-provider legacy path, must declare reasoning
// support and identity-map xhigh/max so the complete CyberPenda effort
// vocabulary (low, medium, high, xhigh, max) passes through to the provider.
func TestProjectPiModelsDeclareReasoningCapability(t *testing.T) {
	assertReasoningEntries := func(t *testing.T, modelsPath, providerID string) {
		t.Helper()
		modelsRaw, err := os.ReadFile(modelsPath)
		if err != nil {
			t.Fatalf("read models.json: %v", err)
		}
		var doc struct {
			Providers map[string]struct {
				Models []struct {
					ID               string         `json:"id"`
					Reasoning        bool           `json:"reasoning"`
					ThinkingLevelMap map[string]any `json:"thinkingLevelMap"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(modelsRaw, &doc); err != nil {
			t.Fatalf("decode models.json: %v", err)
		}
		provider, ok := doc.Providers[providerID]
		if !ok {
			t.Fatalf("missing provider %q in %#v", providerID, doc.Providers)
		}
		if len(provider.Models) == 0 {
			t.Fatalf("provider %q has no models", providerID)
		}
		for _, model := range provider.Models {
			if !model.Reasoning {
				t.Fatalf("model %q must declare reasoning:true: %#v", model.ID, model)
			}
			if model.ThinkingLevelMap["xhigh"] != "xhigh" || model.ThinkingLevelMap["max"] != "max" {
				t.Fatalf("model %q must identity-map xhigh/max: %#v", model.ID, model.ThinkingLevelMap)
			}
		}
	}

	t.Run("Global Model Projection", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		providers := modelprovider.NewService(db)
		primary, err := providers.Create(modelprovider.CreateRequest{
			Name:    "Reasoning Gateway",
			BaseURL: "https://reasoning.example.test/v1",
			Protocols: []modelprovider.Protocol{
				modelprovider.ProtocolOpenAIChatCompletions,
			},
			Catalog: modelprovider.Catalog{
				Manual:       []string{"gpt-reasoning", "gpt-reasoning-large"},
				DefaultModel: "gpt-reasoning",
			},
		})
		if err != nil {
			t.Fatalf("create primary: %v", err)
		}
		t.Setenv(primary.APIKeyEnv, "sk-reasoning-test")

		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-reasoning", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				ModelProviderID: primary.ID,
			},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
			ModelProviders: providers,
		}); err != nil {
			t.Fatalf("project config: %v", err)
		}
		assertReasoningEntries(t, filepath.Join(layout.ProviderHome, "agent", "models.json"), primary.ID)
	})

	t.Run("legacy single provider", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
		isolatePiHostHome(t)
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

		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-legacy", runtimeprofile.ProviderPi)
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
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
			Owner:       owner.NewTaskContract("task-pi-legacy", "project-1", layout.Workdir),
			Credentials: creds,
		}); err != nil {
			t.Fatalf("project config: %v", err)
		}
		assertReasoningEntries(t, filepath.Join(layout.ProviderHome, "agent", "models.json"), "custom")
	})
}

func TestProjectPiModelsWriteResolvedContextWindowAndMaxTokens(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	created, err := providers.Create(modelprovider.CreateRequest{
		Name:    "Limits Gateway",
		BaseURL: "https://limits.example.test/v1",
		Protocols: []modelprovider.Protocol{
			modelprovider.ProtocolOpenAIChatCompletions,
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"cached-model", "override-model"},
			DefaultModel: "cached-model",
			Limits: map[string]modelprovider.CatalogLimits{
				"override-model": {ContextWindow: 999999, MaxOutputTokens: 1111},
			},
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(created.APIKeyEnv, "sk-limits")
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"cached-model": {ContextWindow: 200000, MaxOutputTokens: 32000},
	}, "", nil)
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-limits", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields:   runtimeprofile.Fields{ModelProviderID: created.ID},
	}, runner.ProjectionRequest{
		ModelProviders:  providers,
		CapabilityCache: cache,
	}); err != nil {
		t.Fatalf("project: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			Models []struct {
				ID            string `json:"id"`
				ContextWindow int    `json:"contextWindow"`
				MaxTokens     int    `json:"maxTokens"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]struct {
		ContextWindow int
		MaxTokens     int
	}{}
	for _, model := range doc.Providers[created.ID].Models {
		byID[model.ID] = struct {
			ContextWindow int
			MaxTokens     int
		}{model.ContextWindow, model.MaxTokens}
	}
	if byID["cached-model"].ContextWindow != 200000 || byID["cached-model"].MaxTokens != 32000 {
		t.Fatalf("cache model = %#v", byID["cached-model"])
	}
	if byID["override-model"].ContextWindow != 999999 || byID["override-model"].MaxTokens != 1111 {
		t.Fatalf("override model = %#v", byID["override-model"])
	}
}
