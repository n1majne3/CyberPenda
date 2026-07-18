package runner

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
)

const trustedMCPServerName = "pentest"

// TaskContext carries identifiers runtimes need when calling trusted MCP tools.
type TaskContext struct {
	ProjectID      string
	TaskID         string
	MCPURL         string
	APIURL         string
	AuthToken      string
	InterfaceToken string
	Provider       runtimeprofile.Provider
	Sandbox        bool
	ScopeSnapshot  project.Scope
}

func taskContextFromProjection(req ProjectionRequest, provider runtimeprofile.Provider, mcpURL string) TaskContext {
	ctx := TaskContext{
		ProjectID:      req.ProjectID,
		TaskID:         req.TaskID,
		MCPURL:         mcpURL,
		AuthToken:      req.AuthToken,
		Provider:       provider,
		Sandbox:        req.Sandbox,
		ScopeSnapshot:  req.ScopeSnapshot,
	}
	return ctx
}

// APIEndpointURL returns the daemon API root reachable by a Runtime process.
func APIEndpointURL(daemonAddr string, sandbox bool) string {
	host, port := splitListenHostPort(daemonAddr)
	if sandbox {
		host = "host.docker.internal"
	} else if host == "" || host == "0.0.0.0" || host == "[::]" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s/api", host, port)
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

func collectMCPServers(profile runtimeprofile.Profile, req ProjectionRequest) ([]runtimeprofile.MCPServer, error) {
	servers := append([]runtimeprofile.MCPServer{}, profile.Fields.MCPServers...)
	if trustedMCPDisabled(profile) {
		return servers, nil
	}
	// The generated trusted server identity is always "pentest". When trusted MCP
	// is enabled, that daemon-owned entry is authoritative: replace any profile
	// trusted "pentest" with the current Continuation grant URL, reject an
	// external server using the reserved name, and always inject even when
	// another custom server happens to share the daemon base URL.
	trustedURL := MCPEndpointURL(req.DaemonAddr, req.Sandbox)
	if token := strings.TrimSpace(req.AuthToken); token != "" {
		// The runtime MCP transports cannot always attach per-request headers,
		// so the daemon accepts the token as a query parameter. Embedding it in
		// the trusted server URL authenticates every runtime without per-runtime
		// header plumbing.
		trustedURL = trustedURL + "?token=" + token
	}
	kept := make([]runtimeprofile.MCPServer, 0, len(servers))
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if !strings.EqualFold(name, trustedMCPServerName) {
			kept = append(kept, server)
			continue
		}
		if server.Mode == runtimeprofile.MCPServerTrusted {
			// Drop the stale profile entry; the generated URL below wins.
			continue
		}
		return nil, fmt.Errorf("MCP server name %q is reserved for the trusted Project Interface", trustedMCPServerName)
	}
	return append([]runtimeprofile.MCPServer{{
		Name: trustedMCPServerName,
		Mode: runtimeprofile.MCPServerTrusted,
		URL:  trustedURL,
	}}, kept...), nil
}

func claudeTrustedMCPAllowedTools(servers []runtimeprofile.MCPServer) []string {
	for _, server := range servers {
		if strings.TrimSpace(server.Name) != trustedMCPServerName || server.Mode != runtimeprofile.MCPServerTrusted {
			continue
		}
		tools := []string{
			"blackboard_change", "blackboard_read", "blackboard_history",
			"blackboard_retain_evidence", "blackboard_checkpoint_attempt", "blackboard_finish",
		}
		allowed := make([]string, 0, len(tools))
		for _, name := range tools {
			allowed = append(allowed, "mcp__"+trustedMCPServerName+"__"+name)
		}
		return allowed
	}
	return nil
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
	if err := writeOwnerOnlyFile(path, raw); err != nil {
		return fmt.Errorf("write task context: %w", err)
	}
	if err := writeTaskScopeFile(dir, ctx.ScopeSnapshot); err != nil {
		return err
	}
	if err := writeRuntimeSmokeInstructions(layout.Workdir, ctx); err != nil {
		return err
	}
	return nil
}

func writeOwnerOnlyFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-context-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func writeTaskScopeFile(dir string, scope project.Scope) error {
	raw, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task scope: %w", err)
	}
	path := filepath.Join(dir, "scope.json")
	if err := writeOwnerOnlyFile(path, raw); err != nil {
		return fmt.Errorf("write task scope: %w", err)
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
	b.WriteString("\n## Required workflow\n\n")
	b.WriteString("Use trusted MCP on every blackboard write. Do not rely on chat alone.\n\n")
	b.WriteString("1. Apply durable semantic milestones with `blackboard_change`; use `blackboard_read` and `blackboard_history` before resolving version conflicts.\n")
	b.WriteString("2. Retain reproducible proof with `blackboard_retain_evidence`.\n")
	b.WriteString("3. Checkpoint meaningful Attempt progress with `blackboard_checkpoint_attempt`.\n")
	b.WriteString("4. Before ending a continuation, call `blackboard_finish` after every Attempt is terminal.\n")
	b.WriteString("5. For black-box web targets, discover APIs with `curl`/httpx first (including frontend bundles); use `agent-browser` when you need DOM, cookies, or interactive flows.\n")
	b.WriteString("\n## Authorized scope\n\n")
	b.WriteString("Read `.pentest/scope.json` for the task scope snapshot captured at launch. ")
	b.WriteString("Stay within listed domains, URLs, IPs, ports, exclusions, and testing limits. ")
	b.WriteString("Do not test assets outside this scope until an operator updates the Scope.\n")
	if ctx.Sandbox {
		b.WriteString("\n## Sandbox skills and browser\n\n")
		fmt.Fprintf(&b, "Enabled task skills are linked at `%s/` and materialized under the task-local skills root.\n", SkillsWorkdirRelPath(ctx.Provider))
		b.WriteString("For web testing, read the `agent-browser` skill and use the `agent-browser` CLI.\n")
		b.WriteString("\n## Host-reachable targets\n\n")
		b.WriteString("Loopback targets (`127.0.0.1`, `localhost`) in your task goal have been rewritten to `host.docker.internal` so you can reach services running on the host. Use the `host.docker.internal` addresses exactly as given; do not try to reinstall or relaunch the target service yourself.\n")
	}
	path := filepath.Join(workdir, "AGENTS.md")
	return writeOwnerOnlyFile(path, []byte(b.String()))
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
