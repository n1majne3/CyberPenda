package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
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
	RuntimeConfig    map[string]any
	Selection        runtime.ProviderSessionRequest
	Metadata         func() (runtime.NativeSessionMetadata, error)
	StopConfirmation runtime.StopConfirmation
	ProviderHome     string
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

func (server *Server) buildSessionRuntimePlan(found session.Session, goal string, input sessionRuntimeInput, profile runtimeprofile.Profile, run session.Runner) (sessionRuntimePlan, error) {
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
			RuntimeConfig: config, Selection: selection,
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
	launchProfile := sessionProfileWithoutProjectInterface(profile)
	projectionRequest := runner.ProjectionRequest{
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
	providerCommand, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider: launchProfile.Provider, Profile: launchProfile, Goal: goal,
		ConfigPath: configPath, MCPConfigPath: mcpConfigPath, Sandbox: run == session.RunnerSandbox,
	})
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, run == session.RunnerSandbox, runner.TaskContext{}, runner.ProjectionRequest{
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
		containerCLI := strings.TrimSpace(server.containerCLI)
		if containerCLI == "" {
			containerCLI = "docker"
		}
		containerEnv := make(map[string]string, len(processEnv))
		for key, value := range processEnv {
			containerEnv[key] = sessionContainerPath(value, layout)
		}
		createArgs := []string{
			"create", "-i", "--add-host=host.docker.internal:host-gateway",
			"--cidfile", containerIDFile,
			"-v", filepath.Clean(found.Workdir) + ":/task/workdir",
			"-v", filepath.Clean(layout.RuntimeHome) + ":/task/runtime-home",
			"-w", "/task/workdir",
		}
		keys := make([]string, 0, len(containerEnv))
		for key := range containerEnv {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if strings.TrimSpace(key) != "" {
				createArgs = append(createArgs, "-e", key+"="+containerEnv[key])
			}
		}
		createArgs = append(createArgs, image)
		createArgs = append(createArgs, providerCommand...)
		adapter = runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
			Name: string(profile.Provider), ContainerCLI: containerCLI, Image: image,
			CreateArgs: createArgs, SecretValues: runtime.EnvSecretValues(materialized),
		})
	} else {
		adapter = runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name: string(profile.Provider), Program: providerCommand[0], Args: providerCommand[1:],
			Workdir: found.Workdir, Env: processEnv,
		})
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
		Adapter: adapter, Profile: launchProfile, Runner: run, RuntimeConfig: config, Selection: selection,
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

