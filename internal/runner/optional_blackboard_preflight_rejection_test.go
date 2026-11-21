package runner_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modeskill"
	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func testRuntimeProvidersRejectTrustedMCPConfigWithoutChangingIt(t *testing.T) {
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
				BlackboardMode:              modeskill.ModeDisabled,
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection:        runner.BlackboardProjectionOmitted,
			})
			if err == nil {
				t.Fatal("omitted projection accepted a stale trusted Project Interface config")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func testRuntimeProvidersRejectStaleCustomTrustedMCPWithoutChangingLayout(t *testing.T) {
	for _, provider := range optionalProjectionProviders() {
		t.Run(string(provider), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("USERPROFILE", t.TempDir())
			taskID := "stale-custom-trusted-mcp-" + string(provider)
			layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{
				Env: map[string]string{"PENTEST_DISABLE_TRUSTED_MCP": "1"},
				MCPServers: []runtimeprofile.MCPServer{{
					Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted, URL: "https://memory.example.test/mcp",
				}},
			}}
			required := runner.ProjectionRequest{
				BlackboardMode:              modeskill.ModeDisabled,
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				ScopeSnapshot:               project.Scope{Domains: []string{"scope.example.test"}},
			}
			projection, err := runner.ProjectRuntimeConfig(layout, profile, required)
			if err != nil {
				t.Fatalf("project required Runtime config with custom trusted MCP: %v", err)
			}
			mcpPath := providerMCPConfigPath(layout, provider, projection)
			if raw, err := os.ReadFile(mcpPath); err != nil || !strings.Contains(string(raw), "project-memory") {
				t.Fatalf("required custom trusted MCP projection = %s, err=%v", raw, err)
			}
			for _, path := range []string{
				filepath.Join(layout.Workdir, "AGENTS.md"),
				filepath.Join(layout.Workdir, ".pentest", "context.json"),
			} {
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove isolated Required projection artifact %s: %v", path, err)
				}
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			omitted := required
			omitted.BlackboardProjection = runner.BlackboardProjectionOmitted
			if _, err := runner.ProjectRuntimeConfig(layout, profile, omitted); err == nil {
				t.Fatal("omitted projection accepted stale custom trusted MCP configuration")
			}
			assertRuntimeLayoutUnchanged(t, layout.TaskRoot, before)
		})
	}
}

func TestRuntimeProvidersRejectMalformedKnownMCPConfigWithoutChangingLayout(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider runtimeprofile.Provider
		path     func(runner.Layout) string
		content  []byte
	}{
		{
			name:     "claude .mcp.json",
			provider: runtimeprofile.ProviderClaudeCode,
			path:     func(layout runner.Layout) string { return filepath.Join(layout.Workdir, ".mcp.json") },
			content:  []byte(`{"mcpServers":{"external-docs":`),
		},
		{
			name:     "pi agent/mcp.json",
			provider: runtimeprofile.ProviderPi,
			path:     func(layout runner.Layout) string { return filepath.Join(layout.ProviderHome, "agent", "mcp.json") },
			content:  []byte(`{"mcpServers":{"external-docs":`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			taskID := "malformed-known-mcp-" + string(test.provider)
			layout, err := runner.PrepareTaskLayout(t.TempDir(), taskID, test.provider)
			if err != nil {
				t.Fatalf("prepare Runtime layout: %v", err)
			}
			path := test.path(layout)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("prepare malformed MCP config directory: %v", err)
			}
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatalf("write malformed MCP config: %v", err)
			}
			before := snapshotRuntimeLayout(t, layout.TaskRoot)
			_, err = runner.ProjectRuntimeConfig(layout, runtimeprofile.Profile{
				Provider: test.provider,
				Fields: runtimeprofile.Fields{MCPServers: []runtimeprofile.MCPServer{{
					Name: "external-docs", Mode: runtimeprofile.MCPServerExternal, URL: "https://external.example.test/mcp",
				}}},
			}, runner.ProjectionRequest{
				BlackboardMode:              modeskill.ModeDisabled,
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection:        runner.BlackboardProjectionOmitted,
			})
			if err == nil {
				t.Fatal("omitted projection accepted malformed known MCP configuration")
			}
			if !strings.Contains(err.Error(), "parse known") {
				t.Fatalf("malformed known MCP configuration error = %v, want parse failure", err)
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
				BlackboardMode:              modeskill.ModeDisabled,
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection:        runner.BlackboardProjectionOmitted,
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
				BlackboardMode:              modeskill.ModeDisabled,
				GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
				Owner:                       owner.NewTaskContract(taskID, "project-1", layout.Workdir),
				BlackboardProjection:        runner.BlackboardProjectionOmitted,
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
		BlackboardMode:              modeskill.ModeDisabled,
		GlobalModelProviderSnapshot: runner.CloneGlobalModelProviderSnapshot(nil),
		Owner:                       owner.NewTaskContract("task-without-blackboard", "project-1", layout.Workdir),
		ScopeSnapshot:               project.Scope{Domains: []string{"example.test"}},
		DaemonAddr:                  "127.0.0.1:8787",
		AuthToken:                   "new-grant",
		BlackboardProjection:        runner.BlackboardProjectionOmitted,
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
