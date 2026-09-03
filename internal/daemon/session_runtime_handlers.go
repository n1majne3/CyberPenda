package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/modelprovider"
	"pentest/internal/modeskill"
	"pentest/internal/owner"
	"pentest/internal/preflight"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/skill"
	"pentest/internal/steering"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
	"pentest/internal/workinggraph"
)

// sessionRuntimeInput is deliberately smaller than Task launch input. A
// Session can select a Runtime Profile, provider turn fields, and runner
// activation, but it has no Project defaults, Scope, Project credentials, or
// Project Interface inputs.
type sessionRuntimeInput struct {
	RuntimeProfileID string         `json:"runtime_profile_id"`
	RuntimePluginID  string         `json:"runtime_plugin_id"`
	Provider         string         `json:"provider"`
	RuntimeProvider  string         `json:"runtime_provider"`
	ModelProviderID  string         `json:"model_provider_id"`
	Model            string         `json:"model"`
	ModelOverride    string         `json:"model_override"`
	ReasoningEffort  string         `json:"reasoning_effort"`
	ForceReplace     bool           `json:"force_replace,omitempty"`
	Runner           string         `json:"runner"`
	HostActivated    bool           `json:"host_activated"`
	RuntimeConfig    map[string]any `json:"runtime_config"`
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
	events, err := server.sessions.Events(found.ID)
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

func (server *Server) buildSessionRuntimePlan(found session.Session, goal string, input sessionRuntimeInput, profile runtimeprofile.Profile, run session.Runner, interfaceToken, nativeResumeSessionID string) (sessionRuntimePlan, error) {
	return server.buildSessionRuntimePlanForBlackboardProjection(
		found, goal, input, profile, run, interfaceToken, nativeResumeSessionID, runner.BlackboardProjectionRequired,
	)
}

func (server *Server) buildSessionRuntimePlanForBlackboardProjection(found session.Session, goal string, input sessionRuntimeInput, profile runtimeprofile.Profile, run session.Runner, interfaceToken, nativeResumeSessionID string, blackboardProjection runner.BlackboardProjection) (sessionRuntimePlan, error) {
	return server.buildSessionRuntimePlanForOwnerContext(found, goal, input, profile, run, interfaceToken, nativeResumeSessionID, blackboardProjection, "", nil)
}

func (server *Server) buildSessionRuntimePlanForOwnerContext(found session.Session, goal string, input sessionRuntimeInput, profile runtimeprofile.Profile, run session.Runner, interfaceToken, nativeResumeSessionID string, blackboardProjection runner.BlackboardProjection, continuationID string, graph *workinggraph.Projection) (sessionRuntimePlan, error) {
	launchGoal, err := server.sessionLaunchGoal(found, goal, run)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	launchGoal, err = modeskill.InjectInvocation(launchGoal, modeskill.Mode(found.RunControls.BlackboardMode))
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	capturedSnapshot, err := server.latestSessionRuntimeSnapshot(found.ID)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	modelSnapshot, err := server.resolveSessionModelSnapshotForTurn(profile, input, capturedSnapshot)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if modelSnapshot != nil {
		profile = runner.BlackboardV2ProfileWithModelSnapshot(profile, *modelSnapshot)
	}
	config := sessionRuntimeConfig(profile, run, input)
	if capturedSnapshot != nil {
		config = runtimeSnapshotMap(*capturedSnapshot)
	}
	selection, err := sessionSelection(profile, config, input)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		return sessionRuntimePlan{
			Adapter: runtime.NewFakeAdapter(), Profile: profile, Runner: run,
			LaunchGoal: launchGoal, RuntimeConfig: config, Selection: selection, BlackboardProjection: blackboardProjection,
		}, nil
	}

	runtimeRoot := filepath.Join(found.Workdir, ".runtime")
	providerDir := string(profile.Provider)
	if profile.Provider == runtimeprofile.ProviderClaudeCode {
		providerDir = "claude"
	}
	layout := runner.Layout{
		TaskRoot:     runtimeRoot,
		Workdir:      found.Workdir,
		RuntimeHome:  filepath.Join(runtimeRoot, "runtime-home"),
		ProviderHome: filepath.Join(runtimeRoot, "runtime-home", providerDir),
		SkillsRoot:   filepath.Join(runtimeRoot, "skills"),
		Artifacts:    filepath.Join(runtimeRoot, "artifacts"),
		Logs:         filepath.Join(runtimeRoot, "logs"),
	}
	for _, directory := range []string{layout.RuntimeHome, layout.ProviderHome, layout.SkillsRoot, layout.Artifacts, layout.Logs} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return sessionRuntimePlan{}, fmt.Errorf("prepare Session Runtime directory: %w", err)
		}
	}

	globalSnapshot, err := server.snapshotGlobalModelProviders()
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	materialized, err := runner.MaterializeLaunchCredentials(profile, runner.ProjectionRequest{
		Owner:       found.OwnerContract(),
		Credentials: server.creds, ModelProviders: server.modelProviders,
		GlobalModelProviderSnapshot: globalSnapshot, ModelSnapshot: modelSnapshot,
		BlackboardProjection: blackboardProjection,
		BlackboardMode:       modeskill.Mode(found.RunControls.BlackboardMode),
	})
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if server.skills == nil {
		return sessionRuntimePlan{}, fmt.Errorf("Session Runtime skills service is unavailable")
	}
	var skillBundles []skill.Bundle
	if capturedSnapshot != nil {
		skillBundles, err = server.runtimeSnapshotSkillBundles(*capturedSnapshot)
	} else {
		skillBundles, err = server.skills.EnabledSkillBundles(profile.ID)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	launchProfile := profile
	projectionRequest := runner.ProjectionRequest{
		Owner: found.OwnerContract(), DaemonAddr: server.listenAddr, AuthToken: interfaceToken,
		Credentials: server.creds, MaterializedCredentials: materialized,
		ModelProviders: server.modelProviders, GlobalModelProviderSnapshot: globalSnapshot,
		ModelSnapshot: modelSnapshot, RuntimePlugins: server.runtimePlugins,
		RuntimeExtensions: server.runtimeExtensions, SkillBundles: skillBundles,
		BlackboardMode:       modeskill.Mode(found.RunControls.BlackboardMode),
		LaunchModelOverride:  selection.Model,
		Sandbox:              run == session.RunnerSandbox,
		CapabilityCache:      server.capabilityCache,
		BlackboardProjection: blackboardProjection,
	}
	projection, err := runner.ProjectRuntimeConfig(layout, launchProfile, projectionRequest)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if projection.ResolvedProfile.Provider != "" {
		launchProfile = projection.ResolvedProfile
	}
	configPath := runner.LaunchConfigPath(layout, launchProfile.Provider, projection.ConfigPath, run == session.RunnerSandbox)
	mcpConfigPath := runner.LaunchMCPConfigPath(layout, launchProfile.Provider, run == session.RunnerSandbox, projection)
	providerCommand, err := adapters.BuildLaunchOrResumeArgs(adapters.LaunchArgsRequest{
		Provider: launchProfile.Provider, Profile: launchProfile, Goal: launchGoal,
		ConfigPath: configPath, MCPConfigPath: mcpConfigPath, Sandbox: run == session.RunnerSandbox,
	}, nativeResumeSessionID)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	// Structured launch facts for the provider-session factory: family
	// assemblers consume these instead of re-parsing the rendered one-shot
	// argv.
	launchFacts := ProviderSessionLaunchFacts{
		ProviderBinary: providerCommand[0],
		CustomArgs:     append([]string(nil), launchProfile.Fields.CustomArgs...),
		SettingsPath:   configPath,
		Workdir:        "/task/workdir",
	}
	if run != session.RunnerSandbox {
		launchFacts.Workdir = found.Workdir
	}
	if strings.TrimSpace(selection.Model) != "" {
		launchFacts.Model = strings.TrimSpace(selection.Model)
	} else {
		launchFacts.Model = strings.TrimSpace(launchProfile.Fields.Model)
	}
	launchCtx := runner.RuntimeOwnerContext{
		Owner: found.OwnerContract(), BlackboardMode: string(found.RunControls.BlackboardMode), ContinuationID: continuationID,
	}
	if graph != nil {
		launchCtx.WorkingGraphRoot = graph.Root
		launchCtx.WorkingGraphOutbox = graph.Outbox
		launchCtx.WorkingGraphReceipts = graph.Receipts
	}
	if blackboardProjection != runner.BlackboardProjectionOmitted && strings.TrimSpace(interfaceToken) != "" {
		launchCtx.InterfaceToken = interfaceToken
		launchCtx.APIURL = runner.APIEndpointURL(server.listenAddr, run == session.RunnerSandbox)
	}
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, run == session.RunnerSandbox, launchCtx, runner.ProjectionRequest{
		Owner: found.OwnerContract(), DaemonAddr: server.listenAddr, AuthToken: interfaceToken,
		Credentials: server.creds, MaterializedCredentials: materialized,
		ModelProviders: server.modelProviders, GlobalModelProviderSnapshot: globalSnapshot,
		ModelSnapshot: projection.ModelSnapshot, RuntimePlugins: server.runtimePlugins,
		RuntimeExtensions: server.runtimeExtensions, SkillBundles: skillBundles,
		BlackboardMode:       modeskill.Mode(found.RunControls.BlackboardMode),
		Sandbox:              run == session.RunnerSandbox,
		BlackboardProjection: blackboardProjection,
	})
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	adapter := runtime.Adapter(nil)
	containerIDFile := ""
	if run == session.RunnerSandbox {
		containerIDFile = filepath.Join(layout.Logs, "container.cid")
		if err := os.Remove(containerIDFile); err != nil && !os.IsNotExist(err) {
			return sessionRuntimePlan{}, err
		}
		image := strings.TrimSpace(profile.Fields.SandboxImage)
		if image == "" {
			image = strings.TrimSpace(server.sandboxImage)
		}
		if image == "" {
			image = "kalilinux/kali-rolling"
		}
		sandboxRuntime := providerCommand
		if profile.Provider == runtimeprofile.ProviderPi {
			sandboxRuntime, err = server.sandboxPiRuntimeCommand(providerCommand, launchProfile, task.Runner(run))
			if err != nil {
				return sessionRuntimePlan{}, err
			}
		}
		containerCLI := task.ResolveContainerCLI(input.ContainerCLI, server.containerCLI)
		networkMode := runner.SandboxNetworkDefault
		if strings.TrimSpace(input.SandboxNetwork) == string(runner.SandboxNetworkHostProxyOnly) {
			networkMode = runner.SandboxNetworkHostProxyOnly
		}
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout: layout, Provider: profile.Provider, Image: image,
			ContainerCLI: containerCLI, ContainerIDFile: containerIDFile,
			RuntimeCommand: sandboxRuntime, ProcessEnv: processEnv,
			NetworkMode: networkMode,
			VPNTun:      input.SandboxVPNTun,
			TaskVolume:  server.taskVolume, TaskVolumeRoot: server.taskVolumeRoot,
		})
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		adapter = runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
			Name: string(profile.Provider), ContainerCLI: command.Program, Image: image,
			CreateArgs: command.Args, SecretValues: runtime.EnvSecretValues(processEnv),
		})
	} else {
		adapter = runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name: string(profile.Provider), Program: providerCommand[0], Args: providerCommand[1:],
			Workdir: found.Workdir, Env: processEnv,
		})
	}
	adapter = withPiSessionTail(adapter, run == session.RunnerSandbox, profile.Provider, layout)

	metadata := providerNativeSessionMetadata(run == session.RunnerSandbox, profile.Provider, layout, containerIDFile)
	stopConfirmation := dockerStopConfirmation(input.ContainerCLI, server.containerCLI, containerIDFile)
	return sessionRuntimePlan{
		Adapter: adapter, Profile: launchProfile, Runner: run, LaunchGoal: launchGoal, RuntimeConfig: config, Selection: selection,
		BlackboardProjection: blackboardProjection,
		Metadata:             metadata, StopConfirmation: stopConfirmation, ProviderHome: layout.ProviderHome, Facts: launchFacts,
	}, nil
}

