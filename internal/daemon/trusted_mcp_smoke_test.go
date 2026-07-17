package daemon_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"pentest/internal/daemon"
	"pentest/internal/runtimeprofile"
)

// TestTrustedMCPProjectionSmoke proves task launch keeps the trusted MCP shell
// reachable for every provider without exposing the retired v1 tool catalog.
func TestTrustedMCPProjectionSmoke(t *testing.T) {
	providers := []struct {
		name            string
		provider        runtimeprofile.Provider
		compactV2Launch bool
		networklessV2   bool
		mcpConfigPath   string
		verify          func(t *testing.T, layoutRoot string)
	}{
		{
			name:            "claude_code",
			provider:        runtimeprofile.ProviderClaudeCode,
			compactV2Launch: true,
			mcpConfigPath:   filepath.Join("workdir", ".mcp.json"),
			verify: func(t *testing.T, layoutRoot string) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(layoutRoot, "workdir", ".mcp.json"))
				if err != nil {
					t.Fatalf("read .mcp.json: %v", err)
				}
				if !strings.Contains(string(raw), `"pentest"`) || !strings.Contains(string(raw), `"type": "http"`) {
					t.Fatalf("expected claude mcp config, got %s", string(raw))
				}
				if !strings.Contains(string(raw), "/mcp?token=") {
					t.Fatalf("expected Claude trusted MCP grant URL, got %s", string(raw))
				}
				settingsRaw, err := os.ReadFile(filepath.Join(layoutRoot, "runtime-home", "claude", "settings.json"))
				if err != nil {
					t.Fatalf("read Claude settings: %v", err)
				}
				for _, tool := range []string{
					"mcp__pentest__blackboard_change", "mcp__pentest__blackboard_read",
					"mcp__pentest__blackboard_history", "mcp__pentest__blackboard_retain_evidence",
					"mcp__pentest__blackboard_checkpoint_attempt", "mcp__pentest__blackboard_finish",
				} {
					if !strings.Contains(string(settingsRaw), tool) {
						t.Fatalf("Claude settings missing trusted allowlist entry %q: %s", tool, settingsRaw)
					}
				}
			},
		},
		{
			name:            "codex",
			provider:        runtimeprofile.ProviderCodex,
			compactV2Launch: true,
			networklessV2:   true,
			verify: func(t *testing.T, layoutRoot string) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(layoutRoot, "runtime-home", "codex", "config.toml"))
				if err != nil {
					t.Fatalf("read config.toml: %v", err)
				}
				config := string(raw)
				for _, forbidden := range []string{"[mcp_servers.pentest]", "token=", "/mcp"} {
					if strings.Contains(config, forbidden) {
						t.Fatalf("Codex v2 config retained network credential surface %q:\n%s", forbidden, config)
					}
				}
			},
		},
		{
			name:            "pi",
			provider:        runtimeprofile.ProviderPi,
			compactV2Launch: true,
			mcpConfigPath:   filepath.Join("runtime-home", "pi", "agent", "mcp.json"),
			verify: func(t *testing.T, layoutRoot string) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(layoutRoot, "runtime-home", "pi", "agent", "mcp.json"))
				if err != nil {
					t.Fatalf("read mcp.json: %v", err)
				}
				if !strings.Contains(string(raw), `"pentest"`) || !strings.Contains(string(raw), `"streamable-http"`) {
					t.Fatalf("expected pi mcp config, got %s", string(raw))
				}
				if !strings.Contains(string(raw), "/mcp?token=") {
					t.Fatalf("expected Pi trusted MCP grant URL, got %s", string(raw))
				}
			},
		},
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			daemonBase, daemonServer, runtimeRoot := startDaemonWithHTTP(t)
			projectID := createProject(t, daemonServer, `{
				"name":"MCP Smoke",
				"scope":{"domains":["example.com"]}
			}`)
			profileID := createLocalRuntimeProfile(t, daemonServer, tc.name+" smoke", tc.provider, runtimeprofile.Fields{
				Model:   "smoke-model",
				APIKeys: map[string]string{"SMOKE_API_KEY": "sk-smoke-test"},
			})

			taskID := createTask(t, daemonServer, projectID, `{
				"goal":"write recon fact via trusted mcp",
				"runtime_profile_id":`+quoteJSON(profileID)+`,
				"runner":"sandbox"
			}`)

			layoutRoot := filepath.Join(runtimeRoot, taskID)
			tc.verify(t, layoutRoot)

			mcpURL := daemonBase + "/mcp"
			if tc.compactV2Launch {
				if _, err := os.Stat(filepath.Join(layoutRoot, "workdir", ".pentest", "context.json")); !os.IsNotExist(err) {
					t.Fatalf("v2 launch exposed legacy identity context: %v", err)
				}
				if tc.mcpConfigPath != "" {
					mcpURL = normalizeMCPURLForHost(readProjectedTrustedMCPURL(t, filepath.Join(layoutRoot, tc.mcpConfigPath)), daemonBase)
				}
			} else {
				ctx := readTaskMCPContext(t, layoutRoot)
				if ctx.ProjectID != projectID || ctx.TaskID != taskID {
					t.Fatalf("unexpected task context: %#v", ctx)
				}
				if !strings.Contains(ctx.MCPURL, "/mcp") {
					t.Fatalf("expected mcp url in context, got %q", ctx.MCPURL)
				}
				mcpURL = normalizeMCPURLForHost(ctx.MCPURL, daemonBase)
			}
			if !tc.networklessV2 {
				assertMCPBootstrapHasNoLegacyTools(t, mcpURL)
			}
		})
	}
}