func sessionProfileWithoutProjectInterface(profile runtimeprofile.Profile) runtimeprofile.Profile {
	profile.Fields.Env = cloneStringMap(profile.Fields.Env)
	if profile.Fields.Env == nil {
		profile.Fields.Env = map[string]string{}
	}
	profile.Fields.Env["PENTEST_DISABLE_TRUSTED_MCP"] = "true"
	return profile
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func sessionContainerPath(value string, layout runner.Layout) string {
	if value == layout.Workdir {
		return "/task/workdir"
	}
	if value == layout.RuntimeHome || strings.HasPrefix(value, layout.RuntimeHome+string(filepath.Separator)) {
		return "/task/runtime-home" + strings.TrimPrefix(value, layout.RuntimeHome)
	}
	if value == layout.ProviderHome || strings.HasPrefix(value, layout.ProviderHome+string(filepath.Separator)) {
		return "/task/runtime-home" + strings.TrimPrefix(value, layout.RuntimeHome)
	}
	return value
}

func (server *Server) startSessionRuntime(ctx context.Context, found session.Session, goal string, input sessionRuntimeInput) (session.Continuation, error) {
	previous, err := server.sessions.LatestContinuation(found.ID)
	if err != nil {
		return session.Continuation{}, err
	}
	profile, err := server.resolveSessionRuntimeProfile(input, previous)
	if err != nil {
		server.recordSessionLaunchDiagnostic(found.ID, "launch_selection_failed", err)
		return session.Continuation{}, err
	}
	if found.RunControls.BlackboardConclusionMode == session.BlackboardConclusionModeAssisted {
		if server.providerSessionFactory == nil || !server.supportsAssistedConclusion(profile.Provider) {
			err := errAssistedConclusionUnsupported
			server.recordSessionLaunchDiagnostic(found.ID, "assisted_conclusion_unsupported", err)
			return session.Continuation{}, err
		}
	}
	run, err := resolveSessionRunner(input, profile, previous)
	if err != nil {
		server.recordSessionLaunchDiagnostic(found.ID, "runner_selection_failed", err)
		return session.Continuation{}, err
	}
	launchModel := input.selectedModel()
	preflightResult := server.preflight.Run(ctx, preflightRequestForSession(profile, input, run, launchModel))
	if !preflightResult.Pass {
		server.recordSessionLaunchDiagnostic(found.ID, "preflight_failed", map[string]any{"checks": preflightResult.Checks})
		return session.Continuation{}, fmt.Errorf("Session Runtime preflight failed")
	}
	plan, err := server.buildSessionRuntimePlan(found, goal, input, profile, run)
	if err != nil {
		server.recordSessionLaunchDiagnostic(found.ID, "launch_plan_failed", err)
		return session.Continuation{}, err
	}
	continuation, err := server.sessions.CreateContinuation(found.ID, profile.ID, string(profile.Provider), run, plan.RuntimeConfig)
	if err != nil {
		server.recordSessionLaunchDiagnostic(found.ID, "continuation_create_failed", err)
		return session.Continuation{}, err
	}
	if _, pinErr := server.blackboardV2.BindSessionContinuation(ctx, found.ID, continuation.ID); pinErr != nil {
		server.failSessionProviderLaunch(found.ID, continuation.ID, pinErr)
		return continuation, pinErr
	}
	if server.providerSessionFactory != nil && supportsPersistentProviderSession(task.Runner(run), profile.Provider) {
		binding, factoryErr := server.providerSessionFactory.Open(ctx, ProviderSessionLaunchRequest{
			Owner:        found.OwnerContract(),
			Continuation: ownerContinuationFromSession(continuation), Provider: profile.Provider,
			Runner: task.Runner(run), LaunchGoal: goal, RuntimeConfig: plan.RuntimeConfig,
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
			SessionID: found.ID, Goal: goal, Adapter: plan.Adapter, ContinuationID: continuation.ID,
			Metadata: plan.Metadata, StopConfirmation: plan.StopConfirmation,
		})
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			server.recordSessionLaunchDiagnostic(found.ID, "runtime_failed", runErr)
			_ = server.closeSessionProviderSession(found.ID)
		}
	}()
	return continuation, nil
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
			receipt.ID, time.Now().UTC(), blackboardConclusionRetryCooldown,
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
	timeline, err := server.sessions.Timeline(request.PathValue("id"))
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Events []session.Event `json:"events"`
	}{Events: timeline})
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
	if len(uploads) > 0 {
		attachments := make([]session.Attachment, 0, len(uploads))
		for _, upload := range uploads {
			attachments = append(attachments, session.Attachment{Name: upload.filename, Size: upload.size, Open: upload.open})
		}
		if _, err := server.sessions.AddAttachments(id, attachments); err != nil {
			writeSessionError(response, err)
			return
		}
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
			if _, err := server.sessions.AppendConversationEvent(id, active.ID, "user", message); err != nil {
				writeSessionError(response, err)
				return
			}
			requestID := sessionRequestID(request, "message")
			if !server.enqueueSessionProviderTurn(id, provider, active.ID, requestID, message, selection, runtime.ProviderSessionModeSendTurn) {
				writeError(response, http.StatusConflict, "Session Runtime control operation is unavailable")
				return
			}
			server.writeDecoratedSession(response, http.StatusAccepted, id)
			return
		}
		if err := server.stopSessionRuntime(id); err != nil {
			writeError(response, http.StatusConflict, err.Error())
			return
		}
	}
	if _, err := server.sessions.AppendConversationEvent(id, "", "user", message); err != nil {
		writeSessionError(response, err)
		return
	}
	_, launchErr := server.startSessionRuntime(request.Context(), found, message, sessionRuntimeInputFromCreate(input))
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

