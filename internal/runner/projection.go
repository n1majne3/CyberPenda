package runner

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/modeskill"
	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

var secretEnvKeyPattern = regexp.MustCompile(`(?i)(token|api[_-]?key|secret|password|auth)`)

// piExecuteAgentMarkdown is the CyberPenda Execute subagent type projected
// into every Pi runtime's agent dir. pi-subagents discovers it as the
// `execute` subagent_type, so orchestrator dispatches stop cloning the
// generic general-purpose agent. append mode keeps pi's tool-contract prompt;
// the body carries only the stable Execute identity and fact discipline.
//
//go:embed assets/execute_agent.md
var piExecuteAgentMarkdown []byte

// piDefaultPackages are the Pi packages CyberPenda always projects:
// pi-subagents powers Execute subagent dispatch and pi-web-access provides web
// fetch/search. Both are installed globally in the Sandbox and Hosted images,
// and plain npm: refs without a version range resolve from Pi's global npm
// root without network access, so launches stay offline-safe.
var piDefaultPackages = []string{"npm:@tintinweb/pi-subagents", "npm:pi-web-access"}

// GlobalModelProviderSnapshot is an immutable List() result captured before any
// Store transaction that projects runtime config. Projection filters this list
// for Pi launch-ready providers and never re-queries ModelProviders.Service.
type GlobalModelProviderSnapshot struct {
	Providers []modelprovider.Provider
}

// BlackboardProjection selects whether launch projection includes the trusted
// Project Interface and Blackboard-specific Runtime context. The zero value is
// required so existing Interactive and Assisted launches keep their behavior.
type BlackboardProjection uint8

const (
	BlackboardProjectionRequired BlackboardProjection = iota
	BlackboardProjectionOmitted
)

type ProjectionRequest struct {
	BlackboardProtocol string
	Owner              owner.Contract
	ScopeSnapshot      project.Scope
	Credentials        *credential.Service
	// MaterializedCredentials is an in-memory launch snapshot. A non-nil map
	// prevents projection from resolving credentials through the Store again.
	MaterializedCredentials map[string]string
	DaemonAddr              string
	AuthToken               string
	Sandbox                 bool
	HostBrowserMCPURL       string
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
	BlackboardMode              modeskill.Mode
	// BlackboardProjectionOmitted keeps ordinary Runtime configuration, Skills,
	// external MCP servers, credentials, and Task Scope while withholding every
	// trusted Blackboard interface and its launch context.
	BlackboardProjection BlackboardProjection
	// CredentialEnvNames lists the env var names credential bindings project
	// under. Preview rendering uses it to show redacted placeholders from
	// metadata only — no credential value ever enters preview text.
	CredentialEnvNames []string
	// CapabilityCache resolves Model Context Window and Max Output Tokens
	// when a Catalog override is empty. Nil means no cache lookup.
	CapabilityCache modelprovider.CapabilityLookup
	// RequestedReasoningEffort is the launch-level Reasoning Effort (Launch
	// Reasoning Effort Override or resolved Runtime Turn Selection). Pi
	// projection pins it as settings.json defaultThinkingLevel so subagent
	// sessions spawned by pi-subagents inherit the launch effort instead of a
	// host-copied default.
	RequestedReasoningEffort string
	capturedModelCatalog     *modelprovider.Catalog
}

// ProjectRuntimeConfig writes provider-specific runtime files into the task-local
// provider home. It never writes back to host runtime config.
func ProjectRuntimeConfig(layout Layout, profile runtimeprofile.Profile, req ProjectionRequest) (ConfigProjection, error) {
	if err := validateProjectionOwner(req.Owner); err != nil {
		return ConfigProjection{}, err
	}
	if req.BlackboardProjection == BlackboardProjectionOmitted {
		if err := preflightOmittedBlackboardProjection(layout, profile); err != nil {
			return ConfigProjection{}, err
		}
	}
	profile = profileForBlackboardProjection(profile, req.BlackboardProjection)
	if strings.TrimSpace(layout.ProviderHome) == "" {
		return ConfigProjection{}, fmt.Errorf("provider home is required")
	}
	if err := os.MkdirAll(layout.ProviderHome, 0o700); err != nil {
		return ConfigProjection{}, fmt.Errorf("prepare provider home: %w", err)
	}
	if err := projectModeAndUserSkills(layout, req); err != nil {
		return ConfigProjection{}, err
	}
	if len(req.SkillBundles) > 0 {
		// User bundles were projected by projectModeAndUserSkills above.
	}
	if req.Sandbox || len(req.SkillBundles) > 0 || req.BlackboardMode != "" {
		target := sandboxSkillsImagePath
		if len(req.SkillBundles) > 0 || req.BlackboardMode != "" {
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
	if req.BlackboardProtocol != "fgs" || req.BlackboardMode == modeskill.ModeDisabled {
		addModeSkillProjectionPreview(&projection, req.BlackboardMode, layout)
	}
	return projection, nil
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
	case runtimeprofile.ProviderClaudeCode:
		profile.Fields.Env["ANTHROPIC_BASE_URL"] = snapshot.EndpointBaseURL
		profile.Fields.Env["ANTHROPIC_MODEL"] = snapshot.Model
	}
	return profile
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

// CloneGlobalModelProviderSnapshot returns a deep-enough copy of providers so
// later store mutations cannot change a launch's fixed projected set.
func CloneGlobalModelProviderSnapshot(providers []modelprovider.Provider) *GlobalModelProviderSnapshot {
	cloned := make([]modelprovider.Provider, len(providers))
	for i, provider := range providers {
		cloned[i] = provider
		cloned[i].Catalog.Limits = maps.Clone(provider.Catalog.Limits)
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
