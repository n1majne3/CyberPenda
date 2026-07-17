package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/projectinterface"
)

const blackboardV2HTTPInputLimit = 4 << 20

type blackboardV2Principal struct {
	operator       bool
	actorID        string
	projectID      string
	taskID         string
	continuationID string
	grant          projectinterface.Grant
}

func (server *Server) registerBlackboardV2Routes() {
	if server.blackboardV2 == nil {
		return
	}
	server.mux.HandleFunc("POST /api/v2/projects/{id}/blackboard/changes", server.handleBlackboardV2Change)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/blackboard/snapshot", server.handleBlackboardV2Snapshot)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/blackboard/records/{key}", server.handleBlackboardV2Read)
	server.mux.HandleFunc("GET /api/v2/projects/{id}/blackboard/records/{key}/history", server.handleBlackboardV2History)
	server.mux.HandleFunc("POST /api/v2/projects/{id}/blackboard/evidence:retain", server.handleBlackboardV2EvidenceRetain)
	// net/http patterns cannot suffix a wildcard with ":checkpoint". Capture the
	// final segment and validate/remove that exact suffix in the handler.
	server.mux.HandleFunc("POST /api/v2/projects/{id}/blackboard/attempts/{attempt_action}", server.handleBlackboardV2Checkpoint)
	server.mux.HandleFunc("POST /api/v2/projects/{id}/continuation:finish", server.handleBlackboardV2Finish)
}

func isBlackboardV2HTTPTransport(request *http.Request) bool {
	return strings.HasPrefix(request.URL.Path, "/api/v2/projects/")
}

func (server *Server) authenticateBlackboardV2(request *http.Request, requireContinuation bool) (blackboardV2Principal, *blackboardv2.Error) {
	if request.URL.Query().Get("token") != "" {
		return blackboardV2Principal{}, blackboardV2HTTPError("invalid_schema", "v2 does not accept bearer credentials in query strings", "authorization")
	}
	projectID := strings.TrimSpace(request.PathValue("id"))
	if projectID == "" {
		return blackboardV2Principal{}, blackboardV2HTTPError("invalid_schema", "project id is required", "path.project_id")
	}
	token := projectinterface.BearerToken(request)
	operatorRequest := token == "" && server.authToken == ""
	if token != "" && server.authToken != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(server.authToken)) == 1 {
		operatorRequest = true
	}
	if operatorRequest {
		if requireContinuation {
			return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "this Blackboard capability requires a trusted Continuation", "authority")
		}
		actorID := strings.TrimSpace(request.Header.Get(projectinterface.OperatorActorHeader))
		if actorID == "" {
			actorID = "local-operator"
		}
		return blackboardV2Principal{operator: true, actorID: actorID, projectID: projectID}, nil
	}
	if token == "" {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is required", "authorization")
	}
	if server.projectInterfaceGrants == nil {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is unavailable", "authorization")
	}
	grant, err := server.projectInterfaceGrants.Resolve(request.Context(), token)
	if err != nil {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is invalid", "authorization")
	}
	if !grant.Status().IsReadable() {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "Continuation Interface capability is revoked", "authorization")
	}
	if grant.ProjectID != projectID {
		return blackboardV2Principal{}, blackboardV2HTTPError("authority_denied", "declared Project does not match Continuation Interface capability", "path.project_id")
	}
	return blackboardV2Principal{
		projectID: grant.ProjectID, taskID: grant.TaskID, continuationID: grant.ContinuationID, grant: grant,
	}, nil
}

func (server *Server) handleBlackboardV2Change(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	var body struct {
		Schema  string                `json:"schema"`
		Changes []blackboardv2.Change `json:"changes"`
	}
	if decodeErr := decodeBlackboardV2JSON(request, &body); decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	batch := blackboardv2.ChangeBatch{Schema: body.Schema, IdempotencyKey: idempotencyKey, Changes: body.Changes}
	server.serveBlackboardV2(response, request, principal, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		if principal.operator {
			return server.blackboardV2.Apply(ctx, principal.projectID, batch)
		}
		return server.blackboardV2.ApplyForContinuation(ctx, principal.projectID, principal.continuationID, batch)
	})
}

func (server *Server) handleBlackboardV2Snapshot(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	server.serveBlackboardV2(response, request, principal, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		projection, err := server.blackboardV2.ProjectRuntimeSnapshot(ctx, principal.projectID)
		if err != nil {
			return nil, err
		}
		return projection.Snapshot, nil
	})
}

func (server *Server) handleBlackboardV2Read(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	key := request.PathValue("key")
	server.serveBlackboardV2(response, request, principal, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.ReadCurrent(ctx, principal.projectID, key)
	})
}

