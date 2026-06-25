package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"pentest/internal/adapters"
	"pentest/internal/approval"
	"pentest/internal/blackboard"

	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/transcript"
)

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

	adapter, runtimeConfig, err := server.buildTaskAdapter(created, launchModelOverride)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "prepare runtime adapter")
		return
	}

	if _, err := server.tasks.RecordRuntimeConfig(created.ID, created.RuntimeProfileID, runtimeConfig); err != nil {
		writeError(response, http.StatusInternalServerError, "record task runtime configuration")
		return
	}

	server.recordLoopbackRewriteEvent(created)

	server.launchTaskInBackground(created, adapter)

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

	launched, err := server.tasks.Get(created.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, launched)
}

func (server *Server) launchTaskInBackground(created task.Task, adapter runtime.Adapter) {
	server.logTask(created, "launched", "")
	go func() {
		err := server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:  created.ID,
			Goal:    created.Goal,
			Adapter: adapter,
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
	return server.buildTaskAdapterForGoal(created, created.Goal, launchModelOverride)
}

func (server *Server) buildTaskAdapterForGoal(created task.Task, goal string, launchModelOverride string) (runtime.Adapter, map[string]any, error) {
	runtimeConfig := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}

	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		return nil, nil, err
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		runtimeConfig["provider"] = string(runtimeprofile.ProviderFake)
		runtimeConfig["generated_config"] = runtimeprofile.GeneratedConfig(profile)
		return runtime.NewFakeAdapter(), runtimeConfig, nil
	}
	skillBundles, err := server.skills.EnabledSkillBundles(profile.ID)
	if err != nil {
		return nil, nil, err
	}

	layout, err := runner.PrepareTaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	if err != nil {
		return nil, nil, err
	}

	sandbox := created.Runner == task.RunnerSandbox
	goal = runner.RewriteLoopbackTargets(goal, sandbox)
	mcpURL := runner.MCPEndpointURL(server.listenAddr, sandbox)
	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		ProjectID:           created.ProjectID,
		TaskID:              created.ID,
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
		return nil, nil, err
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
	})
	if err != nil {
		return nil, nil, err
	}

	runtimeCommand := append([]string{}, providerCommand...)
	commandProgram := runtimeCommand[0]
	commandArgs := runtimeCommand[1:]
	workdir := layout.Workdir
	launchCtx := runner.TaskContext{
		ProjectID: created.ProjectID,
		TaskID:    created.ID,
		MCPURL:    mcpURL,
		Sandbox:   sandbox,
	}
	processEnv, err := runner.LaunchProcessEnvWithCredentials(layout, launchProfile, sandbox, launchCtx, runner.ProjectionRequest{
		ProjectID:         created.ProjectID,
		TaskID:            created.ID,
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
		return nil, nil, err
	}
	if sandbox {
		sandboxImage := strings.TrimSpace(profile.Fields.SandboxImage)
		if sandboxImage == "" {
			sandboxImage = server.sandboxImage
		}
		sandboxRuntime := runtimeCommand
		if profile.Provider == runtimeprofile.ProviderPi {
			wrapped, err := runner.WrapSandboxPiCommand(runtimeCommand, launchProfile.Fields.Env)
			if err != nil {
				return nil, nil, err
			}
			sandboxRuntime = wrapped
		}
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout:         layout,
			Provider:       profile.Provider,
			Image:          sandboxImage,
			ContainerCLI:   server.containerCLI,
			RuntimeCommand: sandboxRuntime,
			ProcessEnv:     processEnv,
		})
		if err != nil {
			return nil, nil, err
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
	runtimeConfig["launch_command"] = adapters.Redact(map[string]any{
		"program": commandProgram,
		"args":    commandArgs,
	})

	adapter := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name:    string(profile.Provider),
		Program: commandProgram,
		Args:    commandArgs,
		Workdir: workdir,
		Env:     processEnv,
	})

	// Pi writes its real-time progress to a session jsonl file instead of
	// stdout, so a sandboxed Pi task's timeline is empty until it exits. Wrap
	// the adapter with a session-file tailer that re-emits appended lines as
	// runtime_output events the transcript parser already understands.
	if sandbox && profile.Provider == runtimeprofile.ProviderPi {
		sessionDir := filepath.Join(layout.ProviderHome, "agent", "sessions", "--task-workdir--")
		adapter = runtime.NewPiSessionTailAdapter(adapter, sessionDir)
	}

	return adapter, runtimeConfig, nil
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
	writeJSON(response, http.StatusOK, found)
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

	server.harness.Stop(taskID)
	stopped, err := server.tasks.UpdateStatus(taskID, task.StatusStopped)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, stopped)
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

	factIndex, err := server.facts.FactIndex(projectID, blackboard.FactIndexOptions{})
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load fact index")
		return
	}
	factLines := make([]string, 0, len(factIndex))
	for _, entry := range factIndex {
		factLines = append(factLines, entry.FactKey+": "+entry.Summary)
	}

	taskSummary := ""
	summaries, err := server.tasks.SummaryVersions(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if len(summaries) > 0 {
		taskSummary = summaries[len(summaries)-1].Summary
	}

	steeringDirective := ""
	events, err := server.tasks.Events(taskID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
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

	resumeGoal := adapters.BuildResumePrompt(adapters.ResumeRequest{
		Goal:              found.Goal,
		TaskSummary:       taskSummary,
		FactIndex:         factLines,
		SteeringDirective: steeringDirective,
	})

	adapter, runtimeConfig, err := server.buildTaskAdapterForGoal(found, resumeGoal, "")
	if err != nil {
		writeError(response, http.StatusInternalServerError, "prepare runtime adapter")
		return
	}
	if _, err := server.tasks.RecordRuntimeConfig(found.ID, found.RuntimeProfileID, runtimeConfig); err != nil {
		writeError(response, http.StatusInternalServerError, "record task runtime configuration")
		return
	}

	server.launchTaskInBackground(found, adapter)

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

	updated, err := server.tasks.Get(found.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, updated)
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
		Directive        string         `json:"directive"`
		RuntimeProfileID string         `json:"runtime_profile_id"`
		SubmittedBy      string         `json:"submitted_by"`
		Config           map[string]any `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.Directive == "" {
		writeError(response, http.StatusBadRequest, "steering directive is required")
		return
	}

	payload := task.EventPayload{
		"directive": input.Directive,
	}
	if input.SubmittedBy != "" {
		payload["submitted_by"] = input.SubmittedBy
	}
	if input.RuntimeProfileID != "" {
		payload["runtime_profile_id"] = input.RuntimeProfileID
	}

	event, err := server.tasks.AppendEvent(taskID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return
	}

	var configVersion *task.RuntimeConfigVersion
	if input.RuntimeProfileID != "" {
		config := input.Config
		if config == nil {
			config = map[string]any{}
		}
		config["runtime_profile_id"] = input.RuntimeProfileID
		config["runner"] = found.Runner
		config["steering_event_id"] = event.ID
		recorded, err := server.tasks.RecordRuntimeConfig(taskID, input.RuntimeProfileID, config)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		configVersion = &recorded
	}

	writeJSON(response, http.StatusOK, struct {
		Event                task.Event                 `json:"event"`
		RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
	}{
		Event:                event,
		RuntimeConfigVersion: configVersion,
	})
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
