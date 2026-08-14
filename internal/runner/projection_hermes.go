package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeprofile"
)

func projectHermesHome(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	projected, err := listPiLaunchReadyProviders(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	if len(projected) > 0 {
		materialized, err = mergePiProjectedCredentials(materialized, projected, req)
		if err != nil {
			return ConfigProjection{}, err
		}
	}

	mcpServers, err := collectMCPServers(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	mcpURL := MCPEndpointURL(req.DaemonAddr, req.Sandbox)
	if err := writeTaskContextFiles(layout, taskContextFromProjection(req, profile.Provider, mcpURL)); err != nil {
		return ConfigProjection{}, err
	}

	if err := os.MkdirAll(layout.ProviderHome, 0o700); err != nil {
		return ConfigProjection{}, fmt.Errorf("prepare hermes home: %w", err)
	}
	if err := ensureHermesSkillsDir(layout.ProviderHome); err != nil {
		return ConfigProjection{}, err
	}

	configPath := filepath.Join(layout.ProviderHome, "config.yaml")
	configYAML := buildHermesConfigYAML(profile, projected, mcpServers)
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		return ConfigProjection{}, fmt.Errorf("write hermes config: %w", err)
	}

	envPath := filepath.Join(layout.ProviderHome, ".env")
	if err := writeHermesEnvFile(envPath, materialized); err != nil {
		return ConfigProjection{}, err
	}

	preview := map[string]any{
		"provider":    string(profile.Provider),
		"config_path": configPath,
		"env_path":    envPath,
	}
	if profile.Fields.Model != "" {
		preview["model"] = profile.Fields.Model
	}
	if profile.Fields.Endpoint != "" {
		preview["endpoint"] = profile.Fields.Endpoint
	}
	if servers := mcpPreview(mcpServers); len(servers) > 0 {
		preview["mcp_servers"] = servers
	}
	if piGlobalProjectionAvailable(req) {
		ids := make([]string, 0, len(projected))
		for _, entry := range projected {
			ids = append(ids, entry.Provider.ID)
		}
		preview["projected_model_provider_ids"] = ids
	}
	addModelSnapshotPreview(preview, req.ModelSnapshot)
	return ConfigProjection{ConfigPath: configPath, Config: preview}, nil
}

// ensureHermesSkillsDir leaves a PrepareSandboxSkills symlink in place. MkdirAll
// follows a broken host-side /task/skills link and then fails with EEXIST.
func ensureHermesSkillsDir(providerHome string) error {
	skillsDir := filepath.Join(providerHome, "skills")
	if _, err := os.Lstat(skillsDir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("prepare hermes skills dir: %w", err)
	}
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		return fmt.Errorf("prepare hermes skills dir: %w", err)
	}
	return nil
}

