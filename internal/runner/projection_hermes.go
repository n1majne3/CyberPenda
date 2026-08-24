package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

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

	if err := writeHermesIterationBudgetPlugin(layout.ProviderHome); err != nil {
		return ConfigProjection{}, err
	}

	effort, err := runtimeprofile.NormalizeReasoningEffort(profile.Fields.ReasoningEffort)
	if err != nil {
		return ConfigProjection{}, err
	}

	configPath := filepath.Join(layout.ProviderHome, "config.yaml")
	configYAML, err := applyHermesConfigOverlay(profile, buildHermesConfigYAML(profile, projected, mcpServers, string(effort)))
	if err != nil {
		return ConfigProjection{}, err
	}
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

// applyHermesConfigOverlay deep-merges the Custom Config File over the
// generated Hermes config.yaml text and re-encodes YAML with comments lost
// only where the overlay rewrote the subtree. Structured keys always win.
func applyHermesConfigOverlay(profile runtimeprofile.Profile, generatedYAML string) (string, error) {
	if strings.TrimSpace(profile.Fields.CustomConfigFile) == "" {
		return generatedYAML, nil
	}
	var generated map[string]any
	if err := yaml.Unmarshal([]byte(generatedYAML), &generated); err != nil {
		return "", fmt.Errorf("parse generated hermes config: %w", err)
	}
	if generated == nil {
		generated = map[string]any{}
	}
	merged, err := applyConfigOverlay(profile.Provider, generated, profile.Fields.CustomConfigFile)
	if err != nil {
		return "", fmt.Errorf("apply custom config file: %w", err)
	}
	var b strings.Builder
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	if err := encoder.Encode(merged); err != nil {
		return "", fmt.Errorf("encode merged hermes config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("finalize merged hermes config: %w", err)
	}
	return b.String(), nil
}

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

// writeHermesIterationBudgetPlugin makes ACP honor agent.max_turns. Hermes
// ACP constructs AIAgent without max_iterations, so the constructor default
// of 90 still applies even when config.yaml sets agent.max_turns.
func writeHermesIterationBudgetPlugin(providerHome string) error {
	dir := filepath.Join(providerHome, "plugins", hermesIterationBudgetPlugin)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("prepare hermes iteration-budget plugin: %w", err)
	}
	manifest := "name: " + hermesIterationBudgetPlugin + "\n" +
		"version: 1.0.0\n" +
		"description: Apply projected agent.max_turns and reasoning_effort to ACP AIAgent sessions\n" +
		"kind: standalone\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o600); err != nil {
		return fmt.Errorf("write hermes iteration-budget plugin manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__init__.py"), []byte(hermesIterationBudgetPluginPY), 0o600); err != nil {
		return fmt.Errorf("write hermes iteration-budget plugin: %w", err)
	}
	return nil
}

const hermesIterationBudgetPluginPY = `"""Apply projected agent.max_turns and reasoning_effort to Hermes ACP sessions."""

from __future__ import annotations


def _max_turns() -> int:
    from hermes_cli.config import load_config

    raw = (load_config().get("agent") or {}).get("max_turns")
    try:
        value = int(raw)
    except (TypeError, ValueError):
        return 100000
    if value <= 0:
        return 1_000_000_000
    return value


def _reasoning_effort() -> str:
    from hermes_constants import get_hermes_home

    sidecar = get_hermes_home() / "cyberpenda-requested-reasoning-effort"
    try:
        raw = sidecar.read_text(encoding="utf-8").strip()
        if raw:
            return raw
    except OSError:
        pass
    from hermes_cli.config import load_config

    raw = (load_config().get("agent") or {}).get("reasoning_effort")
    if raw is None:
        return "high"
    value = str(raw).strip()
    if not value:
        return "high"
    return value


def _apply(agent) -> None:
    turns = _max_turns()
    agent.max_iterations = turns
    try:
        from agent.iteration_budget import IterationBudget

        agent.iteration_budget = IterationBudget(turns)
    except Exception:
        pass
    try:
        from hermes_constants import parse_reasoning_effort

        parsed = parse_reasoning_effort(_reasoning_effort())
        if parsed is not None:
            agent.reasoning_config = parsed
    except Exception:
        pass


def register(ctx) -> None:
    try:
        import agent.conversation_loop as conversation_loop

        original = conversation_loop.build_turn_context

        def wrapped(agent, *args, **kwargs):
            _apply(agent)
            return original(agent, *args, **kwargs)

        conversation_loop.build_turn_context = wrapped
    except Exception:
        pass
    try:
        import agent.agent_init as agent_init

        original_init = agent_init.init_agent

        def wrapped_init(agent, *args, **kwargs):
            original_init(agent, *args, **kwargs)
            _apply(agent)

        agent_init.init_agent = wrapped_init
    except Exception:
        pass
`

const (
	hermesIterationBudgetPlugin = "cyberpenda-iteration-budget"
	// 1000 turns is too small for a 6-hour Hosted Evaluation Run.
	// Hermes ACP treats 0 as exhausted immediately, so this is a large finite cap.
	hermesProjectedMaxTurns = 100000
)

func buildHermesConfigYAML(profile runtimeprofile.Profile, projected []piProjectedProvider, servers []runtimeprofile.MCPServer, reasoningEffort string) string {
	var b strings.Builder
	b.WriteString("approvals:\n")
	b.WriteString("  mode: off\n")
	b.WriteString("agent:\n")
	fmt.Fprintf(&b, "  max_turns: %d\n", hermesProjectedMaxTurns)
	fmt.Fprintf(&b, "  reasoning_effort: %s\n", yamlScalar(reasoningEffort))
	b.WriteString("delegation:\n")
	fmt.Fprintf(&b, "  max_iterations: %d\n", hermesProjectedMaxTurns)
	fmt.Fprintf(&b, "  reasoning_effort: %s\n", yamlScalar(reasoningEffort))
	b.WriteString("plugins:\n")
	b.WriteString("  enabled:\n")
	fmt.Fprintf(&b, "    - %s\n", hermesIterationBudgetPlugin)
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