func (server *Server) enqueueSessionProviderTurn(sessionID string, provider runtime.ProviderSession, continuationID, requestID, message string, selection runtime.ProviderSessionRequest, mode runtime.ProviderSessionMode) bool {
	operation := provider.SendTurn
	if mode != runtime.ProviderSessionModeSendTurn {
		operation = nativeSteerOperation(provider, mode)
	}
	if operation == nil {
		return false
	}
	selection.RequestID, selection.Message, selection.TurnKind = requestID, message, runtime.RuntimeTurnKindWork
	if mode != runtime.ProviderSessionModeSendTurn {
		selection.TurnKind = runtime.RuntimeTurnKindControl
	}
	return server.enqueueProviderTaskControlAfterSettlement(sessionID, server.sessionBlackboardConclusionSettlement(sessionID, false), func(ctx context.Context) {
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
	message := strings.TrimSpace(envelope.Message)
	if message == "" {
		message = strings.TrimSpace(envelope.Directive)
	}
	if message == "" {
		writeError(response, http.StatusBadRequest, "steer message is required")
		return
	}
	id := request.PathValue("id")
	_, err := server.sessions.Get(id)
	if err != nil {
		writeSessionError(response, err)
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
	provider, bound := server.sessionProviderSessions.get(id)
	active, activeErr := server.sessions.ActiveContinuation(id)
	if activeErr != nil {
		writeSessionError(response, activeErr)
		return
	}
	if !bound || active == nil || !server.sessionHarness.IsActive(id) {
		server.handleSessionMessage(response, requestWithSessionInput(request, id, message, input))
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
	if _, err := server.sessions.AppendConversationEvent(id, active.ID, "user", message); err != nil {
		writeSessionError(response, err)
		return
	}
	requestID := sessionRequestID(request, "steer")
	if !server.enqueueSessionProviderTurn(id, provider, active.ID, requestID, message, selection, mode) {
		writeError(response, http.StatusConflict, "Session Runtime control operation is unavailable")
		return
	}
	server.writeDecoratedSession(response, http.StatusAccepted, id)
}

func requestWithSessionInput(request *http.Request, id, message string, input sessionRuntimeInput) *http.Request {
	body, _ := json.Marshal(map[string]any{
		"input": message, "runtime_profile_id": input.RuntimeProfileID, "provider": input.Provider,
		"runtime_provider": input.RuntimeProvider, "model_provider_id": input.ModelProviderID,
		"model": input.Model, "model_override": input.ModelOverride, "reasoning_effort": input.ReasoningEffort,
		"runner": input.Runner, "host_activated": input.HostActivated,
	})
	clone := request.Clone(request.Context())
	clone.URL.Path = "/api/sessions/" + id + "/messages"
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.Header.Set("Content-Type", "application/json")
	return clone
}

func (server *Server) handleSessionQueueSteer(response http.ResponseWriter, request *http.Request) {
	server.handleSessionMessage(response, request)
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
	pending, priorOutcome, priorDecision := sessionProviderPermissionStatus(events, permissionID, input.RequestID)
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
				"error_code": sessionProviderPermissionErrorCode(operationErr),
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

func sessionProviderPermissionStatus(events []session.Event, permissionID, requestID string) (pending bool, outcome, decision string) {
	for _, event := range events {
		if value, _ := event.Payload["permission_request_id"].(string); value != permissionID {
			continue
		}
		rid, _ := event.Payload["request_id"].(string)
		if rid == requestID {
			if value, ok := event.Payload["permission_decision"].(string); ok && value != "" {
				decision = normalizePermissionDecision(value)
			}
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			pending = true
		case "provider_permission_response_requested", "provider_permission_response_acknowledged":
			if rid == requestID {
				outcome = "pending"
			}
		case "provider_permission_response_applied":
			pending = false
			if rid == requestID {
				outcome = "applied"
			}
		case "provider_permission_response_failed":
			pending = true
			if rid == requestID {
				outcome = "failed"
			}
		}
	}
	return pending, outcome, decision
}

func sessionProviderPermissionErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "server_closing"
	case errors.Is(err, runtime.ErrProviderSessionClosed):
		return "session_closed"
	case errors.Is(err, runtime.ErrProviderSessionControlConflict):
		return "control_conflict"
	default:
		return "provider_rejected"
	}
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
