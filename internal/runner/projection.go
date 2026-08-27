package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

var secretEnvKeyPattern = regexp.MustCompile(`(?i)(token|api[_-]?key|secret|password|auth)`)

// GlobalModelProviderSnapshot is an immutable List() result captured before any
// Store transaction that projects runtime config. Projection filters this list
// for Pi launch-ready providers and never re-queries ModelProviders.Service.
type GlobalModelProviderSnapshot struct {
	Providers []modelprovider.Provider
}

// ProjectionRequest supplies task and daemon context for launch projection.
type ProjectionRequest struct {
	Owner         owner.Contract
	ScopeSnapshot project.Scope
	Credentials   *credential.Service
	// MaterializedCredentials is an in-memory launch snapshot. A non-nil map
	// prevents projection from resolving credentials through the Store again.
	MaterializedCredentials map[string]string
	DaemonAddr              string
	AuthToken               string
	Sandbox                 bool
	RuntimePlugins          *runtimeplugin.Registry
	RuntimeExtensions       *runtimeextension.Registry
	ModelProviders          modelprovider.ProviderGetter
	// GlobalModelProviderSnapshot is an immutable pre-TX List() snapshot.
	// When non-nil (including empty Providers), Pi global projection uses only
	// this list and never calls ModelProviders.List(). Daemon launch paths that
	// project inside CreateContinuation precommit/BindGrant must set this.
	GlobalModelProviderSnapshot *GlobalModelProviderSnapshot
	ModelSnapshot               *modelprovider.Snapshot
	LaunchModelOverride         string
	SkillBundles                []skill.Bundle
	// CredentialEnvNames lists the env var names credential bindings project
	// under. Preview rendering uses it to show redacted placeholders from
	// metadata only — no credential value ever enters preview text.
	CredentialEnvNames []string
	// CapabilityCache resolves Model Context Window and Max Output Tokens
	// when a Catalog override is empty. Nil means no cache lookup.
	CapabilityCache modelprovider.CapabilityLookup
}

// ProjectRuntimeConfig writes provider-specific runtime files into the task-local
// provider home. It never writes back to host runtime config.
func ProjectRuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	if err := validateProjectionOwner(req.Owner); err != nil {
		return ConfigProjection{}, err
	}
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
			ProjectID:           req.Owner.ProjectID,
			CheckEnv:            true,
			LaunchModelOverride: req.LaunchModelOverride,
			CapabilityCache:     req.CapabilityCache,
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
	case "hermes_home":
		projection, err = projectHermesHome(layout, profile, req)
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

// resolvePreviewProfile applies the same Model Provider snapshot the launch
// projection uses, without requiring an API key. Preview and import baseline
// share this so the editor seed matches the runtime-received main config.
func resolvePreviewProfile(profile runtimeprofile.Profile, req ProjectionRequest) runtimeprofile.Profile {
	if req.ModelSnapshot != nil {
		return profileWithModelSnapshot(profile, *req.ModelSnapshot)
	}
	if req.ModelProviders == nil || strings.TrimSpace(profile.Fields.ModelProviderID) == "" {
		return profile
	}
	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile:             profile,
		Providers:           req.ModelProviders,
		Plugins:             req.RuntimePlugins,
		Credentials:         req.Credentials,
		CheckEnv:            false,
		LaunchModelOverride: req.LaunchModelOverride,
		CapabilityCache:     req.CapabilityCache,
	})
	if err != nil || snapshot.ModelProviderID == "" {
		return profile
	}
	return profileWithModelSnapshot(profile, snapshot)
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
	case runtimeprofile.ProviderHermes:
		profile.Fields.Env["HERMES_PROVIDER_ID"] = snapshot.ModelProviderID
		profile.Fields.Env["HERMES_API_MODE"] = hermesAPIMode(snapshot.Protocol)
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

// writeJSONConfigFile encodes doc as indented JSON and writes it with 0o600
// permissions. Projected runtime config files can carry resolved model
// credentials, so every JSON config is persisted owner-read/write only through
// this single audited path.
func writeJSONConfigFile(path string, doc any) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write runtime config: %w", err)
	}
	return nil
}

