package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/runner"
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