func (server *Server) resolveSessionModelSnapshot(profile runtimeprofile.Profile, input sessionRuntimeInput) (*modelprovider.Snapshot, error) {
	return server.resolveSessionModelSnapshotForTurn(profile, input, nil)
}

func (server *Server) resolveSessionModelSnapshotForTurn(profile runtimeprofile.Profile, input sessionRuntimeInput, prior *runtimeconfig.RuntimeConfigurationSnapshot) (*modelprovider.Snapshot, error) {
	if profile.Provider == runtimeprofile.ProviderFake {
		return nil, nil
	}
	providerID := strings.TrimSpace(input.ModelProviderID)
	if prior != nil && (providerID == "" || providerID == prior.TurnSelection.ModelProviderID) {
		captured := prior.ModelProvider
		captured.Model = firstNonBlank(input.selectedModel(), prior.TurnSelection.Model, captured.Model)
		return &captured, nil
	}
	if providerID == "" {
		providerID = strings.TrimSpace(profile.Fields.ModelProviderID)
	}
	if providerID == "" {
		return nil, nil
	}
	profile.Fields.ModelProviderID = providerID
	snapshot, err := modelprovider.Resolve(modelprovider.ResolveRequest{
		Profile: profile, Providers: server.modelProviders, Plugins: server.runtimePlugins,
		Credentials: server.creds, ProjectID: "", CheckEnv: true,
		LaunchModelOverride: input.selectedModel(),
		CapabilityCache:     server.capabilityCache,
	})
	if err != nil {
		return nil, err
	}
	if snapshot.ModelProviderID == "" {
		return nil, nil
	}
	return &snapshot, nil
}

type sessionRuntimePreparation struct {
	Profile       runtimeprofile.Profile
	Runner        session.Runner
	RuntimeConfig map[string]any
}

