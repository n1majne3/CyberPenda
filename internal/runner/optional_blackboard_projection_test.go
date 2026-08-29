package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

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
