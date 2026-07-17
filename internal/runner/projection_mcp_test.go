package runner_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/adapters"
	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

func TestMCPEndpointURL(t *testing.T) {
	tests := []struct {
		name       string
		daemonAddr string
		sandbox    bool
		want       string
	}{
		{
			name:       "default host",
			daemonAddr: "",
			sandbox:    false,
			want:       "http://127.0.0.1:8787/mcp",
		},
		{
			name:       "custom host port",
			daemonAddr: "127.0.0.1:9999",
			sandbox:    false,
			want:       "http://127.0.0.1:9999/mcp",
		},
		{
			name:       "bind all interfaces",
			daemonAddr: "0.0.0.0:8787",
			sandbox:    false,
			want:       "http://127.0.0.1:8787/mcp",
		},
		{
			name:       "sandbox uses docker host gateway",
			daemonAddr: "127.0.0.1:8787",
			sandbox:    true,
			want:       "http://host.docker.internal:8787/mcp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runner.MCPEndpointURL(tc.daemonAddr, tc.sandbox)
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestEveryBuiltinRuntimeAdapterReconstructsExactCanonicalMainGraphFromRuntimeBlackboardContextV1(t *testing.T) {
	canonicalGraph := []byte(`{"schema_version":1,"project_id":"project-1","project_kind":"pentest","graph_revision":42,"nodes":[],"edges":[],"frontier_node_ids":[],"current_truth_node_ids":[]}`)
	wantDigest := projectinterface.RuntimeProtocolRuleDigest()
	for _, plugin := range runtimeplugin.BuiltinPlugins() {
		plugin := plugin
		t.Run(plugin.ID, func(t *testing.T) {
			layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-1", runtimeprofile.Provider(plugin.ID))
			if err != nil {
				t.Fatalf("prepare layout: %v", err)
			}
			snapshotPath := filepath.Join(layout.Workdir, ".pentest", "blackboard.json")
			if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o700); err != nil {
				t.Fatalf("prepare snapshot directory: %v", err)
			}
			if err := os.WriteFile(snapshotPath, canonicalGraph, 0o600); err != nil {
				t.Fatalf("write canonical graph fixture: %v", err)
			}
			ctx := projectinterface.RuntimeBlackboardContextV1{
				ProjectID: "project-1", TaskID: "task-1", ContinuationID: "continuation-1",
				RuntimeConfigVersionID: "config-1", RuntimeProfileID: "profile-1", RuntimePluginID: plugin.ID,
				Runner: "sandbox", APIURL: "http://host.docker.internal:8787/api", MCPURL: "http://host.docker.internal:8787/mcp",
				ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				BlackboardGraphRevision: 42, BlackboardRendererVersion: blackboard.CanonicalMainGraphRendererV1,
				BlackboardEstimatorVersion: blackboard.UTF8BytesDiv4EstimatorV1,
				BlackboardProjectionHash:   "projection-hash", BlackboardProjectionBytes: len(canonicalGraph),
				BlackboardEstimatedTokens: (len(canonicalGraph) + 3) / 4,
			}
			if err := runner.ProjectRuntimeBlackboardFiles(layout, ctx, project.Scope{}); err != nil {
				t.Fatalf("project Runtime Blackboard files: %v", err)
			}
			contextBytes, err := os.ReadFile(filepath.Join(layout.Workdir, ".pentest", "context.json"))
			if err != nil {
				t.Fatalf("read context: %v", err)
			}
			var reconstructed projectinterface.RuntimeBlackboardContextV1
			if err := json.Unmarshal(contextBytes, &reconstructed); err != nil {
				t.Fatalf("decode context: %v", err)
			}
			if reconstructed.ProtocolVersion != projectinterface.RuntimeProtocolVersion || reconstructed.ProtocolRuleDigest != wantDigest {
				t.Fatalf("protocol identity = %d/%s", reconstructed.ProtocolVersion, reconstructed.ProtocolRuleDigest)
			}
			resolvedGraph, err := os.ReadFile(filepath.Join(layout.Workdir, filepath.FromSlash(reconstructed.BlackboardPath)))
			if err != nil {
				t.Fatalf("read graph through Runtime context: %v", err)
			}
			if !bytes.Equal(resolvedGraph, canonicalGraph) {
				t.Fatalf("adapter reconstructed different graph bytes: got %q want %q", resolvedGraph, canonicalGraph)
			}
			agents, err := os.ReadFile(filepath.Join(layout.Workdir, "AGENTS.md"))
			if err != nil {
				t.Fatalf("read AGENTS.md: %v", err)
			}
			claude, err := os.ReadFile(filepath.Join(layout.Workdir, "CLAUDE.md"))
			if err != nil {
				t.Fatalf("read CLAUDE.md: %v", err)
			}
			if !bytes.Equal(agents, claude) || !bytes.Contains(agents, []byte(wantDigest)) {
				t.Fatalf("instruction projections drifted for %s", plugin.ID)
			}
		})
	}
}

func TestNativeResumeContextSupersedesHistoricalSnapshotBlocks(t *testing.T) {
	graph := []byte(`{"schema_version":1,"graph_revision":9,"nodes":[],"edges":[]}`)
	ctx := projectinterface.RuntimeBlackboardContextV1{
		ContinuationID: "continuation-9", BlackboardGraphRevision: 9,
		BlackboardRendererVersion: blackboard.CanonicalMainGraphRendererV1,
		BlackboardProjectionHash:  "current-hash", BlackboardPath: ".pentest/blackboard.json", ScopePath: ".pentest/scope.json",
	}
	resume := projectinterface.CanonicalRuntimeLaunchContext(ctx, graph, true)
	for _, required := range []string{
		"<<< CURRENT CONTINUATION SNAPSHOT >>>",
		"Older snapshot blocks in this native session are historical and MUST NOT be treated as current.",
		"revision 9, hash current-hash",
		string(graph),
	} {
		if !strings.Contains(resume, required) {
			t.Fatalf("native resume context is missing %q:\n%s", required, resume)
		}
	}
	if strings.Count(resume, string(graph)) != 1 {
		t.Fatalf("native resume context duplicated or omitted the exact graph: %s", resume)
	}
}

func TestBuiltinRuntimeLaunchAndResumeArgsCarryExactCanonicalMainGraphContext(t *testing.T) {
	canonicalGraph := []byte(`{"schema_version":1,"project_id":"project-1","project_kind":"pentest","graph_revision":42,"nodes":[],"edges":[],"frontier_node_ids":[],"current_truth_node_ids":[]}`)
	for _, plugin := range runtimeplugin.BuiltinPlugins() {
		if plugin.ID == "fake" {
			continue
		}
		plugin := plugin
		t.Run(plugin.ID, func(t *testing.T) {
			provider := runtimeprofile.Provider(plugin.ID)
			ctx := projectinterface.RuntimeBlackboardContextV1{
				ProtocolVersion: projectinterface.RuntimeProtocolVersion, ProtocolRuleDigest: projectinterface.RuntimeProtocolRuleDigest(),
				ProjectID: "project-1", TaskID: "task-1", ContinuationID: "continuation-1",
				RuntimeConfigVersionID: "config-1", RuntimeProfileID: "profile-1", RuntimePluginID: plugin.ID,
				Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				BlackboardGraphRevision: 42, BlackboardRendererVersion: blackboard.CanonicalMainGraphRendererV1,
				BlackboardEstimatorVersion: blackboard.UTF8BytesDiv4EstimatorV1,
				BlackboardProjectionHash:   "projection-hash", BlackboardProjectionBytes: len(canonicalGraph),
				BlackboardEstimatedTokens: (len(canonicalGraph) + 3) / 4,
			}
			profile := runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{Model: "test-model", Env: map[string]string{"PI_PROVIDER_ID": "test-provider"}}}
			launchContext := projectinterface.CanonicalRuntimeLaunchContext(ctx, canonicalGraph, false) + "\n\nTASK GOAL:\ncontinue the task"
			launchArgs, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
				Provider: provider, Profile: profile, Goal: launchContext,
				ConfigPath: "/task/runtime-config", MCPConfigPath: "/task/mcp-config", Sandbox: true,
			})
			if err != nil {
				t.Fatalf("render launch args: %v", err)
			}
			assertRenderedAdapterContext(t, launchArgs, launchContext, canonicalGraph)

			resumeContext := projectinterface.CanonicalRuntimeLaunchContext(ctx, canonicalGraph, true) + "\n\nTASK GOAL:\ncontinue the task"
			resumeArgs, err := adapters.BuildNativeResumeArgs(adapters.NativeResumeArgsRequest{
				Provider: provider, Profile: profile, NativeSessionID: "session-1", ResumedMessage: resumeContext,
				ConfigPath: "/task/runtime-config", MCPConfigPath: "/task/mcp-config",
			})
			if err != nil {
				t.Fatalf("render resume args: %v", err)
			}
			assertRenderedAdapterContext(t, resumeArgs, resumeContext, canonicalGraph)
		})
	}
}

