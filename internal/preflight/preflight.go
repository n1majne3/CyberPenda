// Package preflight runs the recorded startup checks that determine whether a
// task can launch its runtime. Preflight fails before runtime execution when a
// required runtime profile, configuration, sandbox, or credential resolution is
// missing. A preflight failure prevents runtime launch and is recorded in the
// Task timeline by the caller.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/modeskill"
	"pentest/internal/runner"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

// CheckStatus is the outcome for a single preflight check.
type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckFail CheckStatus = "fail"
)

// Check is one named preflight result.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

type SkillPreview struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RuntimeExtensionPreview struct {
	ID           string                        `json:"id"`
	Name         string                        `json:"name,omitempty"`
	Source       string                        `json:"source"`
	InstallRef   string                        `json:"install_ref,omitempty"`
	Registry     string                        `json:"registry,omitempty"`
	Requirements runtimeextension.Requirements `json:"requirements,omitempty"`
}

type ModelProviderPreview struct {
	ModelProviderID       string `json:"model_provider_id,omitempty"`
	ModelProviderName     string `json:"model_provider_name,omitempty"`
	EndpointBaseURL       string `json:"endpoint_base_url,omitempty"`
	BaseURL               string `json:"base_url,omitempty"`
	Protocol              string `json:"protocol,omitempty"`
	Model                 string `json:"model,omitempty"`
	APIKeyEnv             string `json:"api_key_env,omitempty"`
	APIKeySource          string `json:"api_key_source,omitempty"`
	ProjectionTarget      string `json:"projection_target,omitempty"`
	ContextWindow         int    `json:"context_window,omitempty"`
	MaxOutputTokens       int    `json:"max_output_tokens,omitempty"`
	ContextWindowSource   string `json:"context_window_source,omitempty"`
	MaxOutputTokensSource string `json:"max_output_tokens_source,omitempty"`
}

// CodexMultiAgentPreview reports the multi-agent tool state the projected
// Codex config will carry for this launch. State is "inherit" (no keys
// projected; Codex's own feature default applies), "on", or "off". It is a
// projection preview only: spawn remains a model tool inside a Work Runtime
// Turn, never a Harness-owned subagent scheduling surface.
type CodexMultiAgentPreview struct {
	State                          string `json:"state"`
	MaxConcurrentThreadsPerSession int    `json:"max_concurrent_threads_per_session,omitempty"`
	MaxDepth                       int    `json:"max_depth,omitempty"`
}

// Result is the full preflight outcome for a task launch.
type Result struct {
	Pass              bool                      `json:"pass"`
	Checks            []Check                   `json:"checks"`
	Skills            []SkillPreview            `json:"skills,omitempty"`
	ModeSkill         *SkillPreview             `json:"mode_skill,omitempty"`
	RuntimeExtensions []RuntimeExtensionPreview `json:"runtime_extensions,omitempty"`
	ModelProvider     *ModelProviderPreview     `json:"model_provider,omitempty"`
	CodexMultiAgent   *CodexMultiAgentPreview   `json:"codex_multi_agent,omitempty"`
}

// Request describes what to validate for a task launch.
type Request struct {
	// RuntimeProfileID is the id of the runtime profile the task will use.
	RuntimeProfileID string
	// Profile is a source-neutral launch adapter. Direct configuration and
	// already-captured Snapshots pass a detached Profile and never require a
	// global Runtime Profile lookup.
	Profile *runtimeprofile.Profile
	// ProjectID scopes credential resolution. Project defaults may be empty when
	// the task overrides them; the caller decides whether that is allowed.
	ProjectID string
	// CredentialRefsToResolve forces resolution of these references in addition
	// to whatever the runtime profile declares. Useful when project defaults add
	// references the profile does not.
	CredentialRefsToResolve []string
	// Runner is the selected runner. An empty runner defaults to sandbox.
	Runner string
	// HostActivated is true when the operator explicitly confirmed host runner.
	HostActivated bool
	// LaunchModelOverride applies a task-only model choice without editing the profile.
	LaunchModelOverride string
	// ModelProviderSnapshot is the immutable provider configuration captured by
	// an existing Runtime Owner. Resume validates its current Secret source but
	// never reloads mutable global Model Provider fields.
	ModelProviderSnapshot *modelprovider.Snapshot
	// CapturedSkillIDs is non-nil for an existing Runtime Owner. Resume checks
	// exactly this immutable set and ignores later defaults and Profile Opt-Outs.
	CapturedSkillIDs []string
	BlackboardMode   modeskill.Mode
	// ProjectKind and ScopeCapabilities are explicit operator-owned state used
	// only to validate Runtime Extension Requirements.
	ProjectKind       string
	ScopeCapabilities []string
	// ContainerCLI is the host container executable (docker or podman). Empty
	// defaults to docker. Used only when Runner is sandbox.
	ContainerCLI string
	// SandboxVPNTun requests opt-in TUN device + NET_ADMIN for OpenVPN.
	SandboxVPNTun bool
	// SandboxNetwork is the selected sandbox network mode (for conflict checks).
	SandboxNetwork string
	// RuntimeRoot is the host directory for task workdirs and bind mounts.
	// Empty skips the path readiness check (daemon default is applied later).
	RuntimeRoot string
}

