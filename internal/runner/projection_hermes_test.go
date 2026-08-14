package runner_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectHermesHomeIsolatesConfigAndProjectsLaunchReadyProviders(t *testing.T) {
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
			Manual:       []string{"gpt-primary"},
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
	if _, err := providers.Create(modelprovider.CreateRequest{
		Name:      "Draft Empty Catalog",
		BaseURL:   "https://draft.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{},
	}); err != nil {
		t.Fatalf("create draft: %v", err)
	}

	t.Setenv(primary.APIKeyEnv, "sk-primary-secret")
	t.Setenv(alternate.APIKeyEnv, "sk-alternate-secret")

	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	if err := os.MkdirAll(filepath.Join(hostHome, ".hermes"), 0o700); err != nil {
		t.Fatalf("host hermes dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hostHome, ".hermes", "config.yaml"), []byte("terminal:\n  backend: docker\n"), 0o600); err != nil {
		t.Fatalf("write host hermes config: %v", err)
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-hermes", runtimeprofile.ProviderHermes)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if layout.ProviderHome != filepath.Join(layout.RuntimeHome, "hermes") {
		t.Fatalf("provider home = %q", layout.ProviderHome)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderHermes,
		Fields: runtimeprofile.Fields{
			ModelProviderID: primary.ID,
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ModelProviders: providers,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	configPath := filepath.Join(layout.ProviderHome, "config.yaml")
	if projection.ConfigPath != configPath {
		t.Fatalf("config path = %q, want %q", projection.ConfigPath, configPath)
	}
	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	config := string(configRaw)
	if strings.Contains(config, "backend: docker") {
		t.Fatal("projected host ~/.hermes config")
	}
	if !strings.Contains(config, "backend: local") {
		t.Fatalf("expected terminal.backend local, got:\n%s", config)
	}
	if !strings.Contains(config, "mode: off") {
		t.Fatalf("expected approvals.mode off, got:\n%s", config)
	}
	if strings.Contains(config, "sk-primary-secret") || strings.Contains(config, "sk-alternate-secret") {
		t.Fatalf("config.yaml leaked API key:\n%s", config)
	}
	if !strings.Contains(config, "https://primary.example.test/v1") {
		t.Fatalf("missing selected endpoint:\n%s", config)
	}
	if !strings.Contains(config, alternate.ID) {
		t.Fatalf("missing alternate provider in Global Model Projection:\n%s", config)
	}

	envRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	env := string(envRaw)
	if !strings.Contains(env, primary.APIKeyEnv+"=sk-primary-secret") {
		t.Fatalf("missing primary key in .env:\n%s", env)
	}
	if !strings.Contains(env, alternate.APIKeyEnv+"=sk-alternate-secret") {
		t.Fatalf("missing alternate key in .env:\n%s", env)
	}

	ids, ok := projection.Config["projected_model_provider_ids"].([]string)
	if !ok {
		t.Fatalf("projected_model_provider_ids = %#v", projection.Config["projected_model_provider_ids"])
	}
	if len(ids) != 2 {
		t.Fatalf("projected ids = %#v", ids)
	}
	if strings.Contains(fmt.Sprint(projection.Config), "sk-primary-secret") ||
		strings.Contains(fmt.Sprint(projection.Config), "sk-alternate-secret") {
		t.Fatalf("preview leaked secret: %#v", projection.Config)
	}
}