func assertRenderedAdapterContext(t *testing.T, args []string, wantContext string, wantGraph []byte) {
	t.Helper()
	var rendered string
	for _, arg := range args {
		if arg == wantContext {
			rendered = arg
			break
		}
	}
	if rendered == "" {
		t.Fatalf("adapter args omit canonical launch context: %q", args)
	}
	for _, required := range []string{
		"Blackboard Runtime Protocol v1",
		projectinterface.RuntimeProtocolRuleDigest(),
		string(wantGraph),
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("rendered adapter context is missing %q", required)
		}
	}
	graphStart := strings.Index(rendered, "\n{"+`"schema_version"`)
	graphEnd := strings.Index(rendered, "\n<<< END CURRENT CONTINUATION SNAPSHOT >>>")
	if graphStart < 0 || graphEnd <= graphStart || !bytes.Equal([]byte(rendered[graphStart+1:graphEnd]), wantGraph) {
		t.Fatalf("adapter did not reconstruct exact CanonicalMainGraphV1 bytes: %q", rendered)
	}
}

func TestProjectRuntimeConfigAutoInjectsTrustedMCP(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-mcp", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model: "claude-sonnet-4",
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-mcp",
		DaemonAddr: "127.0.0.1:8787",
		Sandbox:    true,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	servers, ok := projection.Config["mcp_servers"].([]map[string]any)
	if !ok || len(servers) == 0 {
		t.Fatalf("expected mcp_servers preview, got %#v", projection.Config["mcp_servers"])
	}
	if servers[0]["name"] != "pentest" || servers[0]["url"] != "http://host.docker.internal:8787/mcp" {
		t.Fatalf("expected trusted pentest server first, got %#v", servers[0])
	}

	mcpPath := filepath.Join(layout.Workdir, ".mcp.json")
	raw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var mcpDoc struct {
		MCPServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &mcpDoc); err != nil {
		t.Fatalf("decode .mcp.json: %v", err)
	}
	pentest, ok := mcpDoc.MCPServers["pentest"]
	if !ok {
		t.Fatalf("expected pentest entry in .mcp.json, got %#v", mcpDoc.MCPServers)
	}
	if pentest.Type != "http" || pentest.URL != "http://host.docker.internal:8787/mcp" {
		t.Fatalf("unexpected pentest mcp config: %#v", pentest)
	}

	contextPath := filepath.Join(layout.Workdir, ".pentest", "context.json")
	contextRaw, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read context.json: %v", err)
	}
	var ctx map[string]string
	if err := json.Unmarshal(contextRaw, &ctx); err != nil {
		t.Fatalf("decode context.json: %v", err)
	}
	if ctx["project_id"] != "project-1" || ctx["task_id"] != "task-mcp" {
		t.Fatalf("unexpected task context: %#v", ctx)
	}
	if ctx["mcp_url"] != "http://host.docker.internal:8787/mcp" {
		t.Fatalf("unexpected mcp_url: %q", ctx["mcp_url"])
	}
}

