package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

var secretEnvKeyPattern = regexp.MustCompile(`(?i)(token|api[_-]?key|secret|password|auth)`)

// ProjectionRequest supplies task and daemon context for launch projection.
type ProjectionRequest struct {
	ProjectID           string
	TaskID              string
	ScopeSnapshot       project.Scope
	Credentials         *credential.Service
	DaemonAddr          string
	AuthToken           string
	Sandbox             bool
	RuntimePlugins      *runtimeplugin.Registry
	RuntimeExtensions   *runtimeextension.Registry
	ModelProviders      modelprovider.ProviderGetter
	ModelSnapshot       *modelprovider.Snapshot
	LaunchModelOverride string
	SkillBundles        []skill.Bundle
	RuntimeContext      *projectinterface.RuntimeBlackboardContextV1
}

// ProjectRuntimeConfig writes provider-specific runtime files into the task-local
// provider home. It never writes back to host runtime config.
func ProjectRuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	if strings.TrimSpace(layout.ProviderHome) == "" {
		return ConfigProjection{}, fmt.Errorf("provider home is required")
	}
	if err := os.MkdirAll(layout.ProviderHome, 0o700); err != nil {
		return ConfigProjection{}, fmt.Errorf("prepare provider home: %w", err)
	}
	if len(req.SkillBundles) > 0 {
		if err := projectSkillBundles(layout, req.SkillBundles); err != nil {
			return ConfigProjection{}, err
		}
	}
	if req.Sandbox || len(req.SkillBundles) > 0 {
		target := sandboxSkillsImagePath
		if len(req.SkillBundles) > 0 {
			target = layout.SkillsRoot
			if req.Sandbox {
				target = "/task/skills"
			}
		}
		if err := PrepareSandboxSkills(layout, profile.Provider, target); err != nil {
			return ConfigProjection{}, err
		}
	}

	plugin, ok := runtimePluginForProvider(profile.Provider, req.RuntimePlugins)
	if !ok {
		return projectGenericConfig(layout, profile)
	}
	if req.ModelSnapshot == nil && req.ModelProviders != nil && strings.TrimSpace(profile.Fields.ModelProviderID) != "" {
		snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
			Profile:             profile,
			Providers:           req.ModelProviders,
			Plugins:             req.RuntimePlugins,
			Credentials:         req.Credentials,
			ProjectID:           req.ProjectID,
			CheckEnv:            true,
			LaunchModelOverride: req.LaunchModelOverride,
		})
		if err != nil {
			return ConfigProjection{}, err
		}
		if snapshot.ModelProviderID != "" {
			req.ModelSnapshot = &snapshot
			profile = profileWithModelSnapshot(profile, snapshot)
		}
	}

	var projection ConfigProjection
	var err error
	switch plugin.ConfigProjection.Primitive {
	case "claude_settings":
		projection, err = projectClaudeSettings(layout, profile, req)
	case "codex_home":
		projection, err = projectCodexConfig(layout, profile, req)
	case "pi_agent":
		projection, err = projectPiConfig(layout, profile, req)
	case "none":
		projection = ConfigProjection{Config: runtimeprofile.GeneratedConfig(profile)}
	default:
		projection, err = projectGenericConfig(layout, profile)
	}
	if err != nil {
		return ConfigProjection{}, err
	}
	projection.ResolvedProfile = profile
	projection.ModelSnapshot = req.ModelSnapshot
	if err := projectRuntimeExtensions(layout, profile, req, &projection); err != nil {
		return ConfigProjection{}, err
	}
	if len(req.SkillBundles) > 0 {
		addSkillProjectionPreview(&projection, req.SkillBundles, layout)
	}
	return projection, nil
}

func projectSkillBundles(layout Layout, bundles []skill.Bundle) error {
	if strings.TrimSpace(layout.SkillsRoot) == "" {
		return fmt.Errorf("skills root is required")
	}
	if err := os.MkdirAll(layout.SkillsRoot, 0o700); err != nil {
		return fmt.Errorf("prepare skills root: %w", err)
	}
	targets := map[string]string{}
	for _, bundle := range bundles {
		projectionID := skill.DisplayID(bundle.ID, bundle.Source)
		projectionName := skill.DisplayName(bundle.Name, bundle.ID, bundle.Source)
		if previous, exists := targets[projectionID]; exists {
			return fmt.Errorf("project skill %q: source-free skill folder %q conflicts with %q", bundle.ID, projectionID, previous)
		}
		targets[projectionID] = bundle.ID
		meta := skill.Metadata{ID: projectionID, Name: projectionName}
		if strings.TrimSpace(meta.Name) == "" {
			meta.Name = projectionID
		}
		if err := skill.ValidateBundle(bundle.Path, meta); err != nil {
			return err
		}
		target := filepath.Join(layout.SkillsRoot, projectionID)
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("clear projected skill %q: %w", projectionID, err)
		}
		if err := copyRuntimeExtensionDir(bundle.Path, target); err != nil {
			return fmt.Errorf("project skill %q: %w", projectionID, err)
		}
	}
	return nil
}

