package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/approval"
)

func (server *Server) handleListApprovals(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}

	status := approval.Status(request.URL.Query().Get("status"))
	approvals, err := server.approvals.ListByProject(projectID, status)
	if err != nil {
		writeApprovalError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Approvals []approval.Approval `json:"approvals"`
	}{
		Approvals: approvals,
	})
}

func (server *Server) handleDecideApproval(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	approvalID := request.PathValue("approval_id")
	if !server.requireProject(response, projectID) {
		return
	}

	var input struct {
		Reviewer string             `json:"reviewer"`
		Decision approval.Decision  `json:"decision"`
		Notes    string             `json:"notes"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.Reviewer == "" {
		input.Reviewer = "operator"
	}

	existing, err := server.approvals.Get(approvalID)
	if err != nil {
		writeApprovalError(response, err)
		return
	}
	if existing.ProjectID != projectID {
		writeError(response, http.StatusNotFound, approval.ErrNotFound.Error())
		return
	}

	decided, err := server.approvals.Decide(approval.DecideRequest{
		ApprovalID: approvalID,
		Reviewer:   input.Reviewer,
		Decision:   input.Decision,
		Notes:      input.Notes,
	})
	if err != nil {
		writeApprovalError(response, err)
		return
	}

	if decided.Kind == approval.KindScopeExpansion && decided.Status == approval.StatusApproved {
		found, err := server.projects.Get(projectID)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "load project for scope expansion")
			return
		}
		updatedScope := approval.ApplyScopeExpansion(found.Scope, decided.Payload)
		if _, err := server.projects.Update(projectID, found.Name, found.Description, updatedScope, true, found.Defaults, false); err != nil {
			writeError(response, http.StatusInternalServerError, "apply approved scope expansion")
			return
		}
		if _, err := server.approvals.RecordAudit(approval.AuditEntry{
			ProjectID: projectID,
			TaskID:    decided.TaskID,
			Kind:      "scope_expanded",
			Summary:   "scope expanded after approval: " + decided.RequestedAction,
			Payload:   updatedScope,
		}); err != nil {
			writeError(response, http.StatusInternalServerError, "record scope expansion audit")
			return
		}
	}

	writeJSON(response, http.StatusOK, decided)
}

func (server *Server) handleListAuditLog(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}

	entries, err := server.approvals.ListAudit(projectID, 200)
	if err != nil {
		writeApprovalError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Entries []approval.AuditEntry `json:"entries"`
	}{
		Entries: entries,
	})
}

func writeApprovalError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, approval.ErrMissingProjectID), errors.Is(err, approval.ErrInvalidDecision):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, approval.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, approval.ErrAlreadyDecided):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "approval operation failed")
	}
}