func buildHermesConfigYAML(profile runtimeprofile.Profile, projected []piProjectedProvider, servers []runtimeprofile.MCPServer) string {
	var b strings.Builder
	b.WriteString("approvals:\n")
	b.WriteString("  mode: off\n")
	b.WriteString("model:\n")
	// Bare provider: custom ignores providers.*.api_key_env and sends
	// "no-key-required" to non-OpenAI hosts, which returns HTTP 401.
	providerName := "custom"
	if selected := hermesSelectedProvider(profile, projected); selected != nil {
		if id := strings.TrimSpace(selected.Provider.ID); id != "" {
			if strings.HasPrefix(id, "custom:") {
				providerName = id
			} else {
				providerName = "custom:" + id
			}
		}
	}
	fmt.Fprintf(&b, "  provider: %s\n", yamlScalar(providerName))
	if model := strings.TrimSpace(profile.Fields.Model); model != "" {
		fmt.Fprintf(&b, "  default: %s\n", yamlScalar(model))
	} else if selected := hermesSelectedProvider(profile, projected); selected != nil && len(selected.Models) > 0 {
		fmt.Fprintf(&b, "  default: %s\n", yamlScalar(selected.Models[0]))
	}
	if endpoint := strings.TrimSpace(profile.Fields.Endpoint); endpoint != "" {
		fmt.Fprintf(&b, "  base_url: %s\n", yamlScalar(endpoint))
	} else if selected := hermesSelectedProvider(profile, projected); selected != nil {
		fmt.Fprintf(&b, "  base_url: %s\n", yamlScalar(selected.Endpoint.BaseURL))
	}
	fmt.Fprintf(&b, "  api_mode: %s\n", yamlScalar(hermesAPIMode(hermesSelectedProtocol(profile, projected))))
	if len(projected) > 0 {
		b.WriteString("providers:\n")
		for _, entry := range projected {
			fmt.Fprintf(&b, "  %s:\n", yamlScalar(entry.Provider.ID))
			fmt.Fprintf(&b, "    base_url: %s\n", yamlScalar(entry.Endpoint.BaseURL))
			fmt.Fprintf(&b, "    api_mode: %s\n", yamlScalar(hermesAPIMode(entry.Protocol)))
			fmt.Fprintf(&b, "    key_env: %s\n", yamlScalar(entry.Provider.APIKeyEnv))
		}
	}
	b.WriteString("terminal:\n")
	b.WriteString("  backend: local\n")
	if written := writeHermesMCPYAML(&b, servers); written {
		_ = written
	}
	return b.String()
}

func writeHermesMCPYAML(b *strings.Builder, servers []runtimeprofile.MCPServer) bool {
	entries := 0
	for _, server := range servers {
		if strings.TrimSpace(server.Name) == "" {
			continue
		}
		if strings.TrimSpace(server.URL) == "" && strings.TrimSpace(server.Command) == "" {
			continue
		}
		entries++
	}
	if entries == 0 {
		return false
	}
	b.WriteString("mcp_servers:\n")
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		if url := strings.TrimSpace(server.URL); url != "" {
			fmt.Fprintf(b, "  %s:\n", yamlScalar(name))
			fmt.Fprintf(b, "    url: %s\n", yamlScalar(url))
			continue
		}
		if command := strings.TrimSpace(server.Command); command != "" {
			fmt.Fprintf(b, "  %s:\n", yamlScalar(name))
			fmt.Fprintf(b, "    command: %s\n", yamlScalar(command))
			for _, arg := range server.Args {
				fmt.Fprintf(b, "    - %s\n", yamlScalar(arg))
			}
		}
	}
	return true
}

func writeHermesEnvFile(path string, materialized map[string]string) error {
	keys := make([]string, 0, len(materialized))
	for key, value := range materialized {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "%s=%s\n", key, materialized[key])
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write hermes env: %w", err)
	}
	return nil
}

func hermesSelectedProvider(profile runtimeprofile.Profile, projected []piProjectedProvider) *piProjectedProvider {
	want := strings.TrimSpace(profile.Fields.ModelProviderID)
	for i := range projected {
		if projected[i].Provider.ID == want {
			return &projected[i]
		}
	}
	if len(projected) > 0 {
		return &projected[0]
	}
	return nil
}

func hermesSelectedProtocol(profile runtimeprofile.Profile, projected []piProjectedProvider) modelprovider.Protocol {
	if selected := hermesSelectedProvider(profile, projected); selected != nil {
		return selected.Protocol
	}
	return modelprovider.ProtocolOpenAIChatCompletions
}

func hermesAPIMode(protocol modelprovider.Protocol) string {
	switch protocol {
	case modelprovider.ProtocolOpenAIResponses:
		return "responses"
	case modelprovider.ProtocolAnthropicMessages:
		return "messages"
	default:
		return "chat_completions"
	}
}

func yamlScalar(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, ":#{}[]&*!|>'\"%@` \t\n") {
		return fmt.Sprintf("%q", value)
	}
	return value
}
