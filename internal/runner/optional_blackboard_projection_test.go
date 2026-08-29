package runner_test

import (
	"bytes"
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

func TestRuntimeProvidersRejectRequiredArtifactsWithoutChangingLayout(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "required-then-omitted-" + string(provider)
			layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare reused Runtime layout: %v", err)
			}
			profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{
				Model:   "ordinary-model",
				Env:     map[string]string{"ORDINARY_RUNTIME_SETTING": "preserve-me"},
				APIKeys: map[string]string{"OPENAI_API_KEY": "ordinary-model-credential"},
				MCPServers: []runtimeprofile.MCPServer{
					{Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted, URL: "http://stale.example.test/mcp?token=stale-token"},
					{Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://external.example.test/mcp"},
				},
			}}
			required := runner.ProjectionRequest{
				Owner:         owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				ScopeSnapshot: project.Scope{Domains: []string{"scope.example.test"}},
				DaemonAddr:    "127.0.0.1:8787",
				AuthToken:     "required-token",
			}
			if _, err := runner.ProjectBlackboardV2RuntimeConfig(layout, profile, required); err != nil {
				t.Fatalf("project required Runtime config: %v", err)
			}
			if err := runner.ProjectBlackboardV2Files(layout, profile.Provider, blackboardv2.LaunchHeader{
				Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: "runtime-blackboard/v2", Revision: 9,
			}, required.ScopeSnapshot); err != nil {
				t.Fatalf("project required Blackboard files: %v", err)
			}
			legacyProjection, err := runner.ProjectRuntimeConfig(layout, profile, required)
			if err != nil {
				t.Fatalf("project legacy required Runtime config: %v", err)
			}
			blackboardPath := filepath.Join(layout.Workdir, ".pentest", "blackboard.json")
			if err := os.WriteFile(blackboardPath, []byte(`{"schema":"runtime-blackboard/v2","revision":9}`), 0o600); err != nil {
				t.Fatalf("materialize Working Blackboard Snapshot: %v", err)
			}
			operatorFile := filepath.Join(layout.Workdir, "operator-notes.md")
			if err := os.WriteFile(operatorFile, []byte("preserve operator data"), 0o600); err != nil {
				t.Fatalf("write operator file: %v", err)
			}
			for _, path := range []string{
				filepath.Join(layout.Workdir, "AGENTS.md"),
				filepath.Join(layout.Workdir, ".pentest", "context.json"),
				blackboardPath,
				providerMCPConfigPath(layout, provider, legacyProjection),
				operatorFile,
			} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("required projection artifact %s is missing: %v", path, err)
				}
			}
			if provider == runtimeprofile.ProviderClaudeCode {
				if _, err := os.Stat(filepath.Join(layout.Workdir, "CLAUDE.md")); err != nil {
					t.Fatalf("required Claude checklist is missing: %v", err)
				}
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			omitted := required
			omitted.BlackboardProjection = runner.BlackboardProjectionOmitted
			if _, err := runner.ProjectRuntimeConfig(layout, profile, omitted); err == nil {
				t.Fatal("omitted projection accepted a reused layout with required Blackboard artifacts")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersRejectModifiedGeneratedInstructionsWithoutChangingLayout(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "modified-generated-instructions-" + string(provider)
			layout, err := runner.PrepareBlackboardV2TaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			scope := project.Scope{Domains: []string{"scope.example.test"}}
			if err := runner.ProjectBlackboardV2Files(layout, provider, blackboardv2.LaunchHeader{
				Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: "runtime-blackboard/v2", Revision: 9,
			}, scope); err != nil {
				t.Fatalf("project required Blackboard files: %v", err)
			}
			instructionPath := filepath.Join(layout.Workdir, "AGENTS.md")
			if provider == runtimeprofile.ProviderClaudeCode {
				instructionPath = filepath.Join(layout.Workdir, "CLAUDE.md")
			}
			generated, err := os.ReadFile(instructionPath)
			if err != nil {
				t.Fatalf("read generated instructions: %v", err)
			}
			modified := append(generated, []byte("\nRuntime-added note.\n")...)
			if err := os.WriteFile(instructionPath, modified, 0o600); err != nil {
				t.Fatalf("modify generated instructions: %v", err)
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
				Provider: provider,
			}, runner.ProjectionRequest{
				Owner:                owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				ScopeSnapshot:        scope,
				BlackboardProjection: runner.BlackboardProjectionOmitted,
			})
			if err == nil {
				t.Fatal("omitted projection accepted Runtime-modified Blackboard instructions")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersSupportFreshAndRepeatedOmittedProjection(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		for _, externalMCP := range []bool{false, true} {
			name := "zero-external-mcp"
			if externalMCP {
				name = "external-mcp"
			}
			t.Run(string(provider)+"/"+name, func(t *testing.T) {
				taskID := "repeated-" + string(provider) + "-" + name
				layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
				if err != nil {
					t.Fatalf("prepare Runtime layout: %v", err)
				}
				profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{Model: "ordinary-model"}}
				if externalMCP {
					profile.Fields.MCPServers = []runtimeprofile.MCPServer{{
						Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://external.example.test/mcp",
					}}
				}
				userFiles := map[string][]byte{
					filepath.Join(layout.Workdir, "AGENTS.md"):         []byte("# Operator notes\n\nBlackboard workflow is discussed here, but this is not generated instructions.\n"),
					filepath.Join(layout.Workdir, "CLAUDE.md"):         []byte("Claude operator preferences; no Project Interface authority.\n"),
					filepath.Join(layout.Workdir, "operator-notes.md"): []byte("Trusted MCP is pre-configured\n## Required workflow\nblackboard_finish\n.pentest/context.json\nPENTEST_INTERFACE_TOKEN=operator-example\n"),
				}
				for path, raw := range userFiles {
					if err := os.WriteFile(path, raw, 0o600); err != nil {
						t.Fatalf("write user file %s: %v", path, err)
					}
				}
				request := runner.ProjectionRequest{
					Owner:                owner.NewTaskContract(taskID, "project-1", layout.Workdir),
					ScopeSnapshot:        project.Scope{Domains: []string{"scope.example.test"}},
					BlackboardProjection: runner.BlackboardProjectionOmitted,
				}
				first, err := runner.ProjectRuntimeConfig(layout, profile, request)
				if err != nil {
					t.Fatalf("project fresh omitted Runtime config: %v", err)
				}
				mcpPath := providerMCPConfigPath(layout, provider, first)
				var firstMCP []byte
				if externalMCP {
					firstMCP, err = os.ReadFile(mcpPath)
					if err != nil || !strings.Contains(string(firstMCP), "external-docs") {
						t.Fatalf("External MCP Server projection = %s, err=%v", firstMCP, err)
					}
				}
				scopePath := filepath.Join(layout.Workdir, ".pentest", "scope.json")
				firstScope, err := os.ReadFile(scopePath)
				if err != nil || !strings.Contains(string(firstScope), "scope.example.test") {
					t.Fatalf("Scope projection = %s, err=%v", firstScope, err)
				}
				if _, err := runner.ProjectRuntimeConfig(layout, profile, request); err != nil {
					t.Fatalf("repeat omitted Runtime projection: %v", err)
				}
				for path, want := range userFiles {
					got, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(got, want) {
						t.Errorf("user file %s changed: %q, err=%v", path, got, err)
					}
				}
				if externalMCP {
					secondMCP, err := os.ReadFile(mcpPath)
					if err != nil || !bytes.Equal(secondMCP, firstMCP) {
						t.Errorf("repeated Omitted projection changed External MCP config: %s, err=%v", secondMCP, err)
					}
				}
				if secondScope, err := os.ReadFile(scopePath); err != nil || !bytes.Equal(secondScope, firstScope) {
					t.Errorf("repeated Omitted projection changed Scope: %s, err=%v", secondScope, err)
				}
			})
		}
	}
}

func optionalProjectionProviders() []runtimeprofile.Provider {
	return []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex,
		runtimeprofile.ProviderClaudeCode,
		runtimeprofile.ProviderPi,
		runtimeprofile.ProviderHermes,
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

func TestRuntimeProvidersRejectTrustedMCPConfigWithoutChangingIt(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "stale-trusted-mcp-" + string(provider)
			layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			path, raw := staleTrustedMCPConfig(layout, provider)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("prepare stale MCP config directory: %v", err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatalf("write stale trusted MCP config: %v", err)
			}
			operatorFile := filepath.Join(layout.Workdir, "operator-notes.md")
			if err := os.WriteFile(operatorFile, []byte("preserve operator data"), 0o600); err != nil {
				t.Fatalf("write operator file: %v", err)
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
				Provider: provider,
				Fields: runtimeprofile.Fields{MCPServers: []runtimeprofile.MCPServer{{
					Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://external.example.test/mcp",
				}}},
			}, runner.ProjectionRequest{
				Owner:                owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection: runner.BlackboardProjectionOmitted,
			})
			if err == nil {
				t.Fatal("omitted projection accepted a stale trusted Project Interface config")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersRejectProjectInterfaceTokensWithoutChangingConfig(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "stale-project-interface-token-" + string(provider)
			layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			path, raw := staleProjectInterfaceTokenConfig(layout, provider)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("prepare stale token config directory: %v", err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatalf("write stale Project Interface token: %v", err)
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
				Provider: provider,
			}, runner.ProjectionRequest{
				Owner:                owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection: runner.BlackboardProjectionOmitted,
			})
			if err == nil {
				t.Fatal("omitted projection accepted a stale Project Interface token")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersRejectKnownProjectionSymlinksWithoutDeletion(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			taskID := "omitted-symlink-" + string(provider)
			layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			target := filepath.Join(t.TempDir(), "operator-owned-target.md")
			want := []byte("must survive Omitted preflight")
			if err := os.WriteFile(target, want, 0o600); err != nil {
				t.Fatalf("write symlink target: %v", err)
			}
			knownPath := filepath.Join(layout.Workdir, "AGENTS.md")
			if provider == runtimeprofile.ProviderClaudeCode {
				knownPath = filepath.Join(layout.Workdir, "CLAUDE.md")
			}
			if err := os.Symlink(target, knownPath); err != nil {
				t.Fatalf("install Runtime-controlled symlink: %v", err)
			}
			_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
				Provider: provider,
			}, runner.ProjectionRequest{
				Owner:                owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection: runner.BlackboardProjectionOmitted,
			})
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("omitted projection did not reject known projection symlink: %v", err)
			}
			if got, err := os.Readlink(knownPath); err != nil || got != target {
				t.Fatalf("known projection symlink changed: target=%q err=%v", got, err)
			}
			if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("symlink target changed: %q, err=%v", got, err)
			}
		})
	}
}

