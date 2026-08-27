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

// CodexMultiAgentPreview reports whether the Codex Runtime will receive
// in-turn multi-agent tools for this launch. It is a projection preview only:
// spawn remains a model tool inside a Work Runtime Turn, never a Harness-owned
// worker scheduling surface.
type CodexMultiAgentPreview struct {
	Enabled                        bool `json:"enabled"`
	MaxConcurrentThreadsPerSession int  `json:"max_concurrent_threads_per_session,omitempty"`
	MaxDepth                       int  `json:"max_depth,omitempty"`
}

// Result is the full preflight outcome for a task launch.
type Result struct {
	Pass              bool                      `json:"pass"`
	Checks            []Check                   `json:"checks"`
	Skills            []SkillPreview            `json:"skills,omitempty"`
	RuntimeExtensions []RuntimeExtensionPreview `json:"runtime_extensions,omitempty"`
	ModelProvider     *ModelProviderPreview     `json:"model_provider,omitempty"`
	CodexMultiAgent   *CodexMultiAgentPreview   `json:"codex_multi_agent,omitempty"`
}

// Request describes what to validate for a task launch.
type Request struct {
	// RuntimeProfileID is the id of the runtime profile the task will use.
	RuntimeProfileID string
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

	// Check 1: the runtime profile exists and is loadable.
	profile, err := s.profiles.Get(request.RuntimeProfileID)
	profileLoaded := err == nil
	if err != nil {
		result.add(Check{
			Name:   "runtime_profile",
			Status: CheckFail,
			Detail: notFoundOrError("runtime profile", request.RuntimeProfileID, err),
		})
		// Without a profile we cannot resolve credential refs, but we still run
		// the runner check so the result lists every problem.
	} else {
		result.add(Check{Name: "runtime_profile", Status: CheckPass})
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
		enabledSkills, err := s.skills.EnabledSkills(profile.ID)
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
			bundles, err := s.skills.EnabledSkillBundles(profile.ID)
			if err != nil {
				result.add(Check{
					Name:   "skills",
					Status: CheckFail,
					Detail: fmt.Sprintf("resolve enabled skill bundles: %v", err),
				})
			} else if bundleErr := validateEnabledSkillBundles(bundles); bundleErr != nil {
				result.add(Check{
					Name:   "skills",
					Status: CheckFail,
					Detail: bundleErr.Error(),
				})
			} else if len(enabledSkills) == 0 {
				result.add(Check{Name: "skills", Status: CheckPass, Detail: "no enabled skills"})
			} else {
				result.add(Check{Name: "skills", Status: CheckPass, Detail: fmt.Sprintf("%d enabled skill(s)", len(enabledSkills))})
			}
		}
	}

	if profileLoaded {
		s.checkRuntimeExtensions(&result, profile, request)
	}

	if profileLoaded && s.modelProviders != nil && shouldCheckModelProvider(profile, s.runtimePlugins) {
		snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
			Profile:             profile,
			Providers:           s.modelProviders,
			Plugins:             s.runtimePlugins,
			Credentials:         s.creds,
			ProjectID:           request.ProjectID,
			CheckEnv:            true,
			LaunchModelOverride: request.LaunchModelOverride,
			CapabilityCache:     s.capabilityCache,
		})
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

	// Codex multi-agent tools preview: whether the projected Codex config will
	// give the Runtime in-turn spawn tools. Informational only; it never gates
	// launch and never implies Harness-owned subagent scheduling.
	if profileLoaded && profile.Provider == runtimeprofile.ProviderCodex {
		preview := &CodexMultiAgentPreview{
			Enabled: runtimeprofile.CodexMultiAgentEnabled(profile),
		}
		if settings := profile.Fields.CodexMultiAgent; settings != nil && preview.Enabled {
			preview.MaxConcurrentThreadsPerSession = settings.MaxConcurrentThreadsPerSession
			preview.MaxDepth = settings.MaxDepth
		}
		result.CodexMultiAgent = preview
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

func validateEnabledSkillBundles(bundles []skill.Bundle) error {
	for _, bundle := range bundles {
		meta := skill.Metadata{
			ID:   skill.DisplayID(bundle.ID, bundle.Source),
			Name: skill.DisplayName(bundle.Name, bundle.ID, bundle.Source),
		}
		if err := skill.ValidateBundle(bundle.Path, meta); err != nil {
			return err
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
