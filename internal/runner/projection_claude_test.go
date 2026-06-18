package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectClaudeSettingsWritesEnvAndMaterializedCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "resolved-token-value")

	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	creds := credential.NewService(db)
	if _, err := creds.Upsert("anthropic-token", credential.ScopeGlobal, "", credential.Source{
		Kind:  credential.SourceEnv,
		Value: "ANTHROPIC_AUTH_TOKEN",
	}, false); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-claude", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model:          "glm-5.2",
			Endpoint:       "https://open.bigmodel.cn/api/anthropic",
			CredentialRefs: []string{"anthropic-token"},
			DefaultRunner:  "sandbox",
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:   "project-1",
		Credentials: creds,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	settingsPath := filepath.Join(layout.ProviderHome, "settings.json")
	if projection.ConfigPath != settingsPath {
		t.Fatalf("expected settings.json path, got %q", projection.ConfigPath)
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if settings.Env["ANTHROPIC_BASE_URL"] != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("expected base url in settings env, got %#v", settings.Env)
	}
	if settings.Env["ANTHROPIC_MODEL"] != "glm-5.2" {
		t.Fatalf("expected model in settings env, got %#v", settings.Env)
	}
	if settings.Env["ANTHROPIC_AUTH_TOKEN"] != "resolved-token-value" {
		t.Fatalf("expected materialized token in settings env, got %#v", settings.Env)
	}

	previewEnv, ok := projection.Config["env"].(map[string]any)
	if !ok {
		t.Fatalf("expected preview env map, got %#v", projection.Config["env"])
	}
	if previewEnv["ANTHROPIC_AUTH_TOKEN"] != "[REDACTED]" {
		t.Fatalf("expected redacted token in preview, got %#v", previewEnv["ANTHROPIC_AUTH_TOKEN"])
	}
}

func TestClaudeProcessEnvMaterializesInlineAPIKeys(t *testing.T) {
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model:    "glm-5.2",
			Endpoint: "https://open.bigmodel.cn/api/anthropic",
			APIKeys: map[string]string{
				"ANTHROPIC_AUTH_TOKEN": "zhipu-token",
			},
		},
	}

	env, err := runner.ClaudeProcessEnv(profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("claude process env: %v", err)
	}
	if env["ANTHROPIC_BASE_URL"] != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("expected zhipu base url, got %#v", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "zhipu-token" {
		t.Fatalf("expected materialized auth token, got %#v", env["ANTHROPIC_AUTH_TOKEN"])
	}
}

func TestLaunchConfigPathUsesContainerPathInSandbox(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-1", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	hostPath := filepath.Join(layout.ProviderHome, "settings.json")

	got := runner.LaunchConfigPath(layout, runtimeprofile.ProviderClaudeCode, hostPath, true)
	want := "/task/runtime-home/claude/settings.json"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}