package preflight

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"pentest/internal/credential"
	"pentest/internal/modeskill"
	"pentest/internal/runner"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

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
			if err := modeskill.ValidateFGSBundleCompatibility(mode, bundle); err != nil {
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
	// The probes are independent, so run them concurrently.
	candidates := []string{"docker", "podman"}
	probes := make([]struct {
		info runner.EngineInfo
		err  error
	}, len(candidates))
	var wg sync.WaitGroup
	for index, candidate := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probes[index].info, probes[index].err = s.detectEngine(ctx, candidate)
		}()
	}
	wg.Wait()
	// A cancelled request must not pass preflight, even if a probe double
	// ignored the context.
	if err := ctx.Err(); err != nil {
		result.add(Check{
			Name:   "container_engine",
			Status: CheckFail,
			Detail: fmt.Sprintf("container engine probe cancelled: %v", err),
		})
		return
	}
	availability := make([]string, 0, len(candidates))
	var selectedInfo runner.EngineInfo
	var selectedErr error
	for index, candidate := range candidates {
		info, err := probes[index].info, probes[index].err
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
		info, err := s.detectEngine(ctx, selected)
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
