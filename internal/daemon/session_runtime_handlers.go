package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
)

// sessionRuntimeInput is deliberately smaller than Task launch input. A
// Session can select a Runtime Profile, provider turn fields, and runner
// activation, but it has no Project defaults, Scope, Project credentials, or
// Project Interface inputs.
type sessionRuntimeInput struct {
	RuntimeProfileID   string         `json:"runtime_profile_id"`
	RuntimePluginID    string         `json:"runtime_plugin_id"`
	Provider           string         `json:"provider"`
	RuntimeProvider    string         `json:"runtime_provider"`
	ModelProviderID    string         `json:"model_provider_id"`
	Model              string         `json:"model"`
	ModelOverride      string         `json:"model_override"`
	ReasoningEffort    string         `json:"reasoning_effort"`
	ForceReplace       bool           `json:"force_replace,omitempty"`
	Runner             string         `json:"runner"`
	HostActivated      bool           `json:"host_activated"`
	HostBrowserEnabled bool           `json:"host_browser_enabled"`
	RuntimeConfig      map[string]any `json:"runtime_config"`
	// ContainerCLI / SandboxNetwork / SandboxVPNTun come from run_controls and
	// select the host container engine for this Session launch.
	ContainerCLI   string `json:"container_cli,omitempty"`
	SandboxNetwork string `json:"sandbox_network,omitempty"`
	SandboxVPNTun  bool   `json:"sandbox_vpn_tun,omitempty"`
}

func (input sessionRuntimeInput) selectedModel() string {
	if model := strings.TrimSpace(input.Model); model != "" {
		return model
	}
	return strings.TrimSpace(input.ModelOverride)
}

func (input sessionRuntimeInput) provider() runtimeprofile.Provider {
	value := strings.TrimSpace(input.RuntimeProvider)
	if value == "" {
		value = strings.TrimSpace(input.Provider)
	}
	if value == "" {
		value = strings.TrimSpace(input.RuntimePluginID)
	}
	return runtimeprofile.Provider(value)
}

type sessionRuntimePlan struct {
	Adapter              runtime.Adapter
	Profile              runtimeprofile.Profile
	Runner               session.Runner
	LaunchGoal           string
	RuntimeConfig        map[string]any
	Selection            runtime.ProviderSessionRequest
	BlackboardProjection runner.BlackboardProjection
	Metadata             func() (runtime.NativeSessionMetadata, error)
	StopConfirmation     runtime.StopConfirmation
	ProviderHome         string
	Facts                ProviderSessionLaunchFacts
}