func staleTrustedMCPConfig(layout runner.Layout, provider runtimeprofile.Provider) (string, []byte) {
	const url = "http://stale.example.test/mcp"
	switch provider {
	case runtimeprofile.ProviderCodex:
		return filepath.Join(layout.ProviderHome, "config.toml"), []byte("model = \"ordinary-model\"\n\n[mcp_servers.pentest]\nurl = \"" + url + "\"\n")
	case runtimeprofile.ProviderClaudeCode:
		return filepath.Join(layout.Workdir, ".mcp.json"), []byte(`{"mcpServers":{"external-docs":{"type":"http","url":"https://external.example.test/mcp"},"pentest":{"type":"http","url":"` + url + `"}}}`)
	case runtimeprofile.ProviderPi:
		return filepath.Join(layout.ProviderHome, "agent", "mcp.json"), []byte(`{"mcpServers":{"external-docs":{"type":"http","url":"https://external.example.test/mcp"},"pentest":{"type":"http","url":"` + url + `"}}}`)
	case runtimeprofile.ProviderHermes:
		return filepath.Join(layout.ProviderHome, "config.yaml"), []byte("model: ordinary-model\nmcp_servers:\n  external-docs:\n    url: https://external.example.test/mcp\n  pentest:\n    url: " + url + "\n")
	default:
		panic("unsupported Runtime Provider")
	}
}

