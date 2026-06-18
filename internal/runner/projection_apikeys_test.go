package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestProjectClaudeSettingsUsesInlineAPIKeys(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-claude-inline", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model:    "claude-sonnet-4",
			Endpoint: "https://api.example.test/anthropic",
			APIKeys: map[string]string{
				"ANTHROPIC_AUTH_TOKEN": "inline-token",
			},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	settingsPath := filepath.Join(layout.ProviderHome, "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(raw), "inline-token") {
		t.Fatalf("expected inline token in settings, got %s", string(raw))
	}
	if previewEnv, ok := projection.Config["env"].(map[string]any); !ok || previewEnv["ANTHROPIC_AUTH_TOKEN"] != "[REDACTED]" {
		t.Fatalf("expected redacted preview env, got %#v", projection.Config["env"])
	}
}