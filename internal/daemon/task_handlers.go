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
	"slices"
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/blackboardv2"

	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/store"
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
	Adapter                      runtime.Adapter
	RuntimeConfig                map[string]any
	CapturedRuntimeConfig        map[string]any
	MaterializedCredentials      map[string]string
	Metadata                     func() (runtime.NativeSessionMetadata, error)
	StopConfirmation             runtime.StopConfirmation
	LaunchModelOverride          string
	NativeResumeSessionID        string
	ResolvedProfile              runtimeprofile.Profile
	ModelSnapshot                *modelprovider.Snapshot
	SkillBundles                 []skill.Bundle
	LaunchGoal                   string
	BlackboardV2                 bool
	ValidatedLayout              *runner.Layout
	BlackboardV2SteeringEventIDs []string
}

type continuationLaunchBinding struct {
	V2Header       *blackboardv2.LaunchHeader
	InterfaceToken string
}

func (server *Server) launchTaskInBackground(created task.Task, plan taskLaunchPlan, goal string) error {
	if !plan.BlackboardV2 {
		return fmt.Errorf("Blackboard v2 launch projection is required")
	}
	server.logTaskLaunchStage(created, "prepare_continuation")
	continuation, boundPlan, err := server.prepareBlackboardV2ContinuationLaunch(created, plan, goal)
	if err != nil {
		return err
	}
	plan = boundPlan
	if server.providerSessionFactory != nil && created.Runner == task.RunnerSandbox && supportedProviderSessionFactoryProvider(plan.ResolvedProfile.Provider) {
		binding, factoryErr := server.providerSessionFactory.Open(context.Background(), ProviderSessionLaunchRequest{
			Task: created, Continuation: continuation, Provider: plan.ResolvedProfile.Provider,
			Runner: created.Runner, LaunchGoal: plan.LaunchGoal, RuntimeConfig: plan.CapturedRuntimeConfig,
			LegacyAdapter: plan.Adapter,
		})
		if factoryErr == nil {
			factoryErr = validateProviderSessionBinding(binding)
		}
		if factoryErr != nil {
			redactedErr := &providerSessionFactoryError{cause: factoryErr}
			server.failProviderSessionLaunch(created.ID, continuation.ID, redactedErr)
			return redactedErr
		}
		if bindErr := server.BindProviderSession(created.ID, binding.Session); bindErr != nil {
			_ = binding.Session.Close(context.Background())
			redactedErr := &providerSessionFactoryError{cause: bindErr}
			server.failProviderSessionLaunch(created.ID, continuation.ID, redactedErr)
			return redactedErr
		}
		plan.Adapter = binding.Adapter
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

func (server *Server) failProviderSessionLaunch(taskID, continuationID string, cause error) {
	// The durable Continuation already exists at this point. Marking both
	// records terminal prevents an unbound pending Continuation from looking
	// resumable after a factory crash or setup rejection.
	_, _ = server.tasks.AppendContinuationEvent(taskID, continuationID, task.EventKindLifecycle, task.EventPayload{
		"phase": "provider_session_setup_failed", "error": "provider session setup failed",
	})
	_, _ = server.tasks.UpdateContinuationStatus(continuationID, task.StatusFailed)
	_, _ = server.tasks.UpdateStatus(taskID, task.StatusFailed)
}

func (server *Server) prepareBlackboardV2ContinuationLaunch(created task.Task, plan taskLaunchPlan, goal string) (task.TaskContinuation, taskLaunchPlan, error) {
	if plan.ValidatedLayout == nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, fmt.Errorf("Blackboard v2 layout was not validated")
	}
	provider := plan.ResolvedProfile.Provider
	if provider == "" {
		profile, err := server.profiles.Get(created.RuntimeProfileID)
		if err != nil {
			return task.TaskContinuation{}, taskLaunchPlan{}, err
		}
		provider = profile.Provider
		plan.ResolvedProfile = profile
	}
	if !runner.BlackboardV2SupportsProvider(provider) {
		return task.TaskContinuation{}, taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", provider)
	}
	layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, provider)
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	plan.ValidatedLayout = &layout
	var boundPlan taskLaunchPlan
	var launchHeader blackboardv2.LaunchHeader
	usesTrustedMCP := runner.BlackboardV2UsesTrustedMCP(provider)
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: created.ProjectID, TaskID: created.ID, RuntimeProfileID: created.RuntimeProfileID,
		RuntimeProvider: string(provider), Runner: created.Runner, RuntimeConfig: plan.CapturedRuntimeConfig,
		SteeringEventIDs: plan.BlackboardV2SteeringEventIDs,
		Precommit: func(projection blackboardv2.ContinuationLaunchProjection) error {
			launchHeader = blackboardv2.LaunchHeader{
				Runner: string(created.Runner), ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: projection.Schema, Revision: projection.Revision,
			}
			// Codex completes projection here (networkless). Claude/Pi project
			// grant-less layout/config first; BindGrant re-projects with the
			// Continuation grant before the launch transaction commits.
			binding := &continuationLaunchBinding{V2Header: &launchHeader}
			var err error
			boundPlan, err = server.buildTaskLaunchPlanWithBinding(created, goal, plan.LaunchModelOverride, plan.NativeResumeSessionID, binding, &plan)
			return err
		},
		BindGrant: func(plaintextGrant string) error {
			if !usesTrustedMCP || strings.TrimSpace(plaintextGrant) == "" {
				return nil
			}
			binding := &continuationLaunchBinding{V2Header: &launchHeader, InterfaceToken: plaintextGrant}
			var err error
			boundPlan, err = server.buildTaskLaunchPlanWithBinding(created, goal, plan.LaunchModelOverride, plan.NativeResumeSessionID, binding, &plan)
			if err != nil {
				scrubBlackboardV2GrantBearingProjection(layout, provider)
			}
			return err
		},
		UnbindGrant: func() {
			if usesTrustedMCP {
				scrubBlackboardV2GrantBearingProjection(layout, provider)
			}
		},
	})
	if err != nil {
		return task.TaskContinuation{}, taskLaunchPlan{}, err
	}
	return launch.Continuation, boundPlan, nil
}

