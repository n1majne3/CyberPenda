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
	"pentest/internal/approval"
	"pentest/internal/blackboard"

	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/project"
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
		YOLO             bool              `json:"yolo"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.RunControls.Extras == nil && input.Extras != nil {
		input.RunControls.Extras = input.Extras
	}
	if input.YOLO {
		input.RunControls.YOLO = true
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
		YOLO:                input.RunControls.YOLO,
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
		YOLO:          input.RunControls.YOLO,
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

	if _, err := server.tasks.RecordRuntimeConfig(created.ID, created.RuntimeProfileID, plan.RuntimeConfig); err != nil {
		writeError(response, http.StatusInternalServerError, "record task runtime configuration")
		return
	}

	server.recordLoopbackRewriteEvent(created)

	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		writeError(response, http.StatusInternalServerError, "create task continuation")
		return
	}

	if _, err := server.approvals.RecordAudit(approval.AuditEntry{
		ProjectID: projectID,
		TaskID:    created.ID,
		Kind:      "task_created",
		Summary:   "task launched: " + created.Goal,
		Payload: map[string]any{
			"task_id":            created.ID,
			"runner":             created.Runner,
			"runtime_profile_id": created.RuntimeProfileID,
			"yolo":               created.RunControls.YOLO,
			"host_activated":     created.RunControls.HostActivated,
		},
	}); err != nil {
		writeError(response, http.StatusInternalServerError, "record task audit")
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
	Adapter          runtime.Adapter
	RuntimeConfig    map[string]any
	Metadata         func() (runtime.NativeSessionMetadata, error)
	StopConfirmation runtime.StopConfirmation
}

func (server *Server) launchTaskInBackground(created task.Task, plan taskLaunchPlan, goal string) error {
	continuation, err := server.tasks.CreateContinuation(created.ID, created.RuntimeProfileID, plan.Adapter.Name(), created.Runner)
	if err != nil {
		return err
	}
	server.logTask(created, "launched", "")
	go func() {
		err := server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:           created.ID,
			Goal:             goal,
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
	runtimeConfig := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}

	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		runtimeConfig["provider"] = string(runtimeprofile.ProviderFake)
		runtimeConfig["generated_config"] = runtimeprofile.GeneratedConfig(profile)
		return taskLaunchPlan{Adapter: runtime.NewFakeAdapter(), RuntimeConfig: runtimeConfig}, nil
	}
	skillBundles, err := server.skills.EnabledSkillBundles(profile.ID)
	if err != nil {
		return taskLaunchPlan{}, err
	}

	layout, err := runner.PrepareTaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	if err != nil {
		return taskLaunchPlan{}, err
	}

	sandbox := created.Runner == task.RunnerSandbox
	goal = runner.RewriteLoopbackTargets(goal, sandbox)
	mcpURL := runner.MCPEndpointURL(server.listenAddr, sandbox)
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:           created.ProjectID,
		TaskID:              created.ID,
		ScopeSnapshot:       created.ScopeSnapshot,
		Credentials:         server.creds,
		DaemonAddr:          server.listenAddr,
		Sandbox:             sandbox,
		RuntimePlugins:      server.runtimePlugins,
		RuntimeExtensions:   server.runtimeExtensions,
		ModelProviders:      server.modelProviders,
		LaunchModelOverride: launchModelOverride,
		SkillBundles:        skillBundles,
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
		Goal:          goal,
		ConfigPath:    configPath,
		MCPConfigPath: mcpConfigPath,
		YOLO:          created.RunControls.YOLO,
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
			ResumedMessage:  goal,
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
	launchCtx := runner.TaskContext{
		ProjectID: created.ProjectID,
		TaskID:    created.ID,
		MCPURL:    mcpURL,
		Sandbox:   sandbox,
	}
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, sandbox, launchCtx, runner.ProjectionRequest{
		ProjectID:         created.ProjectID,
		TaskID:            created.ID,
		ScopeSnapshot:     created.ScopeSnapshot,
		Credentials:       server.creds,
		DaemonAddr:        server.listenAddr,
		Sandbox:           sandbox,
		RuntimePlugins:    server.runtimePlugins,
		RuntimeExtensions: server.runtimeExtensions,
		ModelProviders:    server.modelProviders,
		ModelSnapshot:     projection.ModelSnapshot,
		SkillBundles:      skillBundles,
	})
	if err != nil {
		return taskLaunchPlan{}, err
	}
	if sandbox {
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
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout:          layout,
			Provider:        profile.Provider,
			Image:           sandboxImage,
			ContainerCLI:    server.containerCLI,
			ContainerIDFile: containerIDFile,
			RuntimeCommand:  sandboxRuntime,
			ProcessEnv:      processEnv,
			NetworkMode:     sandboxNetworkMode(created.RunControls),
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

	var adapter runtime.Adapter
	if sandbox {
		adapter = runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
			Name:         string(profile.Provider),
			ContainerCLI: commandProgram,
			CreateArgs:   commandArgs,
		})
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
		sessionDir := filepath.Join(layout.ProviderHome, "agent", "sessions", "--task-workdir--")
		adapter = runtime.NewPiSessionTailAdapter(adapter, sessionDir)
	}

	var metadata func() (runtime.NativeSessionMetadata, error)
	if sandbox || profile.Provider == runtimeprofile.ProviderCodex {
		metadata = func() (runtime.NativeSessionMetadata, error) {
			var collected runtime.NativeSessionMetadata
			if containerIDFile != "" {
				containerID, err := runtime.ReadContainerIDFile(containerIDFile)
				if err != nil && !os.IsNotExist(err) {
					return runtime.NativeSessionMetadata{}, err
				}
				collected.ContainerID = containerID
			}
			if profile.Provider == runtimeprofile.ProviderCodex {
				session, err := runtime.DiscoverCodexSession(layout.ProviderHome)
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
		Adapter:          adapter,
		RuntimeConfig:    runtimeConfig,
		Metadata:         metadata,
		StopConfirmation: stopConfirmation,
	}, nil
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
	controls, err := server.runtimeControlsForTask(found, latest)
	if err != nil {
		return task.Task{}, err
	}
	found.RuntimeControls = controls
	found.ActiveContinuation = active
	found.LatestContinuation = latest
	return found, nil
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
		controls.InterruptSteerAvailable = active
	} else {
		controls.NativeResumeReason = fmt.Sprintf("native resume unsupported for provider %s", profile.Provider)
		controls.InterruptSteerReason = controls.NativeResumeReason
	}
	if nativeResumeSupported && !sessionCaptured {
		controls.NativeResumeReason = "native session unavailable"
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
	if _, err := server.tasks.RecordRuntimeConfig(found.ID, found.RuntimeProfileID, plan.RuntimeConfig); err != nil {
		writeError(response, http.StatusInternalServerError, "record task runtime configuration")
		return
	}

	if err := server.launchTaskInBackground(found, plan, resumeGoal); err != nil {
		writeError(response, http.StatusInternalServerError, "create task continuation")
		return
	}

	if _, err := server.approvals.RecordAudit(approval.AuditEntry{
		ProjectID: projectID,
		TaskID:    found.ID,
		Kind:      "task_resumed",
		Summary:   "task resumed with native session",
		Payload: map[string]any{
			"task_id": found.ID,
			"runner":  found.Runner,
		},
	}); err != nil {
		writeError(response, http.StatusInternalServerError, "record task audit")
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
	if _, err := server.tasks.RecordRuntimeConfig(found.ID, found.RuntimeProfileID, plan.RuntimeConfig); err != nil {
		writeError(response, http.StatusInternalServerError, "record task runtime configuration")
		return
	}
	if err := server.launchTaskInBackground(found, plan, resumeGoal); err != nil {
		writeError(response, http.StatusInternalServerError, "create task continuation")
		return
	}
	if _, err := server.approvals.RecordAudit(approval.AuditEntry{
		ProjectID: projectID,
		TaskID:    found.ID,
		Kind:      "task_resumed",
		Summary:   "task resumed with mechanical handoff",
		Payload: map[string]any{
			"task_id": found.ID,
			"runner":  found.Runner,
		},
	}); err != nil {
		writeError(response, http.StatusInternalServerError, "record task audit")
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
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	found.RuntimeProfileID = effectiveProfile.ID
	nativeResumeSessionID, err := server.discoverNativeResumeSession(found)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan, err := server.buildTaskLaunchPlan(found, resumedMessage, "", nativeResumeSessionID)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	return found, resumedMessage, plan, nil
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
	factIndex, err := server.facts.FactIndex(found.ProjectID, blackboard.FactIndexOptions{})
	if err != nil {
		return "", err
	}
	factLines := make([]string, 0, len(factIndex))
	progressFacts := make([]string, 0)
	for _, entry := range factIndex {
		factLines = append(factLines, entry.FactKey+": "+entry.Summary)
		if !strings.HasPrefix(entry.FactKey, "progress:") {
			continue
		}
		fact, err := server.facts.GetFact(found.ProjectID, entry.FactKey)
		if err != nil || strings.TrimSpace(fact.Body) == "" {
			continue
		}
		progressFacts = append(progressFacts, entry.FactKey+":\n"+fact.Body)
	}

	findings, err := server.facts.ListFindings(found.ProjectID)
	if err != nil {
		return "", err
	}
	findingLines := make([]string, 0, len(findings))
	start := 0
	if len(findings) > adapters.MaxResumeFindings {
		start = len(findings) - adapters.MaxResumeFindings
	}
	for _, finding := range findings[start:] {
		findingLines = append(findingLines, finding.FindingKey+": "+finding.Title+" ("+string(finding.Status)+")")
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

		refreshed, err := server.tasks.Get(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		resumedTask, resumeGoal, plan, err := server.prepareNativeResumeContinuation(refreshed, input.Directive)
		if err != nil {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "resume_failed",
				"error": err.Error(),
			})
			server.writeResumePreparationError(response, err)
			return
		}
		if _, err := server.tasks.RecordRuntimeConfig(taskID, resumedTask.RuntimeProfileID, plan.RuntimeConfig); err != nil {
			writeError(response, http.StatusInternalServerError, "record task runtime configuration")
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": "resuming_native",
		})
		if err := server.launchTaskInBackground(resumedTask, plan, resumeGoal); err != nil {
			writeError(response, http.StatusInternalServerError, "create task continuation")
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
	layout, err := runner.PrepareTaskLayout(server.runtimeRoot, found.ID, profile.Provider)
	if err != nil {
		return "", err
	}
	metadata, err := runtime.DiscoverCodexSession(layout.ProviderHome)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(metadata.NativeSessionID) == "" {
		return "", errNativeSessionUnavailable
	}
	return metadata.NativeSessionID, nil
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
		Summary     string `json:"summary"`
		SubmittedBy string `json:"submitted_by"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
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