type taskMCPContext struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	MCPURL    string `json:"mcp_url"`
}

func startDaemonWithHTTP(t *testing.T) (httpBase string, server *daemon.Server, runtimeRoot string) {
	t.Helper()

	runtimeRoot = t.TempDir()
	containerCLI := filepath.Join(t.TempDir(), "fake-docker")
	if err := os.WriteFile(containerCLI, []byte("#!/bin/sh\necho sandbox-command:$*\n"), 0o700); err != nil {
		t.Fatalf("write fake container cli: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()

	server, err = daemon.NewServer(daemon.Config{
		Version:      "test-version",
		DBPath:       filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:  runtimeRoot,
		SandboxImage: "pentest-kali:smoke",
		ContainerCLI: containerCLI,
		ListenAddr:   addr,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	httpServer := &http.Server{Handler: server}
	go func() {
		_ = httpServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = httpServer.Close()
		_ = server.Close()
	})

	u, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatalf("parse daemon url: %v", err)
	}
	return u.String(), server, runtimeRoot
}

func readTaskMCPContext(t *testing.T, layoutRoot string) taskMCPContext {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(layoutRoot, "workdir", ".pentest", "context.json"))
	if err != nil {
		t.Fatalf("read context.json: %v", err)
	}
	var ctx taskMCPContext
	if err := json.Unmarshal(raw, &ctx); err != nil {
		t.Fatalf("decode context.json: %v", err)
	}
	return ctx
}

func readProjectedTrustedMCPURL(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read projected MCP config: %v", err)
	}
	var doc struct {
		MCPServers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode projected MCP config: %v", err)
	}
	server, ok := doc.MCPServers["pentest"]
	if !ok || strings.TrimSpace(server.URL) == "" {
		t.Fatalf("projected MCP config missing trusted pentest URL: %s", raw)
	}
	return server.URL
}

// normalizeMCPURLForHost rewrites sandbox-only hostnames so the test process on
// the host can reach the daemon MCP endpoint.
func normalizeMCPURLForHost(projectedURL, daemonBase string) string {
	projected, err := url.Parse(projectedURL)
	if err != nil {
		return projectedURL
	}
	daemon, err := url.Parse(daemonBase)
	if err != nil {
		return projectedURL
	}
	if projected.Hostname() == "host.docker.internal" {
		projected.Host = net.JoinHostPort(daemon.Hostname(), projected.Port())
	}
	return projected.String()
}

func assertMCPBootstrapHasNoLegacyTools(t *testing.T, endpoint string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "pentest-smoke", Version: "test"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("mcp connect %s: %v", endpoint, err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list v2 bootstrap MCP tools: %v", err)
	}
	want := map[string]bool{
		"blackboard_change": true, "blackboard_read": true, "blackboard_history": true,
		"blackboard_retain_evidence": true, "blackboard_checkpoint_attempt": true, "blackboard_finish": true,
	}
	if len(listed.Tools) != len(want) {
		t.Fatalf("v2 bootstrap MCP tools = %#v, want exactly the six trusted v2 tools", listed.Tools)
	}
	for _, tool := range listed.Tools {
		if !want[tool.Name] {
			t.Fatalf("v2 bootstrap MCP exposed unexpected tool %q", tool.Name)
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("v2 bootstrap MCP missing tools %#v", want)
	}
}