func (server *Server) prepareSessionRuntime(ctx context.Context, mode session.BlackboardMode, input sessionRuntimeInput, previous *session.Continuation) (sessionRuntimePreparation, error) {
	profile, err := server.resolveSessionRuntimeProfile(input, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	run, err := resolveSessionRunner(input, profile, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	var priorSnapshot *runtimeconfig.RuntimeConfigurationSnapshot
	if previous != nil {
		versions, versionErr := server.sessions.RuntimeConfigVersions(previous.SessionID)
		if versionErr != nil || len(versions) == 0 {
			return sessionRuntimePreparation{}, errors.New("runtime configuration snapshot is missing")
		}
		captured, decodeErr := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
		if decodeErr != nil {
			return sessionRuntimePreparation{}, decodeErr
		}
		priorSnapshot = &captured
	}
	preflightRequest := preflightRequestForSession(server, profile, input, run, input.selectedModel())
	preflightRequest.BlackboardMode = modeskill.Mode(mode)
	if priorSnapshot != nil && (strings.TrimSpace(input.ModelProviderID) == "" || strings.TrimSpace(input.ModelProviderID) == priorSnapshot.TurnSelection.ModelProviderID) {
		captured := priorSnapshot.ModelProvider
		captured.Model = firstNonBlank(input.selectedModel(), priorSnapshot.TurnSelection.Model, captured.Model)
		preflightRequest.ModelProviderSnapshot = &captured
	}
	if priorSnapshot != nil {
		preflightRequest.CapturedSkillIDs = append([]string{}, priorSnapshot.EnabledSkillIDs...)
	}
	preflightResult := server.preflight.Run(ctx, preflightRequest)
	if !preflightResult.Pass {
		fails := make([]string, 0, len(preflightResult.Checks))
		for _, check := range preflightResult.Checks {
			if check.Status == preflight.CheckFail {
				detail := strings.TrimSpace(check.Detail)
				if detail == "" {
					fails = append(fails, check.Name)
					continue
				}
				fails = append(fails, check.Name+": "+detail)
			}
		}
		if len(fails) == 0 {
			return sessionRuntimePreparation{}, fmt.Errorf("Session Runtime preflight failed")
		}
		return sessionRuntimePreparation{}, fmt.Errorf("Session Runtime preflight failed: %s", strings.Join(fails, "; "))
	}
	modelSnapshot, err := server.resolveSessionModelSnapshotForTurn(profile, input, priorSnapshot)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	continuationProfile := profile
	resolvedModelSnapshot := modelprovider.Snapshot{}
	if modelSnapshot != nil {
		resolvedModelSnapshot = *modelSnapshot
		continuationProfile = runner.BlackboardV2ProfileWithModelSnapshot(profile, *modelSnapshot)
	}
	runtimeConfig, err := server.resolveSessionSnapshot(continuationProfile, run, input, resolvedModelSnapshot, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	modeSkill, err := modeskill.Resolve(modeskill.Mode(mode))
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	runtimeConfig["mode_skill_id"] = modeSkill.ID
	return sessionRuntimePreparation{
		Profile: profile, Runner: run, RuntimeConfig: runtimeConfig,
	}, nil
}

func (server *Server) startSessionRuntime(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput) (session.Continuation, error) {
	return server.startSessionRuntimeWithConversationInput(ctx, found, goal, input, nil)
}

func (server *Server) startSessionRuntimeWithConversationInput(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput, conversationInput *session.ConversationInput) (session.Continuation, error) {
	previous, err := server.sessions.LatestContinuation(found.ID)
	if err != nil {
		return session.Continuation{}, err
	}
	prepared, err := server.prepareSessionRuntime(ctx, found.RunControls.BlackboardMode, input, previous)
	if err != nil {
		server.recordSessionLaunchDiagnostic(found.ID, "launch_selection_failed", err)
		return session.Continuation{}, err
	}
	return server.startPreparedSessionRuntime(ctx, found, goal, input, previous, prepared, conversationInput)
}

type sessionConversationInputCommitError struct{ cause error }

func (err sessionConversationInputCommitError) Error() string { return err.cause.Error() }
func (err sessionConversationInputCommitError) Unwrap() error { return err.cause }

func (server *Server) startPreparedSessionRuntime(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput, previous *session.Continuation, prepared sessionRuntimePreparation, conversationInput *session.ConversationInput) (session.Continuation, error) {
	launch := resolveOwnerBlackboardRuntimeLaunch(
		goal, found.RunControls.BlackboardMode == session.BlackboardModeDisabled,
	)
	return server.startPreparedSessionRuntimeForBlackboardProjection(
		ctx, found, launch.goal, input, previous, prepared, conversationInput, launch.projection,
	)
}

func (server *Server) startPreparedSessionRuntimeForBlackboardProjection(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput, previous *session.Continuation, prepared sessionRuntimePreparation, conversationInput *session.ConversationInput, blackboardProjection runner.BlackboardProjection) (session.Continuation, error) {
	profile, run, runtimeConfig := prepared.Profile, prepared.Runner, prepared.RuntimeConfig
	var err error
	var continuation session.Continuation
	precreatedInitial := previous == nil && found.LatestContinuation != nil && found.LatestContinuation.Number == 1
	resumeNativeIdentity := previous != nil &&
		previous.RuntimeProvider == string(profile.Provider) && previous.Runner == run &&
		(strings.TrimSpace(previous.NativeSessionID) != "" || strings.TrimSpace(previous.NativeSessionPath) != "")
	if precreatedInitial {
		continuation = *found.LatestContinuation
	} else if resumeNativeIdentity {
		if conversationInput != nil {
			continuation, err = server.sessions.CreateReplacementContinuationWithInput(*previous, runtimeConfig, *conversationInput)
		} else {
			continuation, err = server.sessions.CreateReplacementContinuation(*previous, runtimeConfig)
		}
	} else {
		if conversationInput != nil {
			continuation, err = server.sessions.CreateContinuationWithInput(found.ID, profile.ID, string(profile.Provider), run, runtimeConfig, *conversationInput)
		} else {
			continuation, err = server.sessions.CreateContinuation(found.ID, profile.ID, string(profile.Provider), run, runtimeConfig)
		}
	}
	if err != nil {
		if conversationInput != nil {
			return session.Continuation{}, sessionConversationInputCommitError{cause: err}
		}
		server.recordSessionLaunchDiagnostic(found.ID, "continuation_create_failed", err)
		return session.Continuation{}, err
	}
	interfaceToken := ""
	if blackboardProjection != runner.BlackboardProjectionOmitted {
		if _, pinErr := server.blackboardV2.BindSessionContinuation(ctx, found.ID, continuation.ID); pinErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, pinErr)
			return continuation, pinErr
		}
		var grantErr error
		interfaceToken, _, grantErr = server.projectInterfaceGrants.IssueSession(ctx, projectinterface.IssueSessionGrantRequest{
			SessionID: found.ID, ContinuationID: continuation.ID,
			RuntimeConfigVersionID: continuation.RuntimeConfigID,
			RuntimeProfileID:       continuation.RuntimeProfileID,
			RuntimePluginID:        string(profile.Provider), Runner: string(run),
			Access: blackboardGrantAccessForSessionMode(found.RunControls.BlackboardMode),
		})
		if grantErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, grantErr)
			return continuation, grantErr
		}
	}
	var graphProjection *workinggraph.Projection
	if found.RunControls.BlackboardMode == session.BlackboardModeWorkingGraph {
		preparedGraph, prepareErr := workinggraph.NewService().Prepare(ctx, workinggraph.OwnerContext{
			Owner: found.OwnerContract(), ContinuationID: continuation.ID, Workdir: found.Workdir,
		})
		if prepareErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, prepareErr)
			return continuation, prepareErr
		}
		graphProjection = &preparedGraph
	}
	plan, err := server.buildSessionRuntimePlanForOwnerContext(found, goal, input, profile, run, interfaceToken, continuation.NativeSessionID, blackboardProjection, continuation.ID, graphProjection)
	if err != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, err)
		return continuation, err
	}
	if server.providerSessionFactory != nil && supportsPersistentProviderSession(task.Runner(run), profile.Provider) {
		binding, factoryErr := server.providerSessionFactory.Open(ctx, ProviderSessionLaunchRequest{
			Owner:        found.OwnerContract(),
			Continuation: ownerContinuationFromSession(continuation), Provider: profile.Provider,
			Runner: task.Runner(run), LaunchGoal: plan.LaunchGoal, RuntimeConfig: plan.RuntimeConfig,
			LegacyAdapter: plan.Adapter, Facts: plan.Facts,
		})
		if factoryErr == nil {
			factoryErr = validateProviderSessionBinding(binding)
		}
		if factoryErr != nil {
			server.failSessionProviderLaunch(found.ID, continuation.ID, factoryErr)
			return continuation, factoryErr
		}
		if err := server.BindSessionProviderSession(found.ID, binding.Session); err != nil {
			_ = binding.Session.Close(ctx)
			server.failSessionProviderLaunch(found.ID, continuation.ID, err)
			return continuation, err
		}
		if adapter, ok := binding.Adapter.(*runtime.ProviderSessionRunAdapter); ok {
			adapter.SetInitialTurnSelection(plan.Selection)
		}
		plan.Adapter = wrapPersistentProviderAdapter(binding.Adapter, profile.Provider, plan.ProviderHome)
	}
	if _, err := server.sessions.AppendEvent(found.ID, session.EventKindLifecycle, session.EventPayload{
		"phase": "launch_requested", "continuation_id": continuation.ID,
		"runtime_profile_id": profile.ID, "provider": string(profile.Provider), "runner": string(run),
	}); err != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, err)
		return continuation, err
	}
	launchCtx, err := server.beginRuntimeLaunch()
	if err != nil {
		_ = server.closeSessionProviderSession(found.ID)
		return continuation, err
	}
	go func() {
		defer server.runtimeLaunchWG.Done()
		runErr := server.sessionHarness.Launch(launchCtx, runtime.SessionLaunchRequest{
			SessionID: found.ID, Goal: plan.LaunchGoal, Adapter: plan.Adapter, ContinuationID: continuation.ID,
			Metadata: plan.Metadata, StopConfirmation: plan.StopConfirmation,
		})
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			server.recordSessionLaunchDiagnostic(found.ID, "runtime_failed", runErr)
			_ = server.closeSessionProviderSession(found.ID)
		}
	}()
	return continuation, nil
}

func blackboardGrantAccessForSessionMode(mode session.BlackboardMode) projectinterface.GrantAccess {
	if mode == session.BlackboardModeWorkingGraph {
		return projectinterface.GrantAccessReadOnly
	}
	return projectinterface.GrantAccessFull
}

func (server *Server) handleSessionPreflight(response http.ResponseWriter, request *http.Request) {
	var launch createSessionInput
	if err := json.NewDecoder(request.Body).Decode(&launch); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input := sessionRuntimeInputFromCreate(launch)
	if _, err := normalizeLaunchReasoningEffort(input.ReasoningEffort); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	profile, err := server.resolveSessionRuntimeProfile(input, nil)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	run, err := resolveSessionRunner(input, profile, nil)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	result := server.preflight.Run(
		request.Context(),
		preflightRequestForSession(server, profile, input, run, input.selectedModel()),
	)
	server.logPreflightCustomArgConflict(profile.ID, result)
	writeJSON(response, http.StatusOK, result)
}

