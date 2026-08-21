package runner_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func projectForTest(t *testing.T, provider runtimeprofile.Provider) (runner.Layout, func()) {
	t.Helper()
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-overlay", provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	return layout, func() {}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

// The warp use case: an operator enables a non-catalog Claude Code plugin plus
// extra env through the Custom Config File; both must coexist with the
// structured generated settings in the final projected file.
func TestClaudeSettingsDeepMergesCustomConfigFileOverlay(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderClaudeCode)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Env: map[string]string{"MY_TOOL_TAG": "abc"},
			CustomConfigFile: `{
  "env": {"EXTRA_TOOL_FLAG": "1"},
  "enabledPlugins": {"warp@claude-code-warp": true, "code-review@claude-plugins-official": false},
  "permissions": {"deny": ["Bash(rm -rf *)"]}
}`,
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	settings := readJSONFile(t, projection.ConfigPath)

	env, _ := settings["env"].(map[string]any)
	if env["MY_TOOL_TAG"] != "abc" {
		t.Fatalf("structured env lost in merge: %#v", settings["env"])
	}
	if env["EXTRA_TOOL_FLAG"] != "1" {
		t.Fatalf("overlay env missing: %#v", settings["env"])
	}

	plugins, _ := settings["enabledPlugins"].(map[string]any)
	if plugins["warp@claude-code-warp"] != true {
		t.Fatalf("overlay plugin missing: %#v", settings["enabledPlugins"])
	}

	permissions, _ := settings["permissions"].(map[string]any)
	deny, _ := permissions["deny"].([]any)
	if len(deny) == 0 || deny[0] != "Bash(rm -rf *)" {
		t.Fatalf("overlay permissions.deny missing: %#v", settings["permissions"])
	}
	// permissions.allow is harness-generated (managed); it must survive
	// alongside the non-managed deny key the overlay contributed.
	if _, hasAllow := permissions["allow"]; !hasAllow {
		t.Fatalf("structured permissions.allow must survive the merge: %#v", settings["permissions"])
	}

	// Preview reflects the merged result.
	previewPlugins, ok := projection.Config["enabled_plugins"].([]string)
	if !ok || len(previewPlugins) == 0 {
		t.Fatalf("preview enabled_plugins = %#v", projection.Config["enabled_plugins"])
	}
	joined := strings.Join(previewPlugins, ",")
	if !strings.Contains(joined, "warp@claude-code-warp") {
		t.Fatalf("preview missing warp plugin: %#v", previewPlugins)
	}
}

// Structured fields win: the overlay cannot override a structured-model env
// key. Import would refuse it; projection resolves drift in favor of structure.
func TestClaudeSettingsStructuredFieldsWinOverOverlay(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderClaudeCode)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Env: map[string]string{"MY_TOOL_TAG": "structured"},
			CustomConfigFile: `{
  "env": {"MY_TOOL_TAG": "overlay-loses", "OTHER": "kept"}
}`,
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	settings := readJSONFile(t, projection.ConfigPath)
	env, _ := settings["env"].(map[string]any)
	if env["MY_TOOL_TAG"] != "structured" {
		t.Fatalf("structured env must win, got %#v", env)
	}
	if env["OTHER"] != "kept" {
		t.Fatalf("non-conflicting overlay key must survive, got %#v", env)
	}
}

func TestCodexConfigTOMLOverlayMergesExtraSections(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderCodex)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			CustomConfigFile: "# my extras\n[features]\nweb_search = true\n\n[sandbox_workspace_write]\nnetwork_access = true\n",
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	raw, err := os.ReadFile(projection.ConfigPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	config := string(raw)
	if !strings.Contains(config, "web_search = true") {
		t.Fatalf("overlay [features] missing:\n%s", config)
	}
	if !strings.Contains(config, "network_access = true") {
		t.Fatalf("overlay [sandbox_workspace_write] missing:\n%s", config)
	}
	if !strings.Contains(config, `approval_policy = "never"`) {
		t.Fatalf("generated non-interactive default lost:\n%s", config)
	}
}