func addSkillProjectionPreview(projection *ConfigProjection, bundles []skill.Bundle, layout Layout) {
	if projection.Config == nil {
		projection.Config = map[string]any{}
	}
	previews := make([]map[string]any, 0, len(bundles))
	for _, bundle := range bundles {
		projectionID := skill.DisplayID(bundle.ID, bundle.Source)
		preview := map[string]any{
			"id":     projectionID,
			"name":   skill.DisplayName(bundle.Name, bundle.ID, bundle.Source),
			"target": filepath.Join(layout.SkillsRoot, projectionID),
		}
		previews = append(previews, preview)
	}
	projection.Config["skills"] = previews
}

func projectRuntimeExtensions(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest, projection *ConfigProjection) error {
	if req.RuntimeExtensions == nil || len(profile.Fields.RuntimeExtensions) == 0 {
		return nil
	}
	previews := make([]map[string]any, 0, len(profile.Fields.RuntimeExtensions))
	for _, ref := range profile.Fields.RuntimeExtensions {
		if !runtimeExtensionRefEnabled(ref) {
			continue
		}
		extension, ok := req.RuntimeExtensions.Get(ref.ID)
		if !ok {
			if preview, ok := runtimeExtensionCatalogPreview(ref); ok {
				previews = append(previews, preview)
				continue
			}
			return fmt.Errorf("runtime extension %q not found", ref.ID)
		}
		if !runtimeextension.CompatibleWith(extension, string(profile.Provider)) {
			return fmt.Errorf("runtime extension %q is not compatible with provider %q", ref.ID, profile.Provider)
		}
		targetRoot, err := runtimeExtensionTargetRoot(layout, extension.Projection.Location)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(extension.Projection.Path))
		if err := copyRuntimeExtension(extension, target); err != nil {
			return err
		}
		preview := map[string]any{
			"id":     extension.ID,
			"name":   extension.Name,
			"target": target,
		}
		if len(ref.Config) > 0 {
			preview["config"] = ref.Config
		}
		previews = append(previews, preview)
	}
	if len(previews) == 0 {
		return nil
	}
	if projection.Config == nil {
		projection.Config = map[string]any{}
	}
	projection.Config["runtime_extensions"] = previews
	return nil
}

func runtimeExtensionCatalogPreview(ref runtimeprofile.RuntimeExtensionRef) (map[string]any, bool) {
	registry := strings.TrimSpace(ref.Config["registry"])
	installRef := strings.TrimSpace(ref.Config["install_ref"])
	if registry == "" && installRef == "" {
		return nil, false
	}
	preview := map[string]any{
		"id":     ref.ID,
		"source": "catalog",
	}
	if registry != "" {
		preview["registry"] = registry
	}
	if installRef != "" {
		preview["install_ref"] = installRef
	}
	if sourceURL := strings.TrimSpace(ref.Config["source_url"]); sourceURL != "" {
		preview["source_url"] = sourceURL
	}
	if len(ref.Config) > 0 {
		preview["config"] = ref.Config
	}
	return preview, true
}

func runtimeExtensionRefEnabled(ref runtimeprofile.RuntimeExtensionRef) bool {
	return ref.Enabled == nil || *ref.Enabled
}

func runtimeExtensionTargetRoot(layout Layout, location string) (string, error) {
	switch location {
	case "provider_home":
		return layout.ProviderHome, nil
	case "runtime_home":
		return layout.RuntimeHome, nil
	case "workdir":
		return layout.Workdir, nil
	default:
		return "", fmt.Errorf("unsupported runtime extension projection location %q", location)
	}
}

func copyRuntimeExtension(extension runtimeextension.Extension, target string) error {
	switch extension.Source.Type {
	case "local_dir":
		return copyRuntimeExtensionDir(extension.Source.Path, target)
	case "local_file":
		return copyRuntimeExtensionFile(extension.Source.Path, target)
	default:
		return fmt.Errorf("unsupported runtime extension source type %q", extension.Source.Type)
	}
}

