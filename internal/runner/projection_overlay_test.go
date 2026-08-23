package runner_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"pentest/internal/modelprovider"
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

func TestProjectedConfigTextIncludesCustomConfigFileRemainder(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
	profile.Fields.Env = map[string]string{"STRUCTURED": "wins"}
	profile.Fields.CustomConfigFile = `{"enabledPlugins":{"warp@claude-code-warp":true}}`
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderClaudeCode, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "warp@claude-code-warp") {
		t.Fatalf("editor seed must include the Custom Config File remainder, got:\n%s", text)
	}
	if !strings.Contains(text, "STRUCTURED") {
		t.Fatalf("editor seed must still include structured fields, got:\n%s", text)
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

func TestHermesPluginsEnabledMergesOperatorEntries(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "plugins:\n  enabled:\n    - my-custom-plugin\n"
	merged, err := runner.MergedProjectedConfig(profile.Provider, profile)
	if err != nil {
		t.Fatalf("merged: %v", err)
	}
	plugins, _ := merged["plugins"].(map[string]any)
	enabled, _ := plugins["enabled"].([]any)
	got := make([]string, 0, len(enabled))
	for _, item := range enabled {
		if text, ok := item.(string); ok {
			got = append(got, text)
		}
	}
	if !strings.Contains(strings.Join(got, ","), "cyberpenda-iteration-budget") {
		t.Fatalf("harness-derived plugin must survive, got %#v", got)
	}
	if !strings.Contains(strings.Join(got, ","), "my-custom-plugin") {
		t.Fatalf("operator plugin must coexist, got %#v", got)
	}
}

// Story 14/15: overlay extra permissions.allow entries must not union into
// the harness-generated allow list. Arrays are whole-replace; structured wins.
func TestClaudePermissionsAllowDoesNotUnionOverlayEntries(t *testing.T) {
	layout, _ := projectForTest(t, runtimeprofile.ProviderClaudeCode)
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			CustomConfigFile: `{"permissions":{"allow":["Bash(*)"],"deny":["Bash(rm -rf *)"]}}`,
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	settings := readJSONFile(t, projection.ConfigPath)
	permissions, _ := settings["permissions"].(map[string]any)
	allow, _ := permissions["allow"].([]any)
	for _, item := range allow {
		if text, ok := item.(string); ok && text == "Bash(*)" {
			t.Fatalf("overlay extra allow entry must not union into the managed list: %#v", allow)
		}
	}
	deny, _ := permissions["deny"].([]any)
	if len(deny) == 0 || deny[0] != "Bash(rm -rf *)" {
		t.Fatalf("non-managed deny must still merge: %#v", permissions)
	}
}

// Story 8: re-opening the editor shows the Custom Config File remainder
// with comments and formatting intact, not a re-encoded merge.
func TestProjectedConfigTextPreservesTOMLRemainderComments(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	profile.Fields.CustomConfigFile = "# Please keep this explanation\n[features]\nweb_search = true\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "# Please keep this explanation") {
		t.Fatalf("reopen seed must keep the remainder comment, got:\n%s", text)
	}
	if !strings.Contains(text, "web_search = true") {
		t.Fatalf("reopen seed must keep the remainder keys, got:\n%s", text)
	}
}

// Story 8: reopening a Hermes profile with an operator plugin must produce
// ONE plugins block containing both harness and operator entries — never a
// duplicate top-level key.
func TestProjectedConfigTextHermesReopenSinglePluginsBlock(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "plugins:\n  enabled:\n    - my-custom-plugin\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if strings.Count(text, "plugins:") != 1 {
		t.Fatalf("reopen must contain exactly one plugins block, got:\n%s", text)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("reopen text must parse as YAML: %v\n%s", err, text)
	}
	plugins, _ := doc["plugins"].(map[string]any)
	enabled, _ := plugins["enabled"].([]any)
	joined := make([]string, 0, len(enabled))
	for _, item := range enabled {
		if s, ok := item.(string); ok {
			joined = append(joined, s)
		}
	}
	if !slices.Contains(joined, "cyberpenda-iteration-budget") || !slices.Contains(joined, "my-custom-plugin") {
		t.Fatalf("reopen must show harness + operator plugins, got %#v\n%s", joined, text)
	}
}

// Reopen ordering: a remainder root key must stay at TOML root even when the
// generated seed ends with tables.
func TestProjectedConfigTextTOMLRootKeyStaysAtRoot(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	profile.Fields.Endpoint = "https://api.example.test/v1"
	profile.Fields.CustomConfigFile = "\ncustom_setting = true\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	settingIdx := strings.Index(text, "custom_setting = true")
	tableIdx := strings.Index(text, "[model_providers.")
	if settingIdx == -1 || tableIdx == -1 {
		t.Fatalf("reopen must contain both the root key and the table:\n%s", text)
	}
	if settingIdx > tableIdx {
		t.Fatalf("remainder root key must precede generated tables (TOML scoping):\n%s", text)
	}
}

// Reopen survival: a remainder root scalar survives alongside a colliding
// plugins block.
func TestProjectedConfigTextYAMLRootScalarSurvivesMerge(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "custom_setting: true\nplugins:\n  enabled:\n    - my-custom-plugin\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "custom_setting: true") {
		t.Fatalf("reopen must keep the remainder root scalar:\n%s", text)
	}
	if strings.Count(text, "plugins:") != 1 {
		t.Fatalf("reopen must contain exactly one plugins block:\n%s", text)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("reopen text must parse as YAML: %v\n%s", err, text)
	}
	if doc["custom_setting"] != true {
		t.Fatalf("root scalar must parse at document root: %#v", doc)
	}
}