func preflightRequestForSession(server *Server, profile runtimeprofile.Profile, input sessionRuntimeInput, run session.Runner, launchModel string) preflight.Request {
	return preflight.Request{
		RuntimeProfileID:    profile.ID,
		Profile:             &profile,
		ProjectID:           "",
		Runner:              string(run),
		HostActivated:       input.HostActivated,
		LaunchModelOverride: launchModel,
		ContainerCLI:        task.ResolveContainerCLI(input.ContainerCLI, server.containerCLI),
		SandboxVPNTun:       input.SandboxVPNTun,
		SandboxNetwork:      input.SandboxNetwork,
		RuntimeRoot:         server.runtimeRoot,
	}
}

func (server *Server) failSessionProviderLaunch(sessionID, continuationID string, cause error) {
	_, _ = server.sessions.UpdateContinuationStatus(continuationID, session.RuntimeStatusFailed)
	server.recordSessionLaunchDiagnostic(sessionID, "provider_session_setup_failed", cause)
}

func (server *Server) recordSessionLaunchDiagnostic(sessionID, phase string, detail any) {
	payload := session.EventPayload{"phase": phase}
	switch value := detail.(type) {
	case error:
		payload["error"] = value.Error()
	case string:
		payload["error"] = value
	case map[string]any:
		for key, item := range value {
			payload[key] = item
		}
	default:
		if detail != nil {
			payload["detail"] = fmt.Sprint(detail)
		}
	}
	_, _ = server.sessions.AppendEvent(sessionID, session.EventKindLifecycle, payload)
}

func (server *Server) stopSessionRuntime(sessionID string) error {
	active, err := server.sessions.ActiveContinuation(sessionID)
	if err != nil {
		return err
	}
	bound := false
	if server.sessionProviderSessions != nil {
		_, bound = server.sessionProviderSessions.get(sessionID)
	}
	harnessActive := server.sessionHarness != nil && server.sessionHarness.IsActive(sessionID)
	if active == nil && !bound && !harnessActive {
		return nil
	}
	if server.sessionHarness != nil {
		server.sessionHarness.Stop(sessionID)
	}
	server.cancelProviderTaskControls(sessionID)
	deadline := time.Now().Add(server.runtimeStopTimeout)
	stopContext, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := server.closeSessionProviderSessionForStop(stopContext, sessionID); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
		return err
	}
	if server.sessionHarness != nil && !server.sessionHarness.StopAndWait(sessionID, time.Until(deadline)) {
		return fmt.Errorf("Session Runtime did not stop in time")
	}
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		return err
	}
	if _, err := server.settleSessionWorkingGraph(stopContext, found, true); err != nil {
		return fmt.Errorf("settle Session Working Graph: %w", err)
	}
	active, err = server.sessions.ActiveContinuation(sessionID)
	if err != nil {
		return err
	}
	if active != nil {
		_, err = server.sessions.UpdateContinuationStatus(active.ID, session.RuntimeStatusStopped)
		if err != nil && !errors.Is(err, session.ErrContinuationStatusConflict) {
			return err
		}
	}
	server.settleSessionAcceptedSteering(sessionID, owner.SteeringReasonOwnerStopped, "Session stopped with queued accepted steering")
	_, _ = server.sessions.AppendEvent(sessionID, session.EventKindLifecycle, session.EventPayload{"phase": "stopped"})
	return nil
}

// markStoppedSessionBlackboardConclusionsRecoveryRequired closes the durable
// semantic coordinator after Stop has canceled every provider control. A
// later message or retry therefore sees explicit owner-local recovery instead
// of waiting for a conclusion Turn whose provider session was intentionally
// closed.
func (server *Server) decorateSession(found session.Session) (session.Session, error) {
	active, err := server.sessions.ActiveContinuation(found.ID)
	if err != nil {
		return session.Session{}, err
	}
	latest, err := server.sessions.LatestContinuation(found.ID)
	if err != nil {
		return session.Session{}, err
	}
	found.ActiveContinuation, found.LatestContinuation = active, latest
	provider := ""
	if latest != nil {
		provider = latest.RuntimeProvider
	}
	bound, isBound := server.sessionProviderSessions.get(found.ID)
	harnessActive := server.sessionHarness != nil && server.sessionHarness.IsActive(found.ID)
	activity := session.RuntimeActivity{Liveness: runtimeLivenessOffline}
	if isBound {
		if sessionHealthUnknown(bound) {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessUnknown, Warning: "runtime health cannot currently be determined"}
		} else if sessionOffline(bound) {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessOffline}
		} else {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: runtimeTurnIdle}
			if sessionBusy(bound) {
				activity.TurnActivity = runtimeTurnBusy
			}
		}
	} else if active != nil {
		if harnessActive {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: runtimeTurnIdle}
		} else {
			activity = session.RuntimeActivity{Liveness: runtimeLivenessOrphaned, Warning: "Session Runtime ownership is not currently bound"}
		}
	} else if harnessActive {
		activity = session.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: runtimeTurnIdle}
	}
	found.RuntimeActivity = activity

	controls := session.RuntimeControls{
		RuntimeProvider: provider,
		// An open Session without a live Runtime accepts a new message to start
		// its replacement Runtime. A bound Runtime overrides this with its
		// provider-native SendTurn capability below.
		QueueSteerAvailable: found.Lifecycle == session.LifecycleOpen,
	}
	if latest != nil {
		controls.NativeSessionCaptured = latest.NativeSessionID != "" || latest.NativeSessionPath != ""
		selection, selectionErr := server.sessionCurrentSelection(*latest)
		if selectionErr == nil {
			controls.TurnSelection = &session.RuntimeTurnSelection{
				ModelProviderID: selection.ModelProviderID, Model: selection.Model,
				ReasoningEffort: selection.RequestedReasoningEffort,
			}
		}
		controls.NativeResumeAvailable = active == nil && controls.NativeSessionCaptured && provider != string(runtimeprofile.ProviderFake)
	}
	steeringControl, steeringControlErr := server.latestSteeringControl(owner.KindSession, found.ID)
	if steeringControlErr == nil {
		controls.NativeSteerRequestID = steeringControl.requestID
		controls.NativeSteerState = steeringControl.state
		controls.NativeSteerErrorCode = steeringControl.errorCode
		controls.NativeSteerError = steeringControl.error
	}
	if isBound {
		capabilities := bound.Capabilities()
		controls.QueueSteerAvailable = capabilities.SendTurn
		if mode, modeErr := nativeSteerModeForSession(bound, false); modeErr == nil {
			controls.NativeSteerAvailable = true
			controls.NativeSteerMode = string(mode)
			controls.InterruptSteerAvailable = capabilities.InterruptThenReplace || capabilities.InTurnSteer
		}
		if steeringControl.nonTerminal {
			controls.NativeSteerAvailable = false
		}
	}
	if active != nil && !isBound && !harnessActive {
		controls.RecoveryState = string(session.RuntimeStatusInterrupted)
		controls.RecoveryReason = "runtime ownership must be recovered or retried"
	}
	if active == nil && latest != nil && latest.Status == session.RuntimeStatusInterrupted && !isBound {
		controls.RecoveryState = string(session.RuntimeStatusInterrupted)
		controls.RecoveryReason = "previous Runtime ownership ended at daemon restart; resume creates a fresh continuation"
	}
	if active == nil && latest != nil && latest.Status == session.RuntimeStatusFailed && !isBound {
		controls.RecoveryState = string(session.RuntimeStatusFailed)
		controls.RecoveryReason = "previous Runtime launch failed; send a new message to retry with the saved profile"
	}
	controls.ProviderPermissions = server.sessionProviderPermissions(found.ID)
	found.RuntimeControls = controls
	if versions, versionErr := server.sessions.RuntimeConfigVersions(found.ID); versionErr == nil && len(versions) > 0 {
		if snapshot, snapshotErr := decodeRuntimeSnapshot(versions[len(versions)-1].Config); snapshotErr == nil {
			summary := runtimeconfig.Summarize(snapshot)
			found.RuntimeConfiguration = &summary
		}
	}
	found.BlackboardConclusion.Mode = found.RunControls.BlackboardMode
	found.BlackboardConclusion.State = session.BlackboardConclusionStateClean
	return found, nil
}

func (server *Server) sessionCurrentSelection(continuation session.Continuation) (runtime.ProviderSessionRequest, error) {
	versions, err := server.sessions.RuntimeConfigVersions(continuation.SessionID)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	var config map[string]any
	if len(versions) > 0 {
		config = versions[len(versions)-1].Config
	}
	profile, err := runtimeProfileFromSnapshot(config)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	return sessionSelection(profile, config, sessionRuntimeInput{})
}

