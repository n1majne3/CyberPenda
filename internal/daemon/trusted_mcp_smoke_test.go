package daemon_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

// TestTrustedMCPProjectionSmoke proves task launch projects trusted MCP config
// for each supported provider and a runtime can upsert facts through the HTTP
// MCP endpoint using the projected task context.
func TestTrustedMCPProjectionSmoke(t *testing.T) {
	providers := []struct {
		name     string
		provider runtimeprofile.Provider
		verify   func(t *testing.T, layoutRoot string)
	}{
		{
			name:     "claude_code",
			provider: runtimeprofile.ProviderClaudeCode,
			verify: func(t *testing.T, layoutRoot string) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(layoutRoot, "workdir", ".mcp.json"))
				if err != nil {
					t.Fatalf("read .mcp.json: %v", err)
				}
				if !strings.Contains(string(raw), `"pentest"`) || !strings.Contains(string(raw), `"type": "http"`) {
					t.Fatalf("expected claude mcp config, got %s", string(raw))
				}
			},
		},
		{
			name:     "codex",
			provider: runtimeprofile.ProviderCodex,
			verify: func(t *testing.T, layoutRoot string) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(layoutRoot, "runtime-home", "codex", "config.toml"))
				if err != nil {
					t.Fatalf("read config.toml: %v", err)
				}
				config := string(raw)
				for _, want := range []string{"[mcp_servers.pentest]", "enabled = true"} {
					if !strings.Contains(config, want) {
						t.Fatalf("expected config.toml to contain %q, got:\n%s", want, config)
					}
				}
			},
		},
		{
			name:     "pi",
			provider: runtimeprofile.ProviderPi,
			verify: func(t *testing.T, layoutRoot string) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(layoutRoot, "runtime-home", "pi", "agent", "mcp.json"))
				if err != nil {
					t.Fatalf("read mcp.json: %v", err)
				}
				if !strings.Contains(string(raw), `"pentest"`) || !strings.Contains(string(raw), `"streamable-http"`) {
					t.Fatalf("expected pi mcp config, got %s", string(raw))
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

			ctx := readTaskMCPContext(t, layoutRoot)
			if ctx.ProjectID != projectID || ctx.TaskID != taskID {
				t.Fatalf("unexpected task context: %#v", ctx)
			}
			if !strings.Contains(ctx.MCPURL, "/mcp") {
				t.Fatalf("expected mcp url in context, got %q", ctx.MCPURL)
			}

			mcpURL := normalizeMCPURLForHost(ctx.MCPURL, daemonBase)
			callMCPUpsertFact(t, mcpURL, projectID, "mcp-smoke:"+tc.name, "trusted mcp smoke for "+tc.name)

			waitForFactSummary(t, daemonServer, projectID, "mcp-smoke:"+tc.name, "trusted mcp smoke for "+tc.name)
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

func callMCPUpsertFact(t *testing.T, endpoint, projectID, factKey, summary string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "pentest-smoke", Version: "test"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("mcp connect %s: %v", endpoint, err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "upsert_project_fact",
		Arguments: map[string]any{
			"project_id":   projectID,
			"fact_key":     factKey,
			"category":     "recon",
			"summary":      summary,
			"body":         "written by trusted mcp smoke test",
			"confidence":   "confirmed",
			"scope_status": "in_scope",
		},
	})
	if err != nil {
		t.Fatalf("upsert_project_fact: %v", err)
	}
	if res.IsError {
		t.Fatalf("upsert_project_fact returned error: %#v", res)
	}
}

func waitForFactSummary(t *testing.T, server *daemon.Server, projectID, factKey, wantSummary string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/facts/"+factKey, nil)
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code == http.StatusOK {
			var fact struct {
				Summary string `json:"summary"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&fact); err == nil && fact.Summary == wantSummary {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for fact %q summary %q", factKey, wantSummary)
}