func copyRuntimeExtensionDir(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime extension source must not contain symlinks: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		return copyRuntimeExtensionFile(path, dst)
	})
}

func copyRuntimeExtensionFile(source, target string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read runtime extension source %q: %w", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("prepare runtime extension target: %w", err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		return fmt.Errorf("write runtime extension target %q: %w", target, err)
	}
	return nil
}

func runtimePluginForProvider(provider runtimeprofile.Provider, registry *runtimeplugin.Registry) (runtimeplugin.Plugin, bool) {
	if registry != nil {
		return registry.Get(string(provider))
	}
	return runtimeplugin.MustBuiltinRegistry().Get(string(provider))
}

func profileWithModelSnapshot(profile runtimeprofile.Profile, snapshot modelprovider.Snapshot) runtimeprofile.Profile {
	profile.Fields.Model = snapshot.Model
	profile.Fields.Endpoint = snapshot.EndpointBaseURL
	if profile.Fields.Env == nil {
		profile.Fields.Env = map[string]string{}
	}
	switch profile.Provider {
	case runtimeprofile.ProviderCodex:
		profile.Fields.Env["CODEX_MODEL_PROVIDER"] = snapshot.ModelProviderID
		profile.Fields.Env["CODEX_PROVIDER_NAME"] = snapshot.ModelProviderName
		profile.Fields.Env["CODEX_WIRE_API"] = codexWireAPI(snapshot.Protocol)
	case runtimeprofile.ProviderPi:
		profile.Fields.Env["PI_PROVIDER_ID"] = snapshot.ModelProviderID
		profile.Fields.Env["PI_API"] = piAPIForProtocol(snapshot.Protocol)
	case runtimeprofile.ProviderClaudeCode:
		profile.Fields.Env["ANTHROPIC_BASE_URL"] = snapshot.EndpointBaseURL
		profile.Fields.Env["ANTHROPIC_MODEL"] = snapshot.Model
	}
	return profile
}

func codexWireAPI(protocol modelprovider.Protocol) string {
	switch protocol {
	case modelprovider.ProtocolOpenAIResponses:
		return "responses"
	default:
		return string(protocol)
	}
}

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

func projectGenericConfig(layout Layout, profile runtimeprofile.Profile) (ConfigProjection, error) {
	config := runtimeprofile.GeneratedConfig(profile)
	configPath := filepath.Join(layout.ProviderHome, "config.json")
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return ConfigProjection{}, fmt.Errorf("encode runtime config: %w", err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		return ConfigProjection{}, fmt.Errorf("write runtime config: %w", err)
	}
	return ConfigProjection{ConfigPath: configPath, Config: config}, nil
}

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
	if err := writeTaskContextFiles(layout, taskContextFromProjection(req, profile.Provider, mcpURL)); err != nil {
		return ConfigProjection{}, err
	}
	if len(mcpServers) > 0 {
		if err := writeClaudeMCPConfig(layout.Workdir, mcpServers); err != nil {
			return ConfigProjection{}, err
		}
	}

	settings := map[string]any{"env": env}
	allowedTools := claudeTrustedMCPAllowedTools(mcpServers)
	if len(allowedTools) > 0 {
		settings["permissions"] = map[string]any{"allow": allowedTools}
	}
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
	settingsPath := filepath.Join(layout.ProviderHome, "settings.json")
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return ConfigProjection{}, fmt.Errorf("encode claude settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, raw, 0o600); err != nil {
		return ConfigProjection{}, fmt.Errorf("write claude settings: %w", err)
	}

	preview := map[string]any{
		"provider":      string(profile.Provider),
		"settings_path": settingsPath,
		"env":           redactEnvMap(env),
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
	if len(allowedTools) > 0 {
		preview["allowed_tools"] = allowedTools
	}
	if len(installRefs) > 0 {
		preview["enabled_plugins"] = installRefs
	}
	addModelSnapshotPreview(preview, req.ModelSnapshot)

	return ConfigProjection{ConfigPath: settingsPath, Config: preview}, nil
}

func projectCodexConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return ConfigProjection{}, err
	}
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		value := strings.TrimSpace(os.Getenv(req.ModelSnapshot.APIKeyEnv))
		if value == "" {
			if materialized, ok := materializeModelProviderAPIKey(req); ok {
				value = materialized
			}
		}
		if value != "" {
			materialized = map[string]string{"OPENAI_API_KEY": value}
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

	configPath := filepath.Join(layout.ProviderHome, "config.toml")
	configTOML := buildCodexConfigTOML(profile, mcpServers)
	if err := os.WriteFile(configPath, []byte(configTOML), 0o600); err != nil {
		return ConfigProjection{}, fmt.Errorf("write codex config: %w", err)
	}

	authPath := ""
	var authPreview map[string]any
	if len(materialized) > 0 {
		authPath = filepath.Join(layout.ProviderHome, "auth.json")
		authDoc := buildCodexAuth(materialized)
		raw, err := json.MarshalIndent(authDoc, "", "  ")
		if err != nil {
			return ConfigProjection{}, fmt.Errorf("encode codex auth: %w", err)
		}
		if err := os.WriteFile(authPath, raw, 0o600); err != nil {
			return ConfigProjection{}, fmt.Errorf("write codex auth: %w", err)
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

func projectPiConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return ConfigProjection{}, err
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
	if err := writeTaskContextFiles(layout, taskContextFromProjection(req, profile.Provider, mcpURL)); err != nil {
		return ConfigProjection{}, err
	}
	if len(mcpServers) > 0 {
		if err := writePiMCPConfig(agentDir, mcpServers); err != nil {
			return ConfigProjection{}, err
		}
	}

	modelsDoc := buildPiModels(profile, materialized)
	modelsPath := filepath.Join(agentDir, "models.json")
	if copiedModels, err := copyHostPiModels(agentDir); err != nil {
		return ConfigProjection{}, err
	} else if !copiedModels {
		modelsRaw, err := json.MarshalIndent(modelsDoc, "", "  ")
		if err != nil {
			return ConfigProjection{}, fmt.Errorf("encode pi models: %w", err)
		}
		if err := os.WriteFile(modelsPath, modelsRaw, 0o600); err != nil {
			return ConfigProjection{}, fmt.Errorf("write pi models: %w", err)
		}
	}

	authPath := ""
	var authPreview map[string]any
	if len(materialized) > 0 {
		authPath = filepath.Join(agentDir, "auth.json")
		authDoc := buildPiAuth(profile, materialized)
		authRaw, err := json.MarshalIndent(authDoc, "", "  ")
		if err != nil {
			return ConfigProjection{}, fmt.Errorf("encode pi auth: %w", err)
		}
		if err := os.WriteFile(authPath, authRaw, 0o600); err != nil {
			return ConfigProjection{}, fmt.Errorf("write pi auth: %w", err)
		}
		authPreview = redactPiAuth(authDoc)
	} else if copied, err := copyHostPiAuth(agentDir); err != nil {
		return ConfigProjection{}, err
	} else if copied {
		authPath = filepath.Join(agentDir, "auth.json")
		authPreview = map[string]any{"source": "host_pi_auth"}
	}

	// Catalog-sourced runtime extensions (npm: install refs) are installed by
	// pi on launch when listed in settings.json packages. Project them so a
	// profile's enabled catalog extensions actually take effect.
	packages := enabledExtensionInstallRefs(profile)
	if len(packages) > 0 {
		settings := map[string]any{"packages": packages}
		settingsPath := filepath.Join(agentDir, "settings.json")
		settingsRaw, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return ConfigProjection{}, fmt.Errorf("encode pi settings: %w", err)
		}
		if err := os.WriteFile(settingsPath, settingsRaw, 0o600); err != nil {
			return ConfigProjection{}, fmt.Errorf("write pi settings: %w", err)
		}
	}

	preview := map[string]any{
		"provider":    string(profile.Provider),
		"models_path": modelsPath,
		"models_json": modelsDoc,
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
	addModelSnapshotPreview(preview, req.ModelSnapshot)

	return ConfigProjection{ConfigPath: modelsPath, Config: preview}, nil
}

func addModelSnapshotPreview(preview map[string]any, snapshot *modelprovider.Snapshot) {
	if snapshot == nil || snapshot.ModelProviderID == "" {
		return
	}
	preview["model_provider_snapshot"] = map[string]any{
		"model_provider_id":   snapshot.ModelProviderID,
		"model_provider_name": snapshot.ModelProviderName,
		"endpoint_base_url":   snapshot.EndpointBaseURL,
		"base_url":            snapshot.BaseURL,
		"protocol":            string(snapshot.Protocol),
		"model":               snapshot.Model,
		"api_key_env":         snapshot.APIKeyEnv,
		"api_key_source":      snapshot.APIKeySource,
		"projection_target":   snapshot.ProjectionTarget,
	}
}

// enabledExtensionInstallRefs collects the install_ref of each enabled runtime
// extension whose config carries an install ref. Catalog-sourced extensions
// (selected from a package/plugin catalog) carry an install_ref that the
// runtime consumes on launch: pi lists them in settings.json packages, Claude
// Code lists them in settings.json enabledPlugins. Local-registry extensions
// (copied as files) carry no install_ref and are intentionally excluded.
func enabledExtensionInstallRefs(profile runtimeprofile.Profile) []string {
	var refs []string
	for _, ref := range profile.Fields.RuntimeExtensions {
		if !runtimeExtensionRefEnabled(ref) {
			continue
		}
		installRef := strings.TrimSpace(ref.Config["install_ref"])
		if installRef == "" {
			continue
		}
		refs = append(refs, installRef)
	}
	return refs
}

func resolveMaterializedCredentials(profile runtimeprofile.Profile, req ProjectionRequest) (map[string]string, error) {
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		if value, ok := materializeModelProviderAPIKey(req); ok {
			return map[string]string{req.ModelSnapshot.APIKeyEnv: value}, nil
		}
		value := strings.TrimSpace(os.Getenv(req.ModelSnapshot.APIKeyEnv))
		if value == "" {
			return nil, fmt.Errorf("model provider API key env %s is not configured", req.ModelSnapshot.APIKeyEnv)
		}
		return map[string]string{req.ModelSnapshot.APIKeyEnv: value}, nil
	}
	inline := runtimeprofile.MaterializedAPIKeys(profile)
	if len(inline) > 0 {
		return inline, nil
	}
	if req.Credentials == nil || req.ProjectID == "" || len(profile.Fields.CredentialRefs) == 0 {
		return nil, nil
	}
	return req.Credentials.ResolveMaterializedEnv(req.ProjectID, profile.Fields.CredentialRefs)
}

func buildCodexConfigTOML(profile runtimeprofile.Profile, mcpServers []runtimeprofile.MCPServer) string {
	var b strings.Builder
	if profile.Fields.Model != "" {
		fmt.Fprintf(&b, "model = %q\n", profile.Fields.Model)
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
	appendCodexMCPTOML(&b, mcpServers)
	return b.String()
}

func buildCodexAuth(materialized map[string]string) map[string]string {
	auth := make(map[string]string, len(materialized))
	for key, value := range materialized {
		switch strings.ToUpper(key) {
		case "OPENAI_API_KEY":
			auth["OPENAI_API_KEY"] = value
		default:
			auth[key] = value
		}
	}
	return auth
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

func buildPiModels(profile runtimeprofile.Profile, materialized map[string]string) map[string]any {
	providerID := piProviderID(profile)

	provider := map[string]any{}
	if endpoint := strings.TrimSpace(profile.Fields.Endpoint); endpoint != "" {
		provider["baseUrl"] = strings.TrimRight(endpoint, "/")
	}
	if api := inferPiAPI(profile); api != "" {
		provider["api"] = api
	}
	if apiKeyRef := piAPIKeyRef(materialized); apiKeyRef != "" {
		provider["apiKey"] = apiKeyRef
	}
	if profile.Fields.Model != "" {
		provider["models"] = []map[string]any{{"id": profile.Fields.Model}}
	}

	return map[string]any{
		"providers": map[string]any{
			providerID: provider,
		},
	}
}

func buildPiAuth(profile runtimeprofile.Profile, materialized map[string]string) map[string]map[string]string {
	envKey := piAPIKeyEnv(materialized)
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

func piAPIKeyRef(materialized map[string]string) string {
	if key := piAPIKeyEnv(materialized); key != "" {
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

func piAPIKeyEnv(materialized map[string]string) string {
	keys := make([]string, 0, len(materialized))
	for key := range materialized {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
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
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		value := strings.TrimSpace(os.Getenv(req.ModelSnapshot.APIKeyEnv))
		if value == "" {
			if materialized, ok := materializeModelProviderAPIKey(req); ok {
				value = materialized
			}
		}
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

// LaunchConfigPath returns the config path passed to the runtime CLI. Sandbox
// launches use container paths under /task.
func LaunchConfigPath(layout Layout, provider runtimeprofile.Provider, hostConfigPath string, sandbox bool) string {
	if !sandbox {
		return hostConfigPath
	}
	rel, err := filepath.Rel(layout.TaskRoot, hostConfigPath)
	if err != nil {
		return hostConfigPath
	}
	return "/task/" + filepath.ToSlash(rel)
}

// LaunchProcessEnv returns process environment variables for the launch adapter.
// Claude Code reads settings from CLAUDE_HOME; profile env lives in settings.json.
func LaunchProcessEnv(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx TaskContext) map[string]string {
	return launchProcessEnv(layout, profile, sandbox, ctx, nil)
}

func launchProcessEnv(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx TaskContext, registry *runtimeplugin.Registry) map[string]string {
	env := map[string]string{}
	if sandbox {
		// Claude Code allows --dangerously-skip-permissions in sandboxed containers
		// when IS_SANDBOX=1, even if the process runs as root inside Docker.
		env["IS_SANDBOX"] = "1"
		env["PENTEST_SKILLS_DIR"] = sandboxSkillsImagePath
	}
	if ctx.ProjectID != "" {
		env["PENTEST_PROJECT_ID"] = ctx.ProjectID
	}
	if ctx.TaskID != "" {
		env["PENTEST_TASK_ID"] = ctx.TaskID
	}
	if ctx.MCPURL != "" {
		env["PENTEST_MCP_URL"] = ctx.MCPURL
	}
	if ctx.AuthToken != "" {
		env["PENTEST_AUTH_TOKEN"] = ctx.AuthToken
	}
	if ctx.InterfaceToken != "" {
		env["PENTEST_INTERFACE_TOKEN"] = ctx.InterfaceToken
	}
	if ctx.APIURL != "" {
		env["PENTEST_API_URL"] = ctx.APIURL
	}
	if ctx.RuntimeContext != nil && ctx.RuntimeContext.ContinuationID != "" {
		env["PENTEST_CONTINUATION_ID"] = ctx.RuntimeContext.ContinuationID
	}
	manifestEnvRendered := false
	if plugin, ok := runtimePluginForProvider(profile.Provider, registry); ok {
		rendered, err := runtimeplugin.RenderEnv(plugin.ProcessEnv, processEnvRenderContext(layout, profile, sandbox))
		if err == nil {
			for key, value := range rendered {
				env[key] = value
			}
			manifestEnvRendered = len(rendered) > 0
		}
	}
	if !manifestEnvRendered {
		for key, value := range profile.Fields.Env {
			env[key] = value
		}
	}
	return env
}

func processEnvRenderContext(layout Layout, profile runtimeprofile.Profile, sandbox bool) runtimeplugin.RenderContext {
	runtimeHome := layout.RuntimeHome
	workdir := layout.Workdir
	if sandbox {
		runtimeHome = "/task/runtime-home"
		workdir = "/task/workdir"
	}
	return runtimeplugin.RenderContext{
		Scalars: map[string]string{
			"runtime_home":  runtimeHome,
			"workdir":       workdir,
			"provider_home": filepath.Join(runtimeHome, providerHomeDir(profile.Provider)),
		},
	}
}

// LaunchProcessEnvWithCredentials returns process environment variables for a
// runtime launch, including profile env and resolved API key material needed by
// runtimes that interpolate env references from their generated config.
func LaunchProcessEnvWithCredentials(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx TaskContext, req ProjectionRequest) (map[string]string, error) {
	env := launchProcessEnv(layout, profile, sandbox, ctx, req.RuntimePlugins)
	if sandbox && len(req.SkillBundles) > 0 {
		env["PENTEST_SKILLS_DIR"] = "/task/skills"
	}
	for key, value := range profile.Fields.Env {
		env[key] = value
	}

	if profile.Provider == runtimeprofile.ProviderClaudeCode {
		claudeEnv, err := buildClaudeEnv(profile, req)
		if err != nil {
			return nil, err
		}
		for key, value := range claudeEnv {
			env[key] = value
		}
		return env, nil
	}

	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return nil, err
	}
	for key, value := range materialized {
		env[key] = value
	}
	return env, nil
}

func materializeModelProviderAPIKey(req ProjectionRequest) (string, bool) {
	if req.Credentials == nil || req.ModelSnapshot == nil || strings.TrimSpace(req.ModelSnapshot.APIKeyEnv) == "" {
		return "", false
	}
	resolution, err := req.Credentials.Resolve(req.ModelSnapshot.APIKeyEnv, req.ProjectID)
	if err != nil || !resolution.Found || resolution.Disabled || resolution.Source == nil {
		return "", false
	}
	value, err := credential.Materialize(*resolution.Source)
	if err != nil {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}