// Story 2/16: inline API keys and Model Provider API-key envs render as
// redacted placeholder keys in the Claude preview.
func TestProjectedConfigTextClaudePreviewShowsCredentialChannels(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
	profile.Fields.APIKeys = map[string]string{"ANTHROPIC_API_KEY": "«redacted:sk-…»"}
	req := runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
		ModelSnapshot: &modelprovider.Snapshot{
			APIKeyEnv: "MIMO_API_KEY",
		},
	}
	text, err := runner.ProjectedConfigTextWith(runtimeprofile.ProviderClaudeCode, profile, req)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "MIMO_API_KEY"} {
		if !strings.Contains(text, `"`+key+`"`) {
			t.Fatalf("preview must show credential-channel env key %q:\n%s", key, text)
		}
	}
	if strings.Contains(text, "«redacted:sk-…»") {
		t.Fatalf("preview leaked an inline API key value:\n%s", text)
	}
}

// Story 8 verbatim: the operator's comment inside a colliding block survives
// reopen byte-for-byte; harness entries merge in without re-encoding the
// operator's own lines.
func TestProjectedConfigTextHermesReopenKeepsOperatorComments(t *testing.T) {
	remainder := "plugins:\n  # keep this comment\n  enabled:\n    - my-custom-plugin\n"
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = remainder
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "# keep this comment") {
		t.Fatalf("operator comment must survive reopen, got:\n%s", text)
	}
	if !strings.Contains(text, "my-custom-plugin") || !strings.Contains(text, "cyberpenda-iteration-budget") {
		t.Fatalf("reopen must show both harness and operator plugins, got:\n%s", text)
	}
	if strings.Count(text, "plugins:") != 1 {
		t.Fatalf("reopen must contain exactly one plugins block, got:\n%s", text)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("reopen text must parse as YAML: %v\n%s", err, text)
	}
}

// Story 8 verbatim: a preamble comment before any root key survives reopen.
func TestProjectedConfigTextHermesReopenKeepsPreambleComment(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "# keep this operator note\nskills:\n  autoload: false\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "# keep this operator note") {
		t.Fatalf("preamble comment must survive reopen:\n%s", text)
	}
	if !strings.Contains(text, "autoload: false") {
		t.Fatalf("remainder keys must survive reopen:\n%s", text)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("reopen text must parse as YAML: %v\n%s", err, text)
	}
}

// Story 16: entry dedup must scope to the target list. A sibling list
// (plugins.disabled) carrying the same entry name does not satisfy the
// plugins.enabled union.
func TestProjectedConfigTextHermesSiblingListDoesNotSuppressInjection(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "plugins:\n  enabled:\n    - my-custom-plugin\n  disabled:\n    - cyberpenda-iteration-budget\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("reopen text must parse as YAML: %v\n%s", err, text)
	}
	plugins, _ := doc["plugins"].(map[string]any)
	enabled, _ := plugins["enabled"].([]any)
	joined := make([]string, 0, len(enabled))
	for _, item := range enabled {
		if s, ok := item.(string); ok {
			joined = append(joined, s)
		}
	}
	if !slices.Contains(joined, "cyberpenda-iteration-budget") || !slices.Contains(joined, "my-custom-plugin") {
		t.Fatalf("preview enabled must match the runtime union, got %#v\n%s", joined, text)
	}
}

// Story 8 verbatim: remainder root-key order survives reopen byte-for-byte.
func TestProjectedConfigTextHermesReopenKeepsRootKeyOrder(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "z_custom:\n  value: 1\na_custom:\n  value: 2\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	zIdx := strings.Index(text, "z_custom:")
	aIdx := strings.Index(text, "a_custom:")
	if zIdx == -1 || aIdx == -1 {
		t.Fatalf("reopen must keep both remainder keys:\n%s", text)
	}
	if zIdx > aIdx {
		t.Fatalf("remainder root-key order must be preserved (z_custom before a_custom):\n%s", text)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("reopen text must parse as YAML: %v\n%s", err, text)
	}
}

// Story 16: a nested same-name list (plugins.metadata.enabled) must not
// attract the harness entry; only the direct plugins.enabled unions.
func TestProjectedConfigTextHermesNestedSameNameListStaysScoped(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "plugins:\n  metadata:\n    enabled:\n      - shadow-plugin\n  enabled:\n    - my-custom-plugin\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("reopen text must parse as YAML: %v\n%s", err, text)
	}
	plugins, _ := doc["plugins"].(map[string]any)
	enabled, _ := plugins["enabled"].([]any)
	joined := make([]string, 0, len(enabled))
	for _, item := range enabled {
		if s, ok := item.(string); ok {
			joined = append(joined, s)
		}
	}
	if !slices.Contains(joined, "cyberpenda-iteration-budget") || !slices.Contains(joined, "my-custom-plugin") {
		t.Fatalf("direct plugins.enabled must match the runtime union, got %#v\n%s", joined, text)
	}
	metadata, _ := plugins["metadata"].(map[string]any)
	nested, _ := metadata["enabled"].([]any)
	for _, item := range nested {
		if s, ok := item.(string); ok && s == "cyberpenda-iteration-budget" {
			t.Fatalf("harness entry must not inject into the nested same-name list:\n%s", text)
		}
	}
}