func projectGenericConfig(layout Layout, profile runtimeprofile.Profile) (ConfigProjection, error) {
	config := runtimeprofile.GeneratedConfig(profile)
	configPath := filepath.Join(layout.ProviderHome, "config.json")
	if err := writeJSONConfigFile(configPath, config); err != nil {
		return ConfigProjection{}, err
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
	settings, err = applyConfigOverlay(profile.Provider, settings, profile.Fields.CustomConfigFile)
	if err != nil {
		return ConfigProjection{}, fmt.Errorf("apply custom config file: %w", err)
	}
	// Merge plugin enablement from the overlay into the preview install refs.
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
	if len(materialized) > 0 {
		authPath = filepath.Join(layout.ProviderHome, "auth.json")
		authDoc := buildCodexAuth(materialized)
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
	if err := writeTaskContextFiles(layout, taskContextFromProjection(req, profile.Provider, mcpURL)); err != nil {
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
		authDoc = buildPiAuth(profile, materialized)
	}
	modelsDoc, err = applyConfigOverlay(profile.Provider, modelsDoc, profile.Fields.CustomConfigFile)
	if err != nil {
		return ConfigProjection{}, fmt.Errorf("apply custom config file: %w", err)
	}
	modelsPath := filepath.Join(agentDir, "models.json")
	// ADR 0015: when CyberPenda can list global Model Providers, the projected
	// set is exclusively those launch-ready globals (plus the single-provider
	// profile fallback when none are ready). Host ~/.pi/agent files must not
	// inject non-global / non-launch-ready providers into that runtime.
	// Host models/auth remain a fallback only when ModelProviders is unavailable
	// (legacy host-config launches without global projection).
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

	// Catalog-sourced runtime extensions (npm: install refs) and host packages
	// from ~/.pi/agent/settings.json are merged into settings.json packages.
	packages, err := projectPiSettings(agentDir, profile)
	if err != nil {
		return ConfigProjection{}, err
	}

	preview := map[string]any{
		"provider":    string(profile.Provider),
		"models_path": modelsPath,
		"models_json": modelsDoc,
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

// CloneGlobalModelProviderSnapshot returns a deep-enough copy of providers so
// later store mutations cannot change a launch's fixed projected set.
func CloneGlobalModelProviderSnapshot(providers []modelprovider.Provider) *GlobalModelProviderSnapshot {
	cloned := make([]modelprovider.Provider, len(providers))
	for i, provider := range providers {
		cloned[i] = provider
		if len(provider.Protocols) > 0 {
			cloned[i].Protocols = append([]modelprovider.Protocol(nil), provider.Protocols...)
		}
		if len(provider.Endpoints) > 0 {
			cloned[i].Endpoints = append([]modelprovider.Endpoint(nil), provider.Endpoints...)
		}
		if len(provider.Catalog.Manual) > 0 {
			cloned[i].Catalog.Manual = append([]string(nil), provider.Catalog.Manual...)
		}
		if len(provider.Catalog.Refreshed) > 0 {
			cloned[i].Catalog.Refreshed = append([]string(nil), provider.Catalog.Refreshed...)
		}
	}
	return &GlobalModelProviderSnapshot{Providers: cloned}
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
	// Global Environment Variables: every active global Credential Binding
	// projects into every Runtime as a base layer, independent of the
	// profile's credential_refs. Profile-scoped sources below override these
	// so explicit references still win.
	//
	// Deadlock constraint: this function runs BOTH outside Store transactions
	// (via MaterializeLaunchCredentials) AND inside them (the
	// MaterializedCredentials snapshot path). ResolveGlobalEnv queries SQLite,
	// so it must only run on the pre-transaction path — once global env lands in
	// the MaterializedCredentials snapshot, the in-transaction path reuses it
	// and never touches the Service again.
	env := make(map[string]string)
	if req.MaterializedCredentials != nil {
		// In-transaction path: the snapshot already contains global env vars
		// (merged by MaterializeLaunchCredentials before the TX began).
		for key, value := range req.MaterializedCredentials {
			env[key] = value
		}
		if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" && strings.TrimSpace(env[req.ModelSnapshot.APIKeyEnv]) == "" {
			return nil, fmt.Errorf("model provider API key env %s is not configured", req.ModelSnapshot.APIKeyEnv)
		}
		return env, nil
	}
	if req.Credentials != nil {
		globalEnv, err := req.Credentials.ResolveGlobalEnv()
		if err != nil {
			return nil, err
		}
		for key, value := range globalEnv {
			env[key] = value
		}
	}
	if req.ModelSnapshot != nil && req.ModelSnapshot.APIKeyEnv != "" {
		if value, ok := materializeModelProviderAPIKey(req); ok {
			env[req.ModelSnapshot.APIKeyEnv] = value
		} else {
			value := strings.TrimSpace(os.Getenv(req.ModelSnapshot.APIKeyEnv))
			if value == "" {
				return nil, fmt.Errorf("model provider API key env %s is not configured", req.ModelSnapshot.APIKeyEnv)
			}
			env[req.ModelSnapshot.APIKeyEnv] = value
		}
	}
	inline := runtimeprofile.MaterializedAPIKeys(profile)
	for key, value := range inline {
		env[key] = value
	}
	if req.Credentials != nil && req.Owner.ProjectID != "" && len(profile.Fields.CredentialRefs) > 0 {
		referenced, err := req.Credentials.ResolveMaterializedEnv(req.Owner.ProjectID, profile.Fields.CredentialRefs)
		if err != nil {
			return nil, err
		}
		for key, value := range referenced {
			env[key] = value
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

// MaterializeLaunchCredentials resolves every credential needed by one launch
// before callers enter a Store transaction. The returned values are secret and
// must remain in memory; they are never part of generated config previews.
// For Pi, this includes API keys for every launch-ready global Model Provider
// so cross-provider turns can authenticate without restart.
func MaterializeLaunchCredentials(profile runtimeprofile.Profile, req ProjectionRequest) (map[string]string, error) {
	if err := validateProjectionOwner(req.Owner); err != nil {
		return nil, err
	}
	materialized, err := resolveMaterializedCredentials(profile, req)
	if err != nil {
		return nil, err
	}
	if profile.Provider == runtimeprofile.ProviderPi {
		projected, err := listPiLaunchReadyProviders(profile, req)
		if err != nil {
			return nil, err
		}
		materialized, err = mergePiProjectedCredentials(materialized, projected, req)
		if err != nil {
			return nil, err
		}
	}
	if materialized == nil {
		return map[string]string{}, nil
	}
	return cloneMaterializedCredentials(materialized), nil
}

func validateProjectionOwner(contract owner.Contract) error {
	if contract.ID == "" {
		return nil
	}
	if err := contract.Validate(); err != nil {
		return fmt.Errorf("invalid Runtime owner contract: %w", err)
	}
	return nil
}

func cloneMaterializedCredentials(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
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
// and agent caps. Both states are explicit: Codex releases have shipped with
// the multi_agent feature default-on, so an absent key cannot carry the
// default-off control. `agents.enabled = false` forces the tools off for every
// model; `features.multi_agent = true` turns the V1 tools on for models whose
// metadata does not already select a multi-agent version. An unset cap is not
// written, so a Custom Config File may supply it — the same fill-in behavior
// as other conditional managed keys such as model_context_window.
func appendCodexMultiAgentTOML(b *strings.Builder, profile runtimeprofile.Profile) {
	enabled := runtimeprofile.CodexMultiAgentEnabled(profile)
	settings := profile.Fields.CodexMultiAgent
	b.WriteString("\n[features]\n")
	fmt.Fprintf(b, "multi_agent = %t\n", enabled)
	b.WriteString("\n[agents]\n")
	fmt.Fprintf(b, "enabled = %t\n", enabled)
	if settings != nil && enabled {
		if settings.MaxConcurrentThreadsPerSession > 0 {
			fmt.Fprintf(b, "max_concurrent_threads_per_session = %d\n", settings.MaxConcurrentThreadsPerSession)
		}
		if settings.MaxDepth > 0 {
			fmt.Fprintf(b, "max_depth = %d\n", settings.MaxDepth)
		}
	}
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

// piModelEntry projects one models.json model entry. Pi clamps
// set_thinking_level to "off" — and reasoning_effort never reaches the
// provider request — for entries without reasoning metadata. The identity
// xhigh/max mapping is required because Pi treats those two levels as
// unavailable unless thinkingLevelMap declares them.
func piModelEntry(modelID string, catalog modelprovider.Catalog, cache modelprovider.CapabilityLookup) map[string]any {
	entry := map[string]any{
		"id":               modelID,
		"reasoning":        true,
		"thinkingLevelMap": map[string]any{"xhigh": "xhigh", "max": "max"},
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
	if apiKeyRef := piAPIKeyRef(materialized); apiKeyRef != "" {
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

// piProjectedProvider is one launch-ready Model Provider prepared for Pi
// models.json / auth.json projection.
type piProjectedProvider struct {
	Provider modelprovider.Provider
	Protocol modelprovider.Protocol
	Endpoint modelprovider.Endpoint
	Models   []string
	APIKey   string
}

func listPiLaunchReadyProviders(profile runtimeprofile.Profile, req ProjectionRequest) ([]piProjectedProvider, error) {
	switch profile.Provider {
	case runtimeprofile.ProviderPi, runtimeprofile.ProviderHermes:
	default:
		return nil, nil
	}
	plugin, ok := runtimePluginForProvider(profile.Provider, req.RuntimePlugins)
	if !ok {
		return nil, nil
	}
	all, err := globalModelProvidersForProjection(req)
	if err != nil {
		return nil, err
	}
	if all == nil {
		// Global projection not available for this request.
		return nil, nil
	}
	// Stable order by ID so models.json is deterministic across launches.
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	out := make([]piProjectedProvider, 0, len(all))
	for _, provider := range all {
		entry, ready := piLaunchReadyProvider(provider, plugin, req)
		if !ready {
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

// globalModelProvidersForProjection returns the global provider list for Pi
// projection. Prefer the immutable pre-TX snapshot. List() is only a fallback
// for unit tests that project outside Store transactions; daemon launch paths
// must set GlobalModelProviderSnapshot so precommit never touches the Service.
func globalModelProvidersForProjection(req ProjectionRequest) ([]modelprovider.Provider, error) {
	if req.GlobalModelProviderSnapshot != nil {
		// Non-nil snapshot owns the set (may be empty). Copy so callers cannot
		// mutate the request snapshot while filtering.
		return append([]modelprovider.Provider(nil), req.GlobalModelProviderSnapshot.Providers...), nil
	}
	if req.ModelProviders == nil {
		return nil, nil
	}
	lister, ok := req.ModelProviders.(modelprovider.ProviderLister)
	if !ok {
		return nil, nil
	}
	all, err := lister.List()
	if err != nil {
		return nil, fmt.Errorf("list model providers for pi projection: %w", err)
	}
	return all, nil
}

// piLaunchReadyProvider returns true when a global Model Provider has a
// Pi-compatible endpoint, at least one configured catalog model, and a
// configured API key. Drafts and incomplete providers are skipped.
func piLaunchReadyProvider(provider modelprovider.Provider, plugin runtimeplugin.Plugin, req ProjectionRequest) (piProjectedProvider, bool) {
	models := provider.Catalog.Models()
	if len(models) == 0 {
		return piProjectedProvider{}, false
	}
	protocol, err := resolvePiProtocol(provider, plugin)
	if err != nil {
		return piProjectedProvider{}, false
	}
	endpoint, ok := provider.EndpointFor(protocol)
	if !ok || strings.TrimSpace(endpoint.BaseURL) == "" {
		return piProjectedProvider{}, false
	}
	apiKey, ok := resolveModelProviderAPIKeyValue(provider.APIKeyEnv, req)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return piProjectedProvider{}, false
	}
	return piProjectedProvider{
		Provider: provider,
		Protocol: protocol,
		Endpoint: endpoint,
		Models:   models,
		APIKey:   apiKey,
	}, true
}

func resolvePiProtocol(provider modelprovider.Provider, plugin runtimeplugin.Plugin) (modelprovider.Protocol, error) {
	supported := map[modelprovider.Protocol]bool{}
	for _, protocol := range plugin.ModelProvider.SupportedProtocols {
		supported[modelprovider.Protocol(protocol)] = true
	}
	for _, preferred := range plugin.ModelProvider.ProtocolPreference {
		protocol := modelprovider.Protocol(preferred)
		if supported[protocol] && provider.Supports(protocol) {
			if _, ok := provider.EndpointFor(protocol); ok {
				return protocol, nil
			}
		}
	}
	return "", modelprovider.ErrIncompatibleProtocol
}

func resolveModelProviderAPIKeyValue(envName string, req ProjectionRequest) (string, bool) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", false
	}
	// A non-nil MaterializedCredentials map is the complete launch snapshot.
	// Missing keys mean "not configured" without re-entering the credential
	// service — BindGrant runs under CreateContinuation's open SQLite TX, so
	// Resolve would deadlock and draft/non-ready providers must stay skippable.
	if req.MaterializedCredentials != nil {
		value := strings.TrimSpace(req.MaterializedCredentials[envName])
		return value, value != ""
	}
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value, true
	}
	if req.Credentials == nil {
		return "", false
	}
	resolution, err := req.Credentials.Resolve(envName, req.Owner.ProjectID)
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

func mergePiProjectedCredentials(materialized map[string]string, projected []piProjectedProvider, req ProjectionRequest) (map[string]string, error) {
	if len(projected) == 0 {
		return materialized, nil
	}
	out := cloneMaterializedCredentials(materialized)
	if out == nil {
		out = map[string]string{}
	}
	for _, entry := range projected {
		envName := strings.TrimSpace(entry.Provider.APIKeyEnv)
		if envName == "" {
			continue
		}
		if strings.TrimSpace(out[envName]) != "" {
			continue
		}
		value := strings.TrimSpace(entry.APIKey)
		if value == "" {
			resolved, ok := resolveModelProviderAPIKeyValue(envName, req)
			if !ok {
				// Non-initial providers are skipped earlier; keep defensive.
				continue
			}
			value = resolved
		}
		out[envName] = value
	}
	return out, nil
}

func buildPiModelsFromProjected(projected []piProjectedProvider, req ProjectionRequest) map[string]any {
	providers := make(map[string]any, len(projected))
	for _, entry := range projected {
		models := make([]map[string]any, 0, len(entry.Models))
		for _, modelID := range entry.Models {
			models = append(models, piModelEntry(modelID, entry.Provider.Catalog, req.CapabilityCache))
		}
		provider := map[string]any{
			"baseUrl": strings.TrimRight(entry.Endpoint.BaseURL, "/"),
			"api":     piAPIForProtocol(entry.Protocol),
			"models":  models,
		}
		if envName := strings.TrimSpace(entry.Provider.APIKeyEnv); envName != "" {
			provider["apiKey"] = "$" + envName
		}
		providers[entry.Provider.ID] = provider
	}
	return map[string]any{"providers": providers}
}

func buildPiAuthFromProjected(projected []piProjectedProvider, materialized map[string]string) map[string]map[string]string {
	if len(projected) == 0 {
		return nil
	}
	auth := make(map[string]map[string]string, len(projected))
	for _, entry := range projected {
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			if envName := strings.TrimSpace(entry.Provider.APIKeyEnv); envName != "" {
				key = strings.TrimSpace(materialized[envName])
			}
		}
		if key == "" {
			continue
		}
		auth[entry.Provider.ID] = map[string]string{
			"type": "api_key",
			"key":  key,
		}
	}
	if len(auth) == 0 {
		return nil
	}
	return auth
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

func projectPiSettings(agentDir string, profile runtimeprofile.Profile) ([]string, error) {
	profilePackages := enabledExtensionInstallRefs(profile)
	home, err := os.UserHomeDir()
	var hostSettings map[string]any
	if err == nil {
		hostPath := filepath.Join(home, ".pi", "agent", "settings.json")
		if raw, readErr := os.ReadFile(hostPath); readErr == nil {
			var parsed map[string]any
			if unmarshalErr := json.Unmarshal(raw, &parsed); unmarshalErr == nil {
				hostSettings = parsed
			}
		}
	}

	var combinedPackages []string
	seen := make(map[string]bool)
	if hostSettings != nil {
		if rawPkgs, ok := hostSettings["packages"].([]any); ok {
			for _, item := range rawPkgs {
				if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
					str = strings.TrimSpace(str)
					if !seen[str] {
						seen[str] = true
						combinedPackages = append(combinedPackages, str)
					}
				}
			}
		}
	}
	for _, pkg := range profilePackages {
		pkg = strings.TrimSpace(pkg)
		if pkg != "" && !seen[pkg] {
			seen[pkg] = true
			combinedPackages = append(combinedPackages, pkg)
		}
	}

	if hostSettings == nil && len(combinedPackages) == 0 {
		return nil, nil
	}

	settings := make(map[string]any)
	if hostSettings != nil {
		for k, v := range hostSettings {
			settings[k] = v
		}
	}
	if len(combinedPackages) > 0 {
		settings["packages"] = combinedPackages
	}
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := writeJSONConfigFile(settingsPath, settings); err != nil {
		return nil, err
	}
	return combinedPackages, nil
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
	if limits.MaxOutputTokens >= 1 && strings.TrimSpace(env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"]) == "" {
		env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = strconv.Itoa(limits.MaxOutputTokens)
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
func LaunchProcessEnv(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx RuntimeOwnerContext) map[string]string {
	return launchProcessEnv(layout, profile, sandbox, ctx, nil)
}

func launchProcessEnv(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx RuntimeOwnerContext, registry *runtimeplugin.Registry) map[string]string {
	env := map[string]string{}
	if sandbox {
		// Claude Code allows --dangerously-skip-permissions in sandboxed containers
		// when IS_SANDBOX=1, even if the process runs as root inside Docker.
		env["IS_SANDBOX"] = "1"
		env["PENTEST_SKILLS_DIR"] = sandboxSkillsImagePath
	}
	if ctx.Owner.ProjectID != "" {
		env["PENTEST_PROJECT_ID"] = ctx.Owner.ProjectID
	}
	if ctx.Owner.TaskID != "" {
		env["PENTEST_TASK_ID"] = ctx.Owner.TaskID
	}
	if ctx.Owner.SessionID != "" {
		env["PENTEST_SESSION_ID"] = ctx.Owner.SessionID
	}
	if ctx.MCPURL != "" {
		env["PENTEST_MCP_URL"] = ctx.MCPURL
	}
	// PENTEST_AUTH_TOKEN is deliberately never projected: the daemon operator
	// token authorizes the full API and must stay outside Runtime boundaries.
	// Runtime traffic authenticates with the narrower PENTEST_INTERFACE_TOKEN.
	if ctx.InterfaceToken != "" {
		env["PENTEST_INTERFACE_TOKEN"] = ctx.InterfaceToken
	}
	if ctx.APIURL != "" {
		env["PENTEST_API_URL"] = ctx.APIURL
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
func LaunchProcessEnvWithCredentials(layout Layout, profile runtimeprofile.Profile, sandbox bool, ctx RuntimeOwnerContext, req ProjectionRequest) (map[string]string, error) {
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
	if req.ModelSnapshot == nil || strings.TrimSpace(req.ModelSnapshot.APIKeyEnv) == "" {
		return "", false
	}
	if req.MaterializedCredentials != nil {
		value := strings.TrimSpace(req.MaterializedCredentials[req.ModelSnapshot.APIKeyEnv])
		return value, value != ""
	}
	if req.Credentials == nil {
		return "", false
	}
	resolution, err := req.Credentials.Resolve(req.ModelSnapshot.APIKeyEnv, req.Owner.ProjectID)
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