// ProfileGetter loads runtime profiles for preflight checks.
type ProfileGetter interface {
	Get(id string) (runtimeprofile.Profile, error)
}

type SkillGetter interface {
	Get(id string) (skill.Skill, error)
	EnabledSkills(profileID string) ([]skill.Skill, error)
	EnabledSkillBundles(profileID string) ([]skill.Bundle, error)
}

// Service runs preflight against the runtime profile and credential services.
type Service struct {
	profiles          ProfileGetter
	creds             *credential.Service
	skills            SkillGetter
	modelProviders    modelprovider.ProviderGetter
	runtimePlugins    *runtimeplugin.Registry
	runtimeExtensions *runtimeextension.Registry
	capabilityCache   modelprovider.CapabilityLookup
	// containerRunner probes the host container CLI. Nil uses the real CLI.
	containerRunner runner.CommandRunner
	// hermesACPProbe checks that a host Hermes binary exposes the ACP extra.
	// Nil uses the default `hermes acp --help` probe.
	hermesACPProbe func(binary string) error
}

// NewService returns a preflight Service.
func NewService(profiles ProfileGetter, creds *credential.Service, skillGetters ...SkillGetter) *Service {
	svc := &Service{profiles: profiles, creds: creds}
	if len(skillGetters) > 0 {
		svc.skills = skillGetters[0]
	}
	return svc
}

func (s *Service) WithModelProviders(providers modelprovider.ProviderGetter, plugins *runtimeplugin.Registry) *Service {
	s.modelProviders = providers
	s.runtimePlugins = plugins
	return s
}

func (s *Service) WithRuntimeExtensions(registry *runtimeextension.Registry) *Service {
	s.runtimeExtensions = registry
	return s
}

func (s *Service) WithCapabilityCache(cache modelprovider.CapabilityLookup) *Service {
	s.capabilityCache = cache
	return s
}

// WithContainerRunner injects the container CLI probe used for sandbox engine
// and VPN TUN checks. Tests supply fakes; production leaves this nil.
func (s *Service) WithContainerRunner(run runner.CommandRunner) *Service {
	s.containerRunner = run
	return s
}

// WithHermesACPProbe injects the Host Hermes ACP extra check. Tests supply
// fakes; production leaves this nil and runs `hermes acp --help`.
func (s *Service) WithHermesACPProbe(probe func(binary string) error) *Service {
	s.hermesACPProbe = probe
	return s
}