// scrubBlackboardV2GrantBearingProjection removes trusted MCP config that may
// embed a Continuation grant token after a failed atomic launch.
func scrubBlackboardV2GrantBearingProjection(layout runner.Layout, provider runtimeprofile.Provider) {
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		_ = os.Remove(filepath.Join(layout.Workdir, ".mcp.json"))
	case runtimeprofile.ProviderPi:
		_ = os.Remove(filepath.Join(layout.ProviderHome, "agent", "mcp.json"))
	}
}

func (server *Server) recoverBlackboardV2ContinuationFiles(ctx context.Context) error {
	active, err := server.blackboardV2Continuity.ActiveSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, snapshot := range active {
		provider := runtimeprofile.Provider(snapshot.RuntimeProvider)
		if !runner.BlackboardV2SupportsProvider(provider) {
			continue
		}
		created, err := server.tasks.Get(snapshot.TaskID)
		if err != nil {
			return fmt.Errorf("recover Blackboard v2 Continuation Task: %w", err)
		}
		layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, snapshot.TaskID, provider)
		if err != nil {
			return fmt.Errorf("recover Blackboard v2 layout: %w", err)
		}
		// Restart recovery must rematerialize persisted Working Snapshot bytes
		// (last acknowledged revision), never overwrite a synchronized working
		// file with immutable Launch Pin bytes.
		if err := server.blackboardV2Continuity.MaterializeWorkingSnapshot(ctx, snapshot.ContinuationID); err != nil {
			return fmt.Errorf("recover Blackboard v2 Working Snapshot: %w", err)
		}
		header := blackboardv2.LaunchHeader{
			Runner: string(snapshot.Runner), ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
			Schema: snapshot.Schema, Revision: snapshot.Revision,
		}
		if err := runner.ProjectBlackboardV2Files(layout, provider, header, created.ScopeSnapshot); err != nil {
			return fmt.Errorf("recover Blackboard v2 context: %w", err)
		}
	}
	return nil
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
	plan, err := server.buildTaskLaunchPlanWithBinding(created, created.Goal, launchModelOverride, "", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return plan.Adapter, plan.RuntimeConfig, nil
}