func TestHermesConfigYAMLOverlayMergesExtraKeys(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderHermes)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderHermes,
		Fields: runtimeprofile.Fields{
			CustomConfigFile: "# my extras\nskills:\n  autoload: false\nterminal:\n  shell: /bin/zsh\n",
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	raw, err := os.ReadFile(projection.ConfigPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	config := string(raw)
	if !strings.Contains(config, "autoload: false") {
		t.Fatalf("overlay skills key missing:\n%s", config)
	}
	// terminal.backend is managed (structured non-interactive default);
	// terminal.shell is a different child key and must survive.
	if !strings.Contains(config, "backend: local") {
		t.Fatalf("managed terminal.backend lost:\n%s", config)
	}
	if !strings.Contains(config, "shell: /bin/zsh") {
		t.Fatalf("non-managed terminal.shell missing:\n%s", config)
	}
}

func TestPiModelsJSONOverlayMergesCustomModels(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderPi)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			CustomConfigFile: `{"extra":{"myCustomKey":true}}`,
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	doc := readJSONFile(t, projection.ConfigPath)
	extra, _ := doc["extra"].(map[string]any)
	if extra["myCustomKey"] != true {
		t.Fatalf("overlay key missing from models.json: %#v", doc)
	}
}

func TestProjectionRejectsOverlayWithSecretShapedValues(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderClaudeCode)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			CustomConfigFile: `{"env":{"MY_API_KEY":"sk-live-123"}}`,
		},
	}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{}); err == nil {
		t.Fatal("expected projection to reject secret-shaped overlay value")
	}
}

func TestProjectionRejectsMalformedOverlay(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderHermes)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderHermes,
		Fields: runtimeprofile.Fields{
			CustomConfigFile: "not: [valid: yaml",
		},
	}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{}); err == nil {
		t.Fatal("expected projection to reject malformed overlay")
	}
}

func TestProjectedConfigTextRendersProviderNativeSeeds(t *testing.T) {
	// Story 2: the config editor seeds with a complete, realistic
	// provider-native file, redacted, never a preview envelope.
	cases := []struct {
		provider runtimeprofile.Provider
		contains []string
	}{
		{runtimeprofile.ProviderClaudeCode, []string{`"env"`, `STRUCTURED`}},
		{runtimeprofile.ProviderCodex, []string{"approval_policy", "sandbox_mode"}},
		{runtimeprofile.ProviderHermes, []string{"approvals:", "agent:"}},
		{runtimeprofile.ProviderPi, []string{`"providers"`}},
	}
	for _, tc := range cases {
		profile := runtimeprofile.Profile{Provider: tc.provider}
		profile.Fields.Env = map[string]string{"STRUCTURED": "wins"}
		text, err := runner.ProjectedConfigText(tc.provider, profile)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		for _, want := range tc.contains {
			if !strings.Contains(text, want) {
				t.Fatalf("%s seed must contain %q, got:\n%s", tc.provider, want, text)
			}
		}
	}

	// Secrets never enter the editor text (story 3).
	secretProfile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
	secretProfile.Fields.Env = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api03-live-1234567890abcdef"}
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderClaudeCode, secretProfile)
	if err != nil {
		t.Fatalf("claude secret seed: %v", err)
	}
	if strings.Contains(text, "sk-ant-api03") {
		t.Fatalf("seed must redact secret values, got:\n%s", text)
	}
}

func TestMergedProjectedConfigMergesOverlayIntoNativeShape(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
	profile.Fields.Env = map[string]string{"STRUCTURED": "wins"}
	profile.Fields.CustomConfigFile = `{"env":{"OVERLAY":"adds"},"enabledPlugins":{"warp@claude-code-warp":true}}`
	merged, err := runner.MergedProjectedConfig(profile.Provider, profile)
	if err != nil {
		t.Fatalf("merged projected text: %v", err)
	}
	env, ok := merged["env"].(map[string]any)
	if !ok || env["STRUCTURED"] != "wins" || env["OVERLAY"] != "adds" {
		t.Fatalf("structured must win, overlay must add: %#v", merged["env"])
	}
	if plugins, ok := merged["enabledPlugins"].(map[string]any); !ok || plugins["warp@claude-code-warp"] != true {
		t.Fatalf("overlay plugin must be present: %#v", merged["enabledPlugins"])
	}
}
