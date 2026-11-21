package runner

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"pentest/internal/runtimeprofile"
)

func projectClaudeSettings(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	env, err := buildClaudeEnv(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}

	mcpServers, err := collectMCPServers(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	mcpURL := MCPEndpointURL(req.DaemonAddr, req.Sandbox)
	if err := writeProjectionContextFiles(layout, req, profile.Provider, mcpURL); err != nil {
		return ConfigProjection{}, err
	}
	if len(mcpServers) > 0 {
		if err := writeClaudeMCPConfig(layout.Workdir, mcpServers); err != nil {
			return ConfigProjection{}, err
		}
	}
	// The same Execute agent definition projected for Pi seeds Claude Code's
	// project agents directory, so orchestrator dispatch keeps one
	// CyberPenda-owned source across both runtimes.
	executeAgentPath := filepath.Join(layout.Workdir, ".claude", "agents", "execute.md")
	if err := writeOwnerOnlyFile(executeAgentPath, piExecuteAgentMarkdown); err != nil {
		return ConfigProjection{}, fmt.Errorf("write claude execute agent type: %w", err)
	}

	settings := map[string]any{"env": env}
	// Catalog-sourced plugins (install refs from claude-plugins-official) are
	// installed and enabled by Claude Code when listed under enabledPlugins.
	installRefs := enabledExtensionInstallRefs(profile)
	if len(installRefs) > 0 {
		enabled := make(map[string]bool, len(installRefs))
		for _, ref := range installRefs {
			enabled[ref] = true
		}
		settings["enabledPlugins"] = enabled
	}
	settings, err = applyConfigOverlay(profile.Provider, settings, profile.Fields.CustomConfigFile)
	if err != nil {
		return ConfigProjection{}, fmt.Errorf("apply custom config file: %w", err)
	}
	if overlayPlugins, ok := settings["enabledPlugins"].(map[string]any); ok {
		previewRefs := make([]string, 0, len(installRefs))
		previewRefs = append(previewRefs, installRefs...)
		for ref, value := range overlayPlugins {
			enabled, isBool := value.(bool)
			if isBool && enabled && !slices.Contains(previewRefs, ref) {
				previewRefs = append(previewRefs, ref)
			}
		}
		sort.Strings(previewRefs)
		installRefs = previewRefs
	}
	settingsPath := filepath.Join(layout.ProviderHome, "settings.json")
	if err := writeJSONConfigFile(settingsPath, settings); err != nil {
		return ConfigProjection{}, err
	}

	preview := map[string]any{
		"provider":       string(profile.Provider),
		"settings_path":  settingsPath,
		"env":            redactEnvMap(env),
		"subagent_types": []string{"execute"},
	}
	if profile.Fields.Model != "" {
		preview["model"] = profile.Fields.Model
	}
	if profile.Fields.Endpoint != "" {
		preview["endpoint"] = profile.Fields.Endpoint
	}
	if len(profile.Fields.CredentialRefs) > 0 {
		preview["credential_refs"] = profile.Fields.CredentialRefs
	}
	if profile.Fields.DefaultRunner != "" {
		preview["default_runner"] = profile.Fields.DefaultRunner
	}
	if servers := mcpPreview(mcpServers); len(servers) > 0 {
		preview["mcp_servers"] = servers
		preview["mcp_config_path"] = filepath.Join(layout.Workdir, ".mcp.json")
	}
	if len(installRefs) > 0 {
		preview["enabled_plugins"] = installRefs
	}
	addModelSnapshotPreview(preview, req.ModelSnapshot)

	return ConfigProjection{ConfigPath: settingsPath, Config: preview}, nil
}

// ClaudeProcessEnv returns env vars that must be present on the Claude process.
// settings.json alone is not enough for sandbox login detection.
func ClaudeProcessEnv(profile runtimeprofile.Profile, req ProjectionRequest) (map[string]string, error) {
	return buildClaudeEnv(profile, req)
}

func buildClaudeEnv(profile runtimeprofile.Profile, req ProjectionRequest) (map[string]string, error) {
	env := map[string]string{}
	for key, value := range profile.Fields.Env {
		env[key] = value
	}
	if profile.Fields.Endpoint != "" && env["ANTHROPIC_BASE_URL"] == "" {
		env["ANTHROPIC_BASE_URL"] = profile.Fields.Endpoint
	}
	if profile.Fields.Model != "" && env["ANTHROPIC_MODEL"] == "" {
		env["ANTHROPIC_MODEL"] = profile.Fields.Model
	}

	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return nil, err
	}
	for key, value := range materialized {
		env[key] = value
	}
	limits := resolveLaunchModelLimits(profile, req)
	if limits.ContextWindow >= 1 && strings.TrimSpace(env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]) == "" {
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(limits.ContextWindow)
	}
	if limits.MaxOutputTokens >= 1 && strings.TrimSpace(env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"]) == "" {
		env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = strconv.Itoa(limits.MaxOutputTokens)
	}
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		value, _ := resolveModelProviderAPIKeyValue(req.ModelSnapshot.APIKeyEnv, req)
		if value != "" {
			env[req.ModelSnapshot.APIKeyEnv] = value
			if env["ANTHROPIC_API_KEY"] == "" {
				env["ANTHROPIC_API_KEY"] = value
			}
			if env["ANTHROPIC_AUTH_TOKEN"] == "" {
				env["ANTHROPIC_AUTH_TOKEN"] = value
			}
		}
	}
	return env, nil
}

func redactEnvMap(env map[string]string) map[string]any {
	out := make(map[string]any, len(env))
	for key, value := range env {
		if secretEnvKeyPattern.MatchString(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = value
	}
	return out
}