func (server *Server) handleBlackboardV2History(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	key := request.PathValue("key")
	limit := 0
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "limit must be an integer", "limit"), nil)
			return
		}
		limit = parsed
	}
	options := blackboardv2.HistoryOptions{Cursor: request.URL.Query().Get("cursor"), Limit: limit}
	server.serveBlackboardV2(response, request, principal, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.ReadHistory(ctx, principal.projectID, key, options)
	})
}

func (server *Server) handleBlackboardV2EvidenceRetain(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, true)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	// Transport carries Idempotency-Key; body must not restate authority or
	// the transport idempotency field (parity with CLI daemon mode).
	var body struct {
		Key          string                      `json:"key"`
		Version      int                         `json:"version,omitempty"`
		Attempt      string                      `json:"attempt"`
		SourcePath   string                      `json:"source_path"`
		ArtifactType string                      `json:"artifact_type"`
		Summary      string                      `json:"summary"`
		MediaType    string                      `json:"media_type,omitempty"`
		CapturedAt   string                      `json:"captured_at,omitempty"`
		Links        []blackboardv2.EvidenceLink `json:"links,omitempty"`
	}
	if decodeErr := decodeBlackboardV2JSON(request, &body); decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	req := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: idempotencyKey, Key: body.Key, Version: body.Version, Attempt: body.Attempt,
		SourcePath: body.SourcePath, ArtifactType: body.ArtifactType, Summary: body.Summary,
		MediaType: body.MediaType, CapturedAt: body.CapturedAt, Links: body.Links,
	}
	server.serveBlackboardV2(response, request, principal, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.RetainEvidenceForContinuation(ctx, principal.projectID, principal.continuationID, req)
	})
}

func (server *Server) handleBlackboardV2Checkpoint(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, true)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
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
	var body struct {
		Version int    `json:"version"`
		Summary string `json:"summary"`
	}
	if decodeErr := decodeBlackboardV2JSON(request, &body); decodeErr != nil {
		writeBlackboardV2Error(response, decodeErr, nil)
		return
	}
	req := blackboardv2.CheckpointAttemptRequest{IdempotencyKey: idempotencyKey, Key: key, Version: body.Version, Summary: body.Summary}
	server.serveBlackboardV2(response, request, principal, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.CheckpointAttemptForContinuation(ctx, principal.projectID, principal.continuationID, req)
	})
}

func (server *Server) handleBlackboardV2Finish(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, true)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	idempotencyKey, err := requireBlackboardV2IdempotencyKey(request)
	if err != nil {
		writeBlackboardV2Error(response, err, nil)
		return
	}
	// Finish body is empty; only the transport Idempotency-Key is required.
	if request.Body != nil {
		raw, readErr := io.ReadAll(io.LimitReader(request.Body, blackboardV2HTTPInputLimit+1))
		if readErr != nil {
			writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "read request body: "+readErr.Error(), "body"), nil)
			return
		}
		if len(raw) > blackboardV2HTTPInputLimit {
			writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "request body exceeds 4 MiB", "body"), nil)
			return
		}
		if len(strings.TrimSpace(string(raw))) != 0 {
			var empty map[string]json.RawMessage
			if err := json.Unmarshal(raw, &empty); err != nil || len(empty) != 0 {
				writeBlackboardV2Error(response, blackboardV2HTTPError("invalid_schema", "Finish body must be empty or {}", "body"), nil)
				return
			}
		}
	}
	req := blackboardv2.FinishContinuationRequest{IdempotencyKey: idempotencyKey}
	// Finish exact replay must not attach a new live sync.
	server.serveBlackboardV2(response, request, principal, false, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.FinishContinuation(ctx, principal.projectID, principal.continuationID, req)
	})
}

func (server *Server) serveBlackboardV2(response http.ResponseWriter, request *http.Request, principal blackboardV2Principal, attachSync bool, action func(context.Context, blackboardv2.ContinuationAuthority) (any, error)) {
	ctx := request.Context()
	var authority blackboardv2.ContinuationAuthority
	if !principal.operator {
		requireLive := attachSync
		binding, err := server.blackboardV2.AuthorizeContinuationBinding(ctx, principal.projectID, principal.taskID, principal.continuationID, requireLive)
		if err != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
			return
		}
		authority = binding
	}
	result, err := action(ctx, authority)
	if err != nil {
		var sync *blackboardv2.SynchronizationAttachment
		if !principal.operator && authority.Live && authority.Sync.Pending && attachSync {
			if attachment, syncErr := server.blackboardV2.SynchronizeContinuation(ctx, principal.projectID, principal.taskID, principal.continuationID, authority.Sync.FromRevision); syncErr == nil {
				sync = &attachment
			}
		}
		writeBlackboardV2Error(response, asBlackboardV2Error(err), sync)
		return
	}
	if !principal.operator && authority.Live && authority.Sync.Pending && attachSync {
		if attachment, syncErr := server.blackboardV2.SynchronizeContinuation(ctx, principal.projectID, principal.taskID, principal.continuationID, authority.Sync.FromRevision); syncErr != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(syncErr), nil)
			return
		} else {
			writeBlackboardV2Success(response, result, &attachment)
			return
		}
	}
	writeBlackboardV2Success(response, result, nil)
}

