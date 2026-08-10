package daemon

import (
	"net/http"

	"pentest/internal/finishreadiness"
	"pentest/internal/task"
)

func (server *Server) handleFinishReadiness(response http.ResponseWriter, request *http.Request) {
	projectID, taskID := request.PathValue("id"), request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	readiness, err := server.finishReadiness.Evaluate(request.Context(), projectID, taskID)
	if err != nil {
		if err == task.ErrNotFound {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "evaluate Finish Readiness")
		return
	}
	writeJSON(response, http.StatusOK, readiness)
}

func writeFinishReadinessConflict(response http.ResponseWriter, readiness finishreadiness.Readiness) {
	writeJSON(response, http.StatusConflict, struct {
		Error           string                    `json:"error"`
		FinishReadiness finishreadiness.Readiness `json:"finish_readiness"`
	}{Error: "Task is not ready to finish", FinishReadiness: readiness})
}