func (server *Server) sessionProviderPermissions(sessionID string) []session.ProviderPermission {
	events, err := server.sessions.Events(sessionID)
	if err != nil {
		return nil
	}
	pending := map[string]session.ProviderPermission{}
	for _, event := range events {
		permissionID, _ := event.Payload["permission_request_id"].(string)
		if permissionID == "" {
			continue
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			request := session.ProviderPermission{PermissionRequestID: permissionID, CreatedAt: event.CreatedAt}
			request.RequestID, _ = event.Payload["request_id"].(string)
			request.SessionID, _ = event.Payload["session_id"].(string)
			request.ProviderTurnID, _ = event.Payload["provider_turn_id"].(string)
			request.Provider, _ = event.Payload["provider"].(string)
			pending[permissionID] = request
		case "provider_permission_response_applied":
			delete(pending, permissionID)
		}
	}
	result := make([]session.ProviderPermission, 0, len(pending))
	for _, request := range pending {
		result = append(result, request)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (server *Server) handleSessionConversation(response http.ResponseWriter, request *http.Request) {
	conversation, err := server.sessions.Conversation(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Events []session.Event `json:"events"`
	}{Events: conversation})
}

func (server *Server) handleSessionTimeline(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	page, err := server.sessionOwnerHistory(found).TimelinePage(parseHistoryRequest(request))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		SessionID string          `json:"session_id"`
		Items     []timeline.Item `json:"items"`
		Cursor    int             `json:"cursor"`
		HasOlder  bool            `json:"has_older"`
	}{
		SessionID: found.ID,
		Items:     page.items,
		Cursor:    page.cursor,
		HasOlder:  page.hasOlder,
	})
}

// handleSessionTimelineItem returns one complete retained timeline item by
// stable item ID. A numeric Seq remains valid for retained legacy detail links.
func (server *Server) handleSessionTimelineItem(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	item, foundItem, err := server.sessionOwnerHistory(found).TimelineItem(request.PathValue("seq"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if !foundItem {
		writeError(response, http.StatusNotFound, "timeline item not found")
		return
	}
	writeJSON(response, http.StatusOK, item)
}

func (server *Server) handleSessionTranscript(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	page, err := server.sessionOwnerHistory(found).TranscriptPage(parseHistoryRequest(request))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		SessionID string             `json:"session_id"`
		Entries   []transcript.Entry `json:"entries"`
		Cursor    int                `json:"cursor"`
		HasOlder  bool               `json:"has_older"`
	}{
		SessionID: found.ID,
		Entries:   page.items,
		Cursor:    page.cursor,
		HasOlder:  page.hasOlder,
	})
}

// handleSessionTranscriptEntry returns one complete retained transcript entry
// by ID, including the full payload that the history window preview truncated.
func (server *Server) handleSessionTranscriptEntry(response http.ResponseWriter, request *http.Request) {
	found, err := server.sessions.Get(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	entry, foundEntry, err := server.sessionOwnerHistory(found).TranscriptEntry(request.PathValue("entry_id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if !foundEntry {
		writeError(response, http.StatusNotFound, "transcript entry not found")
		return
	}
	writeJSON(response, http.StatusOK, entry)
}

func (server *Server) handleSessionMessage(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		request.Body = http.MaxBytesReader(response, request.Body, maxTotalUploadBytes)
	}
	input, uploads, err := parseCreateSessionRequest(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	id := request.PathValue("id")
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	server.handleSessionMessageInput(response, request, found, input, uploads)
}

func (server *Server) handleSessionMessageInput(response http.ResponseWriter, request *http.Request, found session.Session, input createSessionInput, uploads []uploadedAttachment) {
	id := found.ID
	if strings.TrimSpace(input.RuntimeProfileID) != "" {
		writeError(response, http.StatusBadRequest, "runtime_profile_locked")
		return
	}
	if found.Lifecycle != session.LifecycleOpen {
		writeSessionError(response, session.ErrSessionNotOpen)
		return
	}
	if err := server.waitForSessionWorkingGraphSettlement(request.Context(), id, false); err != nil {
		if errors.Is(err, errSemanticConclusionActionRequired) {
			writeError(response, http.StatusConflict, "semantic_conclusion_action_required")
			return
		}
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	message := strings.TrimSpace(input.value())
	if message == "" {
		writeError(response, http.StatusBadRequest, "Session message is required")
		return
	}
	active, activeErr := server.sessions.ActiveContinuation(id)
	if activeErr != nil {
		writeSessionError(response, activeErr)
		return
	}
	if active != nil {
		if conflict := sessionContinuationSelectionConflict(*active, sessionRuntimeInputFromCreate(input)); conflict != "" {
			writeError(response, http.StatusConflict, conflict)
			return
		}
		if provider, bound := server.sessionProviderSessions.get(id); bound && server.sessionHarness.IsActive(id) {
			selection, selectionErr := server.sessionCurrentSelection(*active)
			if selectionErr != nil {
				writeSessionError(response, selectionErr)
				return
			}
			selection.TurnKind = runtime.RuntimeTurnKindWork
			runtimeInput := sessionRuntimeInputFromCreate(input)
			if requestedProvider := strings.TrimSpace(runtimeInput.ModelProviderID); requestedProvider != "" && requestedProvider != selection.ModelProviderID {
				writeError(response, http.StatusConflict, "Session Runtime model provider changes require a fresh continuation")
				return
			}
			if runtimeInput.ModelProviderID != "" || runtimeInput.selectedModel() != "" || runtimeInput.ReasoningEffort != "" {
				selection, selectionErr = sessionSelectionFromInput(selection, runtimeInput)
				if selectionErr != nil {
					writeError(response, http.StatusBadRequest, selectionErr.Error())
					return
				}
			}
			if sessionInputChangesTurnSelection(runtimeInput) {
				if err := server.recordSessionTurnSelection(id, *active, selection); err != nil {
					writeSessionError(response, err)
					return
				}
			}
			requestID := sessionRequestID(request, "message")
			reservation, reserved := server.reserveSessionProviderTurn(id, provider, active.ID, requestID, runtime.ProviderSessionModeSendTurn)
			if !reserved {
				writeError(response, http.StatusConflict, "Session Runtime control operation is unavailable")
				return
			}
			uploadedEvents, _, uploadErr := server.appendSessionConversationInput(id, active.ID, message, uploads)
			if uploadErr != nil {
				reservation.cancel()
				writeSessionError(response, uploadErr)
				return
			}
			runtimeMessage := sessionGoalWithAttachmentEvents(message, found.Workdir, active.Runner, uploadedEvents)
			reservation.commit(runtimeMessage, selection)
			server.writeDecoratedSession(response, http.StatusAccepted, id)
			return
		}
		if err := server.stopSessionRuntime(id); err != nil {
			writeError(response, http.StatusConflict, err.Error())
			return
		}
	}
	conversationInput := sessionConversationInput(message, uploads)
	_, launchErr := server.startSessionRuntimeWithConversationInput(request.Context(), found, message, sessionRuntimeInputFromCreate(input), &conversationInput)
	var commitErr sessionConversationInputCommitError
	if errors.As(launchErr, &commitErr) {
		writeSessionError(response, commitErr.cause)
		return
	}
	if launchErr != nil {
		server.writeDecoratedSession(response, http.StatusAccepted, id)
		return
	}
	server.writeDecoratedSession(response, http.StatusAccepted, id)
}

func sessionRuntimeInputFromCreate(input createSessionInput) sessionRuntimeInput {
	return sessionRuntimeInput{
		RuntimeProfileID: input.RuntimeProfileID, RuntimePluginID: input.RuntimePluginID, Provider: input.Provider, RuntimeProvider: input.RuntimeProvider,
		ModelProviderID: input.ModelProviderID, Model: input.Model, ModelOverride: input.ModelOverride,
		ReasoningEffort: input.ReasoningEffort, Runner: input.Runner, HostActivated: input.HostActivated,
		RuntimeConfig:  input.RuntimeConfig,
		ContainerCLI:   input.RunControls.ContainerCLI,
		SandboxNetwork: input.RunControls.SandboxNetwork,
		SandboxVPNTun:  input.RunControls.SandboxVPNTun,
	}
}

func sessionSelectionFromInput(selection runtime.ProviderSessionRequest, input sessionRuntimeInput) (runtime.ProviderSessionRequest, error) {
	if value := strings.TrimSpace(input.ModelProviderID); value != "" {
		selection.ModelProviderID = value
	}
	if value := input.selectedModel(); value != "" {
		selection.Model = value
	}
	if strings.TrimSpace(input.ReasoningEffort) != "" {
		reasoning, err := runtimeprofile.NormalizeReasoningEffort(input.ReasoningEffort)
		if err != nil {
			return runtime.ProviderSessionRequest{}, err
		}
		selection.RequestedReasoningEffort = string(reasoning)
	}
	return selection, nil
}

func sessionInputChangesTurnSelection(input sessionRuntimeInput) bool {
	return strings.TrimSpace(input.ModelProviderID) != "" || input.selectedModel() != "" || strings.TrimSpace(input.ReasoningEffort) != ""
}

func (server *Server) sessionTurnSelectionConfig(sessionID string, continuation session.Continuation, selection runtime.ProviderSessionRequest) (map[string]any, error) {
	versions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil || len(versions) == 0 {
		if err == nil {
			err = errors.New("runtime configuration snapshot is missing")
		}
		return nil, err
	}
	prior, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return nil, err
	}
	cloned, err := server.cloneSnapshotForTurn(prior, "", runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: selection.ModelProviderID,
		Model:           selection.Model,
		ReasoningEffort: selection.RequestedReasoningEffort,
	})
	if err != nil {
		return nil, err
	}
	return runtimeSnapshotMap(cloned), nil
}

func (server *Server) sessionPiProjectedProviderAllowed(sessionID, providerID string) bool {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return false
	}
	versions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil || len(versions) == 0 {
		return false
	}
	snapshot, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return false
	}
	for _, projectedID := range stringSliceFromConfig(snapshot.ConfigProjection["projected_model_provider_ids"]) {
		if projectedID == providerID {
			return true
		}
	}
	return false
}

