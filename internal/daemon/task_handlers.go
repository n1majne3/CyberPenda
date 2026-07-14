package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/blackboard"
	"pentest/internal/blackboardcompat"

	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

var (
	errNativeResumeUnavailable  = errors.New("native resume unavailable")
	errNativeSessionUnavailable = errors.New("native session unavailable")
)

type taskContinuationSelectionInput struct {
	RuntimeProfileID string         `json:"runtime_profile_id"`
	ModelProviderID  string         `json:"model_provider_id"`
	ModelOverride    string         `json:"model_override"`
	SubmittedBy      string         `json:"submitted_by"`
	Config           map[string]any `json:"config"`
}

func (input taskContinuationSelectionInput) hasSelection() bool {
	return strings.TrimSpace(input.RuntimeProfileID) != "" || strings.TrimSpace(input.ModelProviderID) != ""
}

func (server *Server) handleCreateTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}

	var input struct {
		Goal             string            `json:"goal"`
		RuntimeProfileID string            `json:"runtime_profile_id"`
		ModelOverride    string            `json:"model_override,omitempty"`
		Runner           task.Runner       `json:"runner"`
		RunControls      task.RunControls  `json:"run_controls"`
		Extras           map[string]string `json:"extras"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.RunControls.Extras == nil && input.Extras != nil {
		input.RunControls.Extras = input.Extras
	}

	defaulted, err := server.applyTaskLaunchDefaults(projectID, input.RuntimeProfileID, input.Runner)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load project defaults")
		return
	}
	input.RuntimeProfileID = defaulted.runtimeProfileID
	input.Runner = defaulted.runner

	launchModelOverride := strings.TrimSpace(input.ModelOverride)
	preflightResult := server.preflight.Run(request.Context(), preflight.Request{
		RuntimeProfileID:    input.RuntimeProfileID,
		LaunchModelOverride: launchModelOverride,
		ProjectID:           projectID,
		Runner:              string(input.Runner),
		HostActivated:       input.RunControls.HostActivated,
	})
	if !preflightResult.Pass {
		writeJSON(response, http.StatusBadRequest, struct {
			Error     string           `json:"error"`
			Preflight preflight.Result `json:"preflight"`
		}{
			Error:     "preflight failed",
			Preflight: preflightResult,
		})
		return
	}
	if err := runner.ValidateActivation(runner.ActivationRequest{
		Runner:        runner.Runner(input.Runner),
		HostActivated: input.RunControls.HostActivated,
	}); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID:        projectID,
		Goal:             input.Goal,
		RuntimeProfileID: input.RuntimeProfileID,
		Runner:           input.Runner,
		RunControls:      input.RunControls,
	})
	if err != nil {
		writeTaskError(response, err)
		return
	}

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, launchModelOverride, "")
	if err != nil {
		writeTaskAdapterError(response, err)
		return
	}

	server.recordLoopbackRewriteEvent(created)

	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		writeTaskLaunchError(response, err)
		return
	}

	launched, err := server.taskDetail(created.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, launched)
}

type taskLaunchPlan struct {
	Adapter               runtime.Adapter
	RuntimeConfig         map[string]any
	CapturedRuntimeConfig map[string]any
	Metadata              func() (runtime.NativeSessionMetadata, error)
	StopConfirmation      runtime.StopConfirmation
	LaunchModelOverride   string
	NativeResumeSessionID string
	ResolvedProfile       runtimeprofile.Profile
	ModelSnapshot         *modelprovider.Snapshot
	SkillBundles          []skill.Bundle
	LaunchGoal            string
}

type continuationLaunchBinding struct {
	Context        projectinterface.RuntimeBlackboardContextV1
	InterfaceToken string
	Snapshot       []byte
}

func (server *Server) launchTaskInBackground(created task.Task, plan taskLaunchPlan, goal string) error {
	var continuation task.TaskContinuation
	if server.projectInterface != nil {
		prepared, boundPlan, err := server.prepareGraphNativeContinuationLaunch(created, plan, goal)
		if err != nil {
			return err
		}
		continuation = prepared
		plan = boundPlan
	} else {
		if _, err := server.tasks.RecordRuntimeConfig(created.ID, created.RuntimeProfileID, plan.RuntimeConfig); err != nil {
			return err
		}
		var err error
		continuation, err = server.tasks.CreateContinuation(created.ID, created.RuntimeProfileID, plan.Adapter.Name(), created.Runner)
		if err != nil {
			return err
		}
	}
	server.logTask(created, "launched", "")
	go func() {
		launchGoal := plan.LaunchGoal
		if launchGoal == "" {
			launchGoal = goal
		}
		err := server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:           created.ID,
			Goal:             launchGoal,
			Adapter:          plan.Adapter,
			ContinuationID:   continuation.ID,
			Metadata:         plan.Metadata,
			StopConfirmation: plan.StopConfirmation,
		})
		switch {
		case err == nil:
			server.logTask(created, "completed", "")
		case errors.Is(err, context.Canceled):
			server.logTask(created, "stopped", "")
		default:
			server.logTask(created, "failed", err.Error())
		}
	}()
	return nil
}

func (server *Server) prepareGraphNativeContinuationLaunch(created task.Task, plan taskLaunchPlan, goal string) (task.TaskContinuation, taskLaunchPlan, error) {
	launch, err := server.projectInterface.CreateContinuationLaunch(context.Background(), projectinterface.ContinuationLaunchRequest{
		ProjectID: created.ProjectID, TaskID: created.ID, RuntimeProfileID: created.RuntimeProfileID,
		RuntimePluginID: plan.Adapter.Name(), Runner: created.Runner, RuntimeConfig: plan.CapturedRuntimeConfig,
	})
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	layout, err := runner.PrepareTaskLayout(server.runtimeRoot, created.ID, plan.ResolvedProfile.Provider)
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	ctx, pin := server.runtimeBlackboardContext(created, launch.Continuation)
	snapshotPath := filepath.Join(layout.Workdir, filepath.FromSlash(ctx.BlackboardPath))
	if err := server.graph.MaterializeCanonicalMainGraphSnapshot(context.Background(), pin, snapshotPath); err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, projectinterface.ValidationError(projectinterface.ErrCodeSnapshotUnavailable, "materialize pinned full Blackboard snapshot: "+err.Error(), "blackboard_path")
	}
	if err := blackboard.VerifyCanonicalMainGraphSnapshot(pin, snapshotPath); err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, projectinterface.ValidationError(projectinterface.ErrCodeSnapshotUnavailable, "verify pinned full Blackboard snapshot: "+err.Error(), "blackboard_path")
	}
	if err := runner.ProjectRuntimeBlackboardFiles(layout, ctx, created.ScopeSnapshot); err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, projectinterface.ValidationError(projectinterface.ErrCodeSnapshotUnavailable, "materialize Runtime Blackboard context: "+err.Error(), "context")
	}
	binding := &continuationLaunchBinding{Context: ctx, InterfaceToken: launch.Token, Snapshot: append([]byte(nil), launch.Projection.Bytes...)}
	boundPlan, err := server.buildTaskLaunchPlanWithBinding(created, goal, plan.LaunchModelOverride, plan.NativeResumeSessionID, binding, &plan)
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	return launch.Continuation, boundPlan, nil
}

func (server *Server) recoverPinnedContinuationFiles() error {
	continuations, err := server.tasks.ActivePinnedContinuations()
	if err != nil {
		return err
	}
	for _, continuation := range continuations {
		created, err := server.tasks.Get(continuation.TaskID)
		if err != nil {
			return fmt.Errorf("recover pinned Continuation %s Task: %w", continuation.ID, err)
		}
		layout, err := runner.PrepareTaskLayout(server.runtimeRoot, created.ID, runtimeprofile.Provider(continuation.RuntimeProvider))
		if err != nil {
			return fmt.Errorf("recover pinned Continuation %s layout: %w", continuation.ID, err)
		}
		ctx, pin := server.runtimeBlackboardContext(created, continuation)
		if err := server.graph.MaterializeCanonicalMainGraphSnapshot(context.Background(), pin, filepath.Join(layout.Workdir, filepath.FromSlash(ctx.BlackboardPath))); err != nil {
			return projectinterface.ValidationError(projectinterface.ErrCodeSnapshotUnavailable, "recover pinned full Blackboard snapshot: "+err.Error(), "blackboard_path")
		}
		if err := blackboard.VerifyCanonicalMainGraphSnapshot(pin, filepath.Join(layout.Workdir, filepath.FromSlash(ctx.BlackboardPath))); err != nil {
			return projectinterface.ValidationError(projectinterface.ErrCodeSnapshotUnavailable, "verify recovered full Blackboard snapshot: "+err.Error(), "blackboard_path")
		}
		if err := runner.ProjectRuntimeBlackboardFiles(layout, ctx, created.ScopeSnapshot); err != nil {
			return projectinterface.ValidationError(projectinterface.ErrCodeSnapshotUnavailable, "recover Runtime Blackboard context: "+err.Error(), "context")
		}
	}
	return nil
}

func (server *Server) runtimeBlackboardContext(created task.Task, continuation task.TaskContinuation) (projectinterface.RuntimeBlackboardContextV1, blackboard.CanonicalMainGraphPin) {
	sandbox := continuation.Runner == task.RunnerSandbox
	ctx := projectinterface.RuntimeBlackboardContextV1{
		ProjectID: created.ProjectID, TaskID: created.ID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: continuation.RuntimeConfigVersionID, RuntimeProfileID: continuation.RuntimeProfileID,
		RuntimePluginID: continuation.RuntimeProvider, Runner: string(continuation.Runner),
		APIURL: runner.APIEndpointURL(server.listenAddr, sandbox), MCPURL: runner.MCPEndpointURL(server.listenAddr, sandbox),
		ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
		BlackboardGraphRevision:    continuation.BlackboardGraphRevision,
		BlackboardRendererVersion:  continuation.BlackboardRendererVersion,
		BlackboardEstimatorVersion: continuation.BlackboardEstimatorVersion,
		BlackboardProjectionHash:   continuation.BlackboardProjectionHash,
		BlackboardProjectionBytes:  continuation.BlackboardProjectionBytes,
		BlackboardEstimatedTokens:  continuation.BlackboardProjectionEstimatedTokens,
	}
	pin := blackboard.CanonicalMainGraphPin{
		ProjectID: created.ProjectID, GraphRevision: continuation.BlackboardGraphRevision,
		RendererVersion: continuation.BlackboardRendererVersion, EstimatorVersion: continuation.BlackboardEstimatorVersion,
		ProjectionHash: continuation.BlackboardProjectionHash, ProjectionBytes: continuation.BlackboardProjectionBytes,
		EstimatedTokens: continuation.BlackboardProjectionEstimatedTokens,
	}
	return ctx, pin
}

type taskLaunchDefaults struct {
	runtimeProfileID string
	runner           task.Runner
}

func (server *Server) applyTaskLaunchDefaults(projectID, requestedProfileID string, requestedRunner task.Runner) (taskLaunchDefaults, error) {
	found, err := server.projects.Get(projectID)
	if err != nil {
		return taskLaunchDefaults{}, err
	}

	resolved := taskLaunchDefaults{
		runtimeProfileID: requestedProfileID,
		runner:           requestedRunner,
	}
	if resolved.runtimeProfileID == "" {
		resolved.runtimeProfileID = found.Defaults.RuntimeProfile
	}
	if resolved.runner == "" {
		resolved.runner = task.Runner(found.Defaults.Runner)
	}
	if resolved.runner == "" {
		resolved.runner = task.RunnerSandbox
	}
	return resolved, nil
}

// recordLoopbackRewriteEvent records a task event when a sandbox task's goal
// loopback targets were rewritten to host.docker.internal. It is a best-effort
// record: failures are ignored so they cannot block task launch. Host-runner
// tasks and goals without loopback targets produce no event.
func (server *Server) recordLoopbackRewriteEvent(created task.Task) {
	sandbox := created.Runner == task.RunnerSandbox
	if !sandbox {
		return
	}
	rewritten := runner.RewriteLoopbackTargets(created.Goal, sandbox)
	if rewritten == created.Goal {
		return
	}
	_, _ = server.tasks.AppendEvent(created.ID, task.EventKindLifecycle, task.EventPayload{
		"phase": "target_rewrite",
		"from":  created.Goal,
		"to":    rewritten,
		"note":  "loopback targets rewritten to host.docker.internal for sandbox runtime",
	})
}

func (server *Server) buildTaskAdapter(created task.Task, launchModelOverride string) (runtime.Adapter, map[string]any, error) {
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, launchModelOverride, "")
	if err != nil {
		return nil, nil, err
	}
	return plan.Adapter, plan.RuntimeConfig, nil
}

func (server *Server) buildTaskAdapterForGoal(created task.Task, goal string, launchModelOverride string) (runtime.Adapter, map[string]any, error) {
	plan, err := server.buildTaskLaunchPlan(created, goal, launchModelOverride, "")
	if err != nil {
		return nil, nil, err
	}
	return plan.Adapter, plan.RuntimeConfig, nil
}

func (server *Server) buildTaskLaunchPlan(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string) (taskLaunchPlan, error) {
	return server.buildTaskLaunchPlanWithBinding(created, goal, launchModelOverride, nativeResumeSessionID, nil, nil)
}

func (server *Server) buildTaskLaunchPlanWithBinding(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, binding *continuationLaunchBinding, captured *taskLaunchPlan) (taskLaunchPlan, error) {
	runtimeConfig := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}

	var profile runtimeprofile.Profile
	var skillBundles []skill.Bundle
	var capturedModelSnapshot *modelprovider.Snapshot
	if captured != nil {
		profile = captured.ResolvedProfile
		skillBundles = append([]skill.Bundle(nil), captured.SkillBundles...)
		capturedModelSnapshot = captured.ModelSnapshot
	} else {
		var err error
		profile, err = server.profiles.Get(created.RuntimeProfileID)
		if err != nil {
			return taskLaunchPlan{}, err
		}
	}
	sandbox := created.Runner == task.RunnerSandbox
	goal = runner.RewriteLoopbackTargets(goal, sandbox)
	launchGoal := goal
	if binding != nil {
		launchGoal = projectinterface.CanonicalRuntimeLaunchContext(binding.Context, binding.Snapshot, nativeResumeSessionID != "") + "\n\nTASK GOAL:\n" + goal
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		runtimeConfig["provider"] = string(runtimeprofile.ProviderFake)
		runtimeConfig["generated_config"] = runtimeprofile.GeneratedConfig(profile)
		capturedRuntimeConfig := capturedTaskRuntimeConfig(created, profile, runtimeConfig["generated_config"], nil, launchModelOverride)
		if captured != nil {
			capturedRuntimeConfig = captured.CapturedRuntimeConfig
		}
		return taskLaunchPlan{Adapter: runtime.NewFakeAdapter(), RuntimeConfig: runtimeConfig, CapturedRuntimeConfig: capturedRuntimeConfig, LaunchModelOverride: launchModelOverride, NativeResumeSessionID: nativeResumeSessionID, ResolvedProfile: profile, LaunchGoal: launchGoal}, nil
	}
	if captured == nil {
		var err error
		skillBundles, err = server.skills.EnabledSkillBundles(profile.ID)
		if err != nil {
			return taskLaunchPlan{}, err
		}
	}

	layout, err := runner.PrepareTaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	if err != nil {
		return taskLaunchPlan{}, err
	}

	mcpURL := runner.MCPEndpointURL(server.listenAddr, sandbox)
	authToken := server.authToken
	var runtimeContext *projectinterface.RuntimeBlackboardContextV1
	if binding != nil {
		authToken = binding.InterfaceToken
		runtimeContext = &binding.Context
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:           created.ProjectID,
		TaskID:              created.ID,
		ScopeSnapshot:       created.ScopeSnapshot,
		Credentials:         server.creds,
		DaemonAddr:          server.listenAddr,
		AuthToken:           authToken,
		Sandbox:             sandbox,
		RuntimePlugins:      server.runtimePlugins,
		RuntimeExtensions:   server.runtimeExtensions,
		ModelProviders:      server.modelProviders,
		ModelSnapshot:       capturedModelSnapshot,
		LaunchModelOverride: launchModelOverride,
		SkillBundles:        skillBundles,
		RuntimeContext:      runtimeContext,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	configPath := runner.LaunchConfigPath(layout, profile.Provider, projection.ConfigPath, sandbox)
	mcpConfigPath := runner.LaunchMCPConfigPath(layout, profile.Provider, sandbox, projection)
	launchProfile := profile
	if projection.ResolvedProfile.Provider != "" {
		launchProfile = projection.ResolvedProfile
	}
	providerCommand, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider:      profile.Provider,
		Profile:       launchProfile,
		Goal:          launchGoal,
		ConfigPath:    configPath,
		MCPConfigPath: mcpConfigPath,
		Sandbox:       sandbox,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if nativeResumeSessionID != "" {
		providerCommand, err = adapters.BuildNativeResumeArgs(adapters.NativeResumeArgsRequest{
			Provider:        profile.Provider,
			Profile:         launchProfile,
			NativeSessionID: nativeResumeSessionID,
			ResumedMessage:  launchGoal,
			ConfigPath:      configPath,
			MCPConfigPath:   mcpConfigPath,
		})
		if err != nil {
			return taskLaunchPlan{}, err
		}
	}

	runtimeCommand := append([]string{}, providerCommand...)
	commandProgram := runtimeCommand[0]
	commandArgs := runtimeCommand[1:]
	workdir := layout.Workdir
	containerIDFile := ""
	sandboxNetwork := runner.SandboxNetworkDefault
	launchCtx := runner.TaskContext{
		ProjectID:      created.ProjectID,
		TaskID:         created.ID,
		MCPURL:         mcpURL,
		Sandbox:        sandbox,
		RuntimeContext: runtimeContext,
	}
	if binding != nil {
		launchCtx.APIURL = binding.Context.APIURL
		launchCtx.InterfaceToken = binding.InterfaceToken
	} else {
		launchCtx.AuthToken = server.authToken
	}
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, sandbox, launchCtx, runner.ProjectionRequest{
		ProjectID:         created.ProjectID,
		TaskID:            created.ID,
		ScopeSnapshot:     created.ScopeSnapshot,
		Credentials:       server.creds,
		DaemonAddr:        server.listenAddr,
		AuthToken:         authToken,
		Sandbox:           sandbox,
		RuntimePlugins:    server.runtimePlugins,
		RuntimeExtensions: server.runtimeExtensions,
		ModelProviders:    server.modelProviders,
		ModelSnapshot:     projection.ModelSnapshot,
		SkillBundles:      skillBundles,
		RuntimeContext:    runtimeContext,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if sandbox {
		sandboxNetwork = sandboxNetworkMode(created.RunControls)
		sandboxImage := strings.TrimSpace(profile.Fields.SandboxImage)
		if sandboxImage == "" {
			sandboxImage = server.sandboxImage
		}
		containerIDFile = filepath.Join(layout.Logs, "container.cid")
		if err := os.Remove(containerIDFile); err != nil && !os.IsNotExist(err) {
			return taskLaunchPlan{}, err
		}
		sandboxRuntime := runtimeCommand
		if profile.Provider == runtimeprofile.ProviderPi {
			wrapped, err := runner.WrapSandboxPiCommand(runtimeCommand, launchProfile.Fields.Env)
			if err != nil {
				return taskLaunchPlan{}, err
			}
			sandboxRuntime = wrapped
		}
		var readOnlyTaskFiles []string
		if binding != nil {
			readOnlyTaskFiles = []string{"workdir/.pentest/blackboard.json", "workdir/.pentest/scope.json"}
		}
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout:            layout,
			Provider:          profile.Provider,
			Image:             sandboxImage,
			ContainerCLI:      server.containerCLI,
			ContainerIDFile:   containerIDFile,
			RuntimeCommand:    sandboxRuntime,
			ProcessEnv:        processEnv,
			NetworkMode:       sandboxNetwork,
			ReadOnlyTaskFiles: readOnlyTaskFiles,
		})
		if err != nil {
			return taskLaunchPlan{}, err
		}
		commandProgram = command.Program
		commandArgs = command.Args
		workdir = ""
	}

	runtimeConfig["provider"] = string(profile.Provider)
	runtimeConfig["generated_config"] = projection.Config
	if projection.ModelSnapshot != nil {
		runtimeConfig["model_provider_snapshot"] = projection.Config["model_provider_snapshot"]
	}
	if launchModelOverride != "" {
		runtimeConfig["launch_model_override"] = launchModelOverride
	}
	runtimeConfig["layout"] = layout
	if containerIDFile != "" {
		runtimeConfig["container_id_file"] = containerIDFile
	}
	runtimeConfig["launch_command"] = adapters.Redact(map[string]any{
		"program": commandProgram,
		"args":    commandArgs,
	})
	capturedRuntimeConfig := capturedTaskRuntimeConfig(created, launchProfile, runtimeprofile.GeneratedConfig(launchProfile), projection.Config["model_provider_snapshot"], launchModelOverride)
	if captured != nil {
		capturedRuntimeConfig = captured.CapturedRuntimeConfig
	}

	var adapter runtime.Adapter
	if sandbox {
		sandboxConfig := runtime.DockerSandboxConfig{
			Name:         string(profile.Provider),
			ContainerCLI: commandProgram,
			CreateArgs:   commandArgs,
		}
		if sandboxNetwork == runner.SandboxNetworkHostProxyOnly {
			sandboxConfig.RequiredNetwork = &runtime.DockerNetworkRequirement{
				Name:     runner.HostProxyOnlySandboxNetworkName,
				Driver:   "bridge",
				Internal: false,
			}
		}
		adapter = runtime.NewDockerSandboxAdapter(sandboxConfig)
	} else {
		adapter = runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    string(profile.Provider),
			Program: commandProgram,
			Args:    commandArgs,
			Workdir: workdir,
			Env:     processEnv,
		})
	}

	// Pi writes its real-time progress to a session jsonl file instead of
	// stdout, so a sandboxed Pi task's timeline is empty until it exits. Wrap
	// the adapter with a session-file tailer that re-emits appended lines as
	// runtime_output events the transcript parser already understands.
	if sandbox && profile.Provider == runtimeprofile.ProviderPi {
		sessionDir := filepath.Join(layout.ProviderHome, "agent", "sessions")
		adapter = runtime.NewPiSessionTailAdapter(adapter, sessionDir)
	}

	var metadata func() (runtime.NativeSessionMetadata, error)
	if sandbox || profile.Provider == runtimeprofile.ProviderCodex || profile.Provider == runtimeprofile.ProviderPi {
		metadata = func() (runtime.NativeSessionMetadata, error) {
			var collected runtime.NativeSessionMetadata
			if containerIDFile != "" {
				containerID, err := runtime.ReadContainerIDFile(containerIDFile)
				if err != nil && !os.IsNotExist(err) {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.ContainerID = containerID
			}
			switch profile.Provider {
			case runtimeprofile.ProviderCodex:
				session, err := runtime.DiscoverCodexSession(layout.ProviderHome)
				if err != nil {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.NativeSessionID = session.NativeSessionID
				collected.NativeSessionPath = session.NativeSessionPath
			case runtimeprofile.ProviderPi:
				session, err := runtime.DiscoverPiSession(layout.ProviderHome)
				if err != nil {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.NativeSessionID = session.NativeSessionID
				collected.NativeSessionPath = session.NativeSessionPath
			}
			return collected, nil
		}
	}
	var stopConfirmation runtime.StopConfirmation
	if containerIDFile != "" {
		stopConfirmation = runtime.DockerContainerStopConfirmation(server.containerCLI, containerIDFile)
	}

	return taskLaunchPlan{
		Adapter:               adapter,
		RuntimeConfig:         runtimeConfig,
		CapturedRuntimeConfig: capturedRuntimeConfig,
		Metadata:              metadata,
		StopConfirmation:      stopConfirmation,
		LaunchModelOverride:   launchModelOverride,
		NativeResumeSessionID: nativeResumeSessionID,
		ResolvedProfile:       launchProfile,
		ModelSnapshot:         projection.ModelSnapshot,
		SkillBundles:          append([]skill.Bundle(nil), skillBundles...),
		LaunchGoal:            launchGoal,
	}, nil
}

func capturedTaskRuntimeConfig(created task.Task, profile runtimeprofile.Profile, generatedConfig any, modelSnapshot any, launchModelOverride string) map[string]any {
	captured := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runtime_plugin_id":  string(profile.Provider),
		"runner":             created.Runner,
		"generated_config":   generatedConfig,
	}
	if modelSnapshot != nil {
		captured["model_provider_snapshot"] = modelSnapshot
	}
	if launchModelOverride != "" {
		captured["launch_model_override"] = launchModelOverride
	}
	return captured
}

func sandboxNetworkMode(runControls task.RunControls) runner.SandboxNetworkMode {
	switch strings.TrimSpace(runControls.SandboxNetwork) {
	case string(runner.SandboxNetworkHostProxyOnly):
		return runner.SandboxNetworkHostProxyOnly
	}
	if runControls.Extras == nil {
		return runner.SandboxNetworkDefault
	}
	switch strings.TrimSpace(runControls.Extras["sandbox_network"]) {
	case string(runner.SandboxNetworkHostProxyOnly):
		return runner.SandboxNetworkHostProxyOnly
	default:
		return runner.SandboxNetworkDefault
	}
}

func (server *Server) handleListTasks(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}

	tasks, err := server.tasks.ListForProject(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list tasks")
		return
	}
	if tasks == nil {
		tasks = []task.Task{}
	}
	writeJSON(response, http.StatusOK, struct {
		Tasks []task.Task `json:"tasks"`
	}{
		Tasks: tasks,
	})
}

func (server *Server) handleGetTask(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	detailed, err := server.decorateTask(found)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load task continuation")
		return
	}
	writeJSON(response, http.StatusOK, detailed)
}

func (server *Server) handleDeleteTask(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	if err := server.tasks.Delete(found.ID); err != nil {
		writeTaskError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) requireProjectTask(response http.ResponseWriter, request *http.Request) (task.Task, bool) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return task.Task{}, false
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return task.Task{}, false
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return task.Task{}, false
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return task.Task{}, false
	}
	return found, true
}

func (server *Server) taskDetail(taskID string) (task.Task, error) {
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return task.Task{}, err
	}
	return server.decorateTask(found)
}

func (server *Server) decorateTask(found task.Task) (task.Task, error) {
	active, err := server.tasks.ActiveContinuation(found.ID)
	if err != nil {
		return task.Task{}, err
	}
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return task.Task{}, err
	}
	latest, err = server.captureDiscoverableNativeSession(found, latest)
	if err != nil {
		return task.Task{}, err
	}
	if active != nil && latest != nil && active.ID == latest.ID {
		active = latest
	}
	controls, err := server.runtimeControlsForTask(found, latest)
	if err != nil {
		return task.Task{}, err
	}
	found.RuntimeControls = controls
	found.ActiveContinuation = active
	found.LatestContinuation = latest
	return found, nil
}

func (server *Server) captureDiscoverableNativeSession(found task.Task, latest *task.TaskContinuation) (*task.TaskContinuation, error) {
	if latest == nil || strings.TrimSpace(latest.NativeSessionID) != "" {
		return latest, nil
	}
	profile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return nil, err
	}
	if profile.Provider != runtimeprofile.ProviderCodex && profile.Provider != runtimeprofile.ProviderPi {
		return latest, nil
	}
	metadata, err := server.discoverProviderNativeSession(found.ID, profile.Provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(metadata.NativeSessionID) == "" {
		return latest, nil
	}
	updated, err := server.tasks.UpdateContinuationRuntimeMetadata(latest.ID, "", metadata.NativeSessionID, metadata.NativeSessionPath)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (server *Server) runtimeControlsForTask(found task.Task, latest *task.TaskContinuation) (task.RuntimeControls, error) {
	profile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.RuntimeControls{}, err
	}
	plugin, ok := server.runtimePlugins.Get(string(profile.Provider))
	nativeResumeSupported := ok && plugin.NativeResume.Supported
	active := found.Status == task.StatusRunning || found.Status == task.StatusPaused
	sessionCaptured := latest != nil && strings.TrimSpace(latest.NativeSessionID) != ""

	controls := task.RuntimeControls{
		HandoffResumeAvailable:  !active,
		QueueSteerAvailable:     true,
		NativeSessionCaptured:   sessionCaptured,
		SameRuntimeProviderOnly: true,
		RuntimeProvider:         string(profile.Provider),
	}
	if nativeResumeSupported {
		controls.NativeResumeAvailable = !active && sessionCaptured
		controls.InterruptSteerAvailable = active && sessionCaptured
	} else {
		controls.NativeResumeReason = fmt.Sprintf("native resume unsupported for provider %s", profile.Provider)
		controls.InterruptSteerReason = controls.NativeResumeReason
	}
	if nativeResumeSupported && !sessionCaptured {
		controls.NativeResumeReason = "native session unavailable"
		controls.InterruptSteerReason = controls.NativeResumeReason
	}
	return controls, nil
}

func (server *Server) handleTaskEvents(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	if events == nil {
		events = []task.Event{}
	}
	writeJSON(response, http.StatusOK, struct {
		Events []task.Event `json:"events"`
	}{
		Events: events,
	})
}

func (server *Server) handleTaskTimeline(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	items := timeline.Build(events)
	if items == nil {
		items = []timeline.Item{}
	}
	writeJSON(response, http.StatusOK, struct {
		TaskID string          `json:"task_id"`
		Items  []timeline.Item `json:"items"`
	}{
		TaskID: found.ID,
		Items:  items,
	})
}

func (server *Server) handleTaskTranscript(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	entries := transcript.Build(found, events)
	if entries == nil {
		entries = []transcript.Entry{}
	}
	writeJSON(response, http.StatusOK, struct {
		TaskID  string             `json:"task_id"`
		Entries []transcript.Entry `json:"entries"`
	}{
		TaskID:  found.ID,
		Entries: entries,
	})
}

func (server *Server) handleStopTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	if found.Status == task.StatusRunning || found.Status == task.StatusPaused {
		if ok := server.harness.StopAndWait(taskID, 10*time.Second); !ok {
			writeError(response, http.StatusConflict, "runtime did not stop in time")
			return
		}
		stopped, err := server.taskDetail(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, stopped)
		return
	}
	stopped, err := server.tasks.UpdateStatus(taskID, task.StatusStopped)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, stopped)
}

func (server *Server) acquireTaskControl(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	if server.activeControls[taskID] {
		return false
	}
	server.activeControls[taskID] = true
	return true
}

func (server *Server) releaseTaskControl(taskID string) {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	delete(server.activeControls, taskID)
}

func (server *Server) handleResumeTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if server.harness.IsActive(taskID) || found.Status == task.StatusRunning {
		writeError(response, http.StatusConflict, "task is already running")
		return
	}
	var input taskContinuationSelectionInput
	if err := decodeOptionalJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)
	if input.hasSelection() {
		if _, ok := server.recordSelectedRuntimeConfig(response, found, "", input); !ok {
			return
		}
		refreshed, err := server.tasks.Get(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		found = refreshed
	}
	found, resumeGoal, plan, err := server.prepareNativeResumeContinuation(found, "")
	if err != nil {
		server.writeResumePreparationError(response, err)
		return
	}
	if err := server.launchTaskInBackground(found, plan, resumeGoal); err != nil {
		writeTaskLaunchError(response, err)
		return
	}

	updated, err := server.taskDetail(found.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, updated)
}

func (server *Server) handleResumeHandoffTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if server.harness.IsActive(taskID) || found.Status == task.StatusRunning {
		writeError(response, http.StatusConflict, "task is already running")
		return
	}
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)
	found, resumeGoal, plan, err := server.prepareHandoffResumeContinuation(found)
	if err != nil {
		server.writeResumePreparationError(response, err)
		return
	}
	if err := server.launchTaskInBackground(found, plan, resumeGoal); err != nil {
		writeTaskLaunchError(response, err)
		return
	}
	updated, err := server.taskDetail(found.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, updated)
}

func (server *Server) prepareNativeResumeContinuation(found task.Task, resumedMessage string) (task.Task, string, taskLaunchPlan, error) {
	found, resumedMessage, nativeResumeSessionID, err := server.prepareNativeResumeRequest(found, resumedMessage)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan, err := server.buildTaskLaunchPlan(found, resumedMessage, "", nativeResumeSessionID)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	return found, resumedMessage, plan, nil
}

func (server *Server) prepareNativeResumeRequest(found task.Task, resumedMessage string) (task.Task, string, string, error) {
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", "", err
	}
	found.RuntimeProfileID = effectiveProfile.ID
	nativeResumeSessionID, err := server.discoverNativeResumeSession(found)
	if err != nil {
		return task.Task{}, "", "", err
	}
	return found, resumedMessage, nativeResumeSessionID, nil
}

func (server *Server) prepareHandoffResumeContinuation(found task.Task) (task.Task, string, taskLaunchPlan, error) {
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	found.RuntimeProfileID = effectiveProfile.ID

	resumeGoal, err := server.buildResumeGoal(found)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan, err := server.buildTaskLaunchPlan(found, resumeGoal, "", "")
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	return found, resumeGoal, plan, nil
}

func (server *Server) buildResumeGoal(found task.Task) (string, error) {
	var factLines, progressFacts, findingLines []string
	if server.reads == nil {
		var err error
		factLines, progressFacts, findingLines, err = server.resumeGoalBlackboardLinesLegacy(found.ProjectID)
		if err != nil {
			return "", err
		}
	}

	taskSummary := ""
	summaries, err := server.tasks.SummaryVersions(found.ID)
	if err != nil {
		return "", err
	}
	if len(summaries) > 0 {
		taskSummary = summaries[len(summaries)-1].Summary
	}

	steeringDirective := ""
	events, err := server.tasks.Events(found.ID)
	if err != nil {
		return "", err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != task.EventKindSteering {
			continue
		}
		if directive, ok := events[i].Payload["directive"].(string); ok {
			steeringDirective = directive
			break
		}
	}

	return adapters.BuildResumePrompt(adapters.ResumeRequest{
		Goal:              found.Goal,
		TaskSummary:       taskSummary,
		FactIndex:         factLines,
		FindingIndex:      findingLines,
		ProgressFacts:     progressFacts,
		SteeringDirective: steeringDirective,
	}), nil
}

func (server *Server) resumeGoalBlackboardLinesLegacy(projectID string) (factLines, progressFacts, findingLines []string, err error) {
	factIndex, err := server.facts.FactIndex(projectID, blackboard.FactIndexOptions{})
	if err != nil {
		return nil, nil, nil, err
	}
	factLines = make([]string, 0, len(factIndex))
	progressFacts = make([]string, 0)
	for _, entry := range factIndex {
		factLines = append(factLines, entry.FactKey+": "+entry.Summary)
		if !strings.HasPrefix(entry.FactKey, "progress:") {
			continue
		}
		fact, err := server.facts.GetFact(projectID, entry.FactKey)
		if err != nil || strings.TrimSpace(fact.Body) == "" {
			continue
		}
		progressFacts = append(progressFacts, entry.FactKey+":\n"+fact.Body)
	}

	findings, err := server.facts.ListFindings(projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	findingLines = make([]string, 0, len(findings))
	start := 0
	if len(findings) > adapters.MaxResumeFindings {
		start = len(findings) - adapters.MaxResumeFindings
	}
	for _, finding := range findings[start:] {
		findingLines = append(findingLines, finding.FindingKey+": "+finding.Title+" ("+string(finding.Status)+")")
	}
	return factLines, progressFacts, findingLines, nil
}

func (server *Server) writeResumePreparationError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtimeprofile.ErrNotFound):
		writeError(response, http.StatusBadRequest, "runtime profile not found")
	case errors.Is(err, errNativeResumeUnavailable):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNativeSessionUnavailable):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeTaskAdapterError(response, err)
	}
}

func (server *Server) handleQueueSteerTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	var input struct {
		Directive string `json:"directive"`
		taskContinuationSelectionInput
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(input.Directive) == "" {
		writeError(response, http.StatusBadRequest, "steering directive is required")
		return
	}
	payload := task.EventPayload{
		"directive": input.Directive,
		"phase":     "steering_requested",
		"mode":      "queue",
	}
	if input.SubmittedBy != "" {
		payload["submitted_by"] = input.SubmittedBy
	}
	if input.RuntimeProfileID != "" {
		payload["runtime_profile_id"] = input.RuntimeProfileID
	}
	if input.ModelProviderID != "" {
		payload["model_provider_id"] = input.ModelProviderID
	}
	if input.ModelOverride != "" {
		payload["model_override"] = input.ModelOverride
	}
	event, err := server.tasks.AppendEvent(taskID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	var configVersion *task.RuntimeConfigVersion
	if input.hasSelection() {
		recorded, ok := server.recordSelectedRuntimeConfig(response, found, event.ID, input.taskContinuationSelectionInput)
		if !ok {
			return
		}
		configVersion = &recorded
	}
	writeJSON(response, http.StatusOK, struct {
		Event                task.Event                 `json:"event"`
		RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
	}{Event: event, RuntimeConfigVersion: configVersion})
}

func (server *Server) handleSteerTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	var input struct {
		Directive string `json:"directive"`
		taskContinuationSelectionInput
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.Directive == "" {
		writeError(response, http.StatusBadRequest, "steering directive is required")
		return
	}

	activeSteer := found.Status == task.StatusRunning || found.Status == task.StatusPaused
	if activeSteer {
		if !server.acquireTaskControl(taskID) {
			writeError(response, http.StatusConflict, "task control operation already active")
			return
		}
		defer server.releaseTaskControl(taskID)
	}

	payload := task.EventPayload{
		"directive": input.Directive,
		"phase":     "steering_requested",
	}
	if input.SubmittedBy != "" {
		payload["submitted_by"] = input.SubmittedBy
	}
	if input.RuntimeProfileID != "" {
		payload["runtime_profile_id"] = input.RuntimeProfileID
	}
	if input.ModelProviderID != "" {
		payload["model_provider_id"] = input.ModelProviderID
	}
	if input.ModelOverride != "" {
		payload["model_override"] = input.ModelOverride
	}

	event, err := server.tasks.AppendEvent(taskID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return
	}

	var configVersion *task.RuntimeConfigVersion
	if input.hasSelection() {
		recorded, ok := server.recordSelectedRuntimeConfig(response, found, event.ID, input.taskContinuationSelectionInput)
		if !ok {
			return
		}
		configVersion = &recorded
	}

	if activeSteer {
		resumedTask, resumeGoal, nativeResumeSessionID, err := server.prepareNativeResumeRequest(found, input.Directive)
		if err != nil {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "resume_failed",
				"error": err.Error(),
			})
			server.writeResumePreparationError(response, err)
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": "interrupting",
		})
		if ok := server.harness.StopAndWait(taskID, 10*time.Second); !ok {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "stop_failed",
			})
			writeError(response, http.StatusConflict, "runtime did not stop in time")
			return
		}

		plan, err := server.buildTaskLaunchPlan(resumedTask, resumeGoal, "", nativeResumeSessionID)
		if err != nil {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "resume_failed",
				"error": err.Error(),
			})
			server.writeResumePreparationError(response, err)
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": "resuming_native",
		})
		if err := server.launchTaskInBackground(resumedTask, plan, resumeGoal); err != nil {
			writeTaskLaunchError(response, err)
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindSteering, task.EventPayload{
			"phase":              "steering_applied",
			"directive":          input.Directive,
			"requested_event_id": event.ID,
		})
		detailed, err := server.taskDetail(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		writeJSON(response, http.StatusAccepted, struct {
			Event                task.Event                 `json:"event"`
			RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
			Task                 task.Task                  `json:"task"`
		}{
			Event:                event,
			RuntimeConfigVersion: configVersion,
			Task:                 detailed,
		})
		return
	}

	writeJSON(response, http.StatusOK, struct {
		Event                task.Event                 `json:"event"`
		RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
	}{
		Event:                event,
		RuntimeConfigVersion: configVersion,
	})
}

func decodeOptionalJSON(request *http.Request, target any) error {
	if request.Body == nil {
		return nil
	}
	err := json.NewDecoder(request.Body).Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (server *Server) recordSelectedRuntimeConfig(response http.ResponseWriter, found task.Task, steeringEventID string, input taskContinuationSelectionInput) (task.RuntimeConfigVersion, bool) {
	requestedProfile, ok := server.resolveTaskContinuationRuntimeProfile(response, found, input)
	if !ok {
		return task.RuntimeConfigVersion{}, false
	}

	config := input.Config
	if config == nil {
		config = map[string]any{}
	}
	config["runtime_profile_id"] = requestedProfile.ID
	config["runner"] = found.Runner
	if steeringEventID != "" {
		config["steering_event_id"] = steeringEventID
	}
	if requestedProfile.Fields.ModelProviderID != "" {
		config["model_provider_id"] = requestedProfile.Fields.ModelProviderID
	}
	if requestedProfile.Fields.ModelOverride != "" {
		config["model_override"] = requestedProfile.Fields.ModelOverride
	}
	recorded, err := server.tasks.RecordRuntimeConfig(found.ID, requestedProfile.ID, config)
	if err != nil {
		writeTaskError(response, err)
		return task.RuntimeConfigVersion{}, false
	}
	return recorded, true
}

func (server *Server) resolveTaskContinuationRuntimeProfile(response http.ResponseWriter, found task.Task, input taskContinuationSelectionInput) (runtimeprofile.Profile, bool) {
	currentProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		if errors.Is(err, runtimeprofile.ErrNotFound) {
			writeError(response, http.StatusBadRequest, "runtime profile not found")
			return runtimeprofile.Profile{}, false
		}
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return runtimeprofile.Profile{}, false
	}
	return server.resolveSelectedRuntimeProfile(response, currentProfile, input)
}

func (server *Server) resolveSelectedRuntimeProfile(response http.ResponseWriter, currentProfile runtimeprofile.Profile, input taskContinuationSelectionInput) (runtimeprofile.Profile, bool) {
	runtimeProfileID := strings.TrimSpace(input.RuntimeProfileID)
	modelProviderID := strings.TrimSpace(input.ModelProviderID)
	if runtimeProfileID != "" && modelProviderID != "" {
		writeError(response, http.StatusBadRequest, "choose runtime_profile_id or model_provider_id, not both")
		return runtimeprofile.Profile{}, false
	}
	if runtimeProfileID != "" {
		requestedProfile, err := server.profiles.Get(runtimeProfileID)
		if err != nil {
			if errors.Is(err, runtimeprofile.ErrNotFound) {
				writeError(response, http.StatusBadRequest, "runtime profile not found")
				return runtimeprofile.Profile{}, false
			}
			writeError(response, http.StatusInternalServerError, "load runtime profile")
			return runtimeprofile.Profile{}, false
		}
		if requestedProfile.Provider != currentProfile.Provider {
			writeError(response, http.StatusBadRequest, "steering runtime profile must keep the same runtime provider")
			return runtimeprofile.Profile{}, false
		}
		return requestedProfile, true
	}
	if modelProviderID == "" {
		return currentProfile, true
	}

	providerName := modelProviderID
	if server.modelProviders != nil {
		provider, err := server.modelProviders.Get(modelProviderID)
		if err != nil {
			if errors.Is(err, modelprovider.ErrNotFound) {
				writeError(response, http.StatusBadRequest, "model provider not found")
				return runtimeprofile.Profile{}, false
			}
			writeError(response, http.StatusInternalServerError, "load model provider")
			return runtimeprofile.Profile{}, false
		}
		providerName = provider.Name
	}

	modelOverride := strings.TrimSpace(input.ModelOverride)
	if strings.TrimSpace(currentProfile.Fields.ModelProviderID) == modelProviderID &&
		strings.TrimSpace(currentProfile.Fields.ModelOverride) == modelOverride {
		return currentProfile, true
	}
	fields := currentProfile.Fields
	fields.ModelProviderID = modelProviderID
	fields.ModelOverride = modelOverride
	fields.ModelProviderProtocol = ""
	fields.Model = ""
	fields.Endpoint = ""
	fields.APIKeys = nil
	name := runtimeprofile.LaunchProfileName(runtimeprofile.LaunchSelection{
		Provider:        currentProfile.Provider,
		ModelProviderID: modelProviderID,
		ModelOverride:   modelOverride,
	}, providerName)
	created, err := server.profiles.CreateLaunchResolved(name, currentProfile.Provider, fields)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return runtimeprofile.Profile{}, false
	}
	return created, true
}

func (server *Server) resolveTaskRuntimeProfile(found task.Task) (runtimeprofile.Profile, error) {
	profileID := found.RuntimeProfileID
	versions, err := server.tasks.RuntimeConfigVersions(found.ID)
	if err != nil {
		return runtimeprofile.Profile{}, err
	}
	if len(versions) > 0 {
		latest := versions[len(versions)-1]
		if strings.TrimSpace(latest.RuntimeProfileID) != "" {
			profileID = latest.RuntimeProfileID
		}
	}
	return server.profiles.Get(profileID)
}

func (server *Server) discoverNativeResumeSession(found task.Task) (string, error) {
	profile, err := server.profiles.Get(found.RuntimeProfileID)
	if err != nil {
		return "", err
	}
	plugin, ok := server.runtimePlugins.Get(string(profile.Provider))
	if !ok || !plugin.NativeResume.Supported {
		return "", fmt.Errorf("%w for provider %s", errNativeResumeUnavailable, profile.Provider)
	}
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return "", err
	}
	if latest != nil && strings.TrimSpace(latest.NativeSessionID) != "" {
		return latest.NativeSessionID, nil
	}
	metadata, err := server.discoverProviderNativeSession(found.ID, profile.Provider)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(metadata.NativeSessionID) == "" {
		return "", errNativeSessionUnavailable
	}
	return metadata.NativeSessionID, nil
}

func (server *Server) discoverProviderNativeSession(taskID string, provider runtimeprofile.Provider) (runtime.NativeSessionMetadata, error) {
	if provider != runtimeprofile.ProviderCodex && provider != runtimeprofile.ProviderPi {
		return runtime.NativeSessionMetadata{}, nil
	}
	layout, err := runner.PrepareTaskLayout(server.runtimeRoot, taskID, provider)
	if err != nil {
		return runtime.NativeSessionMetadata{}, err
	}
	switch provider {
	case runtimeprofile.ProviderCodex:
		return runtime.DiscoverCodexSession(layout.ProviderHome)
	case runtimeprofile.ProviderPi:
		return runtime.DiscoverPiSession(layout.ProviderHome)
	default:
		return runtime.NativeSessionMetadata{}, nil
	}
}

func (server *Server) handleTaskContinuation(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	versions, err := server.tasks.SummaryVersions(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if len(versions) > 0 {
		writeJSON(response, http.StatusOK, struct {
			Mode    string              `json:"mode"`
			Summary task.SummaryVersion `json:"summary"`
		}{
			Mode:    "summary",
			Summary: versions[len(versions)-1],
		})
		return
	}

	configVersions, err := server.tasks.RuntimeConfigVersions(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	events, err := server.tasks.Events(taskID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}

	type handoffPayload struct {
		TaskID           string           `json:"task_id"`
		ProjectID        string           `json:"project_id"`
		Goal             string           `json:"goal"`
		RuntimeProfileID string           `json:"runtime_profile_id"`
		Runner           task.Runner      `json:"runner"`
		ScopeDomains     []string         `json:"scope_domains"`
		ScopeNotes       string           `json:"scope_notes"`
		RunControls      task.RunControls `json:"run_controls"`
		EventCount       int              `json:"event_count"`
		ConfigVersions   int              `json:"config_versions"`
	}

	writeJSON(response, http.StatusOK, struct {
		Mode    string               `json:"mode"`
		Summary *task.SummaryVersion `json:"summary"`
		Handoff handoffPayload       `json:"handoff"`
	}{
		Mode:    "mechanical_handoff",
		Summary: nil,
		Handoff: handoffPayload{
			TaskID:           found.ID,
			ProjectID:        found.ProjectID,
			Goal:             found.Goal,
			RuntimeProfileID: found.RuntimeProfileID,
			Runner:           found.Runner,
			ScopeDomains:     found.ScopeSnapshot.Domains,
			ScopeNotes:       found.ScopeSnapshot.Notes,
			RunControls:      found.RunControls,
			EventCount:       len(events),
			ConfigVersions:   len(configVersions),
		},
	})
}

func (server *Server) handlePutTaskSummary(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireCompatibilityProject(response, request, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	var input struct {
		Summary        string `json:"summary"`
		SubmittedBy    string `json:"submitted_by"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}
	if !server.decodeCompatibilityJSON(response, request, &input) {
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		key := request.Header.Get("Idempotency-Key")
		if key == "" {
			key = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallPutTaskSummary, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: key,
			TaskSummary: &blackboardcompat.TaskSummaryWrite{TaskID: taskID, Summary: input.Summary, SubmittedBy: input.SubmittedBy},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
		return
	}
	summary, err := server.tasks.PutSummary(taskID, input.Summary, input.SubmittedBy)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, summary)
}

