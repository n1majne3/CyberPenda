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

func piAPIForProtocol(protocol modelprovider.Protocol) string {
	switch protocol {
	case modelprovider.ProtocolAnthropicMessages:
		return "anthropic-messages"
	case modelprovider.ProtocolOpenAIResponses:
		return "openai-responses"
	default:
		return "openai-completions"
	}
}

func projectPiConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	// ADR 0015: every launch-ready global Model Provider is projected into
	// each Pi runtime with its models and credentials. Initial selection is
	// only the starting provider, not a credential boundary.
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

	agentDir := filepath.Join(layout.ProviderHome, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return ConfigProjection{}, fmt.Errorf("prepare pi agent dir: %w", err)
	}
	if err := writeProjectionContextFiles(layout, req, profile.Provider, mcpURL); err != nil {
		return ConfigProjection{}, err
	}
	if len(mcpServers) > 0 {
		if err := writePiMCPConfig(agentDir, mcpServers); err != nil {
			return ConfigProjection{}, err
		}
	}

	var modelsDoc map[string]any
	var authDoc map[string]map[string]string
	if len(projected) > 0 {
		modelsDoc = buildPiModelsFromProjected(projected, req)
		authDoc = buildPiAuthFromProjected(projected, materialized)
	} else {
		modelsDoc = buildPiModels(profile, materialized, req)
		authDoc = buildPiAuth(profile, materialized, req)
	}
	modelsDoc, err = applyConfigOverlay(profile.Provider, modelsDoc, profile.Fields.CustomConfigFile)
	if err != nil {
		return ConfigProjection{}, fmt.Errorf("apply custom config file: %w", err)
	}
	modelsPath := filepath.Join(agentDir, "models.json")
	globalProjection := piGlobalProjectionAvailable(req)
	hostModelsFallback := !globalProjection && len(projected) == 0
	if hostModelsFallback {
		if copiedModels, err := copyHostPiModels(agentDir); err != nil {
			return ConfigProjection{}, err
		} else if !copiedModels {
			if err := writeJSONConfigFile(modelsPath, modelsDoc); err != nil {
				return ConfigProjection{}, err
			}
		}
	} else {
		if err := writeJSONConfigFile(modelsPath, modelsDoc); err != nil {
			return ConfigProjection{}, err
		}
	}

	authPath := ""
	var authPreview map[string]any
	if len(authDoc) > 0 {
		authPath = filepath.Join(agentDir, "auth.json")
		if err := writeJSONConfigFile(authPath, authDoc); err != nil {
			return ConfigProjection{}, err
		}
		authPreview = redactPiAuth(authDoc)
	} else if hostModelsFallback {
		if copied, err := copyHostPiAuth(agentDir); err != nil {
			return ConfigProjection{}, err
		} else if copied {
			authPath = filepath.Join(agentDir, "auth.json")
			authPreview = map[string]any{"source": "host_pi_auth"}
		}
	}

	// settings.json is projection-owned: packages come from catalog-sourced
	// Runtime Extensions, never from host ~/.pi/agent/settings.json.
	packages, err := projectPiSettings(agentDir, profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}

	// The Execute subagent type rides the same agent-dir contract as
	// models.json: Config Projection owns the file, host agent files are not
	// merged, and pi-subagents discovers it without project trust.
	executeAgentPath := filepath.Join(agentDir, "agents", "execute.md")
	if err := writeOwnerOnlyFile(executeAgentPath, piExecuteAgentMarkdown); err != nil {
		return ConfigProjection{}, fmt.Errorf("write pi execute agent type: %w", err)
	}

	preview := map[string]any{
		"provider":       string(profile.Provider),
		"models_path":    modelsPath,
		"models_json":    modelsDoc,
		"subagent_types": []string{"execute"},
	}
	if len(packages) > 0 {
		preview["packages"] = packages
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
		preview["mcp_config_path"] = filepath.Join(agentDir, "mcp.json")
	}
	if len(packages) > 0 {
		preview["packages"] = packages
	}
	// Always record the evaluated projected set when global projection ran,
	// including an empty list. Daemon native cross-provider turns fail closed
	// without this field (fixed-at-restart semantics).
	if globalProjection {
		ids := make([]string, 0, len(projected))
		for _, entry := range projected {
			ids = append(ids, entry.Provider.ID)
		}
		preview["projected_model_provider_ids"] = ids
	}
	addModelSnapshotPreview(preview, req.ModelSnapshot)

	return ConfigProjection{ConfigPath: modelsPath, Config: preview}, nil
}

