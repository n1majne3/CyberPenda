package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestOmittedProjectionClearsGeneratedBlackboardArtifactsFromReusedLayout(t *testing.T) {
	providers := []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex,
		runtimeprofile.ProviderClaudeCode,
		runtimeprofile.ProviderPi,
		runtimeprofile.ProviderHermes,
	}
	for _, provider := range providers {
		for _, externalMCP := range []bool{false, true} {
			name := "zero-external-mcp"
			if externalMCP {
				name = "preserve-external-mcp"
			}
			t.Run(string(provider)+"/"+name, func(t *testing.T) {
				taskID := "reused-" + string(provider)
				layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), taskID, provider)
				if err != nil {
					t.Fatalf("prepare reused Runtime layout: %v", err)
				}
				profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{
					Model: "ordinary-model",
					Env: map[string]string{
						"NORMAL_RUNTIME_SETTING":  "keep-setting",
						"PENTEST_INTERFACE_TOKEN": "stale-profile-token",
					},
					MCPServers: []runtimeprofile.MCPServer{{
						Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted,
						URL: "http://stale-blackboard.example.test/mcp?token=stale-profile-token",
					}},
				}}
				if externalMCP {
					profile.Fields.MCPServers = append(profile.Fields.MCPServers, runtimeprofile.MCPServer{
						Name: "external-docs", Mode: runtimeprofile.MCPServerExternal,
						URL: "https://external.example.test/mcp",
					})
				}
				required := runner.ProjectionRequest{
					Owner:                   owner.NewTaskContract(taskID, "project-1", layout.Workdir),
					ScopeSnapshot:           project.Scope{Domains: []string{"scope.example.test"}},
					DaemonAddr:              "127.0.0.1:8787",
					AuthToken:               "required-launch-token",
					MaterializedCredentials: map[string]string{"MODEL_API_KEY": "keep-credential"},
				}
				if _, err := runner.ProjectBlackboardV2RuntimeConfig(layout, profile, required); err != nil {
					t.Fatalf("project required Runtime config: %v", err)
				}
				if err := runner.ProjectBlackboardV2Files(layout, profile.Provider, blackboardv2.LaunchHeader{
					Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
					Schema: "runtime-blackboard/v2", Revision: 7,
				}, required.ScopeSnapshot); err != nil {
					t.Fatalf("project required Blackboard files: %v", err)
				}
				// Re-project the legacy required contract to model a reused layout
				// that has every CyberPenda-generated discovery file from an
				// earlier launch path.
				if _, err := runner.ProjectRuntimeConfig(layout, profile, required); err != nil {
					t.Fatalf("project legacy required Runtime config: %v", err)
				}
				blackboardPath := filepath.Join(layout.Workdir, ".pentest", "blackboard.json")
				if err := os.WriteFile(blackboardPath, []byte(`{"schema":"runtime-blackboard/v2","revision":7}`), 0o600); err != nil {
					t.Fatalf("materialize required Working Blackboard Snapshot: %v", err)
				}
				requiredFiles := readRegularFiles(t, layout.TaskRoot)
				for _, want := range []string{"required-launch-token", "stale-profile-token", "scope.example.test"} {
					if !strings.Contains(requiredFiles, want) {
						t.Errorf("Required projection lost %q before omission", want)
					}
				}
				operatorFile := filepath.Join(layout.Workdir, "operator-notes.md")
				if err := os.WriteFile(operatorFile, []byte("keep operator notes"), 0o600); err != nil {
					t.Fatalf("write user-owned file: %v", err)
				}

				omitted := required
				omitted.AuthToken = "must-not-remain"
				omitted.BlackboardProjection = runner.BlackboardProjectionOmitted
				projection, err := runner.ProjectRuntimeConfig(layout, profile, omitted)
				if err != nil {
					t.Fatalf("project omitted Runtime config: %v", err)
				}
				if projection.ConfigPath == "" {
					t.Fatal("normal Runtime config path is missing")
				}
				for _, absent := range []string{
					"AGENTS.md",
					"CLAUDE.md",
					filepath.Join(".pentest", "context.json"),
					filepath.Join(".pentest", "blackboard.json"),
				} {
					if _, err := os.Stat(filepath.Join(layout.Workdir, absent)); !os.IsNotExist(err) {
						t.Errorf("generated Blackboard artifact remains at %s: %v", absent, err)
					}
				}
				if raw, err := os.ReadFile(filepath.Join(layout.Workdir, ".pentest", "scope.json")); err != nil {
					t.Errorf("read retained Scope Snapshot: %v", err)
				} else if !strings.Contains(string(raw), "scope.example.test") {
					t.Errorf("retained Scope Snapshot changed: %s", raw)
				}
				if raw, err := os.ReadFile(operatorFile); err != nil {
					t.Errorf("read user-owned file: %v", err)
				} else if string(raw) != "keep operator notes" {
					t.Errorf("user-owned file changed: %q", raw)
				}
				mcpPath := providerMCPConfigPath(layout, provider, projection)
				if externalMCP {
					if raw, err := os.ReadFile(mcpPath); err != nil {
						t.Errorf("read retained External MCP Server config: %v", err)
					} else if !strings.Contains(string(raw), "external-docs") || strings.Contains(string(raw), "project-memory") {
						t.Errorf("External MCP Server config was not isolated from Blackboard authority: %s", raw)
					}
				} else if provider == runtimeprofile.ProviderClaudeCode || provider == runtimeprofile.ProviderPi {
					if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
						t.Errorf("stale provider MCP file remains with zero External MCP Servers: %v", err)
					}
				}
				if _, err := os.Stat(filepath.Join(layout.RuntimeHome, ".cyberpenda-blackboard-projection-files.json")); !os.IsNotExist(err) {
					t.Errorf("Blackboard projection ownership record remains after omission: %v", err)
				}
				projectedFiles := readRegularFiles(t, layout.TaskRoot)
				for _, forbidden := range []string{
					"required-launch-token",
					"must-not-remain",
					"stale-profile-token",
					"project-memory",
					"stale-blackboard.example.test",
					"mcp__pentest__blackboard_",
					"127.0.0.1:8787/mcp",
				} {
					if strings.Contains(projectedFiles, forbidden) {
						t.Errorf("trusted Blackboard value %q remains in reused Runtime layout", forbidden)
					}
				}
				for _, retained := range []string{"ordinary-model", "keep operator notes"} {
					if !strings.Contains(projectedFiles, retained) {
						t.Errorf("ordinary Runtime value %q is missing after omission", retained)
					}
				}
				if externalMCP && !strings.Contains(projectedFiles, "external-docs") {
					t.Error("External MCP Server is missing after omission")
				}
				processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, profile, false, runner.RuntimeOwnerContext{
					Owner: required.Owner, MCPURL: "http://forbidden.example.test/mcp",
					APIURL: "http://forbidden.example.test/api", InterfaceToken: "forbidden-token",
				}, omitted)
				if err != nil {
					t.Fatalf("build ordinary Runtime environment: %v", err)
				}
				for key, want := range map[string]string{
					"NORMAL_RUNTIME_SETTING": "keep-setting",
					"MODEL_API_KEY":          "keep-credential",
				} {
					if got := processEnv[key]; got != want {
						t.Errorf("ordinary Runtime environment %s = %q, want %q", key, got, want)
					}
				}
				for _, key := range []string{"PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_INTERFACE_TOKEN"} {
					if got := processEnv[key]; got != "" {
						t.Errorf("Blackboard authority environment %s remains: %q", key, got)
					}
				}
			})
		}
	}
}

