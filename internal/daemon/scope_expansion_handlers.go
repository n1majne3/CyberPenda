package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/project"
	"pentest/internal/scopeexpansion"
)

func (server *Server) handleListScopeExpansions(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	proposals, err := server.scopeExpansions.List(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list Scope Expansions")
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Expansions []scopeexpansion.Proposal `json:"expansions"`
	}{Expansions: proposals})
}

func (server *Server) handleProposeScopeExpansion(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	var input struct {
		Addition        project.Scope `json:"addition"`
		DiscoverySource string        `json:"discovery_source"`
		Reason          string        `json:"reason"`
		Risk            string        `json:"risk"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	proposal, err := server.scopeExpansions.Propose(scopeexpansion.ProposeRequest{
		ProjectID: projectID, Addition: input.Addition, DiscoverySource: input.DiscoverySource,
		Reason: input.Reason, Risk: input.Risk,
		Origin: scopeexpansion.TrustedOrigin{Kind: scopeexpansion.TrustedOriginOperator},
	})
	if err != nil {
		writeScopeExpansionError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, proposal)
}

func (server *Server) handleApproveScopeExpansion(response http.ResponseWriter, request *http.Request) {
	server.handleScopeExpansionDecision(response, request, true)
}

func (server *Server) handleRejectScopeExpansion(response http.ResponseWriter, request *http.Request) {
	server.handleScopeExpansionDecision(response, request, false)
}

func (server *Server) handleScopeExpansionDecision(response http.ResponseWriter, request *http.Request, approve bool) {
	projectID := request.PathValue("id")
	if !server.requireProject(response, projectID) {
		return
	}
	var proposal scopeexpansion.Proposal
	var updated project.Project
	var err error
	if approve {
		proposal, updated, err = server.scopeExpansions.Approve(projectID, request.PathValue("expansion_id"))
	} else {
		proposal, updated, err = server.scopeExpansions.Reject(projectID, request.PathValue("expansion_id"))
	}
	if err != nil {
		writeScopeExpansionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Expansion scopeexpansion.Proposal `json:"expansion"`
		Project   project.Project         `json:"project"`
	}{Expansion: proposal, Project: updated})
}

func writeScopeExpansionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound), errors.Is(err, scopeexpansion.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, scopeexpansion.ErrInvalid), errors.Is(err, scopeexpansion.ErrTrustedOrigin):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, scopeexpansion.ErrNotProposed):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "Scope Expansion failed")
	}
}