func (server *Server) recordSessionTurnSelection(sessionID string, continuation session.Continuation, selection runtime.ProviderSessionRequest) error {
	config, err := server.sessionTurnSelectionConfig(sessionID, continuation, selection)
	if err != nil {
		return err
	}
	_, err = server.sessions.RecordRuntimeConfig(sessionID, continuation.RuntimeProfileID, config)
	return err
}

func sessionContinuationSelectionConflict(continuation session.Continuation, input sessionRuntimeInput) string {
	if profileID := strings.TrimSpace(input.RuntimeProfileID); profileID != "" && profileID != continuation.RuntimeProfileID {
		return "Session Runtime profile changes require a fresh continuation"
	}
	if provider := input.provider(); provider != "" && string(provider) != continuation.RuntimeProvider {
		return "Session Runtime provider changes require a fresh continuation"
	}
	if requestedRunner := strings.TrimSpace(input.Runner); requestedRunner != "" && requestedRunner != string(continuation.Runner) {
		return "Session Runtime runner changes require a fresh continuation"
	}
	return ""
}

func sessionRequestID(request *http.Request, prefix string) string {
	if value := strings.TrimSpace(request.Header.Get("Idempotency-Key")); value != "" {
		return value
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

type sessionProviderTurnPayload struct {
	Message   string
	Selection runtime.ProviderSessionRequest
}

type sessionProviderTurnReservation struct {
	ready chan sessionProviderTurnPayload
	once  sync.Once
}

func (reservation *sessionProviderTurnReservation) commit(message string, selection runtime.ProviderSessionRequest) {
	reservation.once.Do(func() {
		reservation.ready <- sessionProviderTurnPayload{Message: message, Selection: selection}
		close(reservation.ready)
	})
}

func (reservation *sessionProviderTurnReservation) cancel() {
	reservation.once.Do(func() { close(reservation.ready) })
}

func (server *Server) reserveSessionProviderTurn(sessionID string, provider runtime.ProviderSession, continuationID, requestID string, mode runtime.ProviderSessionMode) (*sessionProviderTurnReservation, bool) {
	reservation := &sessionProviderTurnReservation{ready: make(chan sessionProviderTurnPayload, 1)}
	if !server.enqueueSessionProviderTurn(sessionID, provider, continuationID, requestID, mode, reservation.ready) {
		return nil, false
	}
	return reservation, true
}

func (server *Server) enqueueSessionProviderTurn(sessionID string, provider runtime.ProviderSession, continuationID, requestID string, mode runtime.ProviderSessionMode, turnReady <-chan sessionProviderTurnPayload) bool {
	operation := provider.SendTurn
	if mode != runtime.ProviderSessionModeSendTurn {
		operation = nativeSteerOperation(provider, mode)
	}
	if operation == nil {
		return false
	}
	return server.enqueueProviderTaskControlAfterSettlement(sessionID, server.sessionWorkingGraphSettlement(sessionID, false), func(ctx context.Context) {
		var turn sessionProviderTurnPayload
		select {
		case ready, ok := <-turnReady:
			if !ok {
				return
			}
			turn = ready
		case <-ctx.Done():
			return
		}
		selection, message := turn.Selection, turn.Message
		selection.RequestID, selection.Message, selection.TurnKind = requestID, message, runtime.RuntimeTurnKindWork
		if mode != runtime.ProviderSessionModeSendTurn {
			selection.TurnKind = runtime.RuntimeTurnKindControl
		}
		spec := server.sessionSteerEmitSpec(ctx, sessionID, provider)
		protocol := newSteerEmitProtocol(continuationID, spec)
		result, err := operation(ctx, selection, protocol.emit)
		if err != nil || protocol.transitionErr() != nil {
			server.persistSessionProviderEventForContinuation(sessionID, protocol.currentID(), task.EventKindLifecycle, task.EventPayload{
				"phase": "provider_turn_failed", "request_id": requestID, "mode": string(mode), "outcome": "failed",
			})
			if transitionErr := protocol.transitionErr(); transitionErr != nil {
				_ = server.closeSessionProviderSession(sessionID)
				if current := protocol.currentID(); current != "" {
					_, _ = server.sessions.UpdateContinuationStatus(current, session.RuntimeStatusFailed)
				}
			}
			return
		}
		payload := result.Payload()
		payload["phase"] = "provider_turn_applied"
		payload["outcome"] = "applied"
		// The provider result is a structured Runtime turn result, not a
		// transcript message. Conversation contains only explicit user/runtime
		// messages; provider control/result data stays on the Session Timeline.
		server.persistSessionProviderEventForContinuation(sessionID, protocol.currentID(), task.EventKindConversation, payload)
	})
}

// sessionSteerEmitSpec is the Session owner's adapter into the shared steer
// emit protocol: provider events persist bound to the current Session
// Continuation and an interrupt_then_replace settlement advances it with the
// Session's boundary lifecycle events.
func (server *Server) sessionSteerEmitSpec(ctx context.Context, sessionID string, provider runtime.ProviderSession) steerEmitProtocolSpec {
	return steerEmitProtocolSpec{
		advanceOnSettled: true,
		persistEvent: func(current string, kind task.EventKind, payload task.EventPayload) {
			server.persistSessionProviderEventForContinuation(sessionID, current, kind, payload)
		},
		advance: func(current string) (string, error) {
			next := current
			if err := server.advanceSessionRuntimeContinuation(ctx, sessionID, provider, current, &next); err != nil {
				return "", err
			}
			return next, nil
		},
		onAdvanceSettled: func(current, replacement string, advanceErr error) {
			if advanceErr == nil {
				server.persistSessionProviderEventForContinuation(sessionID, replacement, task.EventKindLifecycle, task.EventPayload{
					"phase": "continuation_replaced", "previous_continuation_id": current,
				})
				return
			}
			server.persistSessionProviderEventForContinuation(sessionID, current, task.EventKindLifecycle, task.EventPayload{
				"phase": "continuation_replace_failed", "outcome": "failed",
			})
		},
	}
}

// executeSessionNativeSteerOperation sends one accepted native steer Turn on a
// Session-owned provider session and projects applied/failed/action_required
// outcome events. The caller runs under the Session provider control queue.
// The returned execution is the durable terminal outcome.
func (server *Server) executeSessionNativeSteerOperation(ctx context.Context, sessionID string, provider runtime.ProviderSession, mode runtime.ProviderSessionMode, operation nativeSteerOperationFunc, providerRequest runtime.ProviderSessionRequest, continuationID string) steeringExecution {
	spec := server.sessionSteerEmitSpec(ctx, sessionID, provider)
	return runNativeSteerTurn(ctx, steerExecutionSpec{
		operation:             operation,
		request:               providerRequest,
		mode:                  mode,
		providerSessionID:     provider.SessionID(),
		initialContinuationID: continuationID,
		advanceOnSettled:      spec.advanceOnSettled,
		vocabulary:            sessionSteerEventVocabulary,
		persistEvent:          spec.persistEvent,
		advance:               spec.advance,
		onAdvanceSettled:      spec.onAdvanceSettled,
		failClosed: func(current string) {
			_ = server.closeSessionProviderSession(sessionID)
			if current != "" {
				_, _ = server.sessions.UpdateContinuationStatus(current, session.RuntimeStatusFailed)
			}
		},
	})
}

func payloadOutcome(payload task.EventPayload) string {
	value, _ := payload["outcome"].(string)
	return strings.TrimSpace(value)
}

func (server *Server) advanceSessionRuntimeContinuation(ctx context.Context, sessionID string, provider runtime.ProviderSession, currentID string, continuationID *string) error {
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		return fmt.Errorf("load Session for replacement continuation: %w", err)
	}
	if _, err := server.settleSessionWorkingGraph(ctx, found, true); err != nil {
		return fmt.Errorf("settle old Session Working Graph: %w", err)
	}
	disabled := found.RunControls.BlackboardMode == session.BlackboardModeDisabled
	old, err := server.sessions.Continuation(currentID)
	if err != nil {
		return fmt.Errorf("load Session continuation: %w", err)
	}
	configVersions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil {
		return fmt.Errorf("load Session Runtime config: %w", err)
	}
	var config map[string]any
	if len(configVersions) > 0 {
		config = configVersions[len(configVersions)-1].Config
	}
	next, err := server.sessions.CreateReplacementContinuation(old, config)
	if err != nil {
		return fmt.Errorf("create Session replacement continuation: %w", err)
	}
	if binder, ok := provider.(runtime.ProviderSessionContinuationBinder); ok {
		if err := binder.BindContinuation(next.ID); err != nil {
			_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
			return fmt.Errorf("bind Session provider continuation: %w", err)
		}
	}
	if _, err := server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusRunning); err != nil {
		_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
		return fmt.Errorf("start Session replacement continuation: %w", err)
	}
	if !disabled && server.blackboardV2 != nil {
		if err := server.blackboardV2.RebindSessionContinuation(ctx, sessionID, old.ID, next.ID); err != nil {
			_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
			return fmt.Errorf("rebind Session Blackboard continuation: %w", err)
		}
	}
	if server.sessionHarness != nil && server.sessionHarness.IsActive(sessionID) {
		if err := server.sessionHarness.RebindContinuation(sessionID, next.ID); err != nil {
			_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
			return fmt.Errorf("rebind Session Runtime continuation: %w", err)
		}
	}
	if _, err := server.sessions.UpdateContinuationStatus(old.ID, session.RuntimeStatusCompleted); err != nil {
		_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
		return fmt.Errorf("settle Session continuation: %w", err)
	}
	*continuationID = next.ID
	return nil
}