func providerMCPConfigPath(layout runner.Layout, provider runtimeprofile.Provider, projection runner.ConfigProjection) string {
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		return filepath.Join(layout.Workdir, ".mcp.json")
	case runtimeprofile.ProviderPi:
		return filepath.Join(layout.ProviderHome, "agent", "mcp.json")
	default:
		return projection.ConfigPath
	}
}

func TestOmittedProjectionDoesNotTrustRuntimeWritableOwnershipMetadata(t *testing.T) {
	t.Run("unrecorded-lookalike", func(t *testing.T) {
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "unrecorded-lookalike", runtimeprofile.ProviderClaudeCode)
		if err != nil {
			t.Fatalf("prepare Runtime layout: %v", err)
		}
		userAgents := filepath.Join(layout.Workdir, "AGENTS.md")
		if err := os.WriteFile(userAgents, []byte("operator-owned instructions"), 0o600); err != nil {
			t.Fatalf("write unrecorded user file: %v", err)
		}
		if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
		}, runner.ProjectionRequest{
			Owner:                owner.NewTaskContract("unrecorded-lookalike", "project-1", layout.Workdir),
			BlackboardProjection: runner.BlackboardProjectionOmitted,
		}); err != nil {
			t.Fatalf("project omitted Runtime config: %v", err)
		}
		if raw, err := os.ReadFile(userAgents); err != nil {
			t.Fatalf("read unrecorded user file: %v", err)
		} else if string(raw) != "operator-owned instructions" {
			t.Fatalf("unrecorded user file changed: %q", raw)
		}
	})

	t.Run("unknown-record-entry", func(t *testing.T) {
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "tampered-record", runtimeprofile.ProviderClaudeCode)
		if err != nil {
			t.Fatalf("prepare Runtime layout: %v", err)
		}
		userFile := filepath.Join(layout.Workdir, "operator-notes.md")
		if err := os.WriteFile(userFile, []byte("must survive"), 0o600); err != nil {
			t.Fatalf("write user file: %v", err)
		}
		tampered := `{"schema":"cyberpenda-blackboard-projection-files/v1","artifacts":["../../operator-notes.md"]}`
		if err := os.WriteFile(filepath.Join(layout.RuntimeHome, ".cyberpenda-blackboard-projection-files.json"), []byte(tampered), 0o600); err != nil {
			t.Fatalf("write tampered ownership record: %v", err)
		}
		_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
		}, runner.ProjectionRequest{
			Owner:                owner.NewTaskContract("tampered-record", "project-1", layout.Workdir),
			BlackboardProjection: runner.BlackboardProjectionOmitted,
		})
		if err == nil || !strings.Contains(err.Error(), "unknown artifact") {
			t.Fatalf("omitted projection accepted a tampered ownership target: %v", err)
		}
		if raw, err := os.ReadFile(userFile); err != nil {
			t.Fatalf("read user file after rejected cleanup: %v", err)
		} else if string(raw) != "must survive" {
			t.Fatalf("tampered ownership record changed user file: %q", raw)
		}
	})

	t.Run("forged-allowlisted-entry", func(t *testing.T) {
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "forged-allowlisted-entry", runtimeprofile.ProviderClaudeCode)
		if err != nil {
			t.Fatalf("prepare Runtime layout: %v", err)
		}
		userFile := filepath.Join(layout.Workdir, "CLAUDE.md")
		if err := os.WriteFile(userFile, []byte("operator-owned Claude instructions"), 0o600); err != nil {
			t.Fatalf("write user file at allowlisted path: %v", err)
		}
		forged := `{"schema":"cyberpenda-blackboard-projection-files/v1","artifacts":["workdir-claude"]}`
		if err := os.WriteFile(filepath.Join(layout.RuntimeHome, ".cyberpenda-blackboard-projection-files.json"), []byte(forged), 0o600); err != nil {
			t.Fatalf("write forged ownership record: %v", err)
		}
		_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
		}, runner.ProjectionRequest{
			Owner:                owner.NewTaskContract("forged-allowlisted-entry", "project-1", layout.Workdir),
			BlackboardProjection: runner.BlackboardProjectionOmitted,
		})
		if err == nil || !strings.Contains(err.Error(), "generator contract") {
			t.Fatalf("omitted projection trusted forged ownership of a user file: %v", err)
		}
		if raw, err := os.ReadFile(userFile); err != nil {
			t.Fatalf("read user file after rejected cleanup: %v", err)
		} else if string(raw) != "operator-owned Claude instructions" {
			t.Fatalf("forged allowlisted record changed user file: %q", raw)
		}
	})

	t.Run("recorded-symlink", func(t *testing.T) {
		layout, err := runner.PrepareTaskLayout(t.TempDir(), "recorded-symlink", runtimeprofile.ProviderClaudeCode)
		if err != nil {
			t.Fatalf("prepare Runtime layout: %v", err)
		}
		required := runner.ProjectionRequest{
			Owner: owner.NewTaskContract("recorded-symlink", "project-1", layout.Workdir),
		}
		if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
		}, required); err != nil {
			t.Fatalf("project required Runtime config: %v", err)
		}
		outside := filepath.Join(t.TempDir(), "operator-owned-target.md")
		if err := os.WriteFile(outside, []byte("must survive symlink cleanup"), 0o600); err != nil {
			t.Fatalf("write symlink target: %v", err)
		}
		agents := filepath.Join(layout.Workdir, "AGENTS.md")
		if err := os.Remove(agents); err != nil {
			t.Fatalf("replace generated instruction in test: %v", err)
		}
		if err := os.Symlink(outside, agents); err != nil {
			t.Fatalf("install Runtime-controlled symlink: %v", err)
		}
		omitted := required
		omitted.BlackboardProjection = runner.BlackboardProjectionOmitted
		if _, err := runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderClaudeCode,
		}, omitted); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("omitted projection followed a recorded symlink: %v", err)
		}
		if raw, err := os.ReadFile(outside); err != nil {
			t.Fatalf("read symlink target after rejected cleanup: %v", err)
		} else if string(raw) != "must survive symlink cleanup" {
			t.Fatalf("recorded symlink changed its target: %q", raw)
		}
	})
}

