package runner

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/runtimeprofile"
)

const trustedMCPServerName = "pentest"

// TaskContext carries identifiers runtimes need when calling trusted MCP tools.
type TaskContext struct {
	ProjectID string
	TaskID    string
	MCPURL    string
	Sandbox   bool
}

// MCPEndpointURL returns the MCP HTTP endpoint reachable from the runtime process.
func MCPEndpointURL(daemonAddr string, sandbox bool) string {
	host, port := splitListenHostPort(daemonAddr)
	if sandbox {
		host = "host.docker.internal"
	} else if host == "" || host == "0.0.0.0" || host == "[::]" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s/mcp", host, port)
}

func splitListenHostPort(addr string) (host, port string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "127.0.0.1", "8787"
	}
	if strings.HasPrefix(addr, "[") {
		if h, p, err := net.SplitHostPort(addr); err == nil {
			return strings.Trim(h, "[]"), p
		}
	}
	if h, p, err := net.SplitHostPort(addr); err == nil {
		return h, p
	}
	return addr, "8787"
}

func trustedMCPDisabled(profile runtimeprofile.Profile) bool {
	value := strings.TrimSpace(profile.Fields.Env["PENTEST_DISABLE_TRUSTED_MCP"])
	return strings.EqualFold(value, "1") || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func collectMCPServers(profile runtimeprofile.Profile, req ProjectionRequest) []runtimeprofile.MCPServer {
	servers := append([]runtimeprofile.MCPServer{}, profile.Fields.MCPServers...)
	if trustedMCPDisabled(profile) {
		return servers
	}
	trustedURL := MCPEndpointURL(req.DaemonAddr, req.Sandbox)
	if hasMCPServerURL(servers, trustedURL) {
		return servers
	}
	return append([]runtimeprofile.MCPServer{{
		Name: trustedMCPServerName,
		Mode: runtimeprofile.MCPServerTrusted,
		URL:  trustedURL,
	}}, servers...)
}

func hasMCPServerURL(servers []runtimeprofile.MCPServer, url string) bool {
	normalized := strings.TrimRight(strings.TrimSpace(url), "/")
	for _, server := range servers {
		if strings.TrimRight(strings.TrimSpace(server.URL), "/") == normalized {
			return true
		}
	}
	return false
}

func writeTaskContextFiles(layout Layout, ctx TaskContext) error {
	if strings.TrimSpace(ctx.ProjectID) == "" && strings.TrimSpace(ctx.TaskID) == "" {
		return nil
	}
	dir := filepath.Join(layout.Workdir, ".pentest")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("prepare task context dir: %w", err)
	}
	payload := map[string]string{}
	if ctx.ProjectID != "" {
		payload["project_id"] = ctx.ProjectID
	}
	if ctx.TaskID != "" {
		payload["task_id"] = ctx.TaskID
	}
	if ctx.MCPURL != "" {
		payload["mcp_url"] = ctx.MCPURL
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task context: %w", err)
	}
	path := filepath.Join(dir, "context.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write task context: %w", err)
	}
	if err := writeRuntimeSmokeInstructions(layout.Workdir, ctx); err != nil {
		return err
	}
	return nil
}

func writeRuntimeSmokeInstructions(workdir string, ctx TaskContext) error {
	if strings.TrimSpace(ctx.ProjectID) == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("# Pentest task context\n\n")
	b.WriteString("Trusted MCP is pre-configured for this task. Use these identifiers on every pentest MCP tool call:\n\n")
	fmt.Fprintf(&b, "- project_id: `%s`\n", ctx.ProjectID)
	if ctx.TaskID != "" {
		fmt.Fprintf(&b, "- task_id: `%s`\n", ctx.TaskID)
	}
	if ctx.MCPURL != "" {
		fmt.Fprintf(&b, "- mcp_url: `%s`\n", ctx.MCPURL)
	}
	b.WriteString("\nRead `.pentest/context.json` or env vars `PENTEST_PROJECT_ID`, `PENTEST_TASK_ID`, `PENTEST_MCP_URL` if needed.\n")
	if ctx.Sandbox {
		b.WriteString("\n## Sandbox skills and browser\n\n")
		b.WriteString("Enabled task skills are linked at `.agents/skills/` and materialized under the task-local skills root.\n")
		b.WriteString("For web testing, read the `agent-browser` skill and use the `agent-browser` CLI.\n")
		b.WriteString("\n## Host-reachable targets\n\n")
		b.WriteString("Loopback targets (`127.0.0.1`, `localhost`) in your task goal have been rewritten to `host.docker.internal` so you can reach services running on the host. Use the `host.docker.internal` addresses exactly as given; do not try to reinstall or relaunch the target service yourself.\n")
	}
	path := filepath.Join(workdir, "AGENTS.md")
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func writeClaudeMCPConfig(workdir string, servers []runtimeprofile.MCPServer) error {
	doc := map[string]any{"mcpServers": claudeMCPServers(servers)}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode claude mcp config: %w", err)
	}
	path := filepath.Join(workdir, ".mcp.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write claude mcp config: %w", err)
	}
	return nil
}

