package daemon

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/projectinterface"
)

// authenticateSessionBlackboardV2 deliberately accepts only the daemon
// operator credential for this initial Session surface. Project Continuation
// grants cannot be widened into Session authority because their contract has
// a Project/Task owner binding.
func (server *Server) authenticateSessionBlackboardV2(request *http.Request) (string, *blackboardv2.Error) {
	if request.URL.Query().Get("token") != "" {
		return "", blackboardV2HTTPError("invalid_schema", "v2 does not accept bearer credentials in query strings", "authorization")
	}
	sessionID := strings.TrimSpace(request.PathValue("id"))
	if sessionID == "" {
		return "", blackboardV2HTTPError("invalid_schema", "session id is required", "path.session_id")
	}
	token := projectinterface.BearerToken(request)
	operator := token == "" && server.authToken == ""
	if token != "" && server.authToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(server.authToken)) == 1 {
		operator = true
	}
	if !operator {
		return "", blackboardV2HTTPError("authority_denied", "Session Blackboard requires the trusted operator authority", "authorization")
	}
	return sessionID, nil
}

func (server *Server) handleSessionBlackboardV2Change(response http.ResponseWriter, request *http.Request) {
	sessionID, authErr := server.authenticateSessionBlackboardV2(request)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	var batch blackboardv2.ChangeBatch
	if decodeErr := decodeBlackboardV2HTTPContractJSON(request, "changeBatch", map[string]string{"idempotency_key": idempotencyKey}, true, &batch); decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	result, applyErr := server.blackboardV2.ApplyForSession(request.Context(), sessionID, batch)
	if applyErr != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(applyErr), nil)
		return
	}
	writeBlackboardV2Success(response, result, nil)
}

func (server *Server) handleSessionBlackboardV2Snapshot(response http.ResponseWriter, request *http.Request) {
	sessionID, authErr := server.authenticateSessionBlackboardV2(request)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	projection, err := server.blackboardV2.ProjectSessionRuntimeSnapshot(request.Context(), sessionID)
	if err != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
		return
	}
	writeBlackboardV2ConditionalSuccess(response, request, projection.Snapshot.Revision, projection.Snapshot, nil)
}

func (server *Server) handleSessionBlackboardV2Health(response http.ResponseWriter, request *http.Request) {
	sessionID, authErr := server.authenticateSessionBlackboardV2(request)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	health, err := server.blackboardV2.SessionSemanticHealth(request.Context(), sessionID)
	if err != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
		return
	}
	writeBlackboardV2ConditionalSuccess(response, request, health.Revision, health, nil)
}

func (server *Server) handleSessionBlackboardV2Read(response http.ResponseWriter, request *http.Request) {
	sessionID, authErr := server.authenticateSessionBlackboardV2(request)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	detail, err := server.blackboardV2.ReadSessionCurrent(request.Context(), sessionID, request.PathValue("key"))
	if err != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
		return
	}
	writeBlackboardV2ConditionalSuccess(response, request, detail.Revision, detail, nil)
}

func (server *Server) handleSessionBlackboardV2History(response http.ResponseWriter, request *http.Request) {
	sessionID, authErr := server.authenticateSessionBlackboardV2(request)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "limit must be an integer", "limit"), nil)
			return
		}
		limit = parsed
	}
	history, err := server.blackboardV2.ReadSessionHistory(request.Context(), sessionID, request.PathValue("key"), blackboardv2.HistoryOptions{
		Cursor: request.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
		return
	}
	writeBlackboardV2ConditionalSuccess(response, request, history.Revision, history, nil)
}

func (server *Server) handleSessionBlackboardV2Checkpoint(response http.ResponseWriter, request *http.Request) {
	sessionID, authErr := server.authenticateSessionBlackboardV2(request)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	action := request.PathValue("attempt_action")
	if !strings.HasSuffix(action, ":checkpoint") || strings.TrimSuffix(action, ":checkpoint") == "" {
		writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "checkpoint path must end in :checkpoint", "path"), nil)
		return
	}
	pathKey := strings.TrimSuffix(action, ":checkpoint")
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	var checkpoint blackboardv2.CheckpointAttemptRequest
	if decodeErr := decodeBlackboardV2HTTPContractJSON(request, "checkpointAttemptRequest", map[string]string{
		"idempotency_key": idempotencyKey,
		"key":             pathKey,
	}, true, &checkpoint); decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	activeID, activeErr := server.activeSessionContinuationID(request.Context(), sessionID)
	if activeErr != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(activeErr), nil)
		return
	}
	if activeID == "" {
		writeBlackboardV2Error(response, blackboardV2HTTPError("authority_denied", "an active Session Continuation is required", "authority"), nil)
		return
	}
	authority, authorityErr := server.blackboardV2.AuthorizeSessionContinuation(request.Context(), sessionID, activeID)
	if authorityErr != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(authorityErr), nil)
		return
	}
	summary := checkpoint.Summary
	result, applyErr := server.blackboardV2.ApplyForSessionAuthority(request.Context(), authority, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: idempotencyKey,
		Changes: []blackboardv2.Change{{Op: "update", Key: pathKey, Version: checkpoint.Version, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: &summary}}},
	})
	if applyErr != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(applyErr), nil)
		return
	}
	writeBlackboardV2Success(response, result, nil)
}

func (server *Server) handleSessionBlackboardV2Finish(response http.ResponseWriter, request *http.Request) {
	sessionID, authErr := server.authenticateSessionBlackboardV2(request)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	if bodyErr := rejectSessionFinishBody(request); bodyErr != nil {
		writeBlackboardV2Error(response, bodyErr, nil)
		return
	}
	activeID, activeErr := server.activeSessionContinuationID(request.Context(), sessionID)
	if activeErr != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(activeErr), nil)
		return
	}
	if activeID == "" {
		writeBlackboardV2Error(response, blackboardV2HTTPError("authority_denied", "an active Session Continuation is required", "authority"), nil)
		return
	}
	authority, authorityErr := server.blackboardV2.AuthorizeSessionContinuation(request.Context(), sessionID, activeID)
	if authorityErr != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(authorityErr), nil)
		return
	}
	result, finishErr := server.blackboardV2.FinishSessionAuthority(request.Context(), authority, idempotencyKey)
	if finishErr != nil {
		writeBlackboardV2Error(response, asBlackboardV2Error(finishErr), nil)
		return
	}
	writeBlackboardV2Success(response, result, nil)
}

func rejectSessionFinishBody(request *http.Request) *blackboardv2.Error {
	if request.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, blackboardV2HTTPInputLimit+1))
	if err != nil {
		return blackboardV2HTTPError("invalid_schema", "read request body: "+err.Error(), "body")
	}
	if len(raw) > blackboardV2HTTPInputLimit {
		return blackboardV2HTTPError("invalid_schema", "request body exceeds 4 MiB", "body")
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return blackboardV2HTTPError("invalid_schema", "Finish body must be empty or {}", "body")
	}
	if len(fields) != 0 {
		return blackboardV2HTTPError("invalid_schema", "Finish body must be empty or {}", "body")
	}
	return nil
}

func (server *Server) activeSessionContinuationID(ctx context.Context, sessionID string) (string, error) {
	var id string
	err := server.db.QueryRowContext(ctx, `
		SELECT id FROM session_continuations
		WHERE session_id=? AND status IN ('pending','running')
		ORDER BY number DESC LIMIT 1`, sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read active Session Continuation: %w", err)
	}
	return id, nil
}
