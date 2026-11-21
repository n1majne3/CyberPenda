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

type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckFail CheckStatus = "fail"
)

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

type Result struct {
	Pass              bool                      `json:"pass"`
	Checks            []Check                   `json:"checks"`
	Skills            []SkillPreview            `json:"skills,omitempty"`
	ModeSkill         *SkillPreview             `json:"mode_skill,omitempty"`
	RuntimeExtensions []RuntimeExtensionPreview `json:"runtime_extensions,omitempty"`
	ModelProvider     *ModelProviderPreview     `json:"model_provider,omitempty"`
	CodexMultiAgent   *CodexMultiAgentPreview   `json:"codex_multi_agent,omitempty"`
}

type Request struct {
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
	Runner                  string
	HostActivated           bool
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
	ContainerCLI   string
	SandboxVPNTun  bool
	SandboxNetwork string
	// RuntimeRoot is the host directory for task workdirs and bind mounts.
	// Empty skips the path readiness check (daemon default is applied later).
	RuntimeRoot string
}

type ProfileGetter interface {
	Get(id string) (runtimeprofile.Profile, error)
}

type SkillGetter interface {
	Get(id string) (skill.Skill, error)
	EnabledSkills(profileID string) ([]skill.Skill, error)
	EnabledSkillBundles(profileID string) ([]skill.Bundle, error)
}

type Service struct {
	profiles          ProfileGetter
	creds             *credential.Service
	skills            SkillGetter
	modelProviders    modelprovider.ProviderGetter
	runtimePlugins    *runtimeplugin.Registry
	runtimeExtensions *runtimeextension.Registry
	capabilityCache   modelprovider.CapabilityLookup
	containerRunner   runner.CommandRunner
}

// detectEngine probes the live engine on every call. Preflight must never
// accept a launch on a stale "ready" result: an engine can stop at any time.
func (s *Service) detectEngine(ctx context.Context, cli string) (runner.EngineInfo, error) {
	if err := ctx.Err(); err != nil {
		return runner.EngineInfo{}, err
	}
	return runner.DetectEngine(ctx, cli, s.containerRunner)
}

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

func (s *Service) Run(ctx context.Context, request Request) Result {
	result := Result{Pass: true}
	if request.BlackboardMode != "" && !modeskill.Valid(request.BlackboardMode) {
		result.add(Check{Name: "blackboard_mode", Status: CheckFail, Detail: "invalid Blackboard Mode"})
	}

	var profile runtimeprofile.Profile
	var err error
	if request.Profile != nil {
		profile = *request.Profile
	} else if s.profiles != nil {
		profile, err = s.profiles.Get(request.RuntimeProfileID)
	} else {
		err = runtimeprofile.ErrNotFound
	}
	if err == nil {
		if _, supported := runtimepluginForProfile(profile, s.runtimePlugins); !supported {
			err = fmt.Errorf("runtime %q is not supported", profile.Provider)
		}
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
