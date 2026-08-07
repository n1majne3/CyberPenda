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
	"strconv"
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/modelprovider"
	"pentest/internal/owner"
	"pentest/internal/preflight"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/steering"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

// sessionRuntimeInput is deliberately smaller than Task launch input. A
// Session can select a Runtime Profile, provider turn fields, and runner
// activation, but it has no Project defaults, Scope, Project credentials, or
// Project Interface inputs.
type sessionRuntimeInput struct {
	RuntimeProfileID string         `json:"runtime_profile_id"`
	Provider         string         `json:"provider"`
	RuntimeProvider  string         `json:"runtime_provider"`
	ModelProviderID  string         `json:"model_provider_id"`
	Model            string         `json:"model"`
	ModelOverride    string         `json:"model_override"`
	ReasoningEffort  string         `json:"reasoning_effort"`
	Runner           string         `json:"runner"`
	HostActivated    bool           `json:"host_activated"`
	RuntimeConfig    map[string]any `json:"runtime_config"`
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
	return runtimeprofile.Provider(value)
}

type sessionRuntimePlan struct {
	Adapter          runtime.Adapter
	Profile          runtimeprofile.Profile
	Runner           session.Runner
	LaunchGoal       string
	RuntimeConfig    map[string]any
	Selection        runtime.ProviderSessionRequest
	Metadata         func() (runtime.NativeSessionMetadata, error)
	StopConfirmation runtime.StopConfirmation
	ProviderHome     string
}

