package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeprofile"
)

func codexWireAPI(protocol modelprovider.Protocol) string {
	switch protocol {
	case modelprovider.ProtocolOpenAIResponses:
		return "responses"
	default:
		return string(protocol)
	}
}

func projectCodexConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		value, _ := resolveModelProviderAPIKeyValue(req.ModelSnapshot.APIKeyEnv, req)
		if value != "" {
			materialized = map[string]string{"OPENAI_API_KEY": value}
		}
	}

	mcpServers, err := collectMCPServers(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	mcpURL := MCPEndpointURL(req.DaemonAddr, req.Sandbox)
	if err := writeProjectionContextFiles(layout, req, profile.Provider, mcpURL); err != nil {
		return ConfigProjection{}, err
	}

	configPath := filepath.Join(layout.ProviderHome, "config.toml")
	catalogPath, err := writeCodexModelCatalog(layout, profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	configTOML, err := applyCodexConfigOverlay(profile, buildCodexConfigTOML(profile, mcpServers, req, catalogPath))
	if err != nil {
		return ConfigProjection{}, err
	}
	if err := os.WriteFile(configPath, []byte(configTOML), 0o600); err != nil {
		return ConfigProjection{}, fmt.Errorf("write codex config: %w", err)
	}

	authPath := ""
	var authPreview map[string]any
	authDoc := buildCodexAuth(materialized)
	if len(authDoc) > 0 {
		authPath = filepath.Join(layout.ProviderHome, "auth.json")
		if err := writeJSONConfigFile(authPath, authDoc); err != nil {
			return ConfigProjection{}, err
		}
		authPreview = redactCodexAuth(authDoc)
	} else if copied, err := copyHostCodexAuth(layout.ProviderHome); err != nil {
		return ConfigProjection{}, err
	} else if copied {
		authPath = filepath.Join(layout.ProviderHome, "auth.json")
		previewAuth := map[string]any{"source": "host_codex_auth"}
		authPreview = previewAuth
	}

	preview := map[string]any{
		"provider":    string(profile.Provider),
		"config_path": configPath,
		"config_toml": configTOML,
	}
	if authPath != "" {
		preview["auth_path"] = authPath
		preview["auth_json"] = authPreview
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
	}
	addModelSnapshotPreview(preview, req.ModelSnapshot)

	return ConfigProjection{ConfigPath: configPath, Config: preview}, nil
}

// applyCodexConfigOverlay deep-merges the Custom Config File over the
// generated Codex config.toml text and re-encodes TOML. Structured keys
// always win; overlay comments are preserved in the stored overlay text.
func applyCodexConfigOverlay(profile runtimeprofile.Profile, generatedTOML string) (string, error) {
	if strings.TrimSpace(profile.Fields.CustomConfigFile) == "" {
		return generatedTOML, nil
	}
	var generated map[string]any
	if err := toml.Unmarshal([]byte(generatedTOML), &generated); err != nil {
		return "", fmt.Errorf("parse generated codex config: %w", err)
	}
	merged, err := applyConfigOverlay(profile.Provider, generated, profile.Fields.CustomConfigFile)
	if err != nil {
		return "", fmt.Errorf("apply custom config file: %w", err)
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(merged); err != nil {
		return "", fmt.Errorf("encode merged codex config: %w", err)
	}
	return b.String(), nil
}

func writeCodexModelCatalog(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (string, error) {
	modelID := strings.TrimSpace(profile.Fields.Model)
	if req.ModelSnapshot != nil && strings.TrimSpace(req.ModelSnapshot.Model) != "" {
		modelID = strings.TrimSpace(req.ModelSnapshot.Model)
	}
	if override := strings.TrimSpace(req.LaunchModelOverride); override != "" {
		modelID = override
	}
	limits := resolveLaunchModelLimits(profile, req)
	if modelID == "" || (limits.ContextWindow < 1 && limits.MaxOutputTokens < 1) {
		return "", nil
	}
	hostPath := filepath.Join(layout.ProviderHome, "model_catalog.json")
	if err := writeJSONConfigFile(hostPath, buildCodexModelCatalog(modelID, limits)); err != nil {
		return "", fmt.Errorf("write codex model catalog: %w", err)
	}
	if req.Sandbox {
		return "/task/runtime-home/codex/model_catalog.json", nil
	}
	return hostPath, nil
}

func buildCodexModelCatalog(modelID string, limits modelprovider.ResolvedLimits) map[string]any {
	contextWindow := limits.ContextWindow
	if contextWindow < 1 {
		contextWindow = 128000
	}
	entry := map[string]any{
		"slug":                    modelID,
		"display_name":            modelID,
		"base_instructions":       "You are Codex, a coding agent.",
		"default_reasoning_level": "high",
		"supported_reasoning_levels": []any{
			map[string]any{"effort": "low", "description": "Low reasoning effort"},
			map[string]any{"effort": "medium", "description": "Medium reasoning effort"},
			map[string]any{"effort": "high", "description": "High reasoning effort"},
			map[string]any{"effort": "xhigh", "description": "Extra high reasoning effort"},
			map[string]any{"effort": "max", "description": "Maximum reasoning effort"},
		},
		"shell_type":                           "shell_command",
		"visibility":                           "list",
		"supported_in_api":                     true,
		"priority":                             0,
		"include_skills_usage_instructions":    false,
		"include_plugin_usage_instructions":    false,
		"include_apps_usage_instructions":      false,
		"supports_reasoning_summaries":         true,
		"supports_reasoning_summary_parameter": true,
		"default_reasoning_summary":            "none",
		"support_verbosity":                    false,
		"default_verbosity":                    "low",
		"apply_patch_tool_type":                "freeform",
		"web_search_tool_type":                 "text_and_image",
		"truncation_policy":                    map[string]any{"mode": "tokens", "limit": 10000},
		"supports_parallel_tool_calls":         true,
		"supports_image_detail_original":       false,
		"context_window":                       contextWindow,
		"max_context_window":                   contextWindow,
		"effective_context_window_percent":     95,
		"experimental_supported_tools":         []any{},
		"input_modalities":                     []any{"text"},
		"supports_search_tool":                 false,
		"use_responses_lite":                   false,
	}
	return map[string]any{"models": []any{entry}}
}

func buildCodexConfigTOML(profile runtimeprofile.Profile, mcpServers []runtimeprofile.MCPServer, req ProjectionRequest, modelCatalogPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "approval_policy = %q\n", "never")
	fmt.Fprintf(&b, "sandbox_mode = %q\n", "danger-full-access")
	if profile.Fields.Model != "" {
		fmt.Fprintf(&b, "model = %q\n", profile.Fields.Model)
	}
	limits := resolveLaunchModelLimits(profile, req)
	if limits.ContextWindow >= 1 {
		fmt.Fprintf(&b, "model_context_window = %d\n", limits.ContextWindow)
	}
	if limits.MaxOutputTokens >= 1 {
		fmt.Fprintf(&b, "model_max_output_tokens = %d\n", limits.MaxOutputTokens)
	}
	if strings.TrimSpace(modelCatalogPath) != "" {
		fmt.Fprintf(&b, "model_catalog_json = %q\n", modelCatalogPath)
	}

	endpoint := strings.TrimSpace(profile.Fields.Endpoint)
	openaiBase := strings.TrimSpace(profile.Fields.Env["OPENAI_BASE_URL"])
	if endpoint == "" && openaiBase != "" {
		endpoint = openaiBase
	}

	if endpoint != "" {
		providerID := strings.TrimSpace(profile.Fields.Env["CODEX_MODEL_PROVIDER"])
		if providerID == "" {
			providerID = "custom"
		}
		wireAPI := strings.TrimSpace(profile.Fields.Env["CODEX_WIRE_API"])
		if wireAPI == "" {
			wireAPI = "responses"
		}
		providerName := strings.TrimSpace(profile.Fields.Env["CODEX_PROVIDER_NAME"])
		if providerName == "" {
			providerName = "Custom"
		}

		fmt.Fprintf(&b, "model_provider = %q\n", providerID)
		fmt.Fprintf(&b, "cli_auth_credentials_store = %q\n", "file")
		fmt.Fprintf(&b, "\n[model_providers.%s]\n", providerID)
		fmt.Fprintf(&b, "name = %q\n", providerName)
		fmt.Fprintf(&b, "base_url = %q\n", strings.TrimRight(endpoint, "/"))
		fmt.Fprintf(&b, "wire_api = %q\n", wireAPI)
		fmt.Fprintf(&b, "requires_openai_auth = true\n")
	}
	appendCodexMultiAgentTOML(&b, profile)
	appendCodexMCPTOML(&b, mcpServers)
	return b.String()
}

// appendCodexMultiAgentTOML writes the Codex-native multi-agent feature flag
// and agent caps. The control is tri-state: unset writes nothing so Codex's
// own feature default applies (CyberPenda does not suppress native
// multi-agent behavior); an explicit on projects `features.multi_agent =
// true` plus `agents.enabled = true` and the caps, which turns the V1 tools
// on. Explicit on also writes `multi_agent_v2 = false` so Codex keeps the V1
// tool generation: this product does not rely on first-party GPT catalogs,
// and V2 spawn encrypts the child task in a way custom model gateways cannot
// read. An explicit off projects both off keys because Codex releases
// have shipped with the feature default-on, so an absent key cannot carry an
// operator's off decision. Off also disables multi_agent_v2 because Codex
// resolves V2 before agents.enabled. An unset cap is not written, so a Custom Config
// File may supply it — the same fill-in behavior as other conditional
// managed keys such as model_context_window.
func appendCodexMultiAgentTOML(b *strings.Builder, profile runtimeprofile.Profile) {
	settings := profile.Fields.CodexMultiAgent
	if settings == nil {
		return
	}
	enabled := settings.Enabled != nil && *settings.Enabled
	b.WriteString("\n[features]\n")
	fmt.Fprintf(b, "multi_agent = %t\n", enabled)
	// Pin V1 for both explicit on and off. Codex treats multi_agent_v2 as an
	// override that wins over model metadata; leaving it true on the on path
	// would advertise encrypted V2 spawn tools.
	b.WriteString("multi_agent_v2 = false\n")
	b.WriteString("\n[agents]\n")
	fmt.Fprintf(b, "enabled = %t\n", enabled)
	if enabled {
		if settings.MaxConcurrentThreadsPerSession > 0 {
			fmt.Fprintf(b, "max_concurrent_threads_per_session = %d\n", settings.MaxConcurrentThreadsPerSession)
		}
		if settings.MaxDepth > 0 {
			fmt.Fprintf(b, "max_depth = %d\n", settings.MaxDepth)
		}
	}
}

func buildCodexAuth(materialized map[string]string) map[string]string {
	auth := make(map[string]string, 1)
	for key, value := range materialized {
		if strings.EqualFold(key, "OPENAI_API_KEY") {
			auth["OPENAI_API_KEY"] = value
		}
	}
	return auth
}

func resolveLaunchModelLimits(profile runtimeprofile.Profile, req ProjectionRequest) modelprovider.ResolvedLimits {
	modelID := strings.TrimSpace(profile.Fields.Model)
	if req.ModelSnapshot != nil && strings.TrimSpace(req.ModelSnapshot.Model) != "" {
		modelID = strings.TrimSpace(req.ModelSnapshot.Model)
	}
	if override := strings.TrimSpace(req.LaunchModelOverride); override != "" {
		modelID = override
	}
	return modelprovider.ResolveLimits(modelID, launchModelCatalog(profile, req), req.CapabilityCache)
}

func launchModelCatalog(profile runtimeprofile.Profile, req ProjectionRequest) modelprovider.Catalog {
	if req.capturedModelCatalog != nil {
		return *req.capturedModelCatalog
	}
	providerID := strings.TrimSpace(profile.Fields.ModelProviderID)
	if req.ModelSnapshot != nil && strings.TrimSpace(req.ModelSnapshot.ModelProviderID) != "" {
		providerID = strings.TrimSpace(req.ModelSnapshot.ModelProviderID)
	}
	if providerID == "" {
		return modelprovider.Catalog{}
	}
	if req.GlobalModelProviderSnapshot != nil {
		for _, provider := range req.GlobalModelProviderSnapshot.Providers {
			if provider.ID == providerID {
				return provider.Catalog
			}
		}
	}
	if req.ModelProviders != nil {
		provider, err := req.ModelProviders.Get(providerID)
		if err == nil {
			return provider.Catalog
		}
	}
	return modelprovider.Catalog{}
}

func redactCodexAuth(auth map[string]string) map[string]any {
	out := make(map[string]any, len(auth))
	for key, value := range auth {
		if secretEnvKeyPattern.MatchString(key) || strings.EqualFold(key, "OPENAI_API_KEY") {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = value
	}
	return out
}

func copyHostCodexAuth(providerHome string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, nil
	}
	src := filepath.Join(home, ".codex", "auth.json")
	raw, err := os.ReadFile(src)
	if err != nil {
		return false, nil
	}
	dst := filepath.Join(providerHome, "auth.json")
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return false, fmt.Errorf("copy host codex auth: %w", err)
	}
	return true, nil
}