// piGlobalProjectionAvailable is true when Config Projection owns the global
// Model Provider set for Pi — either via a pre-TX snapshot or a lister used
// only outside Store transactions (unit tests).
func piGlobalProjectionAvailable(req ProjectionRequest) bool {
	if req.GlobalModelProviderSnapshot != nil {
		return true
	}
	if req.ModelProviders == nil {
		return false
	}
	_, ok := req.ModelProviders.(modelprovider.ProviderLister)
	return ok
}

// piModelEntry projects one models.json model entry. Pi clamps
// set_thinking_level to "off" — and reasoning_effort never reaches the
// provider request — for entries without reasoning metadata. The identity
// xhigh/max mapping is required because Pi treats those two levels as
// unavailable unless thinkingLevelMap declares them. supportsDeveloperRole is
// pinned false because CyberPenda Model Providers are OpenAI-compatible
// gateways, not OpenAI itself: Pi's default detection would pair
// reasoning:true with the OpenAI-only "developer" role for the system prompt,
// which gateways such as Kimi reject with HTTP 400. The classic "system"
// role is accepted everywhere.
func piModelEntry(modelID string, catalog modelprovider.Catalog, cache modelprovider.CapabilityLookup) map[string]any {
	entry := map[string]any{
		"id":               modelID,
		"reasoning":        true,
		"thinkingLevelMap": map[string]any{"xhigh": "xhigh", "max": "max"},
		"compat":           map[string]any{"supportsDeveloperRole": false},
	}
	resolved := modelprovider.ResolveLimits(modelID, catalog, cache)
	if resolved.ContextWindow >= 1 {
		entry["contextWindow"] = resolved.ContextWindow
	}
	if resolved.MaxOutputTokens >= 1 {
		entry["maxTokens"] = resolved.MaxOutputTokens
	}
	return entry
}

func buildPiModels(profile runtimeprofile.Profile, materialized map[string]string, req ProjectionRequest) map[string]any {
	providerID := piProviderID(profile)

	provider := map[string]any{}
	if endpoint := strings.TrimSpace(profile.Fields.Endpoint); endpoint != "" {
		provider["baseUrl"] = strings.TrimRight(endpoint, "/")
	}
	if api := inferPiAPI(profile); api != "" {
		provider["api"] = api
	}
	if apiKeyRef := piAPIKeyRef(profile, materialized, req); apiKeyRef != "" {
		provider["apiKey"] = apiKeyRef
	}
	if profile.Fields.Model != "" {
		provider["models"] = []map[string]any{piModelEntry(profile.Fields.Model, launchModelCatalog(profile, req), req.CapabilityCache)}
	}

	return map[string]any{
		"providers": map[string]any{
			providerID: provider,
		},
	}
}

func buildPiAuth(profile runtimeprofile.Profile, materialized map[string]string, req ProjectionRequest) map[string]map[string]string {
	envKey := piAPIKeyEnv(profile, materialized, req)
	if envKey == "" {
		return nil
	}
	return map[string]map[string]string{
		piProviderID(profile): {
			"type": "api_key",
			"key":  materialized[envKey],
		},
	}
}