func readRegularFiles(t *testing.T, root string) string {
	t.Helper()
	var projected strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			projected.Write(raw)
			projected.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read projected Runtime files: %v", err)
	}
	return projected.String()
}

func TestClaudeRuntimeProjectionCanOmitBlackboardAuthority(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-without-blackboard", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare Runtime layout: %v", err)
	}
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model: "claude-test",
			Env:   map[string]string{"PENTEST_INTERFACE_TOKEN": "profile-grant"},
			MCPServers: []runtimeprofile.MCPServer{
				{Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted, URL: "http://daemon.test/mcp?token=stale-grant"},
				{Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://docs.example.test/mcp"},
			},
		},
	}
	request := runner.ProjectionRequest{
		Owner:                owner.NewTaskContract("task-without-blackboard", "project-1", layout.Workdir),
		ScopeSnapshot:        project.Scope{Domains: []string{"example.test"}},
		DaemonAddr:           "127.0.0.1:8787",
		AuthToken:            "new-grant",
		BlackboardProjection: runner.BlackboardProjectionOmitted,
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, request)
	if err != nil {
		t.Fatalf("project Runtime config without Blackboard: %v", err)
	}
	for _, server := range projection.ResolvedProfile.Fields.MCPServers {
		if server.Mode == runtimeprofile.MCPServerTrusted {
			t.Fatalf("resolved Runtime Profile retained trusted Project Interface configuration: %#v", projection.ResolvedProfile)
		}
	}

	settings, err := os.ReadFile(filepath.Join(layout.ProviderHome, "settings.json"))
	if err != nil {
		t.Fatalf("read normal Claude settings: %v", err)
	}
	if !strings.Contains(string(settings), "claude-test") {
		t.Fatalf("normal model configuration is missing: %s", settings)
	}
	mcpConfig, err := os.ReadFile(filepath.Join(layout.Workdir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read external MCP config: %v", err)
	}
	if !strings.Contains(string(mcpConfig), "external-docs") {
		t.Fatalf("external MCP configuration is missing: %s", mcpConfig)
	}
	for _, forbidden := range []string{"project-memory", "new-grant", "stale-grant", "profile-grant", "mcp__pentest__blackboard_"} {
		if strings.Contains(string(settings)+string(mcpConfig), forbidden) {
			t.Fatalf("Blackboard authority %q was projected: settings=%s mcp=%s", forbidden, settings, mcpConfig)
		}
	}
	if _, err := os.Stat(filepath.Join(layout.Workdir, ".pentest", "scope.json")); err != nil {
		t.Fatalf("Scope Snapshot is missing: %v", err)
	}
	for _, absent := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".pentest", "context.json"), filepath.Join(".pentest", "blackboard.json")} {
		if _, err := os.Stat(filepath.Join(layout.Workdir, absent)); !os.IsNotExist(err) {
			t.Fatalf("unexpected Blackboard projection %s: %v", absent, err)
		}
	}

	env, err := runner.LaunchProcessEnvWithCredentials(
		layout,
		profile,
		false,
		runner.RuntimeOwnerContext{
			Owner:          request.Owner,
			MCPURL:         "http://daemon.test/mcp",
			APIURL:         "http://daemon.test/api",
			InterfaceToken: "new-grant",
		},
		request,
	)
	if err != nil {
		t.Fatalf("build Runtime environment without Blackboard: %v", err)
	}
	for _, forbidden := range []string{"PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_INTERFACE_TOKEN"} {
		if value := env[forbidden]; value != "" {
			t.Fatalf("%s retained Blackboard authority value %q", forbidden, value)
		}
	}
	if projection.ConfigPath == "" {
		t.Fatal("ordinary Runtime configuration path is missing")
	}
}