func (server *Server) handleGetTaskSummary(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		if err := server.compatibility.RejectRetiredRead(request.Context(), blackboardcompat.CallReadTaskSummary); err != nil {
			writeCompatibilityError(response, err)
			return
		}
		if err := server.compatibility.RecordUse(request.Context(), blackboardcompat.Use{ProjectID: projectID, Transport: blackboardcompat.TransportHTTP, Kind: blackboardcompat.CallReadTaskSummary, Mode: blackboardcompat.UseModeRead}); err != nil {
			writeCompatibilityError(response, err)
			return
		}
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	versions, err := server.tasks.SummaryVersions(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if versions == nil {
		versions = []task.SummaryVersion{}
	}
	var latest *task.SummaryVersion
	if len(versions) > 0 {
		latest = &versions[len(versions)-1]
	}
	writeJSON(response, http.StatusOK, struct {
		Summary  *task.SummaryVersion  `json:"summary"`
		Versions []task.SummaryVersion `json:"versions"`
	}{
		Summary:  latest,
		Versions: versions,
	})
}

// requireProject centralizes the project-exists check that every project-scoped
// task route must perform before doing any work: it returns false (and writes the
// response) when the project is unknown or unreadable, matching the check the
// blackboard / credential / dashboard routes already apply.
func (server *Server) requireProject(response http.ResponseWriter, projectID string) bool {
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
		} else {
			writeError(response, http.StatusInternalServerError, "load project")
		}
		return false
	}
	return true
}