func claudeMCPServers(servers []runtimeprofile.MCPServer) map[string]any {
	out := make(map[string]any, len(servers))
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		entry := map[string]any{}
		if url := strings.TrimSpace(server.URL); url != "" {
			entry["type"] = "http"
			entry["url"] = url
		} else if command := strings.TrimSpace(server.Command); command != "" {
			entry["command"] = command
			if len(server.Args) > 0 {
				entry["args"] = server.Args
			}
			if len(server.Env) > 0 {
				entry["env"] = server.Env
			}
		} else {
			continue
		}
		out[name] = entry
	}
	return out
}

func writePiMCPConfig(agentDir string, servers []runtimeprofile.MCPServer) error {
	doc := map[string]any{"mcpServers": piMCPServers(servers)}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pi mcp config: %w", err)
	}
	path := filepath.Join(agentDir, "mcp.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write pi mcp config: %w", err)
	}
	return nil
}

func piMCPServers(servers []runtimeprofile.MCPServer) map[string]any {
	out := make(map[string]any, len(servers))
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		entry := map[string]any{}
		if url := strings.TrimSpace(server.URL); url != "" {
			entry["transport"] = "streamable-http"
			entry["url"] = url
			entry["lifecycle"] = "eager"
		} else if command := strings.TrimSpace(server.Command); command != "" {
			entry["transport"] = "stdio"
			entry["command"] = command
			if len(server.Args) > 0 {
				entry["args"] = server.Args
			}
			if len(server.Env) > 0 {
				entry["env"] = server.Env
			}
			entry["lifecycle"] = "eager"
		} else {
			continue
		}
		out[name] = entry
	}
	return out
}

func appendCodexMCPTOML(builder *strings.Builder, servers []runtimeprofile.MCPServer) {
	if len(servers) == 0 {
		return
	}
	builder.WriteString("\n[mcp_servers]\n")
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		fmt.Fprintf(builder, "\n[mcp_servers.%s]\n", name)
		if url := strings.TrimSpace(server.URL); url != "" {
			fmt.Fprintf(builder, "url = %q\n", url)
			fmt.Fprintf(builder, "enabled = true\n")
			continue
		}
		if command := strings.TrimSpace(server.Command); command != "" {
			fmt.Fprintf(builder, "command = %q\n", command)
			if len(server.Args) > 0 {
				fmt.Fprintf(builder, "args = %s\n", formatTOMLStringArray(server.Args))
			}
			if len(server.Env) > 0 {
				fmt.Fprintf(builder, "\n[mcp_servers.%s.env]\n", name)
				for key, value := range server.Env {
					fmt.Fprintf(builder, "%s = %q\n", key, value)
				}
			}
			fmt.Fprintf(builder, "enabled = true\n")
		}
	}
}

func formatTOMLStringArray(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// LaunchMCPConfigPath returns the Claude Code --mcp-config path when trusted MCP
// is projected for a task.
func LaunchMCPConfigPath(layout Layout, provider runtimeprofile.Provider, sandbox bool, projection ConfigProjection) string {
	if provider != runtimeprofile.ProviderClaudeCode {
		return ""
	}
	if projection.Config["mcp_servers"] == nil {
		return ""
	}
	hostPath := filepath.Join(layout.Workdir, ".mcp.json")
	return LaunchConfigPath(layout, provider, hostPath, sandbox)
}

func mcpPreview(servers []runtimeprofile.MCPServer) []map[string]any {
	if len(servers) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(servers))
	for _, server := range servers {
		entry := map[string]any{
			"name": server.Name,
			"mode": string(server.Mode),
		}
		if server.URL != "" {
			entry["url"] = server.URL
		}
		if server.Command != "" {
			entry["command"] = server.Command
		}
		if len(server.Args) > 0 {
			entry["args"] = server.Args
		}
		out = append(out, entry)
	}
	return out
}