// Run executes all preflight checks for a launch request.
func (s *Service) Run(ctx context.Context, request Request) Result {
	result := Result{Pass: true}
	if request.BlackboardMode != "" {
		spec, err := modeskill.Resolve(request.BlackboardMode)
		if err != nil {
			result.add(Check{Name: "mode_skill", Status: CheckFail, Detail: err.Error()})
		} else {
			result.ModeSkill = &SkillPreview{ID: spec.ID, Name: spec.Name}
			result.add(Check{Name: "mode_skill", Status: CheckPass, Detail: spec.ID})
		}
	}

	// Check 1: a source-neutral Runtime configuration is loadable.
	var profile runtimeprofile.Profile
	var err error
	if request.Profile != nil {
		profile = *request.Profile
	} else if s.profiles != nil {
		profile, err = s.profiles.Get(request.RuntimeProfileID)
	} else {
		err = runtimeprofile.ErrNotFound
	}
	profileLoaded := err == nil
	if err != nil {
		result.add(Check{
			Name:   "runtime_configuration",
			Status: CheckFail,
			Detail: notFoundOrError("runtime configuration", request.RuntimeProfileID, err),
		})
		// Without a profile we cannot resolve credential refs, but we still run
		// the runner check so the result lists every problem.
	} else {
		result.add(Check{Name: "runtime_configuration", Status: CheckPass})
	}

	// Structured Model Provider, model, and Reasoning Effort fields are
	// authoritative. Reject Custom Args that redefine them; do not migrate or
	// strip the conflicting values.
	if profileLoaded {
		if err := runtimeprofile.ValidateCustomArgs(profile.Provider, profile.Fields.CustomArgs); err != nil {
			result.add(Check{
				Name:   "custom_args",
				Status: CheckFail,
				Detail: err.Error(),
			})
		} else {
			result.add(Check{Name: "custom_args", Status: CheckPass})
		}
	}

	if profileLoaded && s.skills != nil {
		var enabledSkills []skill.Skill
		var bundles []skill.Bundle
		var err error
		skipNote := ""
		if request.CapturedSkillIDs != nil {
			enabledSkills = make([]skill.Skill, 0, len(request.CapturedSkillIDs))
			bundles = make([]skill.Bundle, 0, len(request.CapturedSkillIDs))
			unavailable := 0
			for _, skillID := range request.CapturedSkillIDs {
				captured, captureErr := s.skills.Get(skillID)
				if captureErr != nil {
					// Retired builtins stay in historical snapshots; resume
					// skips them instead of failing the skills check.
					if errors.Is(captureErr, skill.ErrNotFound) {
						unavailable++
						continue
					}
					err = captureErr
					break
				}
				enabledSkills = append(enabledSkills, captured)
				bundles = append(bundles, skill.Bundle{ID: captured.ID, Name: captured.Name, Source: captured.Source, Path: captured.BundlePath})
			}
			if err == nil && unavailable > 0 {
				skipNote = fmt.Sprintf(", %d captured skill(s) unavailable and skipped", unavailable)
			}
		} else {
			enabledSkills, err = s.skills.EnabledSkills(profile.ID)
			if err == nil {
				bundles, err = s.skills.EnabledSkillBundles(profile.ID)
			}
		}
		if err != nil {
			result.add(Check{
				Name:   "skills",
				Status: CheckFail,
				Detail: fmt.Sprintf("resolve enabled skills: %v", err),
			})
		} else {
			for _, enabled := range enabledSkills {
				preview := SkillPreview{
					ID:   skill.DisplayID(enabled.ID, enabled.Source),
					Name: skill.DisplayName(enabled.Name, enabled.ID, enabled.Source),
				}
				result.Skills = append(result.Skills, preview)
			}
			if bundleErr := validateEnabledSkillBundles(bundles, request.BlackboardMode); bundleErr != nil {
				result.add(Check{
					Name:   "skills",
					Status: CheckFail,
					Detail: bundleErr.Error(),
				})
			} else if len(enabledSkills) == 0 {
				result.add(Check{Name: "skills", Status: CheckPass, Detail: "no enabled skills" + skipNote})
			} else {
				result.add(Check{Name: "skills", Status: CheckPass, Detail: fmt.Sprintf("%d enabled skill(s)%s", len(enabledSkills), skipNote)})
			}
		}
	}

	if profileLoaded {
		s.checkRuntimeExtensions(&result, profile, request)
	}

	if profileLoaded && shouldCheckModelProvider(profile, s.runtimePlugins) {
		var snapshot modelprovider.Snapshot
		var err error
		if request.ModelProviderSnapshot != nil {
			snapshot = *request.ModelProviderSnapshot
			if snapshot.APIKeyEnv != "" {
				if !modelprovider.APIKeySourceAvailable(s.creds, request.ProjectID, snapshot.APIKeyEnv) {
					err = fmt.Errorf("model provider API key environment variable %s is not configured", snapshot.APIKeyEnv)
				}
			}
		} else if s.modelProviders == nil {
			err = modelprovider.ErrMissingProvider
		} else {
			snapshot, err = modelprovider.Resolve(modelprovider.ResolveRequest{
				Profile:             profile,
				Providers:           s.modelProviders,
				Plugins:             s.runtimePlugins,
				Credentials:         s.creds,
				ProjectID:           request.ProjectID,
				CheckEnv:            true,
				LaunchModelOverride: request.LaunchModelOverride,
				CapabilityCache:     s.capabilityCache,
			})
		}
		if err != nil {
			result.add(Check{Name: "model_provider", Status: CheckFail, Detail: err.Error()})
		} else if snapshot.ModelProviderID != "" {
			result.ModelProvider = &ModelProviderPreview{
				ModelProviderID:       snapshot.ModelProviderID,
				ModelProviderName:     snapshot.ModelProviderName,
				EndpointBaseURL:       snapshot.EndpointBaseURL,
				BaseURL:               snapshot.BaseURL,
				Protocol:              string(snapshot.Protocol),
				Model:                 snapshot.Model,
				APIKeyEnv:             snapshot.APIKeyEnv,
				APIKeySource:          snapshot.APIKeySource,
				ProjectionTarget:      snapshot.ProjectionTarget,
				ContextWindow:         snapshot.ContextWindow,
				MaxOutputTokens:       snapshot.MaxOutputTokens,
				ContextWindowSource:   snapshot.ContextWindowSource,
				MaxOutputTokensSource: snapshot.MaxOutputTokensSource,
			}
			result.add(Check{Name: "model_provider", Status: CheckPass, Detail: fmt.Sprintf("%s via %s", snapshot.Model, snapshot.APIKeyEnv)})
		}
	}

	// Codex multi-agent tools preview: report the final merged config, including
	// Custom Config File values that fill keys the structured control leaves
	// unset. Spawn remains a model tool inside the Work Runtime Turn.
	if profileLoaded && profile.Provider == runtimeprofile.ProviderCodex {
		preview, err := mergedCodexMultiAgentPreview(profile)
		if err != nil {
			result.add(Check{Name: "custom_config_file", Status: CheckFail, Detail: fmt.Sprintf("project Codex config: %v", err)})
		} else {
			result.CodexMultiAgent = preview
		}
	}

	// Check 2: the selected runner is valid. Empty defaults to sandbox.
	runner := request.Runner
	if runner == "" {
		runner = "sandbox"
	}
	if runner != "sandbox" && runner != "host" {
		result.add(Check{
			Name:   "runner",
			Status: CheckFail,
			Detail: fmt.Sprintf("unsupported runner %q (expected sandbox or host)", runner),
		})
	} else {
		result.add(Check{Name: "runner", Status: CheckPass})
	}

	if runner == "host" && !request.HostActivated {
		result.add(Check{
			Name:   "host_activation",
			Status: CheckFail,
			Detail: "host runner requires explicit activation",
		})
	} else if runner == "host" {
		result.add(Check{Name: "host_activation", Status: CheckPass})
	}

	if profileLoaded && runner == "host" {
		s.checkHermesACP(&result, profile)
	}

	if runner == "sandbox" {
		s.checkContainerEngine(ctx, &result, request)
		s.checkSandboxRuntimeRoot(&result, request)
	}

	// Check 3: inline profile API keys, every credential reference, and every
	// global environment variable resolve. Global bindings inject into every
	// Runtime independent of credential_refs, so they are validated even when
	// the profile declares no references.
	refs := collectRefs(profile, request, runtimeprofile.HasInlineAPIKeys(profile))
	if runtimeprofile.HasInlineAPIKeys(profile) && len(refs) == 0 {
		// Inline keys cover model auth, but global env vars still must resolve.
		if detail, ok := globalEnvCheckDetail(s.creds); !ok {
			result.add(Check{Name: "credentials", Status: CheckFail, Detail: detail})
		} else {
			result.add(Check{Name: "credentials", Status: CheckPass, Detail: "inline profile API keys configured"})
		}
		return result
	}
	anyMissing := false
	var failDetails []string
	// Global environment variables are validated first so a broken global
	// binding blocks launch even when the profile has no credential_refs.
	if detail, ok := globalEnvCheckDetail(s.creds); !ok {
		anyMissing = true
		failDetails = append(failDetails, detail)
	}
	if len(refs) == 0 {
		if !anyMissing {
			result.add(Check{Name: "credentials", Status: CheckPass, Detail: "no credential references"})
		}
	} else {
		for _, ref := range refs {
			if ctx.Err() != nil {
				result.add(Check{
					Name:   "credentials",
					Status: CheckFail,
					Detail: "preflight cancelled",
				})
				return result
			}
			resolution, err := s.creds.Resolve(ref, request.ProjectID)
			if err != nil {
				failDetails = append(failDetails, fmt.Sprintf("credential %q: %v", ref, err))
				anyMissing = true
				continue
			}
			if resolution.Disabled {
				failDetails = append(failDetails, fmt.Sprintf("credential %q is disabled for this project", ref))
				anyMissing = true
				continue
			}
			if !resolution.Found {
				failDetails = append(failDetails, fmt.Sprintf("credential %q has no binding (project or global)", ref))
				anyMissing = true
				continue
			}
			// A binding that resolves is not necessarily launchable: the env var
			// may be unset, the file unreadable, the command failing, or a
			// file/command/literal source may lack the destination_env needed to
			// project under a real env var name. Validate exactly what projection
			// does (materialize + resolve destination env) so preflight catches
			// every failure mode the task would otherwise hit mid-run.
			if resolution.Source != nil {
				if _, _, err := credential.ResolveSourceEnv(*resolution.Source); err != nil {
					failDetails = append(failDetails, fmt.Sprintf("credential %q: %v", ref, err))
					anyMissing = true
				}
			}
		}
	}
	if anyMissing {
		result.add(Check{Name: "credentials", Status: CheckFail, Detail: strings.Join(failDetails, "; ")})
	} else if len(refs) > 0 {
		result.add(Check{Name: "credentials", Status: CheckPass})
	}

	return result
}