func writeTaskError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, task.ErrMissingGoal), errors.Is(err, task.ErrUnsupportedRunner), errors.Is(err, task.ErrMissingSummary):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, task.ErrProjectNotFound), errors.Is(err, task.ErrNotFound), errors.Is(err, project.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, task.ErrActiveTask):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "task operation failed")
	}
}

func writeTaskAdapterError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, skill.ErrInvalidSkill),
		errors.Is(err, modelprovider.ErrMissingAPIKeyEnv),
		errors.Is(err, modelprovider.ErrMissingProvider),
		errors.Is(err, modelprovider.ErrMissingModel),
		errors.Is(err, modelprovider.ErrIncompatibleProtocol):
		writeError(response, http.StatusBadRequest, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, err.Error())
	}
}

func writeTaskLaunchError(response http.ResponseWriter, err error) {
	var interfaceErr *projectinterface.Error
	if errors.As(err, &interfaceErr) {
		status := http.StatusInternalServerError
		if interfaceErr.Code == projectinterface.ErrCodeSnapshotUnavailable {
			status = http.StatusServiceUnavailable
		}
		writeJSON(response, status, struct {
			Error *projectinterface.Error `json:"error"`
		}{Error: interfaceErr})
		return
	}
	writeError(response, http.StatusInternalServerError, err.Error())
}
