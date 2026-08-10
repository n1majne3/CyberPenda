package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"pentest/internal/challengeworkflow"
	"pentest/internal/task"
)

type challengeOperationInput struct {
	Platform          string `json:"platform"`
	OperationID       string `json:"operation_id"`
	ChallengeID       string `json:"challenge_id,omitempty"`
	ExternalAttemptID string `json:"external_attempt_id,omitempty"`
	Candidate         string `json:"candidate,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

func (server *Server) handleChallengeAttempts(response http.ResponseWriter, request *http.Request) {
	projectID, taskID := request.PathValue("id"), request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	attempts, err := server.challengeWorkflow.ListAttempts(request.Context(), projectID, taskID)
	if err != nil {
		writeChallengeWorkflowError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Attempts []challengeworkflow.Attempt `json:"attempts"`
	}{Attempts: attempts})
}

func (server *Server) handleChallengeClaim(response http.ResponseWriter, request *http.Request) {
	input, ok := server.decodeChallengeOperation(response, request)
	if !ok {
		return
	}
	result, err := server.challengeWorkflow.Claim(request.Context(), challengeworkflow.ClaimRequest{
		ProjectID: request.PathValue("id"), TaskID: request.PathValue("task_id"), Platform: input.Platform,
		OperationID: input.OperationID, ChallengeID: input.ChallengeID,
	})
	if err != nil {
		writeChallengeWorkflowError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleChallengeSubmit(response http.ResponseWriter, request *http.Request) {
	input, ok := server.decodeChallengeOperation(response, request)
	if !ok {
		return
	}
	result, err := server.challengeWorkflow.Submit(request.Context(), challengeworkflow.SubmitRequest{
		ProjectID: request.PathValue("id"), TaskID: request.PathValue("task_id"), Platform: input.Platform,
		OperationID: input.OperationID, ExternalAttemptID: input.ExternalAttemptID, Candidate: input.Candidate,
	})
	if err != nil {
		writeChallengeWorkflowError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleChallengeAbandon(response http.ResponseWriter, request *http.Request) {
	input, ok := server.decodeChallengeOperation(response, request)
	if !ok {
		return
	}
	result, err := server.challengeWorkflow.Abandon(request.Context(), challengeworkflow.AbandonRequest{
		ProjectID: request.PathValue("id"), TaskID: request.PathValue("task_id"), Platform: input.Platform,
		OperationID: input.OperationID, ExternalAttemptID: input.ExternalAttemptID, Reason: input.Reason,
	})
	if err != nil {
		writeChallengeWorkflowError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleChallengeFinalize(response http.ResponseWriter, request *http.Request) {
	input, ok := server.decodeChallengeOperation(response, request)
	if !ok {
		return
	}
	result, err := server.challengeWorkflow.Finalize(request.Context(), challengeworkflow.FinalizeRequest{
		ProjectID: request.PathValue("id"), TaskID: request.PathValue("task_id"), Platform: input.Platform,
		OperationID: input.OperationID, ExternalAttemptID: input.ExternalAttemptID,
	})
	if err != nil {
		writeChallengeWorkflowError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) decodeChallengeOperation(response http.ResponseWriter, request *http.Request) (challengeOperationInput, bool) {
	projectID, taskID := request.PathValue("id"), request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return challengeOperationInput{}, false
	}
	found, err := server.tasks.Get(taskID)
	if err != nil || found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, task.ErrNotFound.Error())
		return challengeOperationInput{}, false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var input challengeOperationInput
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid Challenge Workflow request")
		return challengeOperationInput{}, false
	}
	input.Platform, input.OperationID = strings.TrimSpace(input.Platform), strings.TrimSpace(input.OperationID)
	if input.Platform == "" || input.OperationID == "" {
		writeError(response, http.StatusBadRequest, challengeworkflow.ErrInvalidRequest.Error())
		return challengeOperationInput{}, false
	}
	return input, true
}

func writeChallengeWorkflowError(response http.ResponseWriter, err error) {
	var policyError *challengeworkflow.PolicyError
	switch {
	case errors.As(err, &policyError):
		writeJSON(response, http.StatusConflict, struct {
			Error  string                         `json:"error"`
			Policy *challengeworkflow.PolicyError `json:"policy"`
		}{Error: policyError.Error(), Policy: policyError})
	case errors.Is(err, challengeworkflow.ErrInvalidRequest), errors.Is(err, challengeworkflow.ErrProjectKind), errors.Is(err, challengeworkflow.ErrTaskType), errors.Is(err, challengeworkflow.ErrPlatformNotFound):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, challengeworkflow.ErrAttemptNotFound), errors.Is(err, task.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, challengeworkflow.ErrOperationConflict), errors.Is(err, challengeworkflow.ErrOperationActionRequired), errors.Is(err, challengeworkflow.ErrAttemptNotOpen):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusBadGateway, err.Error())
	}
}
