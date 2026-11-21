package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/modeskill"
	"pentest/internal/owner"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestProjectPiModelsDeclareReasoningCapability(t *testing.T) {
	assertReasoningEntries := func(t *testing.T, modelsPath, providerID string) {
		t.Helper()
		modelsRaw, err := os.ReadFile(modelsPath)
		if err != nil {
			t.Fatalf("read models.json: %v", err)
		}
		var doc struct {
			Providers map[string]struct {
				Models []struct {
					ID               string         `json:"id"`
					Reasoning        bool           `json:"reasoning"`
					ThinkingLevelMap map[string]any `json:"thinkingLevelMap"`
					Compat           map[string]any `json:"compat"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(modelsRaw, &doc); err != nil {
			t.Fatalf("decode models.json: %v", err)
		}
		provider, ok := doc.Providers[providerID]
		if !ok {
			t.Fatalf("missing provider %q in %#v", providerID, doc.Providers)
		}
		if len(provider.Models) == 0 {
			t.Fatalf("provider %q has no models", providerID)
		}
		for _, model := range provider.Models {
			if !model.Reasoning {
				t.Fatalf("model %q must declare reasoning:true: %#v", model.ID, model)
			}
			if model.ThinkingLevelMap["xhigh"] != "xhigh" || model.ThinkingLevelMap["max"] != "max" {
				t.Fatalf("model %q must identity-map xhigh/max: %#v", model.ID, model.ThinkingLevelMap)
			}
			// CyberPenda-configured gateways are OpenAI-compatible services,
			// not OpenAI itself: Pi must send the system prompt with the
			// classic "system" role. Its default detection pairs
			// reasoning:true with the OpenAI-only "developer" role, which
			// gateways such as Kimi reject with HTTP 400.
			if model.Compat["supportsDeveloperRole"] != false {
				t.Fatalf("model %q must pin compat.supportsDeveloperRole:false: %#v", model.ID, model.Compat)
			}
		}
	}

	t.Run("Global Model Projection", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		providers := modelprovider.NewService(db)
		primary, err := providers.Create(modelprovider.CreateRequest{
			Name:    "Reasoning Gateway",
			BaseURL: "https://reasoning.example.test/v1",
			Protocols: []modelprovider.Protocol{
				modelprovider.ProtocolOpenAIChatCompletions,
			},
			Catalog: modelprovider.Catalog{
				Manual:       []string{"gpt-reasoning", "gpt-reasoning-large"},
				DefaultModel: "gpt-reasoning",
			},
		})
		if err != nil {
			t.Fatalf("create primary: %v", err)
		}
		t.Setenv(primary.APIKeyEnv, "sk-reasoning-test")

		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-reasoning", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				ModelProviderID: primary.ID,
			},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
			BlackboardMode: modeskill.ModeDisabled,
			ModelProviders: providers,
		}); err != nil {
			t.Fatalf("project config: %v", err)
		}
		assertReasoningEntries(t, filepath.Join(layout.ProviderHome, "agent", "models.json"), primary.ID)
	})

	t.Run("legacy single provider", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
		isolatePiHostHome(t)
		db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		creds := credential.NewService(db)
		if _, err := creds.Upsert("anthropic-key", credential.ScopeGlobal, "", credential.Source{
			Kind:  credential.SourceEnv,
			Value: "ANTHROPIC_API_KEY",
		}, false); err != nil {
			t.Fatalf("upsert binding: %v", err)
		}

		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-legacy", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				Model:          "claude-sonnet-4",
				Endpoint:       "https://proxy.example.test/anthropic",
				CredentialRefs: []string{"anthropic-key"},
			},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
			BlackboardMode: modeskill.ModeDisabled,
			Owner:          owner.NewTaskContract("task-pi-legacy", "project-1", layout.Workdir),
			Credentials:    creds,
		}); err != nil {
			t.Fatalf("project config: %v", err)
		}
		assertReasoningEntries(t, filepath.Join(layout.ProviderHome, "agent", "models.json"), "custom")
	})
}

func TestProjectPiModelsWriteResolvedContextWindowAndMaxTokens(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	created, err := providers.Create(modelprovider.CreateRequest{
		Name:    "Limits Gateway",
		BaseURL: "https://limits.example.test/v1",
		Protocols: []modelprovider.Protocol{
			modelprovider.ProtocolOpenAIChatCompletions,
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"cached-model", "override-model"},
			DefaultModel: "cached-model",
			Limits: map[string]modelprovider.CatalogLimits{
				"override-model": {ContextWindow: 999999, MaxOutputTokens: 1111},
			},
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(created.APIKeyEnv, "sk-limits")
	cache := modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
		"cached-model": {ContextWindow: 200000, MaxOutputTokens: 32000},
	}, "", nil)
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-limits", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields:   runtimeprofile.Fields{ModelProviderID: created.ID},
	}, runner.ProjectionRequest{
		BlackboardMode:  modeskill.ModeDisabled,
		ModelProviders:  providers,
		CapabilityCache: cache,
	}); err != nil {
		t.Fatalf("project: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			Models []struct {
				ID            string `json:"id"`
				ContextWindow int    `json:"contextWindow"`
				MaxTokens     int    `json:"maxTokens"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]struct {
		ContextWindow int
		MaxTokens     int
	}{}
	for _, model := range doc.Providers[created.ID].Models {
		byID[model.ID] = struct {
			ContextWindow int
			MaxTokens     int
		}{model.ContextWindow, model.MaxTokens}
	}
	if byID["cached-model"].ContextWindow != 200000 || byID["cached-model"].MaxTokens != 32000 {
		t.Fatalf("cache model = %#v", byID["cached-model"])
	}
	if byID["override-model"].ContextWindow != 999999 || byID["override-model"].MaxTokens != 1111 {
		t.Fatalf("override model = %#v", byID["override-model"])
	}
}

// TestProjectPiConfigWritesExecuteSubagentType proves Config Projection
// seeds the pi-subagents custom agent dir with the CyberPenda Execute type,
// so orchestrator dispatch can target subagent_type "execute" instead of the
// generic general-purpose clone.
func TestProjectPiConfigWritesExecuteSubagentType(t *testing.T) {
	isolatePiHostHome(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	created, err := providers.Create(modelprovider.CreateRequest{
		Name:    "Execute Gateway",
		BaseURL: "https://execute.example.test/v1",
		Protocols: []modelprovider.Protocol{
			modelprovider.ProtocolOpenAIChatCompletions,
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"exec-model"},
			DefaultModel: "exec-model",
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(created.APIKeyEnv, "sk-execute-test")

	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-execute", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			ModelProviderID: created.ID,
		},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		BlackboardMode: modeskill.ModeDisabled,
		ModelProviders: providers,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	agentPath := filepath.Join(layout.ProviderHome, "agent", "agents", "execute.md")
	raw, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatalf("read projected execute agent type: %v", err)
	}
	content := string(raw)
	for _, required := range []string{
		"name: execute",
		// append mode keeps pi's tool-contract prompt; replace would drop it.
		"prompt_mode: append",
		"90 秒",
		"fact 骨架",
		"勿再试",
		"tmux",
		"已收束于 fact_{NNN}",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("projected execute agent type missing %q", required)
		}
	}
	if strings.Contains(content, "tsecbench") {
		t.Error("projected execute agent type must not bind to the TSecBench client")
	}
	info, err := os.Stat(agentPath)
	if err != nil {
		t.Fatalf("stat execute agent type: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("execute agent type mode = %v, want 0600", info.Mode().Perm())
	}
	types, ok := projection.Config["subagent_types"].([]string)
	if !ok || len(types) != 1 || types[0] != "execute" {
		t.Fatalf("expected subagent_types preview [execute], got %#v", projection.Config["subagent_types"])
	}
}

// TestProjectPiSettingsPinDefaultThinkingLevel locks the Subagent Reasoning
// Effort contract: pi-subagents child sessions inherit settings.json
// defaultThinkingLevel, not the main session's runtime-set thinking level, so
// the launch-resolved Requested Reasoning Effort must be pinned there. The
// Launch Reasoning Effort Override beats the Runtime Profile default, and both
// beat a host ~/.pi/agent/settings.json defaultThinkingLevel.
func TestProjectPiSettingsPinDefaultThinkingLevel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")

	t.Run("profile default overrides host defaultThinkingLevel", func(t *testing.T) {
		hostHome := isolatePiHostHome(t)
		hostPiDir := filepath.Join(hostHome, ".pi", "agent")
		if err := os.MkdirAll(hostPiDir, 0o700); err != nil {
			t.Fatalf("mkdir host pi agent dir: %v", err)
		}
		hostSettings := `{"defaultThinkingLevel":"high","theme":"dark"}`
		if err := os.WriteFile(filepath.Join(hostPiDir, "settings.json"), []byte(hostSettings), 0o600); err != nil {
			t.Fatalf("write host settings.json: %v", err)
		}

		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-effort", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields:   runtimeprofile.Fields{Model: "DeepSeek-V4-Pro", ReasoningEffort: "max"},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{BlackboardMode: modeskill.ModeDisabled}); err != nil {
			t.Fatalf("project config: %v", err)
		}

		settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
		if got := settings["defaultThinkingLevel"]; got != "max" {
			t.Fatalf("defaultThinkingLevel = %#v, want max", got)
		}
		if _, ok := settings["theme"]; ok {
			t.Fatalf("host theme must not leak into projected settings.json: %#v", settings)
		}
	})

	t.Run("launch override beats profile default", func(t *testing.T) {
		isolatePiHostHome(t)
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-effort-override", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields:   runtimeprofile.Fields{Model: "DeepSeek-V4-Pro", ReasoningEffort: "max"},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
			BlackboardMode:           modeskill.ModeDisabled,
			RequestedReasoningEffort: "low",
		}); err != nil {
			t.Fatalf("project config: %v", err)
		}

		settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
		if got := settings["defaultThinkingLevel"]; got != "low" {
			t.Fatalf("defaultThinkingLevel = %#v, want low", got)
		}
	})

	t.Run("no effort configured resolves to high", func(t *testing.T) {
		isolatePiHostHome(t)
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-effort-default", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields:   runtimeprofile.Fields{Model: "DeepSeek-V4-Pro"},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{BlackboardMode: modeskill.ModeDisabled}); err != nil {
			t.Fatalf("project config: %v", err)
		}

		settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
		if got := settings["defaultThinkingLevel"]; got != "high" {
			t.Fatalf("defaultThinkingLevel = %#v, want high", got)
		}
	})

	t.Run("invalid launch override fails clearly", func(t *testing.T) {
		isolatePiHostHome(t)
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-effort-invalid", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields:   runtimeprofile.Fields{Model: "DeepSeek-V4-Pro"},
		}
		_, err = runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
			BlackboardMode:           modeskill.ModeDisabled,
			RequestedReasoningEffort: "ultra",
		})
		if err == nil {
			t.Fatal("expected invalid Requested Reasoning Effort to fail projection")
		}
	})
}