func TestProjectClaudeSettingsAllowsCanonicalTrustedMCPTools(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-mcp-permissions", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields:   runtimeprofile.Fields{Model: "claude-sonnet-4"},
	}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-mcp-permissions",
		DaemonAddr: "127.0.0.1:8787",
	}); err != nil {
		t.Fatalf("project config: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "settings.json"))
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode Claude settings: %v", err)
	}

	want := map[string]bool{
		"mcp__pentest__blackboard_change":             true,
		"mcp__pentest__blackboard_read":               true,
		"mcp__pentest__blackboard_history":            true,
		"mcp__pentest__blackboard_retain_evidence":    true,
		"mcp__pentest__blackboard_checkpoint_attempt": true,
		"mcp__pentest__blackboard_finish":             true,
	}
	for _, allowed := range settings.Permissions.Allow {
		if !want[allowed] {
			t.Fatalf("Claude settings unexpectedly pre-authorize %q: %s", allowed, raw)
		}
		delete(want, allowed)
	}
	if len(want) != 0 {
		t.Fatalf("Claude settings do not pre-authorize canonical trusted tools %#v: %s", want, raw)
	}
}

func TestProjectCodexConfigAppendsTrustedMCPTOML(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-codex-mcp", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			Model: "gpt-5",
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
		Sandbox:    false,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	configRaw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	config := string(configRaw)
	for _, want := range []string{
		"[mcp_servers]",
		"[mcp_servers.pentest]",
		`url = "http://127.0.0.1:8787/mcp"`,
		"enabled = true",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("expected config.toml to contain %q, got:\n%s", want, config)
		}
	}

	previewTOML, ok := projection.Config["config_toml"].(string)
	if !ok || !strings.Contains(previewTOML, "[mcp_servers.pentest]") {
		t.Fatalf("expected mcp section in preview config_toml, got %#v", projection.Config["config_toml"])
	}
}

