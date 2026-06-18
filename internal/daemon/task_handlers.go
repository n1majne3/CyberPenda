package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/adapters"
	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func (server *Server) handleCreateTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}

	var input struct {
		Goal             string            `json:"goal"`
		RuntimeProfileID string            `json:"runtime_profile_id"`
		Runner           task.Runner       `json:"runner"`
		RunControls      task.RunControls  `json:"run_controls"`
		Extras           map[string]string `json:"extras"`
		YOLO             bool              `json:"yolo"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.Runner == "" {
		input.Runner = task.RunnerSandbox
	}
	if input.RunControls.Extras == nil && input.Extras != nil {
		input.RunControls.Extras = input.Extras
	}
	if input.YOLO {
		input.RunControls.YOLO = true
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

	adapter, runtimeConfig, err := server.buildTaskAdapter(created)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "prepare runtime adapter")
		return
	}

	if _, err := server.tasks.RecordRuntimeConfig(created.ID, created.RuntimeProfileID, runtimeConfig); err != nil {
		writeError(response, http.StatusInternalServerError, "record task runtime configuration")
		return
	}

	if err := server.harness.Launch(context.Background(), runtime.LaunchRequest{
		TaskID:  created.ID,
		Goal:    created.Goal,
		Adapter: adapter,
	}); err != nil {
		writeError(response, http.StatusInternalServerError, "launch task")
		return
	}

	launched, err := server.tasks.Get(created.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, launched)
}

func (server *Server) buildTaskAdapter(created task.Task) (runtime.Adapter, map[string]any, error) {
	runtimeConfig := map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}

	profile, err := server.profiles.Get(created.RuntimeProfileID)
	if err != nil {
		if errors.Is(err, runtimeprofile.ErrNotFound) {
			runtimeConfig["provider"] = string(runtimeprofile.ProviderFake)
			runtimeConfig["fallback"] = "runtime profile not found; using fake adapter"
			return runtime.NewFakeAdapter(), runtimeConfig, nil
		}
		return nil, nil, err
	}
	if profile.Provider == runtimeprofile.ProviderFake {
		runtimeConfig["provider"] = string(runtimeprofile.ProviderFake)
		runtimeConfig["generated_config"] = runtimeprofile.GeneratedConfig(profile)
		return runtime.NewFakeAdapter(), runtimeConfig, nil
	}

	layout, err := runner.PrepareTaskLayout(server.runtimeRoot, created.ID, profile.Provider)
	if err != nil {
		return nil, nil, err
	}
	projection, err := runner.ProjectRuntimeConfig(layout, profile)
	if err != nil {
		return nil, nil, err
	}
	providerCommand, err := adapters.BuildLaunchArgs(adapters.LaunchArgsRequest{
		Provider:   profile.Provider,
		Profile:    profile,
		Goal:       created.Goal,
		ConfigPath: projection.ConfigPath,
	})
	if err != nil {
		return nil, nil, err
	}

	commandProgram := providerCommand[0]
	commandArgs := providerCommand[1:]
	workdir := layout.Workdir
	if created.Runner == task.RunnerSandbox {
		command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout:         layout,
			Provider:       profile.Provider,
			Image:          server.sandboxImage,
			ContainerCLI:   server.containerCLI,
			RuntimeCommand: providerCommand,
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
	runtimeConfig["layout"] = layout
	runtimeConfig["launch_command"] = adapters.Redact(map[string]any{
		"program": commandProgram,
		"args":    commandArgs,
	})

	return runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name:    string(profile.Provider),
		Program: commandProgram,
		Args:    commandArgs,
		Workdir: workdir,
		Env:     profile.Fields.Env,
	}), runtimeConfig, nil
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
	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handleTaskEvents(response http.ResponseWriter, request *http.Request) {
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

	events, err := server.tasks.Events(taskID)
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
