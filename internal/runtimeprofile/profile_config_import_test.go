package runtimeprofile_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

// The full Profile Config Import flow for the warp use case: edited Claude
// settings.json text maps env back into the structured env field, refuses the
// managed permissions.allow key, and stores the un-mappable remainder
// (enabledPlugins entries) verbatim in the Custom Config File.
func TestImportProfileConfigMapsStructuredAndStoresRemainder(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Preset", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	edited := `{
  "env": {"MY_TOOL_TAG": "abc"},
  "enabledPlugins": {"warp@claude-code-warp": true},
  "permissions": {"deny": ["Bash(rm -rf *)"]}
}`
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if result.Profile.Fields.Env["MY_TOOL_TAG"] != "abc" {
		t.Fatalf("structured env not mapped back, got %#v", result.Profile.Fields.Env)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "warp@claude-code-warp") {
		t.Fatalf("remainder not stored verbatim, got %q", result.Profile.Fields.CustomConfigFile)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "Bash(rm -rf *)") {
		t.Fatalf("permissions.deny must stay in the remainder, got %q", result.Profile.Fields.CustomConfigFile)
	}
	if len(result.MappedKeys) == 0 {
		t.Fatal("mapped keys must be reported")
	}
}

func TestImportProfileConfigPreservesRawTextVerbatim(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Hermes Preset", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Story 8: re-opening the editor must show exactly what the operator
	// wrote — comments and formatting included. Nothing maps for Hermes
	// (no structured env mapping), so the raw text is stored verbatim.
	edited := "# keep my header comment\nskills:\n  autoload: false   # inline comment\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Profile.Fields.CustomConfigFile != edited {
		t.Fatalf("custom config file must be preserved verbatim, got %q", result.Profile.Fields.CustomConfigFile)
	}
	if len(result.MappedKeys) != 0 {
		t.Fatalf("nothing maps for a comment-only remainder, got %v", result.MappedKeys)
	}
}

func TestImportProfileConfigRefusesManagedKeysPerKey(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Hermes Preset", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	edited := "approvals:\n  mode: on\nterminal:\n  backend: docker\nskills:\n  autoload: false\n"
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err == nil {
		t.Fatal("expected managed key refusal")
	}
	var refusal *runtimeprofile.ImportConfigError
	if !asImportConfigError(err, &refusal) {
		t.Fatalf("error = %v (%T), want ImportConfigError", err, err)
	}
	var approvalsErr, terminalErr bool
	for _, keyErr := range refusal.Errors {
		if strings.HasPrefix(keyErr.Key, "approvals.mode") {
			approvalsErr = true
			if keyErr.Field == "" {
				t.Fatalf("managed key error must name the owning structured field, got %#v", keyErr)
			}
		}
		if strings.HasPrefix(keyErr.Key, "terminal.backend") {
			terminalErr = true
		}
		if strings.HasPrefix(keyErr.Key, "skills") {
			t.Fatalf("non-managed key must not be refused: %#v", keyErr)
		}
	}
	if !approvalsErr || !terminalErr {
		t.Fatalf("expected approvals.mode and terminal.backend refusals, got %#v", refusal.Errors)
	}

	// Nothing persisted: the profile is untouched.
	kept, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if kept.Fields.CustomConfigFile != "" || len(kept.Fields.Env) != 0 {
		t.Fatalf("refused import must not persist, got %#v", kept.Fields)
	}
}

func TestImportProfileConfigRefusesSecretShapedValues(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Preset", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	edited := "[features]\nweb_search = true\n"
	bad := "[env]\nMY_API_TOKEN = \"sk-live-123\"\n"
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited + bad})
	if err == nil {
		t.Fatal("expected secret refusal")
	}
	var refusal *runtimeprofile.ImportConfigError
	if !asImportConfigError(err, &refusal) {
		t.Fatalf("error = %v (%T), want ImportConfigError", err, err)
	}
	found := false
	for _, keyErr := range refusal.Errors {
		if strings.Contains(strings.ToLower(keyErr.Key), "token") {
			found = true
		}
		if strings.Contains(keyErr.Message, "sk-live-123") {
			t.Fatalf("refusal must not echo the secret value: %#v", keyErr)
		}
	}
	if !found {
		t.Fatalf("expected token key refusal, got %#v", refusal.Errors)
	}
}

func TestImportProfileConfigRefusesSecretShapedValuesOnInnocentKeys(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Innocent", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A secret-shaped VALUE under a perfectly innocent key must also be
	// refused — the editor never shows secrets, so any credential-looking
	// value in imported text came from somewhere it should not be.
	edited := `{"toolConfig":{"license":"sk-ant-api03-live-1234567890abcdef"}}`
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err == nil {
		t.Fatal("expected secret-value refusal")
	}
	var refusal *runtimeprofile.ImportConfigError
	if !asImportConfigError(err, &refusal) {
		t.Fatalf("error = %v (%T), want ImportConfigError", err, err)
	}
	found := false
	for _, keyErr := range refusal.Errors {
		if strings.Contains(keyErr.Message, "secret-shaped credential") {
			found = true
		}
		if strings.Contains(keyErr.Message, "sk-ant-api03") {
			t.Fatalf("refusal must not echo the secret value: %#v", keyErr)
		}
	}
	if !found {
		t.Fatalf("expected secret-shaped value refusal, got %#v", refusal.Errors)
	}
}

func TestImportProfileConfigRejectsMalformedText(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Hermes Preset", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: "not: [valid: yaml"}); err == nil {
		t.Fatal("expected parse error refusal")
	}
}

func TestImportProfileConfigRefusesProviderWithoutProjection(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Fake", runtimeprofile.ProviderFake, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: "{}"}); err == nil {
		t.Fatal("expected unsupported-provider refusal")
	}
}

func asImportConfigError(err error, target **runtimeprofile.ImportConfigError) bool {
	if direct, ok := err.(*runtimeprofile.ImportConfigError); ok {
		*target = direct
		return true
	}
	return false
}

// Story 6: a structured-expressible key maps back into its field instead of
// living in the overlay.
func TestImportProfileConfigMapsCodexModelIntoFields(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Model", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	edited := "model = \"gpt-5.2\"\n\n[features]\nweb_search = true\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Profile.Fields.Model != "gpt-5.2" {
		t.Fatalf("model must map into the structured field, got %q", result.Profile.Fields.Model)
	}
	if !slices.Contains(result.MappedKeys, "model") {
		t.Fatalf("model must be reported in mapped keys: %v", result.MappedKeys)
	}
	if strings.Contains(result.Profile.Fields.CustomConfigFile, "gpt-5.2") {
		t.Fatalf("model must not stay in the remainder: %q", result.Profile.Fields.CustomConfigFile)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "web_search") {
		t.Fatalf("unstructured keys must stay in the remainder: %q", result.Profile.Fields.CustomConfigFile)
	}
}

// Unchanged Managed Config Keys from the projected seed are not a change:
// import accepts them and strips them from the Custom Config File remainder.
func TestImportProfileConfigAcceptsUnchangedManagedKeys(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Seed", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	seed := "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n"
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return seed, nil })
	edited := seed + "\n[features]\nweb_search = true\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: edited,
	})
	if err != nil {
		t.Fatalf("unchanged managed keys must import, got %v", err)
	}
	if strings.Contains(result.Profile.Fields.CustomConfigFile, "approval_policy") ||
		strings.Contains(result.Profile.Fields.CustomConfigFile, "sandbox_mode") {
		t.Fatalf("unchanged managed keys must be stripped from remainder, got %q", result.Profile.Fields.CustomConfigFile)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "web_search") {
		t.Fatalf("unmanaged remainder must stay, got %q", result.Profile.Fields.CustomConfigFile)
	}
}

func TestImportProfileConfigRefusesChangedManagedKeyValue(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Changed", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := "approval_policy = \"on-request\"\nsandbox_mode = \"danger-full-access\"\n"
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: edited,
	})
	if err == nil {
		t.Fatal("changed managed key must be refused")
	}
	var refusal *runtimeprofile.ImportConfigError
	if !asImportConfigError(err, &refusal) {
		t.Fatalf("error = %v (%T), want ImportConfigError", err, err)
	}
	found := false
	for _, keyErr := range refusal.Errors {
		if keyErr.Key == "approval_policy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected approval_policy change refusal, got %#v", refusal.Errors)
	}
}

// A forged client-provided baseline must not bypass Managed Config Key
// refusal: the service derives the baseline itself, never from the request.
func TestImportProfileConfigIgnoresClientBaselineForManagedKeys(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Forged", runtimeprofile.ProviderCodex, runtimeprofile.Fields{ModelProviderID: "mp-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A client can no longer submit a baseline at all: the request shape has
	// no such field, and the service derives it from the injected projector.
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "approval_policy = \"on-request\"\n",
	})
	var refusal *runtimeprofile.ImportConfigError
	if !errors.As(err, &refusal) {
		t.Fatalf("changed managed key must still be refused, got %v", err)
	}
	if refusal.Errors[0].Key != "approval_policy" {
		t.Fatalf("refusal key = %q, want approval_policy", refusal.Errors[0].Key)
	}
}

// Deleting a Managed Config Key from the edited document is a change and
// must be refused, not silently accepted because the walker never sees it.
func TestImportProfileConfigRefusesDeletedManagedKey(t *testing.T) {
	service := newTestService(t)
	service.SetImportBaseline(func(profile runtimeprofile.Profile) (string, error) {
		return "approval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n", nil
	})
	created, err := service.Create("Codex Delete", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "sandbox_mode = \"danger-full-access\"\n",
	})
	var refusal *runtimeprofile.ImportConfigError
	if !errors.As(err, &refusal) {
		t.Fatalf("deleted managed key must be refused, got %v", err)
	}
	found := false
	for _, keyErr := range refusal.Errors {
		if keyErr.Key == "approval_policy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusal must name the deleted approval_policy, got %+v", refusal.Errors)
	}
}

// Structured env round-trips fully: removing a key from the edited env map
// removes it from the structured field instead of resurrecting it.
func TestImportProfileConfigEnvDeletionRoundTrips(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Env", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Env: map[string]string{"FOO": "1", "BAR": "2"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"env":{"FOO":"1"}}`,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, still := result.Profile.Fields.Env["BAR"]; still {
		t.Fatalf("env deletion must round-trip into the structured field, got %#v", result.Profile.Fields.Env)
	}
}