func TestProjectPiConfigWritesMCPJSON(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-mcp", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Model: "claude-sonnet-4",
		},
	}

	_, err = runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(layout.ProviderHome, "agent", "mcp.json"))
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Transport string `json:"transport"`
			URL       string `json:"url"`
			Lifecycle string `json:"lifecycle"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode mcp.json: %v", err)
	}
	pentest, ok := doc.MCPServers["pentest"]
	if !ok {
		t.Fatalf("expected pentest entry, got %#v", doc.MCPServers)
	}
	if pentest.Transport != "streamable-http" || pentest.Lifecycle != "eager" {
		t.Fatalf("unexpected pi mcp transport: %#v", pentest)
	}
	if pentest.URL != "http://127.0.0.1:8787/mcp" {
		t.Fatalf("unexpected pi mcp url: %q", pentest.URL)
	}
}

func TestTrustedMCPDisabledSkipsAutoInjection(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-disabled", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Env: map[string]string{"PENTEST_DISABLE_TRUSTED_MCP": "1"},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		DaemonAddr: "127.0.0.1:8787",
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}
	if projection.Config["mcp_servers"] != nil {
		t.Fatalf("expected no mcp_servers preview, got %#v", projection.Config["mcp_servers"])
	}
	if _, err := os.Stat(filepath.Join(layout.Workdir, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("expected .mcp.json to be absent when trusted mcp disabled, err=%v", err)
	}
}

func TestLaunchProcessEnvSetsPentestContext(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-env", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	env := runner.LaunchProcessEnv(layout, profile, true, runner.TaskContext{
		ProjectID: "project-1",
		TaskID:    "task-env",
		MCPURL:    "http://host.docker.internal:8787/mcp",
	})

	for key, want := range map[string]string{
		"IS_SANDBOX":         "1",
		"PENTEST_PROJECT_ID": "project-1",
		"PENTEST_TASK_ID":    "task-env",
		"PENTEST_MCP_URL":    "http://host.docker.internal:8787/mcp",
		"CODEX_HOME":         "/task/runtime-home/codex",
	} {
		if env[key] != want {
			t.Fatalf("expected %s=%q, got %q", key, want, env[key])
		}
	}
}

func TestTrustedMCPEmbedsAuthTokenWhenConfigured(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-auth", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-auth",
		DaemonAddr: "127.0.0.1:8787",
		AuthToken:  "secret-token",
		Sandbox:    true,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	servers, ok := projection.Config["mcp_servers"].([]map[string]any)
	if !ok || len(servers) == 0 {
		t.Fatalf("expected mcp_servers preview, got %#v", projection.Config["mcp_servers"])
	}
	wantURL := "http://host.docker.internal:8787/mcp?token=secret-token"
	if servers[0]["url"] != wantURL {
		t.Fatalf("expected trusted URL to embed token, got %q", servers[0]["url"])
	}

	env := runner.LaunchProcessEnv(layout, profile, true, runner.TaskContext{
		ProjectID: "project-1",
		TaskID:    "task-auth",
		MCPURL:    "http://host.docker.internal:8787/mcp",
		AuthToken: "secret-token",
	})
	if env["PENTEST_AUTH_TOKEN"] != "secret-token" {
		t.Fatalf("expected PENTEST_AUTH_TOKEN in sandbox env, got %q", env["PENTEST_AUTH_TOKEN"])
	}
}

// Regression: an existing trusted "pentest" profile entry (stale URL / old grant)
// must be replaced by the daemon-owned URL that embeds the current Continuation
// grant token. Other custom MCP servers must be preserved; URL coincidence with
// the daemon base must not suppress injection.
func TestTrustedMCPUpgradesExistingTrustedPentestWithContinuationToken(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-upgrade-trusted", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderClaudeCode,
		Fields: runtimeprofile.Fields{
			Model: "claude-sonnet-4",
			MCPServers: []runtimeprofile.MCPServer{
				{
					Name: "pentest",
					Mode: runtimeprofile.MCPServerTrusted,
					// Stale: daemon base without the current Continuation grant.
					URL: "http://127.0.0.1:8787/mcp?token=stale-grant",
				},
				{
					Name: "custom-tools",
					Mode: runtimeprofile.MCPServerExternal,
					URL:  "http://custom.example/mcp",
				},
				{
					// Same daemon base URL under a different name must not
					// suppress the authoritative trusted projection.
					Name: "mirror",
					Mode: runtimeprofile.MCPServerExternal,
					URL:  "http://127.0.0.1:8787/mcp",
				},
			},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-upgrade-trusted",
		DaemonAddr: "127.0.0.1:8787",
		AuthToken:  "continuation-grant-token",
		Sandbox:    false,
	})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}

	servers, ok := projection.Config["mcp_servers"].([]map[string]any)
	if !ok {
		t.Fatalf("expected mcp_servers preview, got %#v", projection.Config["mcp_servers"])
	}
	if len(servers) != 3 {
		t.Fatalf("expected upgraded pentest + two custom servers, got %#v", servers)
	}
	wantTrustedURL := "http://127.0.0.1:8787/mcp?token=continuation-grant-token"
	if servers[0]["name"] != "pentest" || servers[0]["mode"] != "trusted" || servers[0]["url"] != wantTrustedURL {
		t.Fatalf("expected authoritative trusted pentest first, got %#v", servers[0])
	}
	if url, _ := servers[0]["url"].(string); strings.Contains(url, "stale-grant") {
		t.Fatalf("stale grant token leaked into trusted URL: %#v", servers[0])
	}

	byName := map[string]map[string]any{}
	for _, server := range servers {
		name, _ := server["name"].(string)
		byName[name] = server
	}
	if byName["custom-tools"]["url"] != "http://custom.example/mcp" {
		t.Fatalf("expected custom-tools preserved, got %#v", byName["custom-tools"])
	}
	if byName["mirror"]["url"] != "http://127.0.0.1:8787/mcp" {
		t.Fatalf("expected mirror server preserved, got %#v", byName["mirror"])
	}

	mcpRaw, err := os.ReadFile(filepath.Join(layout.Workdir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	if !strings.Contains(string(mcpRaw), "continuation-grant-token") {
		t.Fatalf(".mcp.json missing current grant token: %s", mcpRaw)
	}
	if strings.Contains(string(mcpRaw), "stale-grant") {
		t.Fatalf(".mcp.json retained stale grant: %s", mcpRaw)
	}
	if !strings.Contains(string(mcpRaw), "custom.example") {
		t.Fatalf(".mcp.json dropped custom server: %s", mcpRaw)
	}
}

func TestAGENTSMDDocumentsLoopbackRewriteInSandbox(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-agents", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-agents",
		DaemonAddr: "127.0.0.1:8787",
		Sandbox:    true,
	}); err != nil {
		t.Fatalf("project config: %v", err)
	}

	agents, err := os.ReadFile(filepath.Join(layout.Workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "host.docker.internal") {
		t.Fatalf("expected sandbox AGENTS.md to document the loopback rewrite, got:\n%s", agents)
	}
	if !strings.Contains(string(agents), "do not try to reinstall") {
		t.Fatalf("expected sandbox AGENTS.md to warn against reinstalling the target, got:\n%s", agents)
	}
	if !strings.Contains(string(agents), "`.claude/skills/`") {
		t.Fatalf("expected sandbox AGENTS.md to document claude skills path, got:\n%s", agents)
	}
	if !strings.Contains(string(agents), "## Required workflow") {
		t.Fatalf("expected AGENTS.md to document required workflow, got:\n%s", agents)
	}
	if !strings.Contains(string(agents), "upsert_project_fact") ||
		!strings.Contains(string(agents), "record_vulnerability") ||
		!strings.Contains(string(agents), "submit_task_summary") {
		t.Fatalf("expected AGENTS.md to document MCP workflow tools, got:\n%s", agents)
	}
}

func TestAGENTSMDOmitsLoopbackRewriteForHostRunner(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-host", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderClaudeCode}
	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:  "project-1",
		TaskID:     "task-host",
		DaemonAddr: "127.0.0.1:8787",
		Sandbox:    false,
	}); err != nil {
		t.Fatalf("project config: %v", err)
	}

	agents, err := os.ReadFile(filepath.Join(layout.Workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if strings.Contains(string(agents), "Host-reachable targets") {
		t.Fatalf("expected host-runner AGENTS.md to omit the rewrite section, got:\n%s", agents)
	}
}

func TestBuildSandboxCommandPassesProcessEnv(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-123", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout:         layout,
		Provider:       runtimeprofile.ProviderCodex,
		RuntimeCommand: []string{"codex", "run"},
		ProcessEnv: map[string]string{
			"PENTEST_PROJECT_ID": "project-1",
			"PENTEST_MCP_URL":    "http://host.docker.internal:8787/mcp",
		},
	})
	if err != nil {
		t.Fatalf("build command: %v", err)
	}

	joined := strings.Join(command.Args, " ")
	for _, want := range []string{
		"--add-host=host.docker.internal:host-gateway",
		"-e PENTEST_PROJECT_ID=project-1",
		"-e PENTEST_MCP_URL=http://host.docker.internal:8787/mcp",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected sandbox args to contain %q, got %v", want, command.Args)
		}
	}
}
