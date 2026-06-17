package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

func (server *Server) handleCreateTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
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

	if _, err := server.tasks.RecordRuntimeConfig(created.ID, created.RuntimeProfileID, map[string]any{
		"runtime_profile_id": created.RuntimeProfileID,
		"runner":             created.Runner,
	}); err != nil {
		writeError(response, http.StatusInternalServerError, "record task runtime configuration")
		return
	}

	if err := server.harness.Launch(context.Background(), runtime.LaunchRequest{
		TaskID:  created.ID,
		Goal:    created.Goal,
		Adapter: runtime.NewFakeAdapter(),
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

func (server *Server) handleListTasks(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
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
	if projectID == "" || taskID == "" {
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
	if projectID == "" || taskID == "" {
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
	if projectID == "" || taskID == "" {
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

func (server *Server) handlePutTaskSummary(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if projectID == "" || taskID == "" {
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
	if projectID == "" || taskID == "" {
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
