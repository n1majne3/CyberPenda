package hostedcontroller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/hostedcontroller"
	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
)

func TestHostedModelLimitsReachRuntimeConfig(t *testing.T) {
	for _, tc := range []struct{ runtime, protocol string }{
		{"claude_code", "anthropic_messages"},
		{"pi", "anthropic_messages"},
		{"pi", "openai_chat_completions"},
		{"pi", "openai_responses"},
	} {
		t.Run(tc.runtime+"/"+tc.protocol, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			providers := modelprovider.NewService(db)
			var profile runtimeprofile.Profile
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				response := httptest.NewRecorder()
				response.Header().Set("Content-Type", "application/json")
				switch req.Method + " " + req.URL.Path {
				case "POST /api/model-providers":
					var input modelprovider.CreateRequest
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					limits := input.Catalog.Limits["hosted-custom-model"]
					if limits.ContextWindow != 1048576 || limits.MaxOutputTokens != 393216 {
						t.Fatalf("catalog limits = %+v", limits)
					}
					provider, err := providers.Create(input)
					if err != nil {
						t.Fatal(err)
					}
					t.Setenv(provider.APIKeyEnv, "test-model-key")
					_ = json.NewEncoder(response).Encode(provider)
				case "POST /api/runtime-profiles":
					if err := json.NewDecoder(req.Body).Decode(&profile); err != nil {
						t.Fatal(err)
					}
					_, _ = io.WriteString(response, `{"id":"profile-1"}`)
				case "POST /api/projects":
					_, _ = io.WriteString(response, `{"id":"project-1"}`)
				case "POST /api/projects/project-1/tasks":
					_, _ = io.WriteString(response, `{"id":"task-1"}`)
				case "PUT /api/skills/ctf-orchestrator", "PUT /api/projects/project-1/credential-bindings":
					_, _ = io.WriteString(response, `{}`)
				default:
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
				}
				return response.Result(), nil
			})}
			env := validHostedEnv()
			env["CYBERPENDA_RUNTIME"] = tc.runtime
			env["CYBERPENDA_MODEL_PROTOCOL"] = tc.protocol
			env["CYBERPENDA_MODEL"] = "hosted-custom-model"
			env["CYBERPENDA_CONTEXT_WINDOW"] = "1048576"
			env["CYBERPENDA_MAX_OUTPUT_TOKENS"] = "393216"
			env["CYBERPENDA_AUTO_COMPACT_WINDOW"] = "524288"
			env["CYBERPENDA_AUTO_COMPACT_THRESHOLD"] = "80"
			config, err := hostedcontroller.ConfigFromEnv(env)
			if err != nil {
				t.Fatal(err)
			}
			app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", Client: client})
			if _, err := app.Start(context.Background(), hostedcontroller.EvaluationForConfig(config)); err != nil {
				t.Fatal(err)
			}
			// Credential delivery is tested separately. This test checks non-secret model configuration.
			profile.Fields.CredentialRefs = nil
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-limits", profile.Provider)
			if err != nil {
				t.Fatal(err)
			}
			request := runner.ProjectionRequest{ModelProviders: providers, CapabilityCache: modelprovider.NewCapabilityCache(map[string]modelprovider.CatalogLimits{
				"hosted-custom-model": {ContextWindow: 200000, MaxOutputTokens: 32000},
			}, "", nil)}
			projection, err := runner.ProjectRuntimeConfig(layout, profile, request)
			if err != nil {
				t.Fatal(err)
			}
			request.ModelSnapshot = projection.ModelSnapshot
			if tc.runtime == "claude_code" {
				processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, projection.ResolvedProfile, false, runner.RuntimeOwnerContext{}, request)
				if err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "1048576", "CLAUDE_CODE_MAX_OUTPUT_TOKENS": "393216", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "524288", "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "80"} {
					if processEnv[key] != want {
						t.Errorf("%s = %q, want %q", key, processEnv[key], want)
					}
				}
			} else {
				for _, key := range []string{"CLAUDE_CODE_MAX_OUTPUT_TOKENS", "CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"} {
					if _, ok := profile.Fields.Env[key]; ok {
						t.Errorf("Pi received Claude setting %s", key)
					}
				}
				raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "models.json"))
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Providers map[string]struct {
						Models []struct {
							ID            string
							ContextWindow int
							MaxTokens     int
						}
					}
				}
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				models := doc.Providers[profile.Fields.ModelProviderID].Models
				if len(models) != 1 || models[0].ID != "hosted-custom-model" || models[0].ContextWindow != 1048576 || models[0].MaxTokens != 393216 {
					t.Fatalf("Pi models = %+v", models)
				}
			}
		})
	}
}
