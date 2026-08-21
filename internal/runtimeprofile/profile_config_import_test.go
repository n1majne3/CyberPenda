package runtimeprofile_test

import (
	"slices"
	"strings"
	"testing"

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
