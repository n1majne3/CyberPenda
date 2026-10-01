package runner_test

import (
	"context"
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
	"pentest/internal/skill"
	"pentest/internal/store"
)

func TestPreparedConfigProjectionKeepsNativeCredentialsAndLimits(t *testing.T) {
	for _, providerKind := range []runtimeprofile.Provider{runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderCodex, runtimeprofile.ProviderPi} {
		for _, v2 := range []bool{false, true} {
			t.Run(string(providerKind)+"/v2="+map[bool]string{false: "false", true: "true"}[v2], func(t *testing.T) {
				db, err := store.Open("")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				providers := modelprovider.NewService(db)
				protocol := modelprovider.ProtocolOpenAIResponses
				if providerKind == runtimeprofile.ProviderClaudeCode {
					protocol = modelprovider.ProtocolAnthropicMessages
				}
				provider, err := providers.Create(modelprovider.CreateRequest{
					Name: "Native", BaseURL: "https://native.example.test/v1",
					Protocols: []modelprovider.Protocol{protocol},
					Catalog:   modelprovider.Catalog{Manual: []string{"native-model"}, DefaultModel: "native-model"},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv(provider.APIKeyEnv, "captured-native-key")
				global := runner.CloneGlobalModelProviderSnapshot([]modelprovider.Provider{provider})
				global.Providers[0].Catalog.Limits = map[string]modelprovider.CatalogLimits{
					"native-model": {ContextWindow: 120000, MaxOutputTokens: 8000},
				}
				profile := runtimeprofile.Profile{Provider: providerKind, Fields: runtimeprofile.Fields{
					ModelProviderID: provider.ID, Env: map[string]string{"FIXED_SETTING": "captured"},
				}}
				layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), "owner", providerKind)
				if err != nil {
					t.Fatal(err)
				}
				prepared, err := runner.PrepareConfigProjection(profile, runner.ConfigProjectionInput{
					ProjectionRequest: runner.ProjectionRequest{
						Owner:          owner.NewTaskContract("owner", "project", layout.Workdir),
						ModelProviders: providers, GlobalModelProviderSnapshot: global,
						DaemonAddr: "127.0.0.1:8080", BlackboardProtocol: "fgs",
					},
					BlackboardV2: v2,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv(provider.APIKeyEnv, "changed-native-key")
				global.Providers[0].Catalog.Limits["native-model"] = modelprovider.CatalogLimits{ContextWindow: 999}
				credentials := prepared.Credentials()
				credentials[provider.APIKeyEnv] = "changed-copy"
				prepared.Profile().Fields.Env["FIXED_SETTING"] = "changed-copy"
				prepared.ModelSnapshot().Model = "changed-copy"
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				for _, grant := range []string{"first-grant", "second-grant"} {
					projection, env, err := prepared.Render(layout, runner.ProjectionBinding{
						InterfaceToken: grant, ContinuationID: grant + "-continuation",
						WorkingGraphRoot: "graph-root", WorkingGraphOutbox: "graph-outbox", WorkingGraphReceipts: "graph-receipts",
					})
					if err != nil {
						t.Fatal(err)
					}
					if env[provider.APIKeyEnv] != "captured-native-key" || env["FIXED_SETTING"] != "captured" {
						t.Fatalf("captured environment changed: %#v", env)
					}
					if projection.ResolvedProfile.Fields.Model != "native-model" || projection.ModelSnapshot.Model != "native-model" {
						t.Fatalf("captured model changed: %#v", projection.ModelSnapshot)
					}
					if env["PENTEST_INTERFACE_TOKEN"] != grant || env["PENTEST_CONTINUATION_ID"] != grant+"-continuation" || env["PENTEST_WORKING_GRAPH_OUTBOX"] != "graph-outbox" {
						t.Fatalf("late binding did not reach the process environment: %#v", env)
					}
					switch providerKind {
					case runtimeprofile.ProviderClaudeCode:
						settings := readJSONFile(t, projection.ConfigPath)
						native := settings["env"].(map[string]any)
						if native[provider.APIKeyEnv] != "captured-native-key" || native["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "120000" {
							t.Fatalf("Claude native capture changed: %#v", native)
						}
					case runtimeprofile.ProviderCodex:
						auth := readJSONFile(t, filepath.Join(layout.ProviderHome, "auth.json"))
						if auth["OPENAI_API_KEY"] != "captured-native-key" {
							t.Fatalf("Codex native capture changed: %#v", auth)
						}
					case runtimeprofile.ProviderPi:
						raw, err := os.ReadFile(projection.ConfigPath)
						if err != nil || !strings.Contains(string(raw), `"contextWindow": 120000`) {
							t.Fatalf("Pi captured limits changed: %s, %v", raw, err)
						}
					}
					projection.ResolvedProfile.Fields.Env["FIXED_SETTING"] = "changed-result"
					projection.ModelSnapshot.Model = "changed-result"
				}
			})
		}
	}
}

func TestPreparedConfigProjectionKeepsWriteErrorsAndPartialFiles(t *testing.T) {
	for _, kind := range []owner.Kind{owner.KindTask, owner.KindSession} {
		t.Run(string(kind), func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "resume-owner", runtimeprofile.ProviderClaudeCode)
			if err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(layout.ProviderHome, "settings.json")
			if err := os.MkdirAll(settings, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(settings, "pre-existing-resume-state")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			contract := owner.NewTaskContract("resume-owner", "project", layout.Workdir)
			if kind == owner.KindSession {
				contract = owner.NewSessionContract("resume-owner", layout.Workdir)
			}
			profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
			req := runner.ProjectionRequest{Owner: contract}
			prepared, err := runner.PrepareConfigProjection(profile, runner.ConfigProjectionInput{ProjectionRequest: req})
			if err != nil {
				t.Fatal(err)
			}
			projection, env, renderErr := prepared.Render(layout, runner.ProjectionBinding{})
			if renderErr == nil || projection.ConfigPath != "" || env != nil {
				t.Fatalf("expected a failed projection, got %#v, %#v, %v", projection, env, renderErr)
			}
			if _, err := os.Stat(filepath.Join(layout.Workdir, ".claude", "agents", "execute.md")); err != nil {
				t.Fatalf("earlier projected file was removed: %v", err)
			}
			raw, err := os.ReadFile(sentinel)
			if err != nil || string(raw) != "keep" {
				t.Fatalf("pre-existing Resume state changed: %q, %v", raw, err)
			}
			_, directErr := runner.ProjectRuntimeConfig(layout, profile, req)
			if directErr == nil || renderErr.Error() != directErr.Error() {
				t.Fatalf("write error detail changed: prepared=%v, direct=%v", renderErr, directErr)
			}
		})
	}
}

func TestPreparedConfigProjectionKeepsEmptyCredentialCapture(t *testing.T) {
	t.Setenv("PREPARED_TEST_API_KEY", "live-key")
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	req := runner.ProjectionRequest{
		ModelSnapshot:           &modelprovider.Snapshot{APIKeyEnv: "PREPARED_TEST_API_KEY"},
		MaterializedCredentials: map[string]string{},
	}
	_, directErr := runner.MaterializeLaunchCredentials(profile, req)
	_, err := runner.PrepareConfigProjection(profile, runner.ConfigProjectionInput{ProjectionRequest: req})
	if directErr == nil || err == nil || err.Error() != directErr.Error() {
		t.Fatalf("empty capture must not use the live environment: prepared=%v, direct=%v", err, directErr)
	}
}

func TestPreparedConfigProjectionKeepsEmptySkillSelection(t *testing.T) {
	db, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	skills := skill.NewService(db, t.TempDir())
	if _, err := skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "default-skill", Name: "Default Skill"},
		Files:    map[string]string{"SKILL.md": "# Default Skill"},
	}); err != nil {
		t.Fatal(err)
	}
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "empty-skills-owner", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := runner.PrepareConfigProjection(
		runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode},
		runner.ConfigProjectionInput{
			ProjectionRequest: runner.ProjectionRequest{
				Owner: owner.NewSessionContract("empty-skills-owner", layout.Workdir),
			},
			Skills: skills, CapturedSkillIDs: []string{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	projection, _, err := prepared.Render(layout, runner.ProjectionBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := projection.Config["skills"]; ok {
		t.Fatal("an empty captured Skill selection enabled default Skills")
	}
	if _, err := os.Stat(filepath.Join(layout.SkillsRoot, "default-skill")); !os.IsNotExist(err) {
		t.Fatalf("default Skill entered the empty captured selection: %v", err)
	}
}

func TestPreparedConfigProjectionKeepsCapturedFilesAndEnvironment(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		for _, kind := range []owner.Kind{owner.KindTask, owner.KindSession} {
			t.Run(string(kind)+"/sandbox="+map[bool]string{false: "false", true: "true"}[sandbox], func(t *testing.T) {
				db, err := store.Open(filepath.Join(t.TempDir(), "projection.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				providers := modelprovider.NewService(db)
				provider, err := providers.Create(modelprovider.CreateRequest{
					Name: "Captured", BaseURL: "https://captured.example.test/v1",
					Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
					Catalog:   modelprovider.Catalog{Manual: []string{"captured-model"}, DefaultModel: "captured-model"},
				})
				if err != nil {
					t.Fatal(err)
				}
				creds := credential.NewService(db)
				if _, err := creds.Upsert(provider.APIKeyEnv, credential.ScopeGlobal, "", credential.Source{
					Kind: credential.SourceLiteral, Value: "captured-secret",
				}, false); err != nil {
					t.Fatal(err)
				}
				skills := skill.NewService(db, t.TempDir())
				capturedSkill, err := skills.Publish(context.Background(), skill.PublishRequest{
					Metadata: skill.Metadata{ID: "captured-skill", Name: "Captured Skill"},
					Files:    map[string]string{"SKILL.md": "# Captured Skill"},
				})
				if err != nil {
					t.Fatal(err)
				}
				layout, err := runner.PrepareTaskLayout(t.TempDir(), "projection-owner", runtimeprofile.ProviderPi)
				if err != nil {
					t.Fatal(err)
				}
				contract := owner.NewTaskContract("projection-owner", "project-1", layout.Workdir)
				if kind == owner.KindSession {
					contract = owner.NewSessionContract("projection-owner", layout.Workdir)
				}
				profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderPi, Fields: runtimeprofile.Fields{
					ModelProviderID: provider.ID, Env: map[string]string{"FIXED_SETTING": "captured"},
				}}
				prepared, err := runner.PrepareConfigProjection(profile, runner.ConfigProjectionInput{
					ProjectionRequest: runner.ProjectionRequest{
						Owner: contract, Credentials: creds, ModelProviders: providers,
						Sandbox: sandbox, LaunchModelOverride: "captured-model", RequestedReasoningEffort: "low",
					},
					Skills: skills,
				})
				if err != nil {
					t.Fatal(err)
				}
				profile.Fields.Env["FIXED_SETTING"] = "changed"
				endpoints := []modelprovider.Endpoint{{Protocol: modelprovider.ProtocolOpenAIChatCompletions, BaseURL: "https://changed.example.test/v1"}}
				if _, err := providers.Update(provider.ID, modelprovider.UpdateRequest{Endpoints: &endpoints}); err != nil {
					t.Fatal(err)
				}
				if _, err := creds.Upsert(provider.APIKeyEnv, credential.ScopeGlobal, "", credential.Source{
					Kind: credential.SourceLiteral, Value: "changed-secret",
				}, false); err != nil {
					t.Fatal(err)
				}
				if err := skills.SetGlobalOptOut(capturedSkill.ID, true); err != nil {
					t.Fatal(err)
				}
				if _, err := skills.Publish(context.Background(), skill.PublishRequest{
					Metadata: skill.Metadata{ID: "late-skill", Name: "Late Skill"},
					Files:    map[string]string{"SKILL.md": "# Late Skill"},
				}); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				projection, env, err := prepared.Render(layout, runner.ProjectionBinding{ContinuationID: "continuation-1", InterfaceToken: "grant-one"})
				if err != nil {
					t.Fatalf("render after closing the Store: %v", err)
				}
				if env[provider.APIKeyEnv] != "captured-secret" || env["FIXED_SETTING"] != "captured" {
					t.Fatalf("process environment did not retain captured values: %#v", env)
				}
				raw, err := os.ReadFile(projection.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), "https://captured.example.test/v1") || strings.Contains(string(raw), "changed.example.test") {
					t.Fatalf("models did not retain the captured endpoint: %s", raw)
				}
				settings := readJSONFile(t, filepath.Join(layout.ProviderHome, "agent", "settings.json"))
				if settings["defaultModel"] != "captured-model" || settings["defaultThinkingLevel"] != "low" {
					t.Fatalf("launch defaults = %#v", settings)
				}
				if _, err := os.Stat(filepath.Join(layout.SkillsRoot, capturedSkill.ID, "SKILL.md")); err != nil {
					t.Fatalf("captured Skill was not projected: %v", err)
				}
				if _, err := os.Stat(filepath.Join(layout.SkillsRoot, "late-skill")); !os.IsNotExist(err) {
					t.Fatalf("late Skill entered projection: %v", err)
				}
				preview, err := json.Marshal(projection.Config)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(preview), "captured-secret") || strings.Contains(string(preview), "grant-one") {
					t.Fatalf("projection preview contains secret material: %s", preview)
				}
			})
		}
	}
}