func (server *Server) buildTaskAdapterForGoal(created task.Task, goal string, launchModelOverride string) (runtime.Adapter, map[string]any, error) {
	plan, err := server.buildTaskLaunchPlanWithBinding(created, goal, launchModelOverride, "", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return plan.Adapter, plan.RuntimeConfig, nil
}

func (server *Server) buildTaskLaunchPlan(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string) (taskLaunchPlan, error) {
	server.logTaskLaunchStage(created, "build_plan")
	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if server.blackboardV2Continuity != nil && runner.BlackboardV2SupportsProvider(profile.Provider) {
		return server.prepareBlackboardV2TaskLaunchPlan(created, goal, launchModelOverride, nativeResumeSessionID, profile)
	}
	return server.buildTaskLaunchPlanWithBinding(created, goal, launchModelOverride, nativeResumeSessionID, nil, nil)
}

func (server *Server) prepareBlackboardV2TaskLaunchPlan(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, profile runtimeprofile.Profile) (taskLaunchPlan, error) {
	layout, err := runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	skillBundles, err := server.skills.EnabledSkillBundles(profile.ID)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	var modelSnapshot *modelprovider.Snapshot
	if strings.TrimSpace(profile.Fields.ModelProviderID) != "" {
		resolved, err := modelprovider.Resolve(modelprovider.ResolveRequest{
			Profile: profile, Providers: server.modelProviders, Plugins: server.runtimePlugins,
			Credentials: server.creds, ProjectID: created.ProjectID, CheckEnv: true,
			LaunchModelOverride: launchModelOverride,
		})
		if err != nil {
			return taskLaunchPlan{}, err
		}
		if resolved.ModelProviderID != "" {
			modelSnapshot = &resolved
			profile = runner.BlackboardV2ProfileWithModelSnapshot(profile, resolved)
		}
	}
	materializedCredentials, err := runner.MaterializeLaunchCredentials(profile, runner.ProjectionRequest{
		ProjectID:     created.ProjectID,
		Credentials:   server.creds,
		ModelSnapshot: modelSnapshot,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	capturedRuntimeConfig := capturedTaskRuntimeConfig(created, profile, runtimeprofile.GeneratedConfig(profile), blackboardV2ModelSnapshotPreview(modelSnapshot), launchModelOverride)
	return taskLaunchPlan{
		CapturedRuntimeConfig:   capturedRuntimeConfig,
		MaterializedCredentials: materializedCredentials,
		LaunchModelOverride:     launchModelOverride,
		NativeResumeSessionID:   nativeResumeSessionID,
		ResolvedProfile:         profile,
		ModelSnapshot:           modelSnapshot,
		SkillBundles:            append([]skill.Bundle(nil), skillBundles...),
		LaunchGoal:              goal,
		BlackboardV2:            true,
		ValidatedLayout:         &layout,
	}, nil
}

func (server *Server) buildTaskLaunchPlanWithBinding(created task.Task, goal string, launchModelOverride string, nativeResumeSessionID string, binding *continuationLaunchBinding, captured *taskLaunchPlan) (taskLaunchPlan, error) {
	v2 := binding != nil && binding.V2Header != nil
	runtimeConfig := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}

	var profile runtimeprofile.Profile
	var skillBundles []skill.Bundle
	var capturedModelSnapshot *modelprovider.Snapshot
	var materializedCredentials map[string]string
	if captured != nil {
		profile = captured.ResolvedProfile
		skillBundles = append([]skill.Bundle(nil), captured.SkillBundles...)
		capturedModelSnapshot = captured.ModelSnapshot
		materializedCredentials = captured.MaterializedCredentials
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
		if binding.V2Header != nil {
			launchGoal = blackboardv2.RenderLaunchHeader(*binding.V2Header) + "\n\nTASK GOAL:\n" + goal
		}
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

	var layout runner.Layout
	var err error
	if v2 {
		if captured == nil || captured.ValidatedLayout == nil {
			return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 layout was not validated")
		}
		layout, err = runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	} else {
		layout, err = runner.PrepareTaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	}
	if err != nil {
		return taskLaunchPlan{}, err
	}

	authToken := server.authToken
	projectionProfile := profile
	if v2 {
		if profile.Provider == runtimeprofile.ProviderCodex {
			// Codex v2 stays networkless for Project Interface writes.
			projectionProfile = codexV2ProjectionProfile(profile)
			authToken = ""
		} else if runner.BlackboardV2UsesTrustedMCP(profile.Provider) {
			// Claude/Pi use the Continuation grant, never the operator token.
			authToken = ""
			if binding != nil {
				authToken = binding.InterfaceToken
			}
		} else {
			return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", profile.Provider)
		}
	}
	projectionRequest := runner.ProjectionRequest{
		ProjectID:               created.ProjectID,
		TaskID:                  created.ID,
		ScopeSnapshot:           created.ScopeSnapshot,
		Credentials:             server.creds,
		MaterializedCredentials: materializedCredentials,
		DaemonAddr:              server.listenAddr,
		AuthToken:               authToken,
		Sandbox:                 sandbox,
		RuntimePlugins:          server.runtimePlugins,
		RuntimeExtensions:       server.runtimeExtensions,
		ModelProviders:          server.modelProviders,
		ModelSnapshot:           capturedModelSnapshot,
		LaunchModelOverride:     launchModelOverride,
		SkillBundles:            skillBundles,
	}
	var projection runner.ConfigProjection
	if v2 {
		projection, err = runner.ProjectBlackboardV2RuntimeConfig(layout, projectionProfile, projectionRequest)
	} else {
		projection, err = runner.ProjectRuntimeConfig(layout, projectionProfile, projectionRequest)
	}
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if binding != nil && binding.V2Header != nil {
		if !runner.BlackboardV2SupportsProvider(profile.Provider) {
			return taskLaunchPlan{}, fmt.Errorf("Blackboard v2 launch projection is unsupported for provider %q", profile.Provider)
		}
		if err := runner.ProjectBlackboardV2Files(layout, profile.Provider, *binding.V2Header, created.ScopeSnapshot); err != nil {
			return taskLaunchPlan{}, err
		}
	}
	configPath := runner.LaunchConfigPath(layout, profile.Provider, projection.ConfigPath, sandbox)
	mcpConfigPath := runner.LaunchMCPConfigPath(layout, profile.Provider, sandbox, projection)
	if v2 && !sandbox {
		// Host argv must not embed TaskRoot absolute paths (they contain the Task ID).
		configPath = blackboardV2HostRelativePath(layout.Workdir, configPath)
		mcpConfigPath = blackboardV2HostRelativePath(layout.Workdir, mcpConfigPath)
	}
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
	sandboxImage := ""
	launchCtx := runner.TaskContext{Sandbox: sandbox}
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, sandbox, launchCtx, runner.ProjectionRequest{
		ProjectID:               created.ProjectID,
		TaskID:                  created.ID,
		ScopeSnapshot:           created.ScopeSnapshot,
		Credentials:             server.creds,
		MaterializedCredentials: materializedCredentials,
		DaemonAddr:              server.listenAddr,
		AuthToken:               authToken,
		Sandbox:                 sandbox,
		RuntimePlugins:          server.runtimePlugins,
		RuntimeExtensions:       server.runtimeExtensions,
		ModelProviders:          server.modelProviders,
		ModelSnapshot:           projection.ModelSnapshot,
		SkillBundles:            skillBundles,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if v2 {
		processEnv = runner.BlackboardV2ProcessEnv(processEnv, layout, sandbox)
	}
	if sandbox {
		sandboxNetwork = sandboxNetworkMode(created.RunControls)
		sandboxImage = strings.TrimSpace(profile.Fields.SandboxImage)
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
		var readOnlyTaskFiles, readOnlyTaskDirs []string
		if binding != nil {
			if v2 {
				readOnlyTaskDirs = []string{"workdir/.pentest"}
			} else {
				readOnlyTaskFiles = []string{"workdir/.pentest/blackboard.json", "workdir/.pentest/scope.json"}
			}
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
			ReadOnlyTaskDirs:  readOnlyTaskDirs,
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
	if v2 {
		runtimeConfig = map[string]any{}
	}
	capturedRuntimeConfig := capturedTaskRuntimeConfig(created, launchProfile, runtimeprofile.GeneratedConfig(launchProfile), projection.Config["model_provider_snapshot"], launchModelOverride)
	if captured != nil {
		capturedRuntimeConfig = captured.CapturedRuntimeConfig
	}

	var adapter runtime.Adapter
	if sandbox {
		sandboxConfig := runtime.DockerSandboxConfig{
			Name:         string(profile.Provider),
			ContainerCLI: commandProgram,
			Image:        sandboxImage,
			CreateArgs:   commandArgs,
			Log: func(event runtime.DockerSandboxLogEvent) {
				server.logDockerSandboxEvent(created, event)
			},
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
		Adapter:                 adapter,
		RuntimeConfig:           runtimeConfig,
		CapturedRuntimeConfig:   capturedRuntimeConfig,
		MaterializedCredentials: materializedCredentials,
		Metadata:                metadata,
		StopConfirmation:        stopConfirmation,
		LaunchModelOverride:     launchModelOverride,
		NativeResumeSessionID:   nativeResumeSessionID,
		ResolvedProfile:         launchProfile,
		ModelSnapshot:           projection.ModelSnapshot,
		SkillBundles:            append([]skill.Bundle(nil), skillBundles...),
		LaunchGoal:              launchGoal,
		BlackboardV2:            v2,
		ValidatedLayout:         &layout,
	}, nil
}

func blackboardV2ModelSnapshotPreview(snapshot *modelprovider.Snapshot) any {
	if snapshot == nil || snapshot.ModelProviderID == "" {
		return nil
	}
	return map[string]any{
		"model_provider_id": snapshot.ModelProviderID, "model_provider_name": snapshot.ModelProviderName,
		"endpoint_base_url": snapshot.EndpointBaseURL, "base_url": snapshot.BaseURL,
		"protocol": string(snapshot.Protocol), "model": snapshot.Model, "api_key_env": snapshot.APIKeyEnv,
		"api_key_source": snapshot.APIKeySource, "projection_target": snapshot.ProjectionTarget,
	}
}

// blackboardV2HostRelativePath rewrites task-local host paths to workdir-relative
// form so model-visible argv never embeds the Task ID from TaskRoot. Paths under
// runtime-home become "../runtime-home/..." which is intentional and ID-free.
func blackboardV2HostRelativePath(workdir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	relative, err := filepath.Rel(workdir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func codexV2ProjectionProfile(profile runtimeprofile.Profile) runtimeprofile.Profile {
	projected := profile
	projected.Fields.MCPServers = nil
	projected.Fields.Env = make(map[string]string, len(profile.Fields.Env)+1)
	for key, value := range profile.Fields.Env {
		projected.Fields.Env[key] = value
	}
	projected.Fields.Env["PENTEST_DISABLE_TRUSTED_MCP"] = "true"
	return projected
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
	_, providerSessionBound := server.providerSessions.get(found.ID)

	controls := task.RuntimeControls{
		ResumeAvailable:         !active,
		QueueSteerAvailable:     true,
		NativeSessionCaptured:   sessionCaptured,
		SameRuntimeProviderOnly: true,
		RuntimeProvider:         string(profile.Provider),
	}
	if events, eventsErr := server.tasks.Events(found.ID); eventsErr == nil {
		if providerSessionBound {
			controls.ProviderPermissions = providerPermissionRequestsForTask(events)
		}
		for index := len(events) - 1; index >= 0; index-- {
			phase, _ := events[index].Payload["phase"].(string)
			if phase == "started" {
				break
			}
			if phase != "provider_session_recovery_required" {
				continue
			}
			controls.RecoveryState, _ = events[index].Payload["recovery_state"].(string)
			controls.RecoveryReason, _ = events[index].Payload["reason"].(string)
			break
		}
	}
	if session, bound := server.providerSessions.get(found.ID); bound {
		_, outcome, _ := nativeSteerStateForTask(found.ID, server.tasks)
		if selectedMode, modeErr := nativeSteerMode(session.Capabilities()); modeErr == nil {
			controls.NativeSteerAvailable = active
			controls.NativeSteerMode = string(selectedMode)
			controls.NativeSteerState = outcome
			if outcome == "requested" || outcome == "acknowledged" || outcome == "settled" || outcome == "started" {
				controls.NativeSteerAvailable = false
			}
			controls.InterruptSteerAvailable = controls.NativeSteerAvailable
			if !controls.NativeSteerAvailable && active {
				controls.InterruptSteerReason = "native steer request is already in progress"
			}
		} else {
			controls.NativeSteerReason = modeErr.Error()
			controls.InterruptSteerReason = controls.NativeSteerReason
		}
		if events, eventsErr := server.tasks.Events(found.ID); eventsErr == nil {
			for index := len(events) - 1; index >= 0; index-- {
				if requestID, ok := events[index].Payload["request_id"].(string); ok && requestID != "" && events[index].Kind == task.EventKindConversation && events[index].Payload["delivery"] == "native_steer" {
					controls.NativeSteerRequestID = requestID
					break
				}
			}
		}
		if controls.NativeSteerState == "" && active {
			controls.NativeSteerState = "idle"
		}
	}
	if nativeResumeSupported {
		controls.NativeResumeAvailable = !active && sessionCaptured
		if !providerSessionBound {
			controls.InterruptSteerAvailable = active && sessionCaptured
		}
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

func providerPermissionRequestsForTask(events []task.Event) []task.ProviderPermissionRequest {
	requests := make(map[string]task.ProviderPermissionRequest)
	for _, event := range events {
		permissionID, _ := event.Payload["permission_request_id"].(string)
		if permissionID == "" {
			continue
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			request := task.ProviderPermissionRequest{PermissionRequestID: permissionID, CreatedAt: event.CreatedAt}
			request.RequestID, _ = event.Payload["request_id"].(string)
			request.SessionID, _ = event.Payload["session_id"].(string)
			request.ProviderTurnID, _ = event.Payload["provider_turn_id"].(string)
			request.Provider, _ = event.Payload["provider"].(string)
			requests[permissionID] = request
		case "provider_permission_response_applied":
			delete(requests, permissionID)
		}
	}
	result := make([]task.ProviderPermissionRequest, 0, len(requests))
	for _, request := range requests {
		result = append(result, request)
	}
	slices.SortFunc(result, func(left, right task.ProviderPermissionRequest) int {
		return left.CreatedAt.Compare(right.CreatedAt)
	})
	return result
}

func nativeSteerStateForTask(taskID string, tasks *task.Service) (runtime.ProviderSessionMode, string, string) {
	events, err := tasks.Events(taskID)
	if err != nil {
		return "", "", ""
	}
	var requestID string
	for _, event := range events {
		if event.Kind != task.EventKindConversation || event.Payload["delivery"] != "native_steer" {
			continue
		}
		requestID, _ = event.Payload["request_id"].(string)
	}
	if requestID == "" {
		return "", "", ""
	}
	mode, outcome, sessionID := nativeSteerState(events, requestID)
	return mode, outcome, sessionID
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
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)

	if found.Status == task.StatusRunning || found.Status == task.StatusPaused {
		if ok := server.harness.StopAndWait(taskID, 10*time.Second); !ok {
			writeError(response, http.StatusConflict, "runtime did not stop in time")
			return
		}
		if err := server.closeProviderSession(taskID); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
			writeError(response, http.StatusConflict, "provider session did not close")
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
	if err := server.closeProviderSession(taskID); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
		writeError(response, http.StatusConflict, "provider session did not close")
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
	if server.closing || server.activeControls[taskID] {
		return false
	}
	server.activeControls[taskID] = true
	return true
}

func (server *Server) acquireProviderTaskControl(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	if server.closing || server.activeControls[taskID] {
		return false
	}
	server.activeControls[taskID] = true
	server.providerControlWG.Add(1)
	return true
}

func (server *Server) releaseProviderTaskControl(taskID string) {
	server.releaseTaskControl(taskID)
	server.providerControlWG.Done()
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
	found, resumeGoal, plan, err := server.prepareResumeContinuation(found, "")
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
	var steeringEventIDs []string
	if server.canonicalStore == store.CanonicalStoreBlackboardV2 && server.isBlackboardV2Task(found) {
		resumedMessage, steeringEventIDs, err = server.blackboardV2ResumeContext(found)
		if err != nil {
			return task.Task{}, "", taskLaunchPlan{}, err
		}
	}
	plan, err := server.buildTaskLaunchPlan(found, resumedMessage, "", nativeResumeSessionID)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan.BlackboardV2SteeringEventIDs = steeringEventIDs
	return found, resumedMessage, plan, nil
}

// prepareResumeContinuation prefers a provider-native session when one is
// available. Otherwise it creates a fresh continuation from Task-owned Goal,
// interrupted Attempt checkpoints, unconsumed Harness Steering, and the new
// Working Snapshot. No summary or synthetic handoff packet is consulted.
func (server *Server) prepareResumeContinuation(found task.Task, resumedMessage string) (task.Task, string, taskLaunchPlan, error) {
	prepared, goal, plan, err := server.prepareNativeResumeContinuation(found, resumedMessage)
	if err == nil {
		return prepared, goal, plan, nil
	}
	if !errors.Is(err, errNativeSessionUnavailable) && !errors.Is(err, errNativeResumeUnavailable) {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	return server.prepareFreshResumeContinuation(found)
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

func (server *Server) prepareFreshResumeContinuation(found task.Task) (task.Task, string, taskLaunchPlan, error) {
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	found.RuntimeProfileID = effectiveProfile.ID

	resumeGoal := ""
	var steeringEventIDs []string
	if server.canonicalStore == store.CanonicalStoreBlackboardV2 && runner.BlackboardV2SupportsProvider(effectiveProfile.Provider) {
		resumeGoal, steeringEventIDs, err = server.blackboardV2ResumeContext(found)
	} else {
		resumeGoal, err = server.buildResumeGoal(found)
	}
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan, err := server.buildTaskLaunchPlan(found, resumeGoal, "", "")
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan.BlackboardV2SteeringEventIDs = steeringEventIDs
	return found, resumeGoal, plan, nil
}

func (server *Server) buildResumeGoal(found task.Task) (string, error) {
	events, err := server.tasks.Events(found.ID)
	if err != nil {
		return "", err
	}
	if server.canonicalStore == store.CanonicalStoreBlackboardV2 {
		if server.isBlackboardV2Task(found) {
			goal, _, err := server.blackboardV2ResumeContext(found)
			return goal, err
		}
		directives := unconsumedHarnessSteering(events)
		return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{TaskGoal: found.Goal, Steering: directives}), nil
	}

	return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: found.Goal, Steering: unconsumedHarnessSteering(events),
	}), nil
}

func (server *Server) isCodexTask(found task.Task) bool {
	profile, err := server.profiles.Get(found.RuntimeProfileID)
	return err == nil && profile.Provider == runtimeprofile.ProviderCodex
}

func (server *Server) isBlackboardV2Task(found task.Task) bool {
	profile, err := server.profiles.Get(found.RuntimeProfileID)
	return err == nil && runner.BlackboardV2SupportsProvider(profile.Provider)
}

func (server *Server) blackboardV2ResumeContext(found task.Task) (string, []string, error) {
	steering, err := server.tasks.UnconsumedHarnessSteering(context.Background(), found.ID)
	if err != nil {
		return "", nil, err
	}
	var checkpoints []blackboardv2.InterruptedAttemptCheckpoint
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return "", nil, err
	}
	if latest != nil {
		checkpoints, err = server.blackboardV2.InterruptedAttemptCheckpoints(context.Background(), found.ProjectID, latest.ID)
		if err != nil {
			return "", nil, err
		}
	}
	directives := make([]string, len(steering))
	eventIDs := make([]string, len(steering))
	for index, directive := range steering {
		directives[index] = directive.Directive
		eventIDs[index] = directive.EventID
	}
	return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: found.Goal, Steering: directives, InterruptedAttempts: checkpoints,
	}), eventIDs, nil
}

func unconsumedHarnessSteering(events []task.Event) []string {
	consumed := make(map[string]bool)
	for _, event := range events {
		if event.Kind != task.EventKindSteering || event.Payload["phase"] != "steering_applied" {
			continue
		}
		if requestedID, ok := event.Payload["requested_event_id"].(string); ok && requestedID != "" {
			consumed[requestedID] = true
		}
	}
	directives := make([]string, 0)
	for _, event := range events {
		if event.Kind != task.EventKindSteering || event.Payload["phase"] != "steering_requested" || consumed[event.ID] {
			continue
		}
		if directive, ok := event.Payload["directive"].(string); ok && strings.TrimSpace(directive) != "" {
			directives = append(directives, directive)
		}
	}
	return directives
}

func (server *Server) writeResumePreparationError(response http.ResponseWriter, err error) {
	var boardErr *blackboardv2.Error
	if errors.As(err, &boardErr) && boardErr.Code == "reconciliation_incomplete" {
		writeError(response, http.StatusConflict, boardErr.Message)
		return
	}
	switch {
	case errors.Is(err, runtimeprofile.ErrNotFound):
		writeError(response, http.StatusBadRequest, "runtime profile not found")
	case errors.Is(err, errNativeResumeUnavailable):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNativeSessionUnavailable):
		writeError(response, http.StatusConflict, err.Error())
	case errors.Is(err, task.ErrContinuationReconciliationIncomplete), errors.Is(err, task.ErrSteeringSelectionConflict):
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
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)
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
	if session, ok := server.providerSessions.get(taskID); ok {
		server.handleProviderSessionSteer(response, request, found, session)
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
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)

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
		if _, _, _, err := server.prepareNativeResumeRequest(found, input.Directive); err != nil {
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

		resumedTask, resumeGoal, plan, err := server.prepareNativeResumeContinuation(found, input.Directive)
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
		if !plan.BlackboardV2 {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindSteering, task.EventPayload{
				"phase":              "steering_applied",
				"directive":          input.Directive,
				"requested_event_id": event.ID,
			})
		}
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

func (server *Server) handleProviderSessionSteer(response http.ResponseWriter, request *http.Request, found task.Task, session runtime.ProviderSession) {
	var input nativeSteerRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		input.RequestID = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if input.RequestID == "" {
		input.RequestID = newNativeSteerRequestID()
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		input.Message = strings.TrimSpace(input.Directive)
	}
	if input.Message == "" {
		writeError(response, http.StatusBadRequest, "steer message is required")
		return
	}

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	for _, event := range events {
		if event.Kind != task.EventKindConversation || event.Payload["request_id"] != input.RequestID || event.Payload["delivery"] != "native_steer" {
			continue
		}
		if prior, _ := event.Payload["text"].(string); prior != input.Message {
			writeError(response, http.StatusConflict, "steer request id already belongs to a different message")
			return
		}
		mode, outcome, sessionID := nativeSteerState(events, input.RequestID)
		if outcome == "" {
			outcome = "pending"
		}
		if sessionID == "" {
			sessionID = session.SessionID()
		}
		writeJSON(response, http.StatusAccepted, struct {
			RequestID string                      `json:"request_id"`
			SessionID string                      `json:"session_id"`
			Mode      runtime.ProviderSessionMode `json:"mode"`
			Outcome   string                      `json:"outcome"`
		}{RequestID: input.RequestID, SessionID: sessionID, Mode: mode, Outcome: outcome})
		return
	}

	if found.Status != task.StatusRunning && found.Status != task.StatusPaused {
		writeError(response, http.StatusConflict, "native steer requires an active Task")
		return
	}
	mode, err := nativeSteerMode(session.Capabilities())
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	operation := nativeSteerOperation(session, mode)
	if operation == nil {
		writeError(response, http.StatusConflict, "provider session does not support native steer")
		return
	}
	if !server.acquireProviderTaskControl(found.ID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}

	active, err := server.tasks.ActiveContinuation(found.ID)
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}
	conversationPayload := task.EventPayload{
		"role": "user", "text": input.Message, "request_id": input.RequestID,
		"delivery": "native_steer", "outcome": "pending", "mode": string(mode),
		"session_id": session.SessionID(),
	}
	var conversation task.Event
	if active != nil {
		conversation, err = server.tasks.AppendContinuationEvent(found.ID, active.ID, task.EventKindConversation, conversationPayload)
	} else {
		conversation, err = server.tasks.AppendEvent(found.ID, task.EventKindConversation, conversationPayload)
	}
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}

	continuationID := ""
	if active != nil {
		continuationID = active.ID
	}
	var continuationMu sync.Mutex
	var continuationTransitionErr error
	emit := func(kind task.EventKind, payload task.EventPayload) {
		payload["conversation_event_id"] = conversation.ID
		continuationMu.Lock()
		currentContinuationID := continuationID
		if currentContinuationID != "" {
			_, _ = server.tasks.AppendContinuationEvent(found.ID, currentContinuationID, kind, payload)
		}
		if mode == runtime.ProviderSessionModeInterruptThenReplace && kind == task.EventKindSteering && payload["outcome"] == "settled" && currentContinuationID != "" {
			if transitionErr := server.advanceNativeSteerContinuation(currentContinuationID, session, &continuationID); transitionErr != nil {
				continuationTransitionErr = transitionErr
				failure := task.EventPayload{
					"request_id": payload["request_id"], "session_id": payload["session_id"],
					"mode": string(mode), "outcome": "failed", "phase": "replacement_continuation_failed",
					"error_code": "continuation_transition_failed",
				}
				_, _ = server.tasks.AppendContinuationEvent(found.ID, currentContinuationID, task.EventKindSteering, failure)
			}
		}
		continuationMu.Unlock()
		if currentContinuationID == "" {
			_, _ = server.tasks.AppendEvent(found.ID, kind, payload)
		}
	}
	go func() {
		defer server.releaseProviderTaskControl(found.ID)
		ctx, cancel := context.WithTimeout(server.providerControlCtx, 30*time.Second)
		defer cancel()
		result, operationErr := operation(ctx, runtime.ProviderSessionRequest{RequestID: input.RequestID, Message: input.Message}, emit)
		if operationErr != nil {
			errorCode := "provider_rejected"
			switch {
			case errors.Is(operationErr, context.DeadlineExceeded):
				errorCode = "timeout"
			case errors.Is(operationErr, context.Canceled):
				errorCode = "server_closing"
			case errors.Is(operationErr, runtime.ErrProviderSessionClosed):
				errorCode = "session_closed"
			case errors.Is(operationErr, runtime.ErrProviderSessionControlConflict):
				errorCode = "control_conflict"
			}
			emit(task.EventKindSteering, task.EventPayload{
				"request_id": input.RequestID, "session_id": session.SessionID(), "mode": string(mode),
				"outcome": "failed", "phase": "steering_failed", "error_code": errorCode,
			})
			return
		}
		continuationMu.Lock()
		transitionErr := continuationTransitionErr
		continuationMu.Unlock()
		if transitionErr != nil {
			_ = server.closeProviderSession(found.ID)
			if current, _ := server.tasks.ActiveContinuation(found.ID); current != nil {
				_, _ = server.tasks.UpdateContinuationStatus(current.ID, task.StatusFailed)
			}
			_, _ = server.tasks.UpdateStatus(found.ID, task.StatusFailed)
			return
		}
		payload := result.Payload()
		payload["outcome"] = "applied"
		payload["phase"] = "steering_applied"
		emit(task.EventKindSteering, payload)
	}()

	writeJSON(response, http.StatusAccepted, struct {
		RequestID string                      `json:"request_id"`
		SessionID string                      `json:"session_id"`
		Mode      runtime.ProviderSessionMode `json:"mode"`
		Outcome   string                      `json:"outcome"`
	}{RequestID: input.RequestID, SessionID: session.SessionID(), Mode: mode, Outcome: "accepted"})
}

type providerPermissionResponseRequest struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
}

// handleProviderPermissionResponse answers one provider permission request on
// the same Task-owned session. It is authenticated by ServeHTTP's daemon
// middleware and never exposes provider wire payloads.
func (server *Server) handleProviderPermissionResponse(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	session, bound := server.providerSessions.get(found.ID)
	if !bound || session == nil {
		writeError(response, http.StatusConflict, "provider session is unavailable")
		return
	}
	permissionID := strings.TrimSpace(request.PathValue("permission_id"))
	if permissionID == "" {
		writeError(response, http.StatusBadRequest, "permission request id is required")
		return
	}
	var input providerPermissionResponseRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
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

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	pending, priorOutcome, priorDecision := providerPermissionStatus(events, permissionID, input.RequestID)
	if priorDecision != "" && priorDecision != input.Decision {
		writeError(response, http.StatusConflict, "permission request id already belongs to a different decision")
		return
	}
	if priorOutcome != "" {
		writeJSON(response, http.StatusAccepted, map[string]any{
			"request_id": input.RequestID, "permission_request_id": permissionID,
			"session_id": session.SessionID(), "decision": input.Decision, "outcome": priorOutcome,
		})
		return
	}
	if !pending {
		writeError(response, http.StatusNotFound, "provider permission request is no longer pending")
		return
	}
	if found.Status != task.StatusRunning && found.Status != task.StatusPaused {
		writeError(response, http.StatusConflict, "provider permission requires an active Task")
		return
	}
	if !session.Capabilities().PermissionResponse {
		writeError(response, http.StatusConflict, "provider session does not support permission responses")
		return
	}
	if !server.acquireProviderTaskControl(found.ID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	active, err := server.tasks.ActiveContinuation(found.ID)
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}
	continuationID := ""
	if active != nil {
		continuationID = active.ID
	}
	requestedPayload := task.EventPayload{
		"phase": "provider_permission_response_requested", "mode": string(runtime.ProviderSessionModePermissionResponse),
		"outcome": "pending", "request_id": input.RequestID, "permission_request_id": permissionID,
		"permission_decision": input.Decision, "session_id": session.SessionID(),
	}
	if continuationID != "" {
		_, err = server.tasks.AppendContinuationEvent(found.ID, continuationID, task.EventKindLifecycle, requestedPayload)
	} else {
		_, err = server.tasks.AppendEvent(found.ID, task.EventKindLifecycle, requestedPayload)
	}
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}
	emit := func(kind task.EventKind, payload task.EventPayload) {
		redacted := task.EventPayload{}
		for _, key := range []string{"provider", "request_id", "session_id", "provider_turn_id", "mode", "outcome", "permission_request_id", "error_code"} {
			if value, ok := payload[key]; ok {
				redacted[key] = value
			}
		}
		if redacted["request_id"] == nil {
			redacted["request_id"] = input.RequestID
		}
		redacted["permission_request_id"] = permissionID
		if redacted["mode"] == nil {
			redacted["mode"] = string(runtime.ProviderSessionModePermissionResponse)
		}
		switch redacted["outcome"] {
		case "requested":
			redacted["phase"] = "provider_permission_response_requested"
		case "acknowledged":
			redacted["phase"] = "provider_permission_response_acknowledged"
		case "failed":
			redacted["phase"] = "provider_permission_response_failed"
		}
		if continuationID != "" {
			_, _ = server.tasks.AppendContinuationEvent(found.ID, continuationID, kind, redacted)
		} else {
			_, _ = server.tasks.AppendEvent(found.ID, kind, redacted)
		}
	}
	go func() {
		defer server.releaseProviderTaskControl(found.ID)
		ctx, cancel := context.WithTimeout(server.providerControlCtx, 30*time.Second)
		defer cancel()
		result, operationErr := session.RespondPermission(ctx, runtime.ProviderSessionRequest{
			RequestID: input.RequestID, PermissionRequestID: permissionID, PermissionDecision: input.Decision,
		}, emit)
		if operationErr != nil {
			errorCode := "provider_rejected"
			switch {
			case errors.Is(operationErr, context.DeadlineExceeded):
				errorCode = "timeout"
			case errors.Is(operationErr, context.Canceled):
				errorCode = "server_closing"
			case errors.Is(operationErr, runtime.ErrProviderSessionClosed):
				errorCode = "session_closed"
			case errors.Is(operationErr, runtime.ErrProviderSessionControlConflict):
				errorCode = "control_conflict"
			}
			emit(task.EventKindLifecycle, task.EventPayload{"outcome": "failed", "phase": "provider_permission_response_failed", "error_code": errorCode})
			return
		}
		payload := result.Payload()
		payload["phase"] = "provider_permission_response_applied"
		payload["outcome"] = "applied"
		payload["permission_request_id"] = permissionID
		if continuationID != "" {
			_, _ = server.tasks.AppendContinuationEvent(found.ID, continuationID, task.EventKindLifecycle, payload)
		} else {
			_, _ = server.tasks.AppendEvent(found.ID, task.EventKindLifecycle, payload)
		}
	}()
	writeJSON(response, http.StatusAccepted, map[string]any{
		"request_id": input.RequestID, "permission_request_id": permissionID,
		"session_id": session.SessionID(), "decision": input.Decision, "outcome": "accepted",
	})
}

func normalizePermissionDecision(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "allow", "approve", "approved", "yes":
		return "allow"
	case "deny", "reject", "rejected", "no":
		return "deny"
	default:
		return ""
	}
}

func providerPermissionStatus(events []task.Event, permissionID, requestID string) (pending bool, outcome, decision string) {
	for _, event := range events {
		if event.Payload["permission_request_id"] != permissionID {
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

// advanceNativeSteerContinuation performs the old-turn settlement boundary
// while the provider control operation is still serialized. The adapter emits
// settled before sending the replacement turn, so the next provider event is
// guaranteed to land on the fresh Continuation.
func (server *Server) advanceNativeSteerContinuation(currentID string, session runtime.ProviderSession, continuationID *string) error {
	old, err := server.tasks.Continuation(currentID)
	if err != nil {
		return fmt.Errorf("load old continuation: %w", err)
	}
	next, err := server.tasks.CreateReplacementContinuation(old)
	if err != nil {
		return fmt.Errorf("create replacement continuation: %w", err)
	}
	if binder, ok := session.(runtime.ProviderSessionContinuationBinder); ok {
		if err := binder.BindContinuation(next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("bind provider continuation: %w", err)
		}
	}
	if _, err := server.tasks.UpdateContinuationStatus(next.ID, task.StatusRunning); err != nil {
		_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
		return fmt.Errorf("start replacement continuation: %w", err)
	}
	if server.harness.IsActive(old.TaskID) {
		if err := server.harness.RebindContinuation(old.TaskID, next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("rebind runtime continuation: %w", err)
		}
	}
	if _, err := server.tasks.UpdateContinuationStatus(old.ID, task.StatusCompleted); err != nil {
		_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
		return fmt.Errorf("settle old continuation: %w", err)
	}
	*continuationID = next.ID
	return nil
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
	var layout runner.Layout
	var err error
	if server.blackboardV2Continuity != nil && runner.BlackboardV2SupportsProvider(provider) {
		layout, err = runner.PrepareBlackboardV2TaskLayout(server.runtimeRoot, taskID, provider)
	} else {
		layout, err = runner.PrepareTaskLayout(server.runtimeRoot, taskID, provider)
	}
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
		Mode    string         `json:"mode"`
		Handoff handoffPayload `json:"handoff"`
	}{
		Mode: "mechanical_handoff",
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
	case errors.Is(err, task.ErrMissingGoal), errors.Is(err, task.ErrUnsupportedRunner):
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
	if errors.Is(err, task.ErrActiveContinuation) || errors.Is(err, task.ErrContinuationReconciliationIncomplete) || errors.Is(err, task.ErrSteeringSelectionConflict) {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
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
	// Skill/model-provider adapter failures can surface during v2 Continuation
	// Precommit after plan capture; map them like resume-prepare errors.
	writeTaskAdapterError(response, err)
}
