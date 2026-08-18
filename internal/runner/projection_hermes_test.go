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
	"pentest/internal/skill"
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
	if !strings.Contains(config, "provider: \"custom:"+primary.ID+"\"\n") {
		t.Fatalf("expected model.provider custom:%s so Hermes stays on the named endpoint, got:\n%s", primary.ID, config)
	}
	if strings.Contains(config, "provider: custom\n") {
		t.Fatalf("bare custom provider ignores named api_key_env, got:\n%s", config)
	}
	foundKeyEnv := false
	for _, line := range strings.Split(config, "\n") {
		if strings.TrimSpace(line) == "key_env: "+primary.APIKeyEnv {
			foundKeyEnv = true
			break
		}
	}
	if !foundKeyEnv {
		t.Fatalf("Hermes providers dict reads key_env, not api_key_env, got:\n%s", config)
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

func TestProjectRuntimeConfigKeepsHermesSandboxSkillsLink(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "skill-source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("# Recon"), 0o600); err != nil {
		t.Fatalf("write skill doc: %v", err)
	}

	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	layout, err := runner.PrepareTaskLayout(root, "task-hermes-sandbox", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		Sandbox: true,
		SkillBundles: []skill.Bundle{{
			ID:   "recon-helper",
			Name: "Recon Helper",
			Path: sourceDir,
		}},
	}); err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	link := filepath.Join(layout.ProviderHome, "skills")
	if target, err := os.Readlink(link); err != nil || target != "/task/skills" {
		t.Fatalf("hermes provider skills link = %q, err = %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(layout.ProviderHome, "config.yaml")); err != nil {
		t.Fatalf("expected hermes config: %v", err)
	}
}

func TestProjectHermesHomeRaisesACPIterationBudget(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-hermes-budget", runtimeprofile.ProviderHermes)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}, runner.ProjectionRequest{}); err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	configRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	config := string(configRaw)
	if !strings.Contains(config, "agent:\n  max_turns: 100000\n") {
		t.Fatalf("expected agent.max_turns 100000 so a 6-hour Hosted Evaluation Run does not exhaust the Hermes ACP constructor default, got:\n%s", config)
	}
	if !strings.Contains(config, "delegation:\n  max_iterations: 100000\n") {
		t.Fatalf("expected delegation.max_iterations 100000, got:\n%s", config)
	}
	if !strings.Contains(config, "plugins:\n  enabled:\n    - cyberpenda-iteration-budget\n") {
		t.Fatalf("expected iteration-budget plugin enabled, got:\n%s", config)
	}

	pluginYAML := filepath.Join(layout.ProviderHome, "plugins", "cyberpenda-iteration-budget", "plugin.yaml")
	if _, err := os.Stat(pluginYAML); err != nil {
		t.Fatalf("expected projected Hermes plugin: %v", err)
	}
	pluginInit, err := os.ReadFile(filepath.Join(layout.ProviderHome, "plugins", "cyberpenda-iteration-budget", "__init__.py"))
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	body := string(pluginInit)
	if !strings.Contains(body, "max_turns") || !strings.Contains(body, "max_iterations") {
		t.Fatalf("plugin must apply agent.max_turns onto ACP max_iterations, got:\n%s", body)
	}
}

func TestProjectHermesHomeProjectsRequestedReasoningEffort(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-hermes-effort", runtimeprofile.ProviderHermes)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderHermes,
		Fields:   runtimeprofile.Fields{ReasoningEffort: "max"},
	}, runner.ProjectionRequest{}); err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	configRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	config := string(configRaw)
	if !strings.Contains(config, "  reasoning_effort: max\n") {
		t.Fatalf("expected agent.reasoning_effort max, got:\n%s", config)
	}

	pluginInit, err := os.ReadFile(filepath.Join(layout.ProviderHome, "plugins", "cyberpenda-iteration-budget", "__init__.py"))
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	body := string(pluginInit)
	if !strings.Contains(body, "reasoning_effort") || !strings.Contains(body, "reasoning_config") {
		t.Fatalf("plugin must apply agent.reasoning_effort onto ACP reasoning_config, got:\n%s", body)
	}
}

func TestProjectHermesHomeProjectsDefaultHighReasoningEffort(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-hermes-default-effort", runtimeprofile.ProviderHermes)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}, runner.ProjectionRequest{}); err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	configRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(configRaw), "  reasoning_effort: high\n") {
		t.Fatalf("empty Reasoning Effort must project high, got:\n%s", configRaw)
	}
}
