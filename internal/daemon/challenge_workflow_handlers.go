package daemon

import (
	"net/http"
	"pentest/internal/challengeworkflow"
)

func (server *Server) handleChallengeAttempts(w http.ResponseWriter, r *http.Request) {
	found, ok := server.requireProjectTask(w, r)
	if !ok {
		return
	}
	attempts, err := server.challengeWorkflow.ListAttempts(r.Context(), found.ProjectID, found.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read Challenge history")
		return
	}
	operations, err := server.challengeWorkflow.ListOperations(r.Context(), found.ProjectID, found.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read Challenge operation history")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Retired    bool                          `json:"retired"`
		Attempts   []challengeworkflow.Attempt   `json:"attempts"`
		Operations []challengeworkflow.Operation `json:"operations"`
	}{true, attempts, operations})
}

func (server *Server) handleRetiredChallengeOperation(w http.ResponseWriter, r *http.Request) {
	if !server.requireOperatorAuthority(w, r) {
		return
	}
	if _, ok := server.requireProjectTask(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "Challenge Workflow is retired. Historical operations are not replayed. Check unfinished operations on the original Platform.")
}
