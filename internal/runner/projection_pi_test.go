package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/credential"
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
		GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
		Owner:                       owner.NewTaskContract("task-pi", "project-1", layout.Workdir),
		Credentials:                 creds,
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
		GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
		Owner:                       owner.NewTaskContract("task-pi", "project-1", layout.Workdir),
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
	want := map[string]bool{
		"npm:@tintinweb/pi-subagents": true,
		"npm:pi-web-access":           true,
		"npm:pi-mcp-adapter":          true,
		"npm:pi-subagents":            true,
	}
	got := map[string]bool{}
	for _, p := range settings.Packages {
		got[p] = true
	}
	for ref := range want {
		if !got[ref] {
			t.Fatalf("expected packages to contain %q, got %#v", ref, settings.Packages)
		}
	}
	if preview, ok := projection.Config["packages"].([]string); !ok || len(preview) != 4 {
		t.Fatalf("expected packages preview with 4 entries, got %#v", projection.Config["packages"])
	}
}

// TestProjectPiSettingsIgnoresHostSettings locks sandbox projection isolation:
// host ~/.pi/agent/settings.json is never read. No host key (theme,
// defaultModel, defaultProvider, defaultThinkingLevel, packages) leaks into
// the task-local settings.json. packages come only from enabled Runtime
// Extensions; defaultThinkingLevel, defaultProvider, and defaultModel are
// pinned to launch-resolved values so pi-subagents child sessions — which read
// these settings.json defaults instead of inheriting the main session's
// runtime state — stay on the launch selection.
func TestProjectPiSettingsIgnoresHostSettings(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
	hostHome := isolatePiHostHome(t)

	hostPiDir := filepath.Join(hostHome, ".pi", "agent")
	if err := os.MkdirAll(hostPiDir, 0o700); err != nil {
		t.Fatalf("mkdir host pi agent dir: %v", err)
	}
	hostSettings := `{"theme":"dark","defaultModel":"host-model","defaultProvider":"host-provider","defaultThinkingLevel":"low","packages":["npm:pi-web-access","npm:pi-subagents"]}`
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
			Env:   map[string]string{"PI_PROVIDER_ID": "hub"},
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
		GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
		Owner:                       owner.NewTaskContract("task-pi", "project-1", layout.Workdir),
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
	for _, leaked := range []string{"theme"} {
		if _, ok := settings[leaked]; ok {
			t.Fatalf("host key %q leaked into projected settings.json: %#v", leaked, settings)
		}
	}
	if got := settings["defaultProvider"]; got != "hub" {
		t.Fatalf("defaultProvider = %#v, want launch provider hub (never host-provider)", got)
	}
	if got := settings["defaultModel"]; got != "DeepSeek-V4-Pro" {
		t.Fatalf("defaultModel = %#v, want launch model (never host-model)", got)
	}
	if got := settings["defaultThinkingLevel"]; got != "high" {
		t.Fatalf("defaultThinkingLevel = %#v, want resolved high (never host low)", got)
	}
	packages, _ := settings["packages"].([]any)
	if len(packages) != 2 || packages[0] != "npm:@tintinweb/pi-subagents" || packages[1] != "npm:pi-web-access" {
		t.Fatalf("packages = %#v, want only the built-in defaults (host packages must not leak)", packages)
	}
	if preview, ok := projection.Config["packages"].([]string); !ok || len(preview) != 2 {
		t.Fatalf("expected packages preview with 2 entries, got %#v", projection.Config["packages"])
	}
}

// TestProjectPiConfigProjectsAllLaunchReadyGlobalProviders proves ADR 0015:
// every launch-ready global Model Provider is projected into models.json and
// auth.json for a Pi runtime. Drafts are skipped without blocking the launch.