func TestRuntimeProviderConfigsConsumeLaunchesWithoutBlackboardProjection(t *testing.T) {
	for _, tc := range []struct {
		provider runtimeprofile.Provider
		mcpPath  func(runner.Layout, runner.ConfigProjection) string
	}{
		{runtimeprofile.ProviderCodex, func(_ runner.Layout, projection runner.ConfigProjection) string { return projection.ConfigPath }},
		{runtimeprofile.ProviderClaudeCode, func(layout runner.Layout, _ runner.ConfigProjection) string {
			return filepath.Join(layout.Workdir, ".mcp.json")
		}},
		{runtimeprofile.ProviderPi, func(layout runner.Layout, _ runner.ConfigProjection) string {
			return filepath.Join(layout.ProviderHome, "agent", "mcp.json")
		}},
		{runtimeprofile.ProviderHermes, func(_ runner.Layout, projection runner.ConfigProjection) string { return projection.ConfigPath }},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "ordinary-"+string(tc.provider), tc.provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			profile := runtimeprofile.Profile{Provider: tc.provider, Fields: runtimeprofile.Fields{
				Model: "ordinary-model",
				Env:   map[string]string{"PENTEST_INTERFACE_TOKEN": "profile-grant"},
				MCPServers: []runtimeprofile.MCPServer{
					{Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted, URL: "http://daemon.test/mcp?token=stale-grant"},
					{Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://docs.example.test/mcp"},
				},
			}}
			projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
				Owner:         owner.NewTaskContract("ordinary-"+string(tc.provider), "project-1", layout.Workdir),
				ScopeSnapshot: project.Scope{Domains: []string{"example.test"}},
				DaemonAddr:    "127.0.0.1:8787", AuthToken: "launch-grant",
				BlackboardProjection: runner.BlackboardProjectionOmitted,
			})
			if err != nil {
				t.Fatalf("project ordinary Runtime config: %v", err)
			}
			if projection.ConfigPath == "" {
				t.Fatal("ordinary Runtime config path is missing")
			}
			config, err := os.ReadFile(projection.ConfigPath)
			if err != nil {
				t.Fatalf("read ordinary Runtime config: %v", err)
			}
			mcp, err := os.ReadFile(tc.mcpPath(layout, projection))
			if err != nil {
				t.Fatalf("read external MCP projection: %v", err)
			}
			projected := string(config) + string(mcp)
			if !strings.Contains(projected, "external-docs") {
				t.Fatalf("external MCP configuration is missing: %s", projected)
			}
			for _, forbidden := range []string{"project-memory", "stale-grant", "launch-grant", "profile-grant", "mcp__pentest__blackboard_"} {
				if strings.Contains(projected, forbidden) {
					t.Fatalf("Blackboard authority %q was projected: %s", forbidden, projected)
				}
			}
			for _, absent := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".pentest", "context.json"), filepath.Join(".pentest", "blackboard.json")} {
				if _, err := os.Stat(filepath.Join(layout.Workdir, absent)); !os.IsNotExist(err) {
					t.Fatalf("unexpected Blackboard projection %s: %v", absent, err)
				}
			}
		})
	}
}
