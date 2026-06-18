package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectCodexConfigWritesConfigTomlAndAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-openai-key")

	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	creds := credential.NewService(db)
	if _, err := creds.Upsert("codex-api-key", credential.ScopeGlobal, "", credential.Source{
		Kind:  credential.SourceEnv,
		Value: "OPENAI_API_KEY",
	}, false); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-codex", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			Model:          "gpt-5.5",
			Endpoint:       "https://proxy.example.test/v1",
			CredentialRefs: []string{"codex-api-key"},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:   "project-1",
		Credentials: creds,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	configPath := filepath.Join(layout.ProviderHome, "config.toml")
	if projection.ConfigPath != configPath {
		t.Fatalf("expected config.toml path, got %q", projection.ConfigPath)
	}

	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	config := string(configRaw)
	for _, want := range []string{
		`model = "gpt-5.5"`,
		`model_provider = "custom"`,
		`base_url = "https://proxy.example.test/v1"`,
		`wire_api = "responses"`,
		`requires_openai_auth = true`,
		`cli_auth_credentials_store = "file"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("expected config.toml to contain %q, got:\n%s", want, config)
		}
	}

	authPath := filepath.Join(layout.ProviderHome, "auth.json")
	authRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	if !strings.Contains(string(authRaw), "sk-test-openai-key") {
		t.Fatalf("expected materialized api key in auth.json, got %s", string(authRaw))
	}

	authPreview, ok := projection.Config["auth_json"].(map[string]any)
	if !ok {
		t.Fatalf("expected auth_json preview, got %#v", projection.Config["auth_json"])
	}
	if authPreview["OPENAI_API_KEY"] != "[REDACTED]" {
		t.Fatalf("expected redacted auth preview, got %#v", authPreview["OPENAI_API_KEY"])
	}
}

func TestLaunchConfigPathUsesContainerPathForCodex(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-1", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	hostPath := filepath.Join(layout.ProviderHome, "config.toml")

	got := runner.LaunchConfigPath(layout, runtimeprofile.ProviderCodex, hostPath, true)
	want := "/task/runtime-home/codex/config.toml"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}