func sessionGoalWithAttachmentEvents(goal, workdir string, run session.Runner, events []session.Event) string {
	paths := make([]string, 0, len(events))
	for _, event := range events {
		reference, ok := event.AttachmentReference()
		if !ok {
			continue
		}
		hostPath := filepath.Join(workdir, reference.RelativePath)
		info, err := os.Stat(hostPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		paths = append(paths, ownerAttachmentGoalPath(task.Runner(run), workdir, reference.RelativePath))
	}
	return appendAttachmentPathsToGoal(goal, paths)
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
	if profileID == "" && previous != nil {
		profileID = strings.TrimSpace(previous.RuntimeProfileID)
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

	provider := input.provider()
	if provider == runtimeprofile.ProviderFake {
		return runtimeprofile.Profile{}, fmt.Errorf("runtime profile is required for the fake provider")
	}
	if provider == "" || strings.TrimSpace(input.ModelProviderID) == "" {
		return runtimeprofile.Profile{}, session.ErrMissingRuntimeProfile
	}
	providerName := strings.TrimSpace(input.ModelProviderID)
	if found, err := server.modelProviders.Get(input.ModelProviderID); err == nil {
		providerName = found.Name
	}
	resolution, err := server.profiles.ResolveLaunchProfile(runtimeprofile.LaunchSelection{
		Provider: provider, ModelProviderID: input.ModelProviderID, ModelOverride: input.selectedModel(),
	}, providerName)
	if err != nil {
		return runtimeprofile.Profile{}, err
	}
	return resolution.Profile, nil
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
	launchGoal, err := server.sessionLaunchGoal(found, goal, run)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	modelSnapshot, err := server.resolveSessionModelSnapshot(profile, input)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if modelSnapshot != nil {
		profile = runner.BlackboardV2ProfileWithModelSnapshot(profile, *modelSnapshot)
	}
	config := sessionRuntimeConfig(profile, run, input)
	selection, err := sessionSelection(profile, config, input)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		return sessionRuntimePlan{
			Adapter: runtime.NewFakeAdapter(), Profile: profile, Runner: run,
			LaunchGoal: launchGoal, RuntimeConfig: config, Selection: selection,
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
	})
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if server.skills == nil {
		return sessionRuntimePlan{}, fmt.Errorf("Session Runtime skills service is unavailable")
	}
	skillBundles, err := server.skills.EnabledSkillBundles(profile.ID)
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
		LaunchModelOverride: selection.Model,
		Sandbox:             run == session.RunnerSandbox,
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
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, run == session.RunnerSandbox, runner.RuntimeOwnerContext{Owner: found.OwnerContract()}, runner.ProjectionRequest{
		Owner: found.OwnerContract(), DaemonAddr: server.listenAddr, AuthToken: interfaceToken,
		Credentials: server.creds, MaterializedCredentials: materialized,
		ModelProviders: server.modelProviders, GlobalModelProviderSnapshot: globalSnapshot,
		ModelSnapshot: projection.ModelSnapshot, RuntimePlugins: server.runtimePlugins,
		RuntimeExtensions: server.runtimeExtensions, SkillBundles: skillBundles,
		Sandbox: run == session.RunnerSandbox,
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
			usePersistentSession := server.providerSessionFactory != nil &&
				supportsPersistentProviderSession(task.Runner(run), profile.Provider)
			if !usePersistentSession {
				sandboxRuntime, err = runner.WrapSandboxPiCommand(providerCommand, launchProfile.Fields.Env)
				if err != nil {
					return sessionRuntimePlan{}, err
				}
			}
		}
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout: layout, Provider: profile.Provider, Image: image,
			ContainerCLI: server.containerCLI, ContainerIDFile: containerIDFile,
			RuntimeCommand: sandboxRuntime, ProcessEnv: processEnv,
			NetworkMode: runner.SandboxNetworkDefault,
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
	if run == session.RunnerSandbox && profile.Provider == runtimeprofile.ProviderPi {
		adapter = runtime.NewPiSessionTailAdapter(adapter, filepath.Join(layout.ProviderHome, "agent", "sessions"))
	}

	var metadata func() (runtime.NativeSessionMetadata, error)
	if run == session.RunnerSandbox || profile.Provider == runtimeprofile.ProviderCodex || profile.Provider == runtimeprofile.ProviderPi {
		metadata = func() (runtime.NativeSessionMetadata, error) {
			collected := runtime.NativeSessionMetadata{}
			if containerIDFile != "" {
				containerID, err := runtime.ReadContainerIDFile(containerIDFile)
				if err != nil && !os.IsNotExist(err) {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.ContainerID = containerID
			}
			switch profile.Provider {
			case runtimeprofile.ProviderCodex:
				native, err := runtime.DiscoverCodexSession(layout.ProviderHome)
				if err != nil {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.NativeSessionID, collected.NativeSessionPath = native.NativeSessionID, native.NativeSessionPath
			case runtimeprofile.ProviderPi:
				native, err := runtime.DiscoverPiSession(layout.ProviderHome)
				if err != nil {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.NativeSessionID, collected.NativeSessionPath = native.NativeSessionID, native.NativeSessionPath
			}
			return collected, nil
		}
	}
	var stopConfirmation runtime.StopConfirmation
	if containerIDFile != "" {
		containerCLI := strings.TrimSpace(server.containerCLI)
		if containerCLI == "" {
			containerCLI = "docker"
		}
		stopConfirmation = runtime.DockerContainerStopConfirmation(containerCLI, containerIDFile)
	}
	return sessionRuntimePlan{
		Adapter: adapter, Profile: launchProfile, Runner: run, LaunchGoal: launchGoal, RuntimeConfig: config, Selection: selection,
		Metadata: metadata, StopConfirmation: stopConfirmation, ProviderHome: layout.ProviderHome,
	}, nil
}

func (server *Server) resolveSessionModelSnapshot(profile runtimeprofile.Profile, input sessionRuntimeInput) (*modelprovider.Snapshot, error) {
	if profile.Provider == runtimeprofile.ProviderFake {
		return nil, nil
	}
	providerID := strings.TrimSpace(input.ModelProviderID)
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

func (server *Server) prepareSessionRuntime(ctx context.Context, mode session.BlackboardConclusionMode, input sessionRuntimeInput, previous *session.Continuation) (sessionRuntimePreparation, error) {
	profile, err := server.resolveSessionRuntimeProfile(input, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	if mode == session.BlackboardConclusionModeAssisted &&
		(server.providerSessionFactory == nil || !server.supportsAssistedConclusion(profile.Provider)) {
		return sessionRuntimePreparation{}, errAssistedConclusionUnsupported
	}
	run, err := resolveSessionRunner(input, profile, previous)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	preflightResult := server.preflight.Run(ctx, preflightRequestForSession(profile, input, run, input.selectedModel()))
	if !preflightResult.Pass {
		return sessionRuntimePreparation{}, fmt.Errorf("Session Runtime preflight failed")
	}
	modelSnapshot, err := server.resolveSessionModelSnapshot(profile, input)
	if err != nil {
		return sessionRuntimePreparation{}, err
	}
	continuationProfile := profile
	if modelSnapshot != nil {
		continuationProfile = runner.BlackboardV2ProfileWithModelSnapshot(profile, *modelSnapshot)
	}
	return sessionRuntimePreparation{
		Profile: profile, Runner: run, RuntimeConfig: sessionRuntimeConfig(continuationProfile, run, input),
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
	prepared, err := server.prepareSessionRuntime(ctx, found.RunControls.BlackboardConclusionMode, input, previous)
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
	profile, run, runtimeConfig := prepared.Profile, prepared.Runner, prepared.RuntimeConfig
	var err error
	var continuation session.Continuation
	resumeNativeIdentity := previous != nil &&
		previous.RuntimeProfileID == profile.ID && previous.RuntimeProvider == string(profile.Provider) && previous.Runner == run &&
		(strings.TrimSpace(previous.NativeSessionID) != "" || strings.TrimSpace(previous.NativeSessionPath) != "")
	if resumeNativeIdentity {
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
	if _, pinErr := server.blackboardV2.BindSessionContinuation(ctx, found.ID, continuation.ID); pinErr != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, pinErr)
		return continuation, pinErr
	}
	interfaceToken, _, grantErr := server.projectInterfaceGrants.IssueSession(ctx, projectinterface.IssueSessionGrantRequest{
		SessionID: found.ID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: continuation.RuntimeConfigID,
		RuntimeProfileID:       continuation.RuntimeProfileID,
		RuntimePluginID:        string(profile.Provider), Runner: string(run),
	})
	if grantErr != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, grantErr)
		return continuation, grantErr
	}
	plan, err := server.buildSessionRuntimePlan(found, goal, input, profile, run, interfaceToken, continuation.NativeSessionID)
	if err != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, err)
		return continuation, err
	}
	if server.providerSessionFactory != nil && supportsPersistentProviderSession(task.Runner(run), profile.Provider) {
		binding, factoryErr := server.providerSessionFactory.Open(ctx, ProviderSessionLaunchRequest{
			Owner:        found.OwnerContract(),
			Continuation: ownerContinuationFromSession(continuation), Provider: profile.Provider,
			Runner: task.Runner(run), LaunchGoal: plan.LaunchGoal, RuntimeConfig: plan.RuntimeConfig,
			LegacyAdapter: plan.Adapter,
		})
		if factoryErr == nil {
			factoryErr = validateProviderSessionBinding(binding)
		}
		if factoryErr == nil && found.RunControls.BlackboardConclusionMode == session.BlackboardConclusionModeAssisted {
			factoryErr = validateAssistedConclusionBinding(binding)
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
	go func() {
		runErr := server.sessionHarness.Launch(context.Background(), runtime.SessionLaunchRequest{
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
		preflightRequestForSession(profile, input, run, input.selectedModel()),
	)
	server.logPreflightCustomArgConflict(profile.ID, result)
	writeJSON(response, http.StatusOK, result)
}

func preflightRequestForSession(profile runtimeprofile.Profile, input sessionRuntimeInput, run session.Runner, launchModel string) preflight.Request {
	return preflight.Request{
		RuntimeProfileID: profile.ID, ProjectID: "", Runner: string(run), HostActivated: input.HostActivated,
		LaunchModelOverride: launchModel,
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
	if err := server.markStoppedSessionBlackboardConclusionsRecoveryRequired(sessionID); err != nil {
		return err
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
func (server *Server) markStoppedSessionBlackboardConclusionsRecoveryRequired(sessionID string) error {
	receipts, err := server.sessions.BlackboardConclusionRecoveryCandidates()
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if receipt.SessionID != sessionID {
			continue
		}
		if _, _, err := server.sessions.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
			receipt.ID, session.ConclusionRecoveryRuntimeOwnershipNotProven, time.Now().UTC(), blackboardConclusionRetryCooldown,
		); err != nil && !errors.Is(err, session.ErrInvalidBlackboardConclusionReceipt) {
			return err
		}
	}
	return nil
}

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

	controls := session.RuntimeControls{RuntimeProvider: provider}
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
	if isBound {
		capabilities := bound.Capabilities()
		controls.QueueSteerAvailable = capabilities.SendTurn
		if mode, modeErr := nativeSteerMode(capabilities); modeErr == nil {
			controls.NativeSteerAvailable = true
			controls.NativeSteerMode = string(mode)
			controls.InterruptSteerAvailable = capabilities.InterruptThenReplace || capabilities.InTurnSteer
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
	found.BlackboardConclusion.Mode = found.RunControls.BlackboardConclusionMode
	found.BlackboardConclusion.State = session.BlackboardConclusionStateClean
	if receipt, receiptErr := server.sessions.LatestBlackboardConclusion(found.ID); receiptErr == nil && receipt != nil {
		found.BlackboardConclusion = receipt.View(found.RunControls.BlackboardConclusionMode)
	}
	return found, nil
}

func (server *Server) sessionCurrentSelection(continuation session.Continuation) (runtime.ProviderSessionRequest, error) {
	profile, err := server.profiles.Get(continuation.RuntimeProfileID)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	versions, err := server.sessions.RuntimeConfigVersions(continuation.SessionID)
	if err != nil {
		return runtime.ProviderSessionRequest{}, err
	}
	var config map[string]any
	if len(versions) > 0 {
		config = versions[len(versions)-1].Config
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
	sessionID := request.PathValue("id")
	allEvents, err := server.sessions.Events(sessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	// Feed the shared builder the same full event stream as tasks; kind
	// routing (conversation excluded, lifecycle/steering parsed) lives in
	// timeline.Build, not in the session store.
	events := make([]timeline.Event, 0, len(allEvents))
	for _, event := range allEvents {
		events = append(events, timeline.Event{
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	items := timeline.Build(events)
	if items == nil {
		items = []timeline.Item{}
	}
	detailBase := fmt.Sprintf("/api/sessions/%s/timeline/items", sessionID)
	page := historyResponseFor(items, parseHistoryRequest(request), func(item timeline.Item) int {
		return item.Seq
	}, func(item timeline.Item) (timeline.Item, int) {
		return boundedTimelineItem(item, detailBase)
	})
	writeJSON(response, http.StatusOK, struct {
		SessionID string          `json:"session_id"`
		Items     []timeline.Item `json:"items"`
		Cursor    int             `json:"cursor"`
		HasOlder  bool            `json:"has_older"`
	}{
		SessionID: sessionID,
		Items:     page.items,
		Cursor:    page.cursor,
		HasOlder:  page.hasOlder,
	})
}

// handleSessionTimelineItem returns one complete retained timeline item by
// Seq, including the full payload that the history window preview truncated.
func (server *Server) handleSessionTimelineItem(response http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("id")
	seq, err := strconv.Atoi(request.PathValue("seq"))
	if err != nil || seq <= 0 {
		writeError(response, http.StatusNotFound, "timeline item not found")
		return
	}
	allEvents, err := server.sessions.Events(sessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	events := make([]timeline.Event, 0, len(allEvents))
	for _, event := range allEvents {
		events = append(events, timeline.Event{
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	for _, item := range timeline.Build(events) {
		if item.Seq == seq {
			writeJSON(response, http.StatusOK, item)
			return
		}
	}
	writeError(response, http.StatusNotFound, "timeline item not found")
}

func (server *Server) handleSessionTranscript(response http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("id")
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	events, err := server.sessions.Events(sessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	converted := make([]transcript.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, transcript.Event{
			ID:        event.ID,
			Seq:       event.Seq,
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	entries := transcript.Build(transcript.Subject{
		ID:        found.ID,
		CreatedAt: found.CreatedAt,
		// No Title: the Session's initial input is already a conversation
		// event, so a synthetic goal row would duplicate it.
	}, converted)
	if entries == nil {
		entries = []transcript.Entry{}
	}
	detailBase := fmt.Sprintf("/api/sessions/%s/transcript/entries", sessionID)
	page := historyResponseFor(entries, parseHistoryRequest(request), func(entry transcript.Entry) int {
		return entry.Seq
	}, func(entry transcript.Entry) (transcript.Entry, int) {
		return boundedTranscriptEntry(entry, detailBase)
	})
	writeJSON(response, http.StatusOK, struct {
		SessionID string             `json:"session_id"`
		Entries   []transcript.Entry `json:"entries"`
		Cursor    int                `json:"cursor"`
		HasOlder  bool               `json:"has_older"`
	}{
		SessionID: sessionID,
		Entries:   page.items,
		Cursor:    page.cursor,
		HasOlder:  page.hasOlder,
	})
}

// handleSessionTranscriptEntry returns one complete retained transcript entry
// by ID, including the full payload that the history window preview truncated.
func (server *Server) handleSessionTranscriptEntry(response http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("id")
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	entryID := request.PathValue("entry_id")
	if entryID == "" {
		writeError(response, http.StatusNotFound, "transcript entry not found")
		return
	}
	events, err := server.sessions.Events(sessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	converted := make([]transcript.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, transcript.Event{
			ID:        event.ID,
			Seq:       event.Seq,
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	for _, entry := range transcript.Build(transcript.Subject{
		ID:        found.ID,
		CreatedAt: found.CreatedAt,
	}, converted) {
		if entry.ID == entryID {
			writeJSON(response, http.StatusOK, entry)
			return
		}
	}
	writeError(response, http.StatusNotFound, "transcript entry not found")
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
	if found.Lifecycle != session.LifecycleOpen {
		writeSessionError(response, session.ErrSessionNotOpen)
		return
	}
	if err := server.waitForSessionAssistedConclusionSettlement(request.Context(), id, false); err != nil {
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
		RuntimeProfileID: input.RuntimeProfileID, Provider: input.Provider, RuntimeProvider: input.RuntimeProvider,
		ModelProviderID: input.ModelProviderID, Model: input.Model, ModelOverride: input.ModelOverride,
		ReasoningEffort: input.ReasoningEffort, Runner: input.Runner, HostActivated: input.HostActivated,
		RuntimeConfig: input.RuntimeConfig,
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

func (server *Server) recordSessionTurnSelection(sessionID string, continuation session.Continuation, selection runtime.ProviderSessionRequest) error {
	versions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil {
		return err
	}
	config := map[string]any{
		"runtime_profile_id": continuation.RuntimeProfileID,
		"provider":           continuation.RuntimeProvider,
		"runner":             string(continuation.Runner),
	}
	if len(versions) > 0 {
		for key, value := range versions[len(versions)-1].Config {
			config[key] = value
		}
	}
	if selection.ModelProviderID != "" {
		config["model_provider_id"] = selection.ModelProviderID
	}
	if selection.Model != "" {
		config["model"] = selection.Model
	}
	if selection.RequestedReasoningEffort != "" {
		config["reasoning_effort"] = selection.RequestedReasoningEffort
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
	return server.enqueueProviderTaskControlAfterSettlement(sessionID, server.sessionBlackboardConclusionSettlement(sessionID, false), func(ctx context.Context) {
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
		var continuationMu sync.Mutex
		currentContinuationID := continuationID
		var transitionErr error
		currentID := func() string {
			continuationMu.Lock()
			defer continuationMu.Unlock()
			return currentContinuationID
		}
		emit := func(kind task.EventKind, payload task.EventPayload) {
			continuationMu.Lock()
			current := currentContinuationID
			server.persistSessionProviderEventForContinuation(sessionID, current, kind, payload)
			if transitionErr == nil && mode == runtime.ProviderSessionModeInterruptThenReplace && kind == task.EventKindSteering && payloadOutcome(payload) == "settled" && current != "" {
				transitionErr = server.advanceSessionRuntimeContinuation(ctx, sessionID, provider, current, &currentContinuationID)
				if transitionErr == nil {
					server.persistSessionProviderEventForContinuation(sessionID, currentContinuationID, task.EventKindLifecycle, task.EventPayload{
						"phase": "continuation_replaced", "previous_continuation_id": current,
					})
				} else {
					server.persistSessionProviderEventForContinuation(sessionID, current, task.EventKindLifecycle, task.EventPayload{
						"phase": "continuation_replace_failed", "outcome": "failed",
					})
				}
			}
			continuationMu.Unlock()
		}
		result, err := operation(ctx, selection, emit)
		if err != nil || transitionErr != nil {
			server.persistSessionProviderEventForContinuation(sessionID, currentID(), task.EventKindLifecycle, task.EventPayload{
				"phase": "provider_turn_failed", "request_id": requestID, "mode": string(mode), "outcome": "failed",
			})
			if transitionErr != nil {
				_ = server.closeSessionProviderSession(sessionID)
				if current := currentID(); current != "" {
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
		server.persistSessionProviderEventForContinuation(sessionID, currentID(), task.EventKindConversation, payload)
	})
}

// executeSessionNativeSteerOperation sends one accepted native steer Turn on a
// Session-owned provider session and projects applied/failed/action_required
// outcome events. The caller runs under the Session provider control queue.
// The returned execution is the durable terminal outcome.
func (server *Server) executeSessionNativeSteerOperation(ctx context.Context, sessionID string, provider runtime.ProviderSession, mode runtime.ProviderSessionMode, operation nativeSteerOperationFunc, providerRequest runtime.ProviderSessionRequest, continuationID string) steeringExecution {
	selection := providerRequest
	selection.TurnKind = runtime.RuntimeTurnKindControl
	var continuationMu sync.Mutex
	currentContinuationID := continuationID
	var transitionErr error
	currentID := func() string {
		continuationMu.Lock()
		defer continuationMu.Unlock()
		return currentContinuationID
	}
	emit := func(kind task.EventKind, payload task.EventPayload) {
		continuationMu.Lock()
		current := currentContinuationID
		server.persistSessionProviderEventForContinuation(sessionID, current, kind, payload)
		if transitionErr == nil && mode == runtime.ProviderSessionModeInterruptThenReplace && kind == task.EventKindSteering && payloadOutcome(payload) == "settled" && current != "" {
			transitionErr = server.advanceSessionRuntimeContinuation(ctx, sessionID, provider, current, &currentContinuationID)
			if transitionErr == nil {
				server.persistSessionProviderEventForContinuation(sessionID, currentContinuationID, task.EventKindLifecycle, task.EventPayload{
					"phase": "continuation_replaced", "previous_continuation_id": current,
				})
			} else {
				server.persistSessionProviderEventForContinuation(sessionID, current, task.EventKindLifecycle, task.EventPayload{
					"phase": "continuation_replace_failed", "outcome": "failed",
				})
			}
		}
		continuationMu.Unlock()
	}
	result, err := operation(ctx, selection, emit)
	if err != nil || transitionErr != nil {
		if transitionErr != nil {
			_ = server.closeSessionProviderSession(sessionID)
			if current := currentID(); current != "" {
				_, _ = server.sessions.UpdateContinuationStatus(current, session.RuntimeStatusFailed)
			}
			return steeringExecution{state: owner.SteeringFailed, reason: owner.SteeringReasonContinuationUnavailable, message: transitionErr.Error()}
		}
		errorCode, errorMessage := nativeSteerFailurePresentation(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// Post-fence with no provider outcome: delivery is ambiguous. The
			// request is never replayed automatically.
			server.persistSessionProviderEventForContinuation(sessionID, currentID(), task.EventKindLifecycle, task.EventPayload{
				"phase": "provider_turn_failed", "request_id": providerRequest.RequestID, "mode": string(mode),
				"outcome": "action_required", "error_code": string(owner.SteeringReasonDeliveryAmbiguous), "error": errorMessage,
			})
			return steeringExecution{state: owner.SteeringActionRequired, reason: owner.SteeringReasonDeliveryAmbiguous, message: errorMessage}
		}
		server.persistSessionProviderEventForContinuation(sessionID, currentID(), task.EventKindLifecycle, task.EventPayload{
			"phase": "provider_turn_failed", "request_id": providerRequest.RequestID, "mode": string(mode),
			"outcome": "failed", "error_code": errorCode, "error": errorMessage,
		})
		return steeringExecution{state: owner.SteeringFailed, reason: steerReasonFromFailureCode(errorCode), message: errorMessage}
	}
	payload := result.Payload()
	payload["phase"] = "provider_turn_applied"
	payload["outcome"] = "applied"
	// The provider result is a structured Runtime turn result, not a
	// transcript message. Conversation contains only explicit user/runtime
	// messages; provider control/result data stays on the Session Timeline.
	server.persistSessionProviderEventForContinuation(sessionID, currentID(), task.EventKindConversation, payload)
	return steeringExecution{state: owner.SteeringApplied, result: result.Payload()}
}

func payloadOutcome(payload task.EventPayload) string {
	value, _ := payload["outcome"].(string)
	return strings.TrimSpace(value)
}

func (server *Server) advanceSessionRuntimeContinuation(ctx context.Context, sessionID string, provider runtime.ProviderSession, currentID string, continuationID *string) error {
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
	if server.blackboardV2 != nil {
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
	mode, err := nativeSteerMode(provider.Capabilities())
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	if nativeSteerOperation(provider, mode) == nil {
		writeError(response, http.StatusConflict, "provider session does not support native steer")
		return
	}
	selection, err := server.sessionCurrentSelection(*active)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if requestedProvider := strings.TrimSpace(input.ModelProviderID); requestedProvider != "" && requestedProvider != selection.ModelProviderID {
		writeError(response, http.StatusConflict, "Session Runtime model provider changes require a fresh continuation")
		return
	}
	selection, err = sessionSelectionFromInput(selection, input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if sessionInputChangesTurnSelection(input) {
		if err := server.recordSessionTurnSelection(id, *active, selection); err != nil {
			writeSessionError(response, err)
			return
		}
	}
	requestID := sessionRequestID(request, "steer")

	// Repeated requests with the same request identity return the durable
	// current outcome and never create a second queue item. Conflicting content
	// under the same identity returns a conflict.
	if record, err := server.steering.ByRequestID(owner.KindSession, id, requestID); err == nil {
		if conflict := steeringConflictMessage(record, message, selection, owner.SteeringMode(mode)); conflict != "" {
			writeError(response, http.StatusConflict, conflict)
			return
		}
		server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(record))
		return
	} else if !errors.Is(err, steering.ErrNotFound) {
		writeSessionError(response, err)
		return
	}

	// Durable acceptance: the operator message and the dispatch record commit
	// before the request returns 202. The accepted steering is dispatched by
	// the owner-neutral FIFO queue after the assisted conclusion settles.
	adapter := sessionSteeringAdapter(server, id, server.sessionConclusionSettlementForID(id))
	accept := func(runtimeMessage, conversationID string) error {
		_, acceptErr := server.acceptSteeringDurably(request.Context(), adapter, steering.AcceptRequest{
			RequestID:                requestID,
			Message:                  runtimeMessage,
			Mode:                     owner.SteeringMode(mode),
			ModelProviderID:          selection.ModelProviderID,
			Model:                    selection.Model,
			RequestedReasoningEffort: selection.RequestedReasoningEffort,
			SessionID:                provider.SessionID(),
		}, func(tx *sql.Tx) (string, error) {
			if conversationID != "" {
				return conversationID, nil
			}
			payload := session.EventPayload{
				"role": "user", "text": message, "request_id": requestID,
				"delivery": "native_steer", "outcome": "pending", "mode": string(mode),
				"session_id":        provider.SessionID(),
				"model_provider_id": selection.ModelProviderID, "model": selection.Model,
				"requested_reasoning_effort": selection.RequestedReasoningEffort,
			}
			if active != nil {
				payload["continuation_id"] = active.ID
			}
			event, eventErr := server.sessions.AppendEventTx(tx, id, session.EventKindConversation, payload)
			if eventErr != nil {
				return "", eventErr
			}
			return event.ID, nil
		})
		return acceptErr
	}

	runtimeMessage := message
	if len(uploads) > 0 {
		// Attachments are staged by the existing input path; the conversation
		// event it produces becomes the projection reference for the record.
		uploadedEvents, conversation, uploadErr := server.appendSessionConversationInput(id, active.ID, message, uploads)
		if uploadErr != nil {
			writeSessionError(response, uploadErr)
			return
		}
		runtimeMessage = sessionGoalWithAttachmentEvents(message, found.Workdir, active.Runner, uploadedEvents)
		if err := accept(runtimeMessage, conversation.ID); err != nil {
			writeSessionError(response, err)
			return
		}
		server.writeDecoratedSession(response, http.StatusAccepted, id)
		return
	}
	if err := accept(runtimeMessage, ""); err != nil {
		if errors.Is(err, steering.ErrDuplicateRequest) {
			// A concurrent duplicate committed first; the same identity rules
			// apply: matching content replays the durable outcome, conflicting
			// content is a conflict.
			replayed, replayErr := server.steering.ByRequestID(owner.KindSession, id, requestID)
			if replayErr != nil {
				writeSessionError(response, replayErr)
				return
			}
			if conflict := steeringConflictMessage(replayed, message, selection, owner.SteeringMode(mode)); conflict != "" {
				writeError(response, http.StatusConflict, conflict)
				return
			}
			server.writeDecoratedSessionWithSteeringOutcome(response, http.StatusAccepted, id, steeringOutcomeFromRecord(replayed))
			return
		}
		writeSessionError(response, err)
		return
	}
	server.writeDecoratedSession(response, http.StatusAccepted, id)
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
	if err := server.waitForSessionAssistedConclusionSettlement(request.Context(), id, true); err != nil {
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
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		input.RequestID = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if input.RequestID == "" {
		input.RequestID = "permission-" + permissionID + "-" + input.Decision
	}
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
		writeJSON(response, http.StatusAccepted, map[string]any{
			"request_id": input.RequestID, "permission_request_id": permissionID,
			"session_id": provider.SessionID(), "decision": input.Decision, "outcome": priorOutcome,
		})
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
	requested := task.EventPayload{
		"phase": "provider_permission_response_requested", "mode": string(runtime.ProviderSessionModePermissionResponse),
		"outcome": "pending", "request_id": input.RequestID, "permission_request_id": permissionID,
		"permission_decision": input.Decision, "session_id": provider.SessionID(),
	}
	server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle, requested)
	queued := server.enqueueProviderTaskControlAfterSettlement(id, server.sessionBlackboardConclusionSettlement(id, true), func(ctx context.Context) {
		operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		emit := func(kind task.EventKind, payload task.EventPayload) {
			if payload == nil {
				payload = task.EventPayload{}
			}
			payload["request_id"] = input.RequestID
			payload["permission_request_id"] = permissionID
			if _, ok := payload["mode"]; !ok {
				payload["mode"] = string(runtime.ProviderSessionModePermissionResponse)
			}
			server.persistSessionProviderEventForContinuation(id, active.ID, kind, payload)
		}
		result, operationErr := provider.RespondPermission(operationCtx, runtime.ProviderSessionRequest{
			RequestID: input.RequestID, PermissionRequestID: permissionID, PermissionDecision: input.Decision,
		}, emit)
		if operationErr != nil {
			server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle, task.EventPayload{
				"phase": "provider_permission_response_failed", "outcome": "failed", "request_id": input.RequestID,
				"permission_request_id": permissionID, "mode": string(runtime.ProviderSessionModePermissionResponse),
				"error_code": permissionResponseErrorCode(operationErr),
			})
			return
		}
		payload := result.Payload()
		payload["phase"] = "provider_permission_response_applied"
		payload["outcome"] = "applied"
		payload["permission_request_id"] = permissionID
		server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle, payload)
	})
	if !queued {
		server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle, task.EventPayload{
			"phase": "provider_permission_response_failed", "outcome": "failed", "request_id": input.RequestID,
			"permission_request_id": permissionID, "mode": string(runtime.ProviderSessionModePermissionResponse), "error_code": "control_unavailable",
		})
		writeError(response, http.StatusConflict, "Session Runtime control operation is unavailable")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{
		"request_id": input.RequestID, "permission_request_id": permissionID,
		"session_id": provider.SessionID(), "decision": input.Decision, "outcome": "accepted",
	})
}

// sessionEventsAsTimeline projects session events into the neutral event shape
// shared with timeline.Build and the owner-neutral permission status scanner.
func sessionEventsAsTimeline(events []session.Event) []timeline.Event {
	converted := make([]timeline.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, timeline.Event{
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