func (server *Server) handleSessionSteer(response http.ResponseWriter, request *http.Request) {
	var input sessionRuntimeInput
	var message string
	var uploads []uploadedAttachment
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		request.Body = http.MaxBytesReader(response, request.Body, maxTotalUploadBytes)
		parsed, foundUploads, err := parseCreateSessionRequest(request)
		if err != nil {
			writeError(response, http.StatusBadRequest, err.Error())
			return
		}
		input = sessionRuntimeInputFromCreate(parsed)
		message = strings.TrimSpace(parsed.value())
		uploads = foundUploads
	} else {
		var envelope struct {
			Message   string `json:"message"`
			Directive string `json:"directive"`
			sessionRuntimeInput
		}
		if err := decodeOptionalJSON(request, &envelope); err != nil {
			writeError(response, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input = envelope.sessionRuntimeInput
		message = strings.TrimSpace(envelope.Message)
		if message == "" {
			message = strings.TrimSpace(envelope.Directive)
		}
	}
	if message == "" {
		writeError(response, http.StatusBadRequest, "steer message is required")
		return
	}
	id := request.PathValue("id")
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	requestID := sessionRequestID(request, "steer")
	adapter := sessionSteeringAdapter(server, id, server.sessionConclusionSettlementForID(id))
	clientSelectionIdentity := canonicalSteeringClientSelectionIdentity(input.ModelProviderID, input.selectedModel(), input.ReasoningEffort)
	replayIdentity := steeringReplayIdentity{
		requestID: requestID, operatorMessage: message,
		clientSelectionIdentity: clientSelectionIdentity,
		modelProviderID:         input.ModelProviderID, model: input.selectedModel(),
		requestedReasoningEffort: input.ReasoningEffort,
	}
	if record, replayed, replayErr := server.findAcceptedSteeringReplayByIdentity(adapter, replayIdentity); replayErr != nil || replayed {
		if replayErr != nil {
			var conflict *steeringRequestConflictError
			if errors.As(replayErr, &conflict) {
				writeError(response, http.StatusConflict, conflict.Error())
			} else {
				writeSessionError(response, replayErr)
			}
			return
		}
		server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
		return
	}
	provider, bound := server.sessionProviderSessions.get(id)
	active, activeErr := server.sessions.ActiveContinuation(id)
	if activeErr != nil {
		writeSessionError(response, activeErr)
		return
	}
	if !bound || active == nil || !server.sessionHarness.IsActive(id) {
		server.handleSessionMessageInput(response, request, found, createSessionInputFromRuntime(message, input), uploads)
		return
	}
	if conflict := sessionContinuationSelectionConflict(*active, input); conflict != "" {
		writeError(response, http.StatusConflict, conflict)
		return
	}
	selection, err := server.sessionCurrentSelection(*active)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if requestedProvider := strings.TrimSpace(input.ModelProviderID); requestedProvider != "" && requestedProvider != selection.ModelProviderID {
		if runtimeprofile.Provider(active.RuntimeProvider) != runtimeprofile.ProviderPi || !server.sessionPiProjectedProviderAllowed(id, requestedProvider) {
			writeError(response, http.StatusConflict, "Session Runtime model provider changes require a fresh continuation")
			return
		}
	}
	currentSelection := selection
	selection, err = sessionSelectionFromInput(selection, input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	prepared, err := prepareAcceptedNativeSteer(
		request.Context(), provider,
		providerSelectionRequiresReplacement(runtimeprofile.Provider(active.RuntimeProvider), currentSelection, selection),
		input.ForceReplace, adapter.settlement,
	)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	if nativeSteerOperation(provider, prepared.Mode) == nil {
		writeError(response, http.StatusConflict, "provider session does not support native steer")
		return
	}
	mode := prepared.Mode
	expectedProviderTurnID := prepared.ExpectedProviderTurnID
	releaseSteeringRequest := server.steering.AcquireRequest(owner.KindSession, id, requestID)
	defer releaseSteeringRequest()

	acceptInput := steering.AcceptRequest{
		RequestID:                requestID,
		ClientSelectionIdentity:  clientSelectionIdentity,
		OperatorMessage:          message,
		Message:                  message,
		Mode:                     owner.SteeringMode(mode),
		ModelProviderID:          selection.ModelProviderID,
		Model:                    selection.Model,
		RequestedReasoningEffort: selection.RequestedReasoningEffort,
		SessionID:                provider.SessionID(),
		ExpectedProviderTurnID:   expectedProviderTurnID,
	}
	if record, replayed, replayErr := server.findAcceptedSteeringReplay(adapter, acceptInput); replayErr != nil || replayed {
		if replayErr != nil {
			var conflict *steeringRequestConflictError
			if errors.As(replayErr, &conflict) {
				writeError(response, http.StatusConflict, conflict.Error())
			} else {
				writeSessionError(response, replayErr)
			}
			return
		}
		server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
		return
	}

	var turnSelectionConfig map[string]any
	if sessionInputChangesTurnSelection(input) {
		turnSelectionConfig, err = server.sessionTurnSelectionConfig(id, *active, selection)
		if err != nil {
			writeSessionError(response, err)
			return
		}
	}

	// Durable acceptance: the operator message, attachments, Turn Selection,
	// and dispatch record commit in one transaction before the request returns
	// 202. The accepted steering is dispatched by the owner-neutral FIFO queue after the
	// Working Graph settlement completes.
	accept := func(runtimeMessage string, preparedInput *session.PreparedConversationInput) (*owner.SteeringRecord, error) {
		acceptInput.Message = runtimeMessage
		record, _, acceptErr := server.acceptSteeringOrReplay(request.Context(), adapter, acceptInput, func(tx *sql.Tx) (string, error) {
			if preparedInput != nil {
				_, conversation, appendErr := preparedInput.AppendTx(tx)
				if appendErr != nil {
					return "", appendErr
				}
				if turnSelectionConfig != nil {
					if _, configErr := server.sessions.RecordRuntimeConfigTx(tx, id, active.RuntimeProfileID, turnSelectionConfig); configErr != nil {
						return "", configErr
					}
				}
				return conversation.ID, nil
			}
			payload := session.EventPayload{
				"role": "user", "text": message, "request_id": requestID,
				"delivery": "native_steer", "outcome": "pending", "mode": string(mode),
				"session_id":        provider.SessionID(),
				"model_provider_id": selection.ModelProviderID, "model": selection.Model,
				"requested_reasoning_effort": selection.RequestedReasoningEffort,
			}
			if expectedProviderTurnID != "" {
				payload["provider_turn_id"] = expectedProviderTurnID
			}
			if active != nil {
				payload["continuation_id"] = active.ID
			}
			event, eventErr := server.sessions.AppendEventTx(tx, id, session.EventKindConversation, payload)
			if eventErr != nil {
				return "", eventErr
			}
			if turnSelectionConfig != nil {
				if _, configErr := server.sessions.RecordRuntimeConfigTx(tx, id, active.RuntimeProfileID, turnSelectionConfig); configErr != nil {
					return "", configErr
				}
			}
			return event.ID, nil
		})
		return record, acceptErr
	}

	runtimeMessage := message
	if len(uploads) > 0 {
		input := sessionConversationInput(message, uploads)
		preparedInput, prepareErr := server.sessions.PrepareConversationInput(id, active.ID, input.Role, input.Text, input.Attachments)
		if prepareErr != nil {
			writeSessionError(response, prepareErr)
			return
		}
		defer preparedInput.Rollback()
		runtimeMessage = sessionGoalWithAttachmentReferences(message, found.Workdir, active.Runner, preparedInput.AttachmentReferences())
		record, err := accept(runtimeMessage, preparedInput)
		if err != nil {
			var conflict *steeringRequestConflictError
			if errors.As(err, &conflict) {
				writeError(response, http.StatusConflict, conflict.Error())
				return
			}
			writeSessionError(response, err)
			return
		}
		preparedInput.Commit()
		server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
		return
	}
	record, err := accept(runtimeMessage, nil)
	if err != nil {
		var conflict *steeringRequestConflictError
		if errors.As(err, &conflict) {
			writeError(response, http.StatusConflict, conflict.Error())
			return
		}
		writeSessionError(response, err)
		return
	}
	server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
}
func sessionConversationInput(message string, uploads []uploadedAttachment) session.ConversationInput {
	attachments := make([]session.Attachment, 0, len(uploads))
	for _, upload := range uploads {
		attachments = append(attachments, session.Attachment{Name: upload.filename, Size: upload.size, Open: upload.open})
	}
	return session.ConversationInput{Role: "user", Text: message, Attachments: attachments}
}

func (server *Server) appendSessionConversationInput(id, continuationID, message string, uploads []uploadedAttachment) ([]session.Event, session.Event, error) {
	input := sessionConversationInput(message, uploads)
	return server.sessions.AppendConversationInput(id, continuationID, input.Role, input.Text, input.Attachments)
}

func createSessionInputFromRuntime(message string, input sessionRuntimeInput) createSessionInput {
	return createSessionInput{
		Input: message, RuntimeProfileID: input.RuntimeProfileID, Provider: input.Provider,
		RuntimeProvider: input.RuntimeProvider, ModelProviderID: input.ModelProviderID,
		Model: input.Model, ModelOverride: input.ModelOverride, ReasoningEffort: input.ReasoningEffort,
		Runner: input.Runner, HostActivated: input.HostActivated, RuntimeConfig: input.RuntimeConfig,
	}
}

func (server *Server) handleSessionQueueSteer(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		request.Body = http.MaxBytesReader(response, request.Body, maxTotalUploadBytes)
	}
	input, uploads, err := parseCreateSessionRequest(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	id := request.PathValue("id")
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	// Queue-steering a Session is a directive, not a plain message: record the
	// same steering event tasks emit so the timeline and transcript surface it
	// identically for both owner kinds. The send below stays the Session queue
	// path.
	if directive := strings.TrimSpace(input.value()); directive != "" {
		payload := session.EventPayload{
			"directive": directive,
			"phase":     "steering_requested",
			"mode":      "queue",
		}
		if _, err := server.sessions.AppendEvent(id, session.EventKindSteering, payload); err != nil {
			writeSessionError(response, err)
			return
		}
	}
	server.handleSessionMessageInput(response, request, found, input, uploads)
}

// writeDecoratedSessionWithSteeringOutcome returns the decorated Session with
// the durable Accepted Steering outcome of the repeated request identity.
func (server *Server) writeDecoratedSessionWithSteeringOutcome(response http.ResponseWriter, status int, id, outcome string) {
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, status, struct {
		session.Session
		AcceptedSteeringOutcome string `json:"accepted_steering_outcome,omitempty"`
	}{Session: decorated, AcceptedSteeringOutcome: outcome})
}

func (server *Server) writeDecoratedSession(response http.ResponseWriter, status int, id string) {
	found, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, status, decorated)
}