func TestProjectPiSettingsPinsLaunchProviderAndModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")

	t.Run("launch provider and model pinned", func(t *testing.T) {
		isolatePiHostHome(t)
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-pin", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				Model: "deepseek-flash",
				Env:   map[string]string{"PI_PROVIDER_ID": "hub"},
			},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{BlackboardMode: modeskill.ModeDisabled}); err != nil {
			t.Fatalf("project config: %v", err)
		}
		settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
		if got := settings["defaultProvider"]; got != "hub" {
			t.Fatalf("defaultProvider = %#v, want hub", got)
		}
		if got := settings["defaultModel"]; got != "deepseek-flash" {
			t.Fatalf("defaultModel = %#v, want deepseek-flash", got)
		}
	})

	t.Run("launch model override wins", func(t *testing.T) {
		isolatePiHostHome(t)
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-pin-override", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				Model: "deepseek-flash",
				Env:   map[string]string{"PI_PROVIDER_ID": "hub"},
			},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
			BlackboardMode:      modeskill.ModeDisabled,
			LaunchModelOverride: "deepseek-v4-pro",
		}); err != nil {
			t.Fatalf("project config: %v", err)
		}
		settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
		if got := settings["defaultModel"]; got != "deepseek-v4-pro" {
			t.Fatalf("defaultModel = %#v, want launch override deepseek-v4-pro", got)
		}
	})

	t.Run("no provider configuration pins no defaultProvider", func(t *testing.T) {
		isolatePiHostHome(t)
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pi-pin-none", runtimeprofile.ProviderPi)
		if err != nil {
			t.Fatalf("prepare layout: %v", err)
		}
		profile := runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields:   runtimeprofile.Fields{Model: "deepseek-flash"},
		}
		if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{BlackboardMode: modeskill.ModeDisabled}); err != nil {
			t.Fatalf("project config: %v", err)
		}
		settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
		if _, ok := settings["defaultProvider"]; ok {
			t.Fatalf("defaultProvider must stay unset without provider configuration: %#v", settings)
		}
		if got := settings["defaultModel"]; got != "deepseek-flash" {
			t.Fatalf("defaultModel = %#v, want deepseek-flash", got)
		}
	})
}
