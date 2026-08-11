package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/reasontask"
)

func (server *Server) handleListReasonTaskProposals(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	proposals, err := server.reasonTasks.List(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list Reason Task proposals")
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Proposals []reasontask.Proposal `json:"proposals"`
	}{Proposals: proposals})
}

func (server *Server) handleProposeReasonTaskChanges(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	var input struct {
		NextTaskGoals               []string              `json:"next_task_goals"`
		ExplorationObjectiveChanges []string              `json:"exploration_objective_changes"`
		ReadinessJudgment           string                `json:"readiness_judgment"`
		Changes                     []blackboardv2.Change `json:"changes"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	proposal, err := server.reasonTasks.Propose(reasontask.ProposeRequest{
		ProjectID: projectID, ReasonTaskID: request.PathValue("task_id"), NextTaskGoals: input.NextTaskGoals,
		ExplorationObjectiveChanges: input.ExplorationObjectiveChanges,
		ReadinessJudgment:           input.ReadinessJudgment, Changes: input.Changes,
	})
	if err != nil {
		writeReasonTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, proposal)
}

func (server *Server) handleApproveReasonTaskProposal(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	proposal, result, err := server.reasonTasks.Approve(request.Context(), projectID, request.PathValue("proposal_id"))
	if err != nil {
		writeReasonTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Proposal reasontask.Proposal       `json:"proposal"`
		Result   blackboardv2.ChangeResult `json:"result"`
	}{Proposal: proposal, Result: result})
}

func (server *Server) handleRejectReasonTaskProposal(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	proposal, err := server.reasonTasks.Reject(projectID, request.PathValue("proposal_id"))
	if err != nil {
		writeReasonTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, proposal)
}

func writeReasonTaskError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound), errors.Is(err, reasontask.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, reasontask.ErrInvalid), errors.Is(err, reasontask.ErrNotReason):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, reasontask.ErrNotProposed):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusBadRequest, err.Error())
	}
}
