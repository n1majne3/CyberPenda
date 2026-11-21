package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2input"
	"pentest/internal/task"
)

func (server *Server) handleBlackboardV2EvidenceRetain(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, true)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	if !server.allowLegacyGraphWrite(response, principal) {
		return
	}

	if accessErr := requireBlackboardV2WriteAccess(principal); accessErr != nil {
		writeBlackboardV2Error(response, accessErr, nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	// Transport carries Idempotency-Key; body must not restate authority or
	// the transport idempotency field (parity with CLI daemon mode).
	contractRaw, decodeErr := prepareBlackboardV2HTTPContractJSON(request, map[string]string{"idempotency_key": idempotencyKey})
	if decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	var req blackboardv2.RetainEvidenceRequest
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		if contractErr := blackboardv2input.DecodeContractInput("retainEvidenceRequest", false, contractRaw, &req); contractErr != nil {
			return nil, contractErr
		}
		return server.blackboardV2.RetainEvidenceForContinuation(ctx, principal.projectID, principal.continuationID, req)
	})
}

// handleDisabledTaskOutputEvidenceRetain is an operator-only promotion seam.
// It selects one Disabled Task Workdir file without granting or synchronizing
// Blackboard state to that Task's Runtime.
func (server *Server) handleDisabledTaskOutputEvidenceRetain(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	if !principal.operator {
		writeBlackboardV2Error(response, blackboardV2HTTPError("authority_denied", "authenticated operator authority is required", "authorization"), nil)
		return
	}
	selectedTaskID := strings.TrimSpace(request.PathValue("task_id"))
	found, err := server.tasks.Get(selectedTaskID)
	if errors.Is(err, task.ErrNotFound) {
		writeBlackboardV2Error(response, blackboardV2HTTPError("not_found", "Task was not found in this Project", "path.task_id"), nil)
		return
	}
	if err != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
		return
	}
	if found.ProjectID != principal.projectID {
		writeBlackboardV2Error(response, blackboardV2HTTPError("not_found", "Task was not found in this Project", "path.task_id"), nil)
		return
	}
	if found.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		writeBlackboardV2Error(response, blackboardV2HTTPError("semantic_validation", "selected output retention requires a Disabled Task", "path.task_id"), nil)
		return
	}
	continuation, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
		return
	}
	if continuation == nil {
		writeBlackboardV2Error(response, blackboardV2HTTPError("not_found", "Disabled Task has no source Continuation", "path.task_id"), nil)
		return
	}
	idempotencyKey, keyErr := requireBlackboardV2IdempotencyKey(request)
	if keyErr != nil {
		writeBlackboardV2Error(response, keyErr, nil)
		return
	}
	contractRaw, decodeErr := prepareBlackboardV2HTTPContractJSON(request, map[string]string{"idempotency_key": idempotencyKey})
	if decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	var retain blackboardv2.RetainEvidenceRequest
	server.serveBlackboardV2(response, request, principal, false, false, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		if contractErr := blackboardv2input.DecodeContractInput("retainEvidenceRequest", false, contractRaw, &retain); contractErr != nil {
			return nil, contractErr
		}
		result, retainErr := server.blackboardV2.RetainTaskEvidenceForOperator(
			ctx, principal.projectID, found.ID, continuation.ID, principal.actorID, retain,
		)
		if retainErr != nil && server.logger != nil {
			server.logger.Printf("operator Disabled output retention failed project=%s task=%s: %v", principal.projectID, found.ID, retainErr)
		}
		return result, retainErr
	})
}

func (server *Server) handleBlackboardV2Checkpoint(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, true)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	if !server.allowLegacyGraphWrite(response, principal) {
		return
	}

	if accessErr := requireBlackboardV2WriteAccess(principal); accessErr != nil {
		writeBlackboardV2Error(response, accessErr, nil)
		return
	}
	action := request.PathValue("attempt_action")
	key, ok := strings.CutSuffix(action, ":checkpoint")
	if !ok || key == "" {
		writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "checkpoint path must end in :checkpoint", "path"), nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	contractRaw, decodeErr := prepareBlackboardV2HTTPContractJSON(request, map[string]string{
		"idempotency_key": idempotencyKey,
		"key":             key,
	})
	if decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	var req blackboardv2.CheckpointAttemptRequest
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		if contractErr := blackboardv2input.DecodeContractInput("checkpointAttemptRequest", false, contractRaw, &req); contractErr != nil {
			return nil, contractErr
		}
		return server.blackboardV2.CheckpointAttemptForContinuation(ctx, principal.projectID, principal.continuationID, req)
	})
}