// handleSessionProviderPermissionResponse answers a provider approval request
// on the Session-owned persistent provider session. It intentionally mirrors
// the provider control contract without routing through Project/Task lookup.
func (server *Server) handleSessionProviderPermissionResponse(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := server.sessions.Get(id); err != nil {
		writeSessionError(response, err)
		return
	}
	if err := server.waitForSessionWorkingGraphSettlement(request.Context(), id, true); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	provider, bound := server.sessionProviderSessions.get(id)
	if !bound || provider == nil {
		writeError(response, http.StatusConflict, "Session Runtime provider session is unavailable")
		return
	}
	permissionID := strings.TrimSpace(request.PathValue("permission_id"))
	if permissionID == "" {
		writeError(response, http.StatusBadRequest, "permission request id is required")
		return
	}
	var input providerPermissionResponseRequest
	if err := decodeOptionalJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.Decision = normalizePermissionDecision(input.Decision)
	if input.Decision == "" {
		writeError(response, http.StatusBadRequest, "permission decision must be allow or deny")
		return
	}
	derivePermissionResponseRequestID(request, &input)
	events, err := server.sessions.Events(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	pending, priorOutcome, priorDecision := providerPermissionStatus(sessionEventsAsTimeline(events), permissionID, input.RequestID)
	if priorDecision != "" && priorDecision != input.Decision {
		writeError(response, http.StatusConflict, "permission request id already belongs to a different decision")
		return
	}
	if priorOutcome != "" {
		writePermissionResponseAccepted(response, input, permissionID, provider.SessionID(), priorOutcome)
		return
	}
	if !pending {
		writeError(response, http.StatusNotFound, "provider permission request is no longer pending")
		return
	}
	if !provider.Capabilities().PermissionResponse {
		writeError(response, http.StatusConflict, "provider session does not support permission responses")
		return
	}
	active, err := server.sessions.ActiveContinuation(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if active == nil {
		writeError(response, http.StatusConflict, "provider permission requires an active Session Runtime")
		return
	}
	server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle,
		permissionResponseRequestedPayload(input, permissionID, provider.SessionID()))
	persistLadderEvent := func(kind task.EventKind, payload task.EventPayload) {
		server.persistSessionProviderEventForContinuation(id, active.ID, kind, payload)
	}
	emit := newPermissionResponseEmit(input, permissionID, persistLadderEvent)
	queued := server.enqueueProviderTaskControlAfterSettlement(id, server.sessionWorkingGraphSettlement(id, true), func(ctx context.Context) {
		operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		deliverPermissionResponse(operationCtx, provider, input, permissionID, emit, persistLadderEvent)
	})
	if !queued {
		server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle, task.EventPayload{
			"phase": "provider_permission_response_failed", "outcome": "failed", "request_id": input.RequestID,
			"permission_request_id": permissionID, "mode": string(runtime.ProviderSessionModePermissionResponse), "error_code": "control_unavailable",
		})
		writeError(response, http.StatusConflict, "Session Runtime control operation is unavailable")
		return
	}
	writePermissionResponseAccepted(response, input, permissionID, provider.SessionID(), "accepted")
}

// sessionEventsAsTimeline projects session events into the neutral event shape
// shared with timeline.Build and the owner-neutral permission status scanner.
func sessionEventsAsTimeline(events []session.Event) []timeline.Event {
	converted := make([]timeline.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, timeline.Event{
			ID:        event.ID,
			Seq:       event.Seq,
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	return converted
}

func (server *Server) handleSessionStop(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := server.sessions.Get(id); err != nil {
		writeSessionError(response, err)
		return
	}
	if err := server.stopSessionRuntime(id); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	server.writeDecoratedSession(response, http.StatusOK, id)
}