func (s *Service) checkRuntimeExtensions(result *Result, profile runtimeprofile.Profile, request Request) {
	enabled := enabledRuntimeExtensionRefs(profile.Fields.RuntimeExtensions)
	if len(enabled) == 0 {
		result.add(Check{Name: "runtime_extensions", Status: CheckPass, Detail: "no enabled runtime extensions"})
		return
	}

	var failures []string
	var requirementFailures []string
	for _, ref := range enabled {
		preview, err := resolveRuntimeExtensionPreview(ref, profile.Provider, s.runtimeExtensions)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		result.RuntimeExtensions = append(result.RuntimeExtensions, preview)
		if len(preview.Requirements.ProjectKinds) > 0 && !containsString(preview.Requirements.ProjectKinds, request.ProjectKind) {
			requirementFailures = append(requirementFailures, fmt.Sprintf("runtime extension %q requires Project kind %s", preview.ID, strings.Join(preview.Requirements.ProjectKinds, " or ")))
		}
		for _, capability := range preview.Requirements.ScopeCapabilities {
			if !containsString(request.ScopeCapabilities, capability) {
				requirementFailures = append(requirementFailures, fmt.Sprintf("runtime extension %q requires Scope capability %q", preview.ID, capability))
			}
		}
	}
	if len(failures) > 0 {
		result.add(Check{
			Name:   "runtime_extensions",
			Status: CheckFail,
			Detail: strings.Join(failures, "; "),
		})
		return
	}
	result.add(Check{
		Name:   "runtime_extensions",
		Status: CheckPass,
		Detail: fmt.Sprintf("%d enabled runtime extension(s)", len(result.RuntimeExtensions)),
	})
	if len(requirementFailures) > 0 {
		result.add(Check{Name: "runtime_extension_requirements", Status: CheckFail, Detail: strings.Join(requirementFailures, "; ")})
	} else {
		result.add(Check{Name: "runtime_extension_requirements", Status: CheckPass})
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func enabledRuntimeExtensionRefs(refs []runtimeprofile.RuntimeExtensionRef) []runtimeprofile.RuntimeExtensionRef {
	enabled := make([]runtimeprofile.RuntimeExtensionRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Enabled == nil || *ref.Enabled {
			enabled = append(enabled, ref)
		}
	}
	return enabled
}

func resolveRuntimeExtensionPreview(
	ref runtimeprofile.RuntimeExtensionRef,
	provider runtimeprofile.Provider,
	registry *runtimeextension.Registry,
) (RuntimeExtensionPreview, error) {
	if registry != nil {
		if extension, ok := registry.Get(ref.ID); ok {
			if !runtimeextension.CompatibleWith(extension, string(provider)) {
				return RuntimeExtensionPreview{}, fmt.Errorf(
					"runtime extension %q is not compatible with provider %q",
					ref.ID,
					provider,
				)
			}
			return RuntimeExtensionPreview{
				ID:           extension.ID,
				Name:         extension.Name,
				Source:       "registry",
				Requirements: extension.Requirements,
			}, nil
		}
	}
	if preview, ok := catalogRuntimeExtensionPreview(ref); ok {
		return preview, nil
	}
	return RuntimeExtensionPreview{}, fmt.Errorf("runtime extension %q not found", ref.ID)
}

func catalogRuntimeExtensionPreview(ref runtimeprofile.RuntimeExtensionRef) (RuntimeExtensionPreview, bool) {
	registry := trim(ref.Config["registry"])
	installRef := trim(ref.Config["install_ref"])
	if registry == "" && installRef == "" {
		return RuntimeExtensionPreview{}, false
	}
	preview := RuntimeExtensionPreview{
		ID:         ref.ID,
		Source:     "catalog",
		InstallRef: installRef,
		Registry:   registry,
	}
	return preview, true
}

func shouldCheckModelProvider(profile runtimeprofile.Profile, registry *runtimeplugin.Registry) bool {
	if strings.TrimSpace(profile.Fields.ModelProviderID) != "" ||
		strings.TrimSpace(profile.Fields.ModelProviderProtocol) != "" ||
		strings.TrimSpace(profile.Fields.ModelOverride) != "" {
		return true
	}
	if hasLegacyModelConfig(profile) {
		return false
	}
	plugin, ok := runtimepluginForProfile(profile, registry)
	if !ok {
		return false
	}
	return plugin.ModelProvider.Requirement == "required"
}

func hasLegacyModelConfig(profile runtimeprofile.Profile) bool {
	return strings.TrimSpace(profile.Fields.Model) != "" ||
		strings.TrimSpace(profile.Fields.Endpoint) != "" ||
		len(profile.Fields.APIKeys) > 0 ||
		len(profile.Fields.CredentialRefs) > 0
}

func runtimepluginForProfile(profile runtimeprofile.Profile, registry *runtimeplugin.Registry) (runtimeplugin.Plugin, bool) {
	if registry != nil {
		return registry.Get(string(profile.Provider))
	}
	return runtimeplugin.MustBuiltinRegistry().Get(string(profile.Provider))
}

func validateEnabledSkillBundles(bundles []skill.Bundle, mode modeskill.Mode) error {
	for _, bundle := range bundles {
		meta := skill.Metadata{
			ID:   skill.DisplayID(bundle.ID, bundle.Source),
			Name: skill.DisplayName(bundle.Name, bundle.ID, bundle.Source),
		}
		if err := skill.ValidateBundle(bundle.Path, meta); err != nil {
			return err
		}
		if mode != "" {
			if err := modeskill.ValidateBundleCompatibility(mode, bundle); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) checkSandboxRuntimeRoot(result *Result, request Request) {
	root := strings.TrimSpace(request.RuntimeRoot)
	if root == "" {
		result.add(Check{
			Name:   "sandbox_runtime_root",
			Status: CheckPass,
			Detail: "daemon default runtime root will be used at launch",
		})
		return
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		result.add(Check{
			Name:   "sandbox_runtime_root",
			Status: CheckFail,
			Detail: fmt.Sprintf("resolve runtime root %q: %v", root, err),
		})
		return
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		detail := fmt.Sprintf("cannot create runtime root %q: %v", abs, err)
		if runtime.GOOS == "windows" || runner.FormatContainerHostPath(abs) != abs {
			detail += "; on Windows share this drive with Docker/Podman Desktop File Sharing so the Desktop WSL VM can bind-mount task paths"
		}
		result.add(Check{Name: "sandbox_runtime_root", Status: CheckFail, Detail: detail})
		return
	}
	probe := filepath.Join(abs, ".pentest-preflight-write")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		detail := fmt.Sprintf("runtime root %q is not writable: %v", abs, err)
		if runtime.GOOS == "windows" {
			detail += "; check NTFS permissions and Desktop File Sharing for this path"
		}
		result.add(Check{Name: "sandbox_runtime_root", Status: CheckFail, Detail: detail})
		return
	}
	_ = os.Remove(probe)
	mountSrc := runner.FormatContainerHostPath(abs)
	detail := fmt.Sprintf("writable; sandbox bind source %s", mountSrc)
	if mountSrc != abs {
		detail += " (Windows path normalized for Desktop Linux containers)"
	}
	result.add(Check{Name: "sandbox_runtime_root", Status: CheckPass, Detail: detail})
}

func (s *Service) checkContainerEngine(ctx context.Context, result *Result, request Request) {
	selected := runner.NormalizeContainerCLI(request.ContainerCLI)
	if request.SandboxVPNTun && strings.TrimSpace(request.SandboxNetwork) == "host_proxy_only" {
		result.add(Check{
			Name:   "sandbox_vpn_tun",
			Status: CheckFail,
			Detail: "VPN TUN cannot combine with host_proxy_only network",
		})
	}

	// Probe both engines so the UI can show which CLIs are ready on this host.
	availability := make([]string, 0, 2)
	var selectedInfo runner.EngineInfo
	var selectedErr error
	for _, candidate := range []string{"docker", "podman"} {
		info, err := runner.DetectEngine(ctx, candidate, s.containerRunner)
		if err != nil {
			availability = append(availability, candidate+": unavailable")
			if candidate == selected {
				selectedErr = err
			}
			continue
		}
		availability = append(availability, candidate+": "+info.Detail)
		if candidate == selected {
			selectedInfo = info
		}
	}
	result.add(Check{
		Name:   "container_engines",
		Status: CheckPass,
		Detail: strings.Join(availability, "; "),
	})

	if selectedErr != nil {
		result.add(Check{
			Name:   "container_engine",
			Status: CheckFail,
			Detail: fmt.Sprintf("selected %s is not ready: %v", selected, selectedErr),
		})
		if request.SandboxVPNTun && strings.TrimSpace(request.SandboxNetwork) != "host_proxy_only" {
			result.add(Check{
				Name:   "sandbox_vpn_tun",
				Status: CheckFail,
				Detail: "VPN TUN requires a ready container engine",
			})
		}
		return
	}
	// DetectEngine may have used absolute test CLI only for selected path; when
	// probing bare "docker"/"podman" the selected info is from the loop above.
	if selectedInfo.CLI == "" {
		info, err := runner.DetectEngine(ctx, selected, s.containerRunner)
		if err != nil {
			result.add(Check{
				Name:   "container_engine",
				Status: CheckFail,
				Detail: err.Error(),
			})
			return
		}
		selectedInfo = info
	}
	detail := selectedInfo.Detail
	if runtime.GOOS == "windows" {
		detail += "; Windows host paths use bind mounts into the Desktop Linux VM (ADR 0025)"
	}
	result.add(Check{
		Name:   "container_engine",
		Status: CheckPass,
		Detail: fmt.Sprintf("selected %s — %s", selected, detail),
	})
	if !request.SandboxVPNTun {
		return
	}
	if strings.TrimSpace(request.SandboxNetwork) == "host_proxy_only" {
		return
	}
	ok, vpnDetail := selectedInfo.SupportsVPNTun()
	if !ok {
		result.add(Check{Name: "sandbox_vpn_tun", Status: CheckFail, Detail: vpnDetail})
		return
	}
	result.add(Check{Name: "sandbox_vpn_tun", Status: CheckPass, Detail: vpnDetail})
}

func (s *Service) checkHermesACP(result *Result, profile runtimeprofile.Profile) {
	if profile.Provider != runtimeprofile.ProviderHermes {
		return
	}
	binary := strings.TrimSpace(profile.Fields.BinaryPath)
	if binary == "" {
		binary = "hermes"
	}
	probe := s.hermesACPProbe
	if probe == nil {
		probe = defaultHermesACPProbe
	}
	if err := probe(binary); err != nil {
		result.add(Check{
			Name:   "hermes_acp",
			Status: CheckFail,
			Detail: err.Error(),
		})
		return
	}
	result.add(Check{Name: "hermes_acp", Status: CheckPass, Detail: binary + " acp"})
}

func defaultHermesACPProbe(binary string) error {
	resolved := binary
	if !filepath.IsAbs(binary) && !strings.Contains(binary, string(os.PathSeparator)) {
		found, err := exec.LookPath(binary)
		if err != nil {
			return fmt.Errorf("hermes binary %q not found on PATH", binary)
		}
		resolved = found
	}
	cmd := exec.Command(resolved, "acp", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("Hermes ACP extra is not available: %s", detail)
	}
	return nil
}

func (r *Result) add(check Check) {
	r.Checks = append(r.Checks, check)
	if check.Status == CheckFail {
		r.Pass = false
	}
}

func collectRefs(profile runtimeprofile.Profile, request Request, skipProfileRefs bool) []string {
	seen := map[string]bool{}
	var refs []string
	add := func(ref string) {
		ref = trim(ref)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if !skipProfileRefs {
		for _, ref := range profile.Fields.CredentialRefs {
			add(ref)
		}
	}
	for _, ref := range request.CredentialRefsToResolve {
		add(ref)
	}
	return refs
}

func notFoundOrError(kind, id string, err error) string {
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		return fmt.Sprintf("%s %q not found", kind, id)
	}
	return fmt.Sprintf("load %s: %v", kind, err)
}

func trim(s string) string {
	return strings.TrimSpace(s)
}

// globalEnvCheckDetail validates that every active global Credential Binding
// (a Global Environment Variable) can be materialized and projects under a real
// env var name. It returns (detail, true) when all global bindings resolve and
// (detail, false) when one cannot, with detail naming the offending credential
// reference so preflight can block launch. Global bindings inject into every
// Runtime independent of credential_refs, so this check runs unconditionally.
func globalEnvCheckDetail(creds *credential.Service) (string, bool) {
	if creds == nil {
		return "", true
	}
	if _, err := creds.ResolveGlobalEnv(); err != nil {
		return err.Error(), false
	}
	return "", true
}

func mergedCodexMultiAgentPreview(profile runtimeprofile.Profile) (*CodexMultiAgentPreview, error) {
	config, err := runner.MergedProjectedConfig(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		return nil, err
	}
	preview := &CodexMultiAgentPreview{State: "inherit"}
	features, _ := config["features"].(map[string]any)
	agents, _ := config["agents"].(map[string]any)

	v2Active := false
	v2Value := features["multi_agent_v2"]
	if v2, present := codexFeatureEnabled(v2Value); present && v2 {
		preview.State = "on"
		v2Active = true
	} else if enabled, present := boolValue(agents["enabled"]); present && !enabled {
		preview.State = "off"
	} else if enabled, present := codexFeatureEnabled(features["multi_agent"]); present {
		if enabled {
			preview.State = "on"
		} else {
			preview.State = "off"
		}
	}

	if preview.State == "on" {
		if v2Active {
			if v2Config, ok := v2Value.(map[string]any); ok {
				preview.MaxConcurrentThreadsPerSession, err = positiveConfigInt(
					"features.multi_agent_v2.max_concurrent_threads_per_session",
					v2Config["max_concurrent_threads_per_session"],
				)
			} else {
				// A boolean V2 feature uses the legacy agents thread cap as its
				// compatibility fallback. max_depth remains V1-only.
				preview.MaxConcurrentThreadsPerSession, err = positiveConfigInt(
					"agents.max_concurrent_threads_per_session",
					agents["max_concurrent_threads_per_session"],
				)
			}
		} else {
			preview.MaxConcurrentThreadsPerSession, err = positiveConfigInt(
				"agents.max_concurrent_threads_per_session",
				agents["max_concurrent_threads_per_session"],
			)
			if err == nil {
				preview.MaxDepth, err = positiveConfigInt("agents.max_depth", agents["max_depth"])
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return preview, nil
}

func codexFeatureEnabled(value any) (bool, bool) {
	if enabled, ok := boolValue(value); ok {
		return enabled, true
	}
	if config, ok := value.(map[string]any); ok {
		// Codex's configurable feature form is enabled only by its explicit
		// `enabled` member. Other settings (for example V2 thread caps) do not
		// turn the feature on by themselves.
		return boolValue(config["enabled"])
	}
	return false, false
}

func boolValue(value any) (bool, bool) {
	enabled, ok := value.(bool)
	return enabled, ok
}

func positiveConfigInt(path string, value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	var converted int64
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return typed, nil
		}
		return 0, nil
	case int64:
		converted = typed
	case uint64:
		if typed > uint64(^uint(0)>>1) {
			return 0, fmt.Errorf("%s is outside the supported integer range", path)
		}
		return int(typed), nil
	default:
		return 0, fmt.Errorf("%s must be an integer", path)
	}
	if converted < 1 || int64(int(converted)) != converted {
		if converted < 1 {
			return 0, nil
		}
		return 0, fmt.Errorf("%s is outside the supported integer range", path)
	}
	return int(converted), nil
}
