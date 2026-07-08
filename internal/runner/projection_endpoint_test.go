package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

// TestProjectRuntimeConfigWritesSelectedEndpointBaseURL locks the contract that
// Codex, Claude Code, and Pi runtime projections each receive the selected
// Model Provider Endpoint base URL as a runtime-consumed base URL. The daemon
// must not append operation suffixes such as messages, responses, or chat
// completions; runtimes remain responsible for adding their own. Each runtime
// selects the endpoint for its preferred protocol from a provider with split
// protocol paths.
func TestProjectRuntimeConfigWritesSelectedEndpointBaseURL(t *testing.T) {
	const (
		openaiBase    = "https://api.example.test/api/coding/paas/v4"
		anthropicBase = "https://api.example.test/api/anthropic"
	)
	operationSuffixes := []string{"/messages", "/responses", "/chat/completions"}

	cases := []struct {
		name          string
		provider      runtimeprofile.Provider
		wantBaseURL   string
		wantProtocol  modelprovider.Protocol
		assertWritten func(t *testing.T, layout runner.Layout)
	}{
		{
			name:         "codex openai responses endpoint",
			provider:     runtimeprofile.ProviderCodex,
			wantBaseURL:  openaiBase,
			wantProtocol: modelprovider.ProtocolOpenAIResponses,
			assertWritten: func(t *testing.T, layout runner.Layout) {
				raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "config.toml"))
				if err != nil {
					t.Fatalf("read config.toml: %v", err)
				}
				config := string(raw)
				// The selected OpenAI-family endpoint base URL must be written
				// verbatim, without a daemon-appended responses suffix.
				if !strings.Contains(config, `base_url = "`+openaiBase+`"`) {
					t.Fatalf("expected base_url %q in config.toml, got:\n%s", openaiBase, config)
				}
				for _, suffix := range operationSuffixes {
					if strings.Contains(config, `base_url = "`+openaiBase+suffix) {
						t.Fatalf("config.toml base_url has daemon-added operation suffix %q:\n%s", suffix, config)
					}
				}
			},
		},
		{
			name:         "claude code anthropic messages endpoint",
			provider:     runtimeprofile.ProviderClaudeCode,
			wantBaseURL:  anthropicBase,
			wantProtocol: modelprovider.ProtocolAnthropicMessages,
			assertWritten: func(t *testing.T, layout runner.Layout) {
				raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "settings.json"))
				if err != nil {
					t.Fatalf("read settings.json: %v", err)
				}
				var settings struct {
					Env map[string]string `json:"env"`
				}
				if err := json.Unmarshal(raw, &settings); err != nil {
					t.Fatalf("decode settings.json: %v", err)
				}
				// ANTHROPIC_BASE_URL is the runtime base URL before Claude Code
				// appends its own messages operation path.
				if got := settings.Env["ANTHROPIC_BASE_URL"]; got != anthropicBase {
					t.Fatalf("ANTHROPIC_BASE_URL = %q, want %q", got, anthropicBase)
				}
				for _, suffix := range operationSuffixes {
					if strings.Contains(settings.Env["ANTHROPIC_BASE_URL"], suffix) {
						t.Fatalf("ANTHROPIC_BASE_URL has daemon-added operation suffix %q: %q", suffix, settings.Env["ANTHROPIC_BASE_URL"])
					}
				}
			},
		},
		{
			name:         "pi prefers openai chat completions endpoint",
			provider:     runtimeprofile.ProviderPi,
			wantBaseURL:  openaiBase,
			wantProtocol: modelprovider.ProtocolOpenAIChatCompletions,
			assertWritten: func(t *testing.T, layout runner.Layout) {
				raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
				if err != nil {
					t.Fatalf("read models.json: %v", err)
				}
				var models struct {
					Providers map[string]struct {
						BaseURL string `json:"baseUrl"`
						API     string `json:"api"`
					} `json:"providers"`
				}
				if err := json.Unmarshal(raw, &models); err != nil {
					t.Fatalf("decode models.json: %v", err)
				}
				if len(models.Providers) != 1 {
					t.Fatalf("expected exactly one pi provider, got %#v", models.Providers)
				}
				var piBaseURL string
				for _, p := range models.Providers {
					piBaseURL = p.BaseURL
				}
				if piBaseURL != openaiBase {
					t.Fatalf("pi baseUrl = %q, want %q", piBaseURL, openaiBase)
				}
				for _, suffix := range operationSuffixes {
					if strings.Contains(piBaseURL, suffix) {
						t.Fatalf("pi baseUrl has daemon-added operation suffix %q: %q", suffix, piBaseURL)
					}
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open("")
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			providers := modelprovider.NewService(db)
			provider, err := providers.Create(modelprovider.CreateRequest{
				Name: "Split Path",
				Endpoints: []modelprovider.Endpoint{
					{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: openaiBase},
					{Protocol: modelprovider.ProtocolOpenAIChatCompletions, BaseURL: openaiBase},
					{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: anthropicBase},
				},
				Catalog: modelprovider.Catalog{Manual: []string{"glm"}, DefaultModel: "glm"},
			})
			if err != nil {
				t.Fatalf("create provider: %v", err)
			}
			t.Setenv(provider.APIKeyEnv, "sk-projection-secret")

			taskRoot := t.TempDir()
			layout, err := runner.PrepareTaskLayout(taskRoot, "task-"+string(tc.provider), tc.provider)
			if err != nil {
				t.Fatalf("prepare layout: %v", err)
			}
			profile := runtimeprofile.Profile{
				Provider: tc.provider,
				Fields:   runtimeprofile.Fields{ModelProviderID: provider.ID},
			}
			projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
				ModelProviders: providers,
			})
			if err != nil {
				t.Fatalf("project runtime config: %v", err)
			}

			if projection.ModelSnapshot == nil {
				t.Fatalf("expected model snapshot to be resolved")
			}
			if projection.ModelSnapshot.EndpointBaseURL != tc.wantBaseURL {
				t.Fatalf("snapshot endpoint_base_url = %q, want %q", projection.ModelSnapshot.EndpointBaseURL, tc.wantBaseURL)
			}
			if projection.ModelSnapshot.Protocol != tc.wantProtocol {
				t.Fatalf("snapshot protocol = %q, want %q", projection.ModelSnapshot.Protocol, tc.wantProtocol)
			}

			tc.assertWritten(t, layout)

			// The persisted model provider snapshot preview carries the selected
			// endpoint base URL alongside the transitional base_url alias.
			snapshot, ok := projection.Config["model_provider_snapshot"].(map[string]any)
			if !ok {
				t.Fatalf("expected model_provider_snapshot in config, got %#v", projection.Config["model_provider_snapshot"])
			}
			if snapshot["endpoint_base_url"] != tc.wantBaseURL {
				t.Fatalf("snapshot preview endpoint_base_url = %#v, want %q", snapshot["endpoint_base_url"], tc.wantBaseURL)
			}
			if snapshot["base_url"] != tc.wantBaseURL {
				t.Fatalf("snapshot preview base_url alias = %#v, want %q", snapshot["base_url"], tc.wantBaseURL)
			}
			if snapshot["protocol"] != string(tc.wantProtocol) {
				t.Fatalf("snapshot preview protocol = %#v, want %q", snapshot["protocol"], tc.wantProtocol)
			}
			// Snapshots are non-secret: no API key value leaks into the projection.
			encoded, err := json.Marshal(projection.Config)
			if err != nil {
				t.Fatalf("encode projection config: %v", err)
			}
			if strings.Contains(string(encoded), "sk-projection-secret") {
				t.Fatalf("projection config leaked API key value: %s", encoded)
			}
		})
	}
}