func staleProjectInterfaceTokenConfig(layout runner.Layout, provider runtimeprofile.Provider) (string, []byte) {
	const token = "stale-project-interface-token"
	switch provider {
	case runtimeprofile.ProviderCodex:
		return filepath.Join(layout.ProviderHome, "auth.json"), []byte(`{"PENTEST_INTERFACE_TOKEN":"` + token + `","OPENAI_API_KEY":"ordinary-model-credential"}`)
	case runtimeprofile.ProviderClaudeCode:
		return filepath.Join(layout.ProviderHome, "settings.json"), []byte(`{"env":{"PENTEST_INTERFACE_TOKEN":"` + token + `","ORDINARY_RUNTIME_SETTING":"preserve-me"}}`)
	case runtimeprofile.ProviderPi:
		return filepath.Join(layout.ProviderHome, "agent", "auth.json"), []byte(`{"PENTEST_INTERFACE_TOKEN":"` + token + `","ordinary":{"token":"ordinary-model-credential"}}`)
	case runtimeprofile.ProviderHermes:
		return filepath.Join(layout.ProviderHome, ".env"), []byte("PENTEST_INTERFACE_TOKEN=" + token + "\nORDINARY_MODEL_CREDENTIAL=preserve-me\n")
	default:
		panic("unsupported Runtime Provider")
	}
}

type runtimeLayoutEntry struct {
	mode    os.FileMode
	content string
}

func snapshotRuntimeLayout(t *testing.T, root string) map[string]runtimeLayoutEntry {
	t.Helper()
	snapshot := map[string]runtimeLayoutEntry{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := runtimeLayoutEntry{mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.content, err = os.Readlink(path)
		case info.Mode().IsRegular():
			var raw []byte
			raw, err = os.ReadFile(path)
			item.content = string(raw)
		}
		if err != nil {
			return err
		}
		snapshot[relative] = item
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot Runtime layout: %v", err)
	}
	return snapshot
}

func assertRuntimeLayoutUnchanged(t *testing.T, root string, before map[string]runtimeLayoutEntry) {
	t.Helper()
	after := snapshotRuntimeLayout(t, root)
	if len(after) != len(before) {
		t.Fatalf("rejected Omitted projection changed Runtime layout entries: before=%d after=%d", len(before), len(after))
	}
	for path, want := range before {
		if got, ok := after[path]; !ok {
			t.Errorf("rejected Omitted projection removed %s", path)
		} else if got != want {
			t.Errorf("rejected Omitted projection changed %s", path)
		}
	}
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