func requireBlackboardV2IdempotencyKey(request *http.Request) (string, *blackboardv2.Error) {
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", blackboardV2HTTPError("semantic_validation", "Idempotency-Key is required", "idempotency_key")
	}
	return key, nil
}

func decodeBlackboardV2JSON(request *http.Request, target any) *blackboardv2.Error {
	raw, err := io.ReadAll(io.LimitReader(request.Body, blackboardV2HTTPInputLimit+1))
	if err != nil {
		return blackboardV2HTTPError("invalid_schema", "read request body: "+err.Error(), "body")
	}
	if len(raw) > blackboardV2HTTPInputLimit {
		return blackboardV2HTTPError("invalid_schema", "request body exceeds 4 MiB", "body")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return blackboardV2HTTPError("invalid_schema", "request body is required", "body")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return blackboardV2HTTPError("invalid_schema", "invalid JSON body: "+err.Error(), "body")
	}
	if decoder.More() {
		return blackboardV2HTTPError("invalid_schema", "request body must contain a single JSON value", "body")
	}
	return nil
}

func writeBlackboardV2Success(response http.ResponseWriter, result any, sync *blackboardv2.SynchronizationAttachment) {
	response.Header().Set("Cache-Control", "no-store")
	if sync == nil {
		writeJSON(response, http.StatusOK, result)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		writeBlackboardV2Error(response, blackboardV2HTTPError("internal", "encode Blackboard v2 result: "+err.Error(), ""), nil)
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		writeBlackboardV2Error(response, blackboardV2HTTPError("internal", "compose Blackboard v2 synchronization: "+err.Error(), ""), nil)
		return
	}
	syncRaw, err := json.Marshal(sync)
	if err != nil {
		writeBlackboardV2Error(response, blackboardV2HTTPError("internal", "encode Blackboard v2 synchronization: "+err.Error(), ""), nil)
		return
	}
	object["sync"] = syncRaw
	writeJSON(response, http.StatusOK, object)
}

func writeBlackboardV2Error(response http.ResponseWriter, err *blackboardv2.Error, sync *blackboardv2.SynchronizationAttachment) {
	if err == nil {
		err = blackboardV2HTTPError("internal", "unknown Blackboard v2 error", "")
	}
	status := blackboardV2HTTPStatus(err)
	response.Header().Set("Cache-Control", "no-store")
	if err.Retryable || err.Code == "storage_busy" {
		response.Header().Set("Retry-After", "1")
	}
	payload := struct {
		Error *blackboardv2.Error                     `json:"error"`
		Sync  *blackboardv2.SynchronizationAttachment `json:"sync,omitempty"`
	}{Error: err, Sync: sync}
	writeJSON(response, status, payload)
}

func asBlackboardV2Error(err error) *blackboardv2.Error {
	var semantic *blackboardv2.Error
	if errors.As(err, &semantic) {
		return semantic
	}
	return blackboardV2HTTPError("internal", err.Error(), "")
}

func blackboardV2HTTPError(code, message, path string) *blackboardv2.Error {
	return &blackboardv2.Error{Code: code, Message: message, Path: path, Retryable: code == "storage_busy"}
}

func blackboardV2HTTPStatus(err *blackboardv2.Error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	if err.Retryable || err.Code == "storage_busy" {
		return http.StatusServiceUnavailable
	}
	switch err.Code {
	case "invalid_schema":
		return http.StatusBadRequest
	case "authority_denied":
		if strings.Contains(err.Path, "authorization") {
			return http.StatusUnauthorized
		}
		return http.StatusForbidden
	case "not_found":
		return http.StatusNotFound
	case "closed_continuation":
		return http.StatusGone
	case "version_conflict", "key_conflict", "relationship_conflict", "idempotency_conflict", "finish_conflict":
		return http.StatusConflict
	case "semantic_validation", "continuation_open_attempts", "continuation_pending_writes", "project_kind_mismatch":
		return http.StatusUnprocessableEntity
	case "internal":
		return http.StatusInternalServerError
	default:
		return http.StatusUnprocessableEntity
	}
}