// TestModelProviderSnapshotCarriesEndpointProvenanceWithoutSecrets locks the
// Task Runtime Configuration snapshot contract: the model provider snapshot
// captures the selected endpoint_base_url alongside the transitional base_url
// alias, the selected protocol, model, and API key source provenance, while
// never storing API key values or full Model Catalog contents.
func TestModelProviderSnapshotCarriesEndpointProvenanceWithoutSecrets(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers := modelprovider.NewService(db)
	provider, err := providers.Create(modelprovider.CreateRequest{
		Name: "Split Path",
		Endpoints: []modelprovider.Endpoint{
			{Protocol: modelprovider.ProtocolOpenAIResponses, BaseURL: "https://api.example.test/api/coding/paas/v4"},
			{Protocol: modelprovider.ProtocolAnthropicMessages, BaseURL: "https://api.example.test/api/anthropic"},
		},
		Catalog: modelprovider.Catalog{
			Manual:       []string{"glm", "gpt"},
			DefaultModel: "glm",
		},
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-snapshot-secret")

	taskRoot := t.TempDir()
	layout, err := runner.PrepareTaskLayout(taskRoot, "task-claude", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields:   runtimeprofile.Fields{ModelProviderID: provider.ID},
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ModelProviders: providers,
	})
	if err != nil {
		t.Fatalf("project runtime config: %v", err)
	}
	if projection.ModelSnapshot == nil {
		t.Fatalf("expected model snapshot")
	}
	s := *projection.ModelSnapshot
	if s.EndpointBaseURL != "https://api.example.test/api/anthropic" {
		t.Fatalf("endpoint_base_url = %q", s.EndpointBaseURL)
	}
	if s.BaseURL != s.EndpointBaseURL {
		t.Fatalf("base_url alias = %q, want endpoint_base_url %q", s.BaseURL, s.EndpointBaseURL)
	}
	if s.Protocol != modelprovider.ProtocolAnthropicMessages {
		t.Fatalf("protocol = %q", s.Protocol)
	}
	if s.Model != "glm" {
		t.Fatalf("model = %q", s.Model)
	}
	if s.APIKeyEnv != provider.APIKeyEnv || s.APIKeySource == "" {
		t.Fatalf("api key source provenance missing: env=%q source=%q", s.APIKeyEnv, s.APIKeySource)
	}

	// The snapshot is non-secret and must not carry the full Model Catalog.
	snapshot, ok := projection.Config["model_provider_snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("expected model_provider_snapshot in config, got %#v", projection.Config["model_provider_snapshot"])
	}
	if _, present := snapshot["catalog"]; present {
		t.Fatalf("snapshot must not store full catalog contents: %#v", snapshot["catalog"])
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	if strings.Contains(string(encoded), "sk-snapshot-secret") {
		t.Fatalf("snapshot leaked API key value: %s", encoded)
	}
	// The non-default catalog entry must not leak into the snapshot.
	if strings.Contains(string(encoded), `"gpt"`) {
		t.Fatalf("snapshot leaked full catalog model list: %s", encoded)
	}
}
