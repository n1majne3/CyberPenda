package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/owner"
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
		Owner:       owner.NewTaskContract("task-codex", "project-1", layout.Workdir),
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
		`approval_policy = "never"`,
		`sandbox_mode = "danger-full-access"`,
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

func TestProjectCodexConfigWritesResolvedModelLimits(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-codex-limits", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"gpt-strong": {ContextWindow: 272000, MaxOutputTokens: 128000},
	}, "", nil)
	if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields:   runtimeprofile.Fields{Model: "gpt-strong"},
	}, runner.ProjectionRequest{CapabilityCache: cache}); err != nil {
		t.Fatalf("project: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(raw)
	if !strings.Contains(config, "model_context_window = 272000") || !strings.Contains(config, "model_max_output_tokens = 128000") {
		t.Fatalf("missing resolved limits:\n%s", config)
	}
	catalogPath := filepath.Join(layout.ProviderHome, "model_catalog.json")
	if !strings.Contains(config, "model_catalog_json = \""+catalogPath+"\"") {
		t.Fatalf("missing model_catalog_json:\n%s", config)
	}
	rawCatalog, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read model catalog: %v", err)
	}
	var catalog struct {
		Models []struct {
			Slug                     string `json:"slug"`
			DisplayName              string `json:"display_name"`
			BaseInstructions         string `json:"base_instructions"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			SupportedReasoningLevels []struct {
				Effort      string `json:"effort"`
				Description string `json:"description"`
			} `json:"supported_reasoning_levels"`
			ShellType               string `json:"shell_type"`
			Visibility              string `json:"visibility"`
			SupportedInAPI          bool   `json:"supported_in_api"`
			Priority                int    `json:"priority"`
			SupportVerbosity        *bool  `json:"support_verbosity"`
			DefaultVerbosity        string `json:"default_verbosity"`
			ApplyPatchToolType      string `json:"apply_patch_tool_type"`
			WebSearchToolType       string `json:"web_search_tool_type"`
			SupportsImageDetailOrig *bool  `json:"supports_image_detail_original"`
			ContextWindow           int    `json:"context_window"`
			MaxContextWindow        int    `json:"max_context_window"`
			TruncationPolicy        *struct {
				Mode  string `json:"mode"`
				Limit int    `json:"limit"`
			} `json:"truncation_policy"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rawCatalog, &catalog); err != nil {
		t.Fatalf("parse model catalog: %v\n%s", err, rawCatalog)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("models = %#v", catalog.Models)
	}
	model := catalog.Models[0]
	if model.Slug != "gpt-strong" || model.ContextWindow != 272000 || model.MaxContextWindow != 272000 {
		t.Fatalf("model = %#v", model)
	}
	if model.BaseInstructions == "" || model.DefaultReasoningLevel != "high" || model.ShellType != "shell_command" || !model.SupportedInAPI {
		t.Fatalf("required identity fields = %#v", model)
	}
	if model.SupportVerbosity == nil || model.DefaultVerbosity == "" || model.ApplyPatchToolType == "" || model.WebSearchToolType == "" {
		t.Fatalf("missing Codex ModelInfo required fields: %#v", model)
	}
	if model.TruncationPolicy == nil || model.TruncationPolicy.Mode == "" || model.TruncationPolicy.Limit < 1 {
		t.Fatalf("missing truncation_policy: %#v", model.TruncationPolicy)
	}
	gotEfforts := make([]string, 0, len(model.SupportedReasoningLevels))
	for _, level := range model.SupportedReasoningLevels {
		if strings.TrimSpace(level.Effort) == "" || strings.TrimSpace(level.Description) == "" {
			t.Fatalf("incomplete reasoning level %#v", level)
		}
		gotEfforts = append(gotEfforts, level.Effort)
	}
	wantEfforts := []string{"low", "medium", "high", "xhigh", "max"}
	if len(gotEfforts) != len(wantEfforts) {
		t.Fatalf("supported_reasoning_levels = %#v, want %v", gotEfforts, wantEfforts)
	}
	for i := range wantEfforts {
		if gotEfforts[i] != wantEfforts[i] {
			t.Fatalf("supported_reasoning_levels = %#v, want %v", gotEfforts, wantEfforts)
		}
	}
}

func TestProjectCodexConfigUsesSandboxModelCatalogPath(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-codex-sandbox-catalog", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"x-preview-f-free": {ContextWindow: 1000000, MaxOutputTokens: 131072},
	}, "", nil)
	if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields:   runtimeprofile.Fields{Model: "x-preview-f-free"},
	}, runner.ProjectionRequest{Sandbox: true, CapabilityCache: cache}); err != nil {
		t.Fatalf("project: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(raw)
	if !strings.Contains(config, `model_catalog_json = "/task/runtime-home/codex/model_catalog.json"`) {
		t.Fatalf("expected sandbox catalog path:\n%s", config)
	}
}