func redactPiAuth(auth map[string]map[string]string) map[string]any {
	out := make(map[string]any, len(auth))
	for providerKey, entry := range auth {
		redacted := make(map[string]any, len(entry))
		for key, value := range entry {
			if key == "key" || secretEnvKeyPattern.MatchString(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = value
		}
		out[providerKey] = redacted
	}
	return out
}

func inferPiAPI(profile runtimeprofile.Profile) string {
	if api := strings.TrimSpace(profile.Fields.Env["PI_API"]); api != "" {
		return api
	}
	endpoint := strings.ToLower(profile.Fields.Endpoint)
	switch {
	case strings.Contains(endpoint, "anthropic"):
		return "anthropic-messages"
	case strings.Contains(endpoint, "generativelanguage") || strings.Contains(endpoint, "googleapis"):
		return "google-generative-ai"
	default:
		return "openai-completions"
	}
}

func piAPIKeyRef(profile runtimeprofile.Profile, materialized map[string]string, req ProjectionRequest) string {
	if key := piAPIKeyEnv(profile, materialized, req); key != "" {
		return "$" + key
	}
	return ""
}

func piProviderID(profile runtimeprofile.Profile) string {
	if providerID := strings.TrimSpace(profile.Fields.Env["PI_PROVIDER_ID"]); providerID != "" {
		return providerID
	}
	return "custom"
}

func piAPIKeyEnv(profile runtimeprofile.Profile, materialized map[string]string, req ProjectionRequest) string {
	if len(materialized) == 0 {
		return ""
	}
	if req.ModelSnapshot != nil {
		if explicit := strings.TrimSpace(req.ModelSnapshot.APIKeyEnv); explicit != "" {
			if _, ok := materialized[explicit]; ok {
				return explicit
			}
		}
	}
	var preferred []string
	switch inferPiAPI(profile) {
	case "anthropic-messages":
		preferred = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	case "google-generative-ai":
		preferred = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	default:
		preferred = []string{"OPENAI_API_KEY"}
	}
	for _, candidate := range preferred {
		if _, ok := materialized[candidate]; ok {
			return candidate
		}
	}
	for _, candidate := range []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"OPENROUTER_API_KEY",
		"DEEPSEEK_API_KEY",
		"MISTRAL_API_KEY",
		"GROQ_API_KEY",
		"XAI_API_KEY",
		"TOGETHER_API_KEY",
	} {
		if _, ok := materialized[candidate]; ok {
			return candidate
		}
	}
	keys := make([]string, 0, len(materialized))
	for key := range materialized {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.HasSuffix(strings.ToUpper(key), "_API_KEY") {
			return key
		}
	}
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func copyHostPiModels(agentDir string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, nil
	}
	src := filepath.Join(home, ".pi", "agent", "models.json")
	raw, err := os.ReadFile(src)
	if err != nil {
		return false, nil
	}
	dst := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return false, fmt.Errorf("copy host pi models: %w", err)
	}
	return true, nil
}

func copyHostPiAuth(agentDir string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, nil
	}
	src := filepath.Join(home, ".pi", "agent", "auth.json")
	raw, err := os.ReadFile(src)
	if err != nil {
		return false, nil
	}
	dst := filepath.Join(agentDir, "auth.json")
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return false, fmt.Errorf("copy host pi auth: %w", err)
	}
	return true, nil
}

// projectPiSettings writes the task-local settings.json from projection-owned
// values only. Host ~/.pi/agent/settings.json is never read: the sandbox
// projection is independent of host configuration. packages come only from the
// profile's enabled Runtime Extensions. defaultThinkingLevel is pinned to the
// launch-resolved Requested Reasoning Effort, and defaultProvider/defaultModel
// to the launch selection, because pi-subagents child sessions read these
// settings.json defaults instead of inheriting the main session's runtime
// state — pi/set_thinking_level and --provider/--model apply only to the main
// session.
func projectPiSettings(agentDir string, profile runtimeprofile.Profile, req ProjectionRequest) ([]string, error) {
	packages := append([]string(nil), piDefaultPackages...)
	seen := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		seen[pkg] = true
	}
	for _, ref := range enabledExtensionInstallRefs(profile) {
		if !seen[ref] {
			seen[ref] = true
			packages = append(packages, ref)
		}
	}

	effort, err := runtimeprofile.ResolveRequestedReasoningEffort("", req.RequestedReasoningEffort, profile.Fields.ReasoningEffort)
	if err != nil {
		return nil, err
	}

	settings := map[string]any{"defaultThinkingLevel": string(effort)}
	if len(packages) > 0 {
		settings["packages"] = packages
	}
	if provider := piLaunchProviderKey(profile); provider != "" {
		settings["defaultProvider"] = provider
	}
	if model := piLaunchModel(profile, req); model != "" {
		settings["defaultModel"] = model
	}
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := writeJSONConfigFile(settingsPath, settings); err != nil {
		return nil, err
	}
	return packages, nil
}

func piLaunchProviderKey(profile runtimeprofile.Profile) string {
	if id := strings.TrimSpace(profile.Fields.Env["PI_PROVIDER_ID"]); id != "" {
		return id
	}
	if strings.TrimSpace(profile.Fields.ModelProviderID) != "" || strings.TrimSpace(profile.Fields.Endpoint) != "" {
		return "custom"
	}
	return ""
}

// piLaunchModel returns the launch-resolved model: Launch Model Override,
// Model Provider Snapshot, profile Model Override, then profile Model.
func piLaunchModel(profile runtimeprofile.Profile, req ProjectionRequest) string {
	candidates := []string{req.LaunchModelOverride}
	if req.ModelSnapshot != nil {
		candidates = append(candidates, req.ModelSnapshot.Model)
	}
	candidates = append(candidates, profile.Fields.ModelOverride, profile.Fields.Model)
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