// A known catalog plugin maps back into the structured Runtime Extensions
// field instead of lingering in the Custom Config File remainder.
func TestImportProfileConfigMapsKnownPluginToRuntimeExtensions(t *testing.T) {
	service := newTestService(t)
	service.SetKnownInstallRefs(func() []string { return []string{"frontend-design@claude-plugins-official"} })
	created, err := service.Create("Claude Plugin", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"enabledPlugins":{"frontend-design@claude-plugins-official":true}}`,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	found := false
	for _, ref := range result.Profile.Fields.RuntimeExtensions {
		if ref.Config["install_ref"] == "frontend-design@claude-plugins-official" {
			found = true
		}
	}
	if !found {
		t.Fatalf("known plugin must map into Runtime Extensions, got %#v remainder %q", result.Profile.Fields.RuntimeExtensions, result.Profile.Fields.CustomConfigFile)
	}
	if strings.Contains(result.Profile.Fields.CustomConfigFile, "frontend-design") {
		t.Fatalf("mapped plugin must not linger in the remainder: %q", result.Profile.Fields.CustomConfigFile)
	}
}

// Comments in the operator's provider-native text survive an import that
// maps keys away: the remainder keeps the operator's lines verbatim.
func TestImportProfileConfigPreservesCommentsInRemainder(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Comments", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	raw := "model = \"gpt-5.2\"\n\n# Please keep this explanation\n[features]\nweb_search = true\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: raw})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "# Please keep this explanation") {
		t.Fatalf("comment must survive import verbatim, got:\n%s", result.Profile.Fields.CustomConfigFile)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "[features]") {
		t.Fatalf("features section must stay in the remainder, got:\n%s", result.Profile.Fields.CustomConfigFile)
	}
}

// Story 21: a Managed Config Key on a list field locks only the entries the
// harness itself derives. The Hermes iteration-budget entry stays pinned,
// while operator-added provider-native plugins join the array freely.
func TestImportProfileConfigHermesPluginEntryGranularity(t *testing.T) {
	service := newTestService(t)
	service.SetManagedKeyDeclarations(map[runtimeprofile.Provider][]runtimeprofile.ManagedKeyDeclaration{
		runtimeprofile.ProviderHermes: {{
			Key:   "plugins.enabled",
			Field: "runtime extensions",
		}},
	})
	baseline := "plugins:\n  enabled:\n    - cyberpenda-iteration-budget\n"
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return baseline, nil })
	created, err := service.Create("Hermes Plugins", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Operator adds their own plugin entry alongside the managed one.
	edited := "plugins:\n  enabled:\n    - cyberpenda-iteration-budget\n    - my-custom-plugin\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err != nil {
		t.Fatalf("operator-added plugin entry must import freely, got %v", err)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "my-custom-plugin") {
		t.Fatalf("operator entry must survive in the remainder, got %q", result.Profile.Fields.CustomConfigFile)
	}
	if strings.Contains(result.Profile.Fields.CustomConfigFile, "cyberpenda-iteration-budget") {
		t.Fatalf("managed entry must be stripped from the remainder, got %q", result.Profile.Fields.CustomConfigFile)
	}

	// Removing the harness-derived entry is a managed change → refused.
	removed := "plugins:\n  enabled:\n    - my-custom-plugin\n"
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: removed})
	if err == nil {
		t.Fatal("deleting the harness-derived plugin entry must be refused")
	}
}

// Structured Codex model round-trips fully: removing the model key from
// the edited document clears the structured field instead of resurrecting it.
func TestImportProfileConfigModelDeletionRoundTrips(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Model Delete", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-5.2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "[features]\nweb_search = true\n",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Profile.Fields.Model != "" {
		t.Fatalf("model deletion must round-trip into the structured field, got %q", result.Profile.Fields.Model)
	}
}

// Story 14/15: permissions.allow is a Managed array with whole-replace
// semantics. Extra entries are a change, not a coexistence.
func TestImportProfileConfigRefusesExtraPermissionsAllow(t *testing.T) {
	service := newTestService(t)
	baseline := `{
  "permissions": {"allow": ["mcp__pentest__blackboard_read"]}
}`
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return baseline, nil })
	created, err := service.Create("Claude Allow", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := `{
  "permissions": {"allow": ["mcp__pentest__blackboard_read", "Bash(*)"]}
}`
	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err == nil {
		t.Fatal("extra permissions.allow entry must be refused")
	}
	var refusal *runtimeprofile.ImportConfigError
	if !errors.As(err, &refusal) {
		t.Fatalf("want ImportConfigError, got %v", err)
	}
	found := false
	for _, keyErr := range refusal.Errors {
		if strings.Contains(keyErr.Key, "permissions.allow") {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusal must name permissions.allow, got %+v", refusal.Errors)
	}
}

// Story 6: deleting the whole env section clears the structured env field.
func TestImportProfileConfigEnvSectionDeletionClearsStructuredEnv(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Env Section", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Env: map[string]string{"FOO": "1", "BAR": "2"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"enabledPlugins":{"warp@claude-code-warp":true}}`,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Profile.Fields.Env) != 0 {
		t.Fatalf("deleting the env section must clear structured env, got %#v", result.Profile.Fields.Env)
	}
}