func sessionGoalWithAttachmentReferences(goal, workdir string, run session.Runner, references []session.AttachmentReference) string {
	paths := make([]string, 0, len(references))
	for _, reference := range references {
		hostPath := filepath.Join(workdir, reference.RelativePath)
		info, err := os.Stat(hostPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		paths = append(paths, ownerAttachmentGoalPath(task.Runner(run), workdir, reference.RelativePath))
	}
	return appendAttachmentPathsToGoal(goal, paths)
}

func sessionGoalWithAttachmentEvents(goal, workdir string, run session.Runner, events []session.Event) string {
	references := make([]session.AttachmentReference, 0, len(events))
	for _, event := range events {
		reference, ok := event.AttachmentReference()
		if ok {
			references = append(references, reference)
		}
	}
	return sessionGoalWithAttachmentReferences(goal, workdir, run, references)
}

func (server *Server) sessionLaunchGoal(found session.Session, goal string, run session.Runner) (string, error) {
	events, err := server.sessions.EventsByKind(found.ID, session.EventKindAttachment)
	if err != nil {
		return "", err
	}
	return sessionGoalWithAttachmentEvents(goal, found.Workdir, run, events), nil
}

func (server *Server) resolveSessionRuntimeProfile(input sessionRuntimeInput, previous *session.Continuation) (runtimeprofile.Profile, error) {
	profileID := strings.TrimSpace(input.RuntimeProfileID)
	if previous != nil && profileID != "" {
		return runtimeprofile.Profile{}, fmt.Errorf("runtime_profile_locked")
	}
	if profileID != "" {
		profile, err := server.profiles.Get(profileID)
		if err != nil {
			return runtimeprofile.Profile{}, err
		}
		if provider := input.provider(); provider != "" && provider != profile.Provider {
			return runtimeprofile.Profile{}, fmt.Errorf("runtime profile provider does not match requested provider")
		}
		return profile, nil
	}
	if previous != nil {
		versions, err := server.sessions.RuntimeConfigVersions(previous.SessionID)
		if err != nil || len(versions) == 0 {
			return runtimeprofile.Profile{}, session.ErrMissingRuntimeProfile
		}
		latest := versions[len(versions)-1]
		captured, err := runtimeProfileFromSnapshot(latest.Config)
		if err != nil {
			return runtimeprofile.Profile{}, err
		}
		provider := captured.Provider
		fields := captured.Fields
		if requested := strings.TrimSpace(input.ModelProviderID); requested != "" {
			fields.ModelProviderID = requested
		}
		if model := input.selectedModel(); model != "" {
			fields.ModelOverride = model
		}
		if effort := strings.TrimSpace(input.ReasoningEffort); effort != "" {
			fields.ReasoningEffort = effort
		}
		return runtimeprofile.Profile{Provider: provider, Fields: fields}, nil
	}

	provider := input.provider()
	if provider == runtimeprofile.ProviderFake {
		return runtimeprofile.Profile{}, fmt.Errorf("runtime profile is required for the fake provider")
	}
	if provider == "" || strings.TrimSpace(input.ModelProviderID) == "" {
		return runtimeprofile.Profile{}, session.ErrMissingRuntimeProfile
	}
	if input.selectedModel() == "" {
		return runtimeprofile.Profile{}, modelprovider.ErrMissingModel
	}
	return runtimeprofile.Profile{Provider: provider, Fields: runtimeprofile.Fields{
		ModelProviderID: input.ModelProviderID,
		ModelOverride:   input.selectedModel(),
		ReasoningEffort: input.ReasoningEffort,
		DefaultRunner:   input.Runner,
	}}, nil
}

func resolveSessionRunner(input sessionRuntimeInput, profile runtimeprofile.Profile, previous *session.Continuation) (session.Runner, error) {
	value := strings.TrimSpace(input.Runner)
	if value == "" && previous != nil {
		value = string(previous.Runner)
	}
	if value == "" {
		value = strings.TrimSpace(profile.Fields.DefaultRunner)
	}
	if value == "" {
		value = string(session.RunnerSandbox)
	}
	run := session.Runner(value)
	if run != session.RunnerSandbox && run != session.RunnerHost {
		return "", session.ErrInvalidRunner
	}
	if err := runner.ValidateActivation(runner.ActivationRequest{Runner: runner.Runner(run), HostActivated: input.HostActivated}); err != nil {
		return "", err
	}
	return run, nil
}

func sessionRuntimeConfig(profile runtimeprofile.Profile, run session.Runner, input sessionRuntimeInput) map[string]any {
	config := map[string]any{
		"runtime_profile_id": profile.ID,
		"provider":           string(profile.Provider),
		"runner":             string(run),
	}
	if providerID := strings.TrimSpace(input.ModelProviderID); providerID != "" {
		config["model_provider_id"] = providerID
	} else if providerID := strings.TrimSpace(profile.Fields.ModelProviderID); providerID != "" {
		config["model_provider_id"] = providerID
	}
	if model := input.selectedModel(); model != "" {
		config["model"] = model
	} else if model := strings.TrimSpace(profile.Fields.ModelOverride); model != "" {
		config["model"] = model
	} else if model := strings.TrimSpace(profile.Fields.Model); model != "" {
		config["model"] = model
	}
	if effort := strings.TrimSpace(input.ReasoningEffort); effort != "" {
		config["reasoning_effort"] = effort
	} else if effort := strings.TrimSpace(profile.Fields.ReasoningEffort); effort != "" {
		config["reasoning_effort"] = effort
	}
	return config
}

func sessionSelection(profile runtimeprofile.Profile, config map[string]any, input sessionRuntimeInput) (runtime.ProviderSessionRequest, error) {
	selection := runtime.ProviderSessionRequest{TurnKind: runtime.RuntimeTurnKindWork}
	selection.ModelProviderID = stringConfig(config, "model_provider_id")
	if selection.ModelProviderID == "" {
		selection.ModelProviderID = strings.TrimSpace(profile.Fields.ModelProviderID)
	}
	selection.Model = stringConfig(config, "model")
	if selection.Model == "" {
		selection.Model = strings.TrimSpace(profile.Fields.ModelOverride)
	}
	if selection.Model == "" {
		selection.Model = strings.TrimSpace(profile.Fields.Model)
	}
	if model := input.selectedModel(); model != "" {
		selection.Model = model
	}
	currentEffort := stringConfig(config, "reasoning_effort")
	if currentEffort == "" {
		currentEffort = profile.Fields.ReasoningEffort
	}
	reasoning, err := runtimeprofile.ResolveRequestedReasoningEffort(input.ReasoningEffort, currentEffort, profile.Fields.ReasoningEffort)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	selection.RequestedReasoningEffort = string(reasoning)
	return selection, nil
}

func stringConfig(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	value, _ := config[key].(string)
	return strings.TrimSpace(value)
}
