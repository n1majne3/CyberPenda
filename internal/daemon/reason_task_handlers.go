package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/reasontask"
)

func isReasonTaskProposalCreateHTTPTransport(request *http.Request) bool {
	if request.Method != http.MethodPost {
		return false
	}
	path := request.URL.Path
	if !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(segments) == 6 &&
		segments[0] == "api" && segments[1] == "projects" && strings.TrimSpace(segments[2]) != "" &&
		segments[3] == "reason-tasks" && strings.TrimSpace(segments[4]) != "" && segments[5] == "proposals"
}

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
	reasonTaskID := request.PathValue("task_id")
	if !server.requireReasonTaskProposalAuthority(response, request, projectID, reasonTaskID) {
		return
	}
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
		ProjectID: projectID, ReasonTaskID: reasonTaskID, NextTaskGoals: input.NextTaskGoals,
		ExplorationObjectiveChanges: input.ExplorationObjectiveChanges,
		ReadinessJudgment:           input.ReadinessJudgment, Changes: input.Changes,
	})
	if err != nil {
		writeReasonTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, proposal)
}

func (server *Server) requireReasonTaskProposalAuthority(
	response http.ResponseWriter,
	request *http.Request,
	projectID string,
	reasonTaskID string,
) bool {
	// Query credentials are reserved for the MCP fallback. This ordinary HTTP
	// route requires an Authorization bearer and never accepts operator identity
	// or browser-origin metadata as Runtime authority.
	if request.URL.Query().Get("token") != "" || server.projectInterfaceGrants == nil || server.blackboardV2 == nil {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return false
	}
	token := projectinterface.BearerToken(request)
	grant, err := server.projectInterfaceGrants.Resolve(request.Context(), token)
	if err != nil || !grant.Status().IsWriteable() || grant.IsSession() ||
		grant.Owner.ProjectID != strings.TrimSpace(projectID) || grant.Owner.TaskID != strings.TrimSpace(reasonTaskID) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if _, err := server.blackboardV2.AuthorizeContinuationBinding(
		request.Context(), grant.Owner.ProjectID, grant.Owner.TaskID, grant.ContinuationID, true,
	); err != nil {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func (server *Server) handleApproveReasonTaskProposal(response http.ResponseWriter, request *http.Request) {
	if !server.requireOperatorAuthority(response, request) {
		return
	}
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