// Story 6/7: a known plugin already on Runtime Extensions must still be
// stripped from the remainder, and setting it false must disable it.
func TestImportProfileConfigKnownPluginDisableAndNoRemainderDuplicate(t *testing.T) {
	service := newTestService(t)
	service.SetKnownInstallRefs(func() []string { return []string{"frontend-design@claude-plugins-official"} })
	enabled := true
	created, err := service.Create("Claude Plugin Dup", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{{
			ID:      "frontend-design",
			Enabled: &enabled,
			Config:  map[string]string{"install_ref": "frontend-design@claude-plugins-official"},
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"enabledPlugins":{"frontend-design@claude-plugins-official":true}}`,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if strings.Contains(result.Profile.Fields.CustomConfigFile, "frontend-design") {
		t.Fatalf("already-structured plugin must not copy into remainder: %q", result.Profile.Fields.CustomConfigFile)
	}

	disabled, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"enabledPlugins":{"frontend-design@claude-plugins-official":false}}`,
	})
	if err != nil {
		t.Fatalf("disable import: %v", err)
	}
	for _, ref := range disabled.Profile.Fields.RuntimeExtensions {
		if ref.Config["install_ref"] == "frontend-design@claude-plugins-official" && (ref.Enabled == nil || *ref.Enabled) {
			t.Fatalf("false must disable the structured plugin, got %#v", disabled.Profile.Fields.RuntimeExtensions)
		}
	}

	removed, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"enabledPlugins":{"warp@claude-code-warp":true}}`,
	})
	if err != nil {
		t.Fatalf("delete import: %v", err)
	}
	for _, ref := range removed.Profile.Fields.RuntimeExtensions {
		if ref.Config["install_ref"] == "frontend-design@claude-plugins-official" {
			t.Fatalf("omitting the plugin must drop it from Runtime Extensions, got %#v", removed.Profile.Fields.RuntimeExtensions)
		}
	}
}

// Story 8/21: the stored remainder must stay valid YAML — dropping the
// managed list entry must not remove the enabled: container line.
func TestImportProfileConfigHermesRemainderStaysValidYAML(t *testing.T) {
	service := newTestService(t)
	service.SetManagedKeyDeclarations(map[runtimeprofile.Provider][]runtimeprofile.ManagedKeyDeclaration{
		runtimeprofile.ProviderHermes: {{
			Key:   "plugins.enabled",
			Field: "runtime extensions",
		}},
	})
	baseline := "plugins:\n  enabled:\n    - cyberpenda-iteration-budget\n"
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return baseline, nil })
	created, err := service.Create("Hermes YAML Shape", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := "plugins:\n  enabled:\n    - cyberpenda-iteration-budget\n    - my-custom-plugin\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(result.Profile.Fields.CustomConfigFile), &doc); err != nil {
		t.Fatalf("stored remainder must parse as YAML: %v\n%s", err, result.Profile.Fields.CustomConfigFile)
	}
	plugins, ok := doc["plugins"].(map[string]any)
	if !ok {
		t.Fatalf("plugins must stay an object, got %#v\n%s", doc["plugins"], result.Profile.Fields.CustomConfigFile)
	}
	enabled, ok := plugins["enabled"].([]any)
	if !ok {
		t.Fatalf("plugins.enabled must stay a list, got %#v\n%s", plugins["enabled"], result.Profile.Fields.CustomConfigFile)
	}
	if len(enabled) != 1 || enabled[0] != "my-custom-plugin" {
		t.Fatalf("remainder must keep exactly the operator plugin, got %#v", enabled)
	}
}

// Story 6: without a Model Provider, ANTHROPIC_MODEL / ANTHROPIC_BASE_URL are
// projections of the structured Model / Endpoint fields. Import consumes them
// back instead of freezing them into Fields.Env.
func TestImportProfileConfigClaudeLegacyModelEndpointRoundTrip(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Legacy", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Model:    "claude-a",
		Endpoint: "https://a.example.test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"env":{"ANTHROPIC_MODEL":"claude-a","ANTHROPIC_BASE_URL":"https://a.example.test"}}`,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Profile.Fields.Model != "claude-a" {
		t.Fatalf("structured model must survive, got %q", result.Profile.Fields.Model)
	}
	if result.Profile.Fields.Endpoint != "https://a.example.test" {
		t.Fatalf("structured endpoint must survive, got %q", result.Profile.Fields.Endpoint)
	}
	if _, present := result.Profile.Fields.Env["ANTHROPIC_MODEL"]; present {
		t.Fatalf("ANTHROPIC_MODEL must be consumed into the structured field, got %#v", result.Profile.Fields.Env)
	}
	if _, present := result.Profile.Fields.Env["ANTHROPIC_BASE_URL"]; present {
		t.Fatalf("ANTHROPIC_BASE_URL must be consumed into the structured field, got %#v", result.Profile.Fields.Env)
	}

	changed, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"env":{"ANTHROPIC_MODEL":"claude-b","ANTHROPIC_BASE_URL":"https://a.example.test"}}`,
	})
	if err != nil {
		t.Fatalf("change import: %v", err)
	}
	if changed.Profile.Fields.Model != "claude-b" {
		t.Fatalf("edited model must update the structured field, got %q", changed.Profile.Fields.Model)
	}

	cleared, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: `{"env":{"ANTHROPIC_BASE_URL":"https://a.example.test"}}`,
	})
	if err != nil {
		t.Fatalf("delete import: %v", err)
	}
	if cleared.Profile.Fields.Model != "" {
		t.Fatalf("removing ANTHROPIC_MODEL must clear the structured model, got %q", cleared.Profile.Fields.Model)
	}
}

// Full semantic round-trip: the stored remainder must re-merge so the final
// projection keeps both the harness plugin and the operator plugin.
func TestImportProfileConfigHermesRemainderProjectsBothPlugins(t *testing.T) {
	service := newTestService(t)
	service.SetManagedKeyDeclarations(map[runtimeprofile.Provider][]runtimeprofile.ManagedKeyDeclaration{
		runtimeprofile.ProviderHermes: {{
			Key:   "plugins.enabled",
			Field: "runtime extensions",
		}},
	})
	baseline := "plugins:\n  enabled:\n    - cyberpenda-iteration-budget\n"
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return baseline, nil })
	created, err := service.Create("Hermes Round Trip", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := "plugins:\n  enabled:\n    - cyberpenda-iteration-budget\n    - my-custom-plugin\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: edited})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	merged, err := runner.MergedProjectedConfig(runtimeprofile.ProviderHermes, result.Profile)
	if err != nil {
		t.Fatalf("merged config: %v", err)
	}
	plugins, _ := merged["plugins"].(map[string]any)
	enabled, _ := plugins["enabled"].([]any)
	joined := make([]string, 0, len(enabled))
	for _, item := range enabled {
		if text, ok := item.(string); ok {
			joined = append(joined, text)
		}
	}
	if !slices.Contains(joined, "cyberpenda-iteration-budget") || !slices.Contains(joined, "my-custom-plugin") {
		t.Fatalf("final plugins.enabled must contain harness + operator entries, got %#v", joined)
	}
}

// Generated redactions round-trip via provenance: credential-generated paths
// import cleanly and never persist into Fields.Env.
func TestImportProfileConfigCredentialPlaceholderProvenance(t *testing.T) {
	service := newTestService(t)
	baseline := "{\n  \"env\": {\"GITHUB_TOKEN\": \"[REDACTED]\", \"MY_CRED\": \"[REDACTED]\"}\n}"
	service.SetImportBaselineProvenance(func(runtimeprofile.Profile) (string, []string, error) {
		return baseline, []string{"env.GITHUB_TOKEN", "env.MY_CRED"}, nil
	})
	created, err := service.Create("Cred Provenance", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	unchanged, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "{\n  \"env\": {\"GITHUB_TOKEN\": \"[REDACTED]\", \"MY_CRED\": \"[REDACTED]\"}\n}",
	})
	if err != nil {
		t.Fatalf("placeholder no-op import must succeed, got %v", err)
	}
	if _, present := unchanged.Profile.Fields.Env["GITHUB_TOKEN"]; present {
		t.Fatalf("generated placeholder must not persist into Env, got %#v", unchanged.Profile.Fields.Env)
	}
	if _, present := unchanged.Profile.Fields.Env["MY_CRED"]; present {
		t.Fatalf("generated placeholder must not persist into Env, got %#v", unchanged.Profile.Fields.Env)
	}

	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "{\n  \"env\": {\"GITHUB_TOKEN\": \"[REDACTED]\", \"MY_CRED\": \"sk-test-forged-value-123\"}\n}",
	})
	if err == nil {
		t.Fatal("replacing a generated placeholder must be refused")
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Fatalf("refusal must point at the credential channel, got %v", err)
	}
}

// Managed subtrees leave the raw text too: Hermes providers and Codex
// model_providers.* tables must not survive into the stored remainder.
func TestImportProfileConfigManagedSubtreeLeavesRawText(t *testing.T) {
	service := newTestService(t)
	service.SetManagedKeyDeclarations(map[runtimeprofile.Provider][]runtimeprofile.ManagedKeyDeclaration{
		runtimeprofile.ProviderHermes: {{Key: "providers", Field: "model providers"}},
		runtimeprofile.ProviderCodex:  {{Key: "model_providers.*", Field: "model_provider_id", Condition: "model_provider_resolved"}},
	})

	hermesBaseline := "model:\n  provider: custom:a\nproviders:\n  a:\n    base_url: https://a.example.test\n"
	service.SetImportBaseline(func(p runtimeprofile.Profile) (string, error) {
		if p.Provider == runtimeprofile.ProviderHermes {
			return hermesBaseline, nil
		}
		return "approval_policy = \"never\"\n", nil
	})
	hermes, err := service.Create("Hermes Subtree", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create hermes: %v", err)
	}
	result, err := service.ImportConfig(hermes.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "model:\n  provider: custom:a\nproviders:\n  a:\n    base_url: https://a.example.test\nskills:\n  autoload: false\n",
	})
	if err != nil {
		t.Fatalf("hermes import: %v", err)
	}
	if strings.Contains(result.Profile.Fields.CustomConfigFile, "base_url") || strings.Contains(result.Profile.Fields.CustomConfigFile, "providers") {
		t.Fatalf("managed providers subtree must leave the remainder, got:\n%s", result.Profile.Fields.CustomConfigFile)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "autoload: false") {
		t.Fatalf("operator keys must survive:\n%s", result.Profile.Fields.CustomConfigFile)
	}

	codex, err := service.Create("Codex Subtree", runtimeprofile.ProviderCodex, runtimeprofile.Fields{ModelProviderID: "prov-a"})
	if err != nil {
		t.Fatalf("create codex: %v", err)
	}
	codexBaseline := "approval_policy = \"never\"\n\n[model_providers.prov-a]\nname = \"A\"\nbase_url = \"https://a.example.test/v1\"\nwire_api = \"responses\"\n"
	service.SetImportBaseline(func(p runtimeprofile.Profile) (string, error) {
		if p.Provider == runtimeprofile.ProviderCodex {
			return codexBaseline, nil
		}
		return hermesBaseline, nil
	})
	codexResult, err := service.ImportConfig(codex.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "approval_policy = \"never\"\n\n[model_providers.prov-a]\nname = \"A\"\nbase_url = \"https://a.example.test/v1\"\nwire_api = \"responses\"\n\n[features]\nweb_search = true\n",
	})
	if err != nil {
		t.Fatalf("codex import: %v", err)
	}
	if strings.Contains(codexResult.Profile.Fields.CustomConfigFile, "model_providers") || strings.Contains(codexResult.Profile.Fields.CustomConfigFile, "base_url") {
		t.Fatalf("managed model_providers table must leave the remainder, got:\n%s", codexResult.Profile.Fields.CustomConfigFile)
	}
	if !strings.Contains(codexResult.Profile.Fields.CustomConfigFile, "web_search") {
		t.Fatalf("operator table must survive:\n%s", codexResult.Profile.Fields.CustomConfigFile)
	}
}

// MCP config sections are out of scope for the Custom Config File: with no
// baseline section, operator-added mcp_servers is refused (the structured
// MCPServers field owns MCP config).
func TestImportProfileConfigStripsMCPServersFromRemainder(t *testing.T) {
	service := newTestService(t)
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return "", nil })
	for _, provider := range []runtimeprofile.Provider{runtimeprofile.ProviderCodex, runtimeprofile.ProviderHermes} {
		created, err := service.Create("MCP "+string(provider), provider, runtimeprofile.Fields{})
		if err != nil {
			t.Fatalf("create %s: %v", provider, err)
		}
		var text string
		if provider == runtimeprofile.ProviderCodex {
			text = "\n[mcp_servers.pentest]\nurl = \"http://127.0.0.1:8787/mcp\"\nenabled = true\n"
		} else {
			text = "mcp_servers:\n  pentest:\n    url: http://127.0.0.1:8787/mcp\n"
		}
		if _, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: text}); err == nil {
			t.Fatalf("%s operator-added mcp_servers must be refused", provider)
		}
	}
}

// Provenance-based placeholder enforcement: the daemon reports which paths
// are credential-generated. Replacing or deleting them is refused; a literal
// [REDACTED] value on a NON-generated path is ordinary data.
func TestImportProfileConfigProvenanceRefusesNonSecretReplacement(t *testing.T) {
	service := newTestService(t)
	service.SetImportBaselineProvenance(func(runtimeprofile.Profile) (string, []string, error) {
		return "{\n  \"env\": {\"MY_CRED\": \"[REDACTED]\", \"FOO\": \"[REDACTED]\"}\n}",
			[]string{"env.MY_CRED"}, nil
	})
	created, err := service.Create("Provenance", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	replaced, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "{\n  \"env\": {\"MY_CRED\": \"hello\", \"FOO\": \"[REDACTED]\"}\n}",
	})
	if err == nil {
		t.Fatalf("replacing a generated placeholder with plain text must be refused, got %#v", replaced.Profile.Fields.Env)
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Fatalf("refusal must point at the credential channel, got %v", err)
	}

	unchanged, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "{\n  \"env\": {\"MY_CRED\": \"[REDACTED]\", \"FOO\": \"[REDACTED]\"}\n}",
	})
	if err != nil {
		t.Fatalf("unchanged import must succeed: %v", err)
	}
	if _, present := unchanged.Profile.Fields.Env["MY_CRED"]; present {
		t.Fatalf("generated placeholder must not persist into Env, got %#v", unchanged.Profile.Fields.Env)
	}
	// A literal [REDACTED] on a non-generated path is ordinary operator data.
	if unchanged.Profile.Fields.Env["FOO"] != "[REDACTED]" {
		t.Fatalf("non-generated literal placeholder must survive in Env, got %#v", unchanged.Profile.Fields.Env)
	}

	deleted, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "{\n  \"env\": {\"FOO\": \"[REDACTED]\"}\n}",
	})
	if err == nil {
		t.Fatalf("deleting a generated placeholder must be refused, got %#v", deleted.Profile.Fields.Env)
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Fatalf("deletion refusal must point at the credential channel, got %v", err)
	}
}

// Modified MCP sections are refused: the structured MCPServers field owns MCP
// config, the Custom Config File never carries it.
func TestImportProfileConfigRefusesModifiedMCPServers(t *testing.T) {
	service := newTestService(t)
	baseline := "mcp_servers:\n  pentest:\n    url: http://127.0.0.1:8787/mcp\n"
	service.SetImportBaseline(func(runtimeprofile.Profile) (string, error) { return baseline, nil })
	created, err := service.Create("MCP Guard", runtimeprofile.ProviderHermes, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "mcp_servers:\n  pentest:\n    url: http://evil.example.com/mcp\n",
	})
	if err == nil {
		t.Fatal("modified MCP section must be refused")
	}
	if !strings.Contains(err.Error(), "mcp_servers") {
		t.Fatalf("refusal must name mcp_servers, got %v", err)
	}

	unchanged, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: baseline,
	})
	if err != nil {
		t.Fatalf("unchanged MCP section must import cleanly: %v", err)
	}
	if strings.Contains(unchanged.Profile.Fields.CustomConfigFile, "mcp_servers") {
		t.Fatalf("unchanged MCP section must not persist into remainder: %q", unchanged.Profile.Fields.CustomConfigFile)
	}

	added, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{
		ConfigText: "skills:\n  autoload: false\nmcp_servers:\n  extra:\n    url: http://x.example.com\n",
	})
	if err == nil {
		t.Fatalf("operator-added MCP section must be refused, got remainder %q", added.Profile.Fields.CustomConfigFile)
	}
}

// Story 8 verbatim: a JSON remainder with operator spacing/layout keeps its
// exact bytes when structured keys map away.
func TestImportProfileConfigJSONRemainderVerbatimBytes(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude JSON Verbatim", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	raw := "{\n  \"env\":{\"FOO\":\"1\"},\n  \"enabledPlugins\" : { \"warp@claude-code-warp\" : true }\n}"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: raw})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(result.Profile.Fields.CustomConfigFile, "\"enabledPlugins\" : { \"warp@claude-code-warp\" : true }") {
		t.Fatalf("JSON remainder must keep operator spacing verbatim, got:\n%s", result.Profile.Fields.CustomConfigFile)
	}
}

// Story 8: minified JSON and multiple members on one line still remove the
// structured env member without canonicalizing the surviving raw member.
func TestImportProfileConfigMinifiedJSONRemainderVerbatim(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Claude Minified JSON", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	raw := `{"env":{"FOO":"1"},"enabledPlugins" : { "warp@claude-code-warp" : true }}`
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: raw})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := `{"enabledPlugins" : { "warp@claude-code-warp" : true }}`
	if result.Profile.Fields.CustomConfigFile != want {
		t.Fatalf("minified JSON remainder must stay verbatim, got %q want %q", result.Profile.Fields.CustomConfigFile, want)
	}
}

// Story 4/6/8: a known plugin maps to Runtime Extensions while an unknown
// plugin survives in the same minified enabledPlugins object verbatim.
func TestImportProfileConfigMixedKnownUnknownPluginsVerbatim(t *testing.T) {
	service := newTestService(t)
	service.SetKnownInstallRefs(func() []string { return []string{"frontend-design@claude-plugins-official"} })
	created, err := service.Create("Claude Mixed Plugins", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	raw := `{"enabledPlugins":{"frontend-design@claude-plugins-official":true,"warp@claude-code-warp" : true}}`
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: raw})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := `{"enabledPlugins":{"warp@claude-code-warp" : true}}`
	if result.Profile.Fields.CustomConfigFile != want {
		t.Fatalf("unknown plugin remainder must stay verbatim, got %q want %q", result.Profile.Fields.CustomConfigFile, want)
	}
	if len(result.Profile.Fields.RuntimeExtensions) != 1 || result.Profile.Fields.RuntimeExtensions[0].Config["install_ref"] != "frontend-design@claude-plugins-official" {
		t.Fatalf("known plugin must map to Runtime Extensions: %#v", result.Profile.Fields.RuntimeExtensions)
	}
}

// Story 8: importing a structured Codex model must not break or reformat an
// unrelated repeated array-of-table remainder.
func TestImportProfileConfigTOMLArrayOfTablesVerbatim(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Array Tables", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	remainder := "[[custom.backends]]\nname = \"a\"\n\n[[custom.backends]]\nname = \"b\"\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: "model = \"gpt-5.2\"\n" + remainder})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Profile.Fields.CustomConfigFile != remainder {
		t.Fatalf("array-of-table remainder must stay verbatim, got %q want %q", result.Profile.Fields.CustomConfigFile, remainder)
	}
}

// Story 8: a multiline root TOML value remains contiguous and verbatim when a
// sibling structured key maps away.
func TestImportProfileConfigTOMLMultilineRootVerbatim(t *testing.T) {
	service := newTestService(t)
	created, err := service.Create("Codex Multiline", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	remainder := "my_list = [\n  \"a\",\n  \"b\",\n]\n"
	result, err := service.ImportConfig(created.ID, runtimeprofile.ImportConfigRequest{ConfigText: "model = \"gpt-5.2\"\n" + remainder})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Profile.Fields.CustomConfigFile != remainder {
		t.Fatalf("multiline root remainder must stay verbatim, got %q want %q", result.Profile.Fields.CustomConfigFile, remainder)
	}
}
