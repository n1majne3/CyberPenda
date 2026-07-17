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
	// Exact replay remains available after Finish/supersession; only live
	// Continuations may attach synchronization.
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
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
	// Live read/current knowledge authority only; closed Continuations keep
	// exact write/finish replay but not current knowledge reads.
	server.serveBlackboardV2Conditional(response, request, principal, true, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		projection, err := server.blackboardV2.ProjectRuntimeSnapshot(ctx, principal.projectID)
		if err != nil {
			return nil, 0, err
		}
		return projection.Snapshot, projection.Snapshot.Revision, nil
	})
}

func (server *Server) handleBlackboardV2Read(response http.ResponseWriter, request *http.Request) {
	principal, authErr := server.authenticateBlackboardV2(request, false)
	if authErr != nil {
		writeBlackboardV2Error(response, authErr, nil)
		return
	}
	key := request.PathValue("key")
	server.serveBlackboardV2Conditional(response, request, principal, true, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		detail, err := server.blackboardV2.ReadCurrent(ctx, principal.projectID, key)
		if err != nil {
			return nil, 0, err
		}
		return detail, detail.Revision, nil
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
	server.serveBlackboardV2(response, request, principal, true, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
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
	// Exact Evidence retain replay remains available after Finish/supersession.
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
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
	// Exact checkpoint replay remains available after Finish/supersession.
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
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
	// Initial live Finish may carry pending synchronization; exact Finish replay
	// redelivers the durable attachment via the request fingerprint contract.
	server.serveBlackboardV2(response, request, principal, false, true, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, error) {
		return server.blackboardV2.FinishContinuation(ctx, principal.projectID, principal.continuationID, req)
	})
}

func (server *Server) serveBlackboardV2(response http.ResponseWriter, request *http.Request, principal blackboardV2Principal, requireLive, attachSync bool, action func(context.Context, blackboardv2.ContinuationAuthority) (any, error)) {
	server.serveBlackboardV2Result(response, request, principal, requireLive, attachSync, false, func(ctx context.Context, live blackboardv2.ContinuationAuthority) (any, int, error) {
		result, err := action(ctx, live)
		return result, 0, err
	})
}

// serveBlackboardV2Conditional serves Snapshot/detail with a revision ETag and
// honest If-None-Match behavior (200 body or 304 empty).
func (server *Server) serveBlackboardV2Conditional(response http.ResponseWriter, request *http.Request, principal blackboardV2Principal, requireLive, attachSync bool, action func(context.Context, blackboardv2.ContinuationAuthority) (any, int, error)) {
	server.serveBlackboardV2Result(response, request, principal, requireLive, attachSync, true, action)
}

// serveBlackboardV2Result authenticates Continuation binding, runs the action,
// and optionally attaches same-Project synchronization. requireLive gates
// offline read/current knowledge authority. Mutating tools that support exact
// replay pass requireLive=false so stored non-mutating replays reach the
// service after Finish/supersession; the service still rejects changed retries
// and new writes. attachSync uses CaptureTrustedSynchronization so Pending
// deliveries and exact response-loss replays of the same Idempotency-Key share
// one deterministic attachment, while ordinary later responses stay clean.
func (server *Server) serveBlackboardV2Result(response http.ResponseWriter, request *http.Request, principal blackboardV2Principal, requireLive, attachSync bool, conditional bool, action func(context.Context, blackboardv2.ContinuationAuthority) (any, int, error)) {
	ctx := request.Context()
	var authority blackboardv2.ContinuationAuthority
	if !principal.operator {
		binding, err := server.blackboardV2.AuthorizeContinuationBinding(ctx, principal.projectID, principal.taskID, principal.continuationID, requireLive)
		if err != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(err), nil)
			return
		}
		authority = binding
	}
	fingerprint := blackboardV2SyncFingerprint(request)
	// Claim the pending notice before the action so concurrent different
	// fingerprints cannot both deliver, and so Finish/action can absorb Pending
	// while still finalizing this request's durable receipt afterward.
	if !principal.operator && attachSync && fingerprint != "" && authority.Sync.Pending {
		if _, claimErr := server.blackboardV2.ClaimTrustedSynchronization(ctx, principal.projectID, principal.taskID, principal.continuationID, fingerprint, authority.Sync); claimErr != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(claimErr), nil)
			return
		}
	}
	result, revision, err := action(ctx, authority)
	if err != nil {
		var sync *blackboardv2.SynchronizationAttachment
		if !principal.operator && attachSync {
			if attachment, syncErr := server.blackboardV2.CaptureTrustedSynchronization(ctx, principal.projectID, principal.taskID, principal.continuationID, authority.Sync, authority.Live, fingerprint); syncErr == nil {
				sync = attachment
			}
		}
		writeBlackboardV2Error(response, asBlackboardV2Error(err), sync)
		return
	}
	var sync *blackboardv2.SynchronizationAttachment
	if !principal.operator && attachSync {
		attachment, syncErr := server.blackboardV2.CaptureTrustedSynchronization(ctx, principal.projectID, principal.taskID, principal.continuationID, authority.Sync, authority.Live, fingerprint)
		if syncErr != nil {
			writeBlackboardV2Error(response, asBlackboardV2Error(syncErr), nil)
			return
		}
		sync = attachment
		if sync != nil && revision == 0 {
			revision = sync.Revision
		}
	}
	if conditional {
		writeBlackboardV2ConditionalSuccess(response, request, revision, result, sync)
		return
	}
	writeBlackboardV2Success(response, result, sync)
}

// blackboardV2SyncFingerprint binds POST Idempotency-Key deliveries for exact
// response-loss replay. GET reads stay Pending-only (empty fingerprint).
func blackboardV2SyncFingerprint(request *http.Request) string {
	if request == nil || request.Method == http.MethodGet {
		return ""
	}
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		return ""
	}
	path := request.URL.Path
	switch {
	case strings.HasSuffix(path, "/blackboard/changes"):
		return blackboardv2.SynchronizationDeliveryFingerprint("change", key)
	case strings.HasSuffix(path, "/blackboard/evidence:retain"):
		return blackboardv2.SynchronizationDeliveryFingerprint("evidence", key)
	case strings.HasSuffix(path, ":checkpoint"):
		return blackboardv2.SynchronizationDeliveryFingerprint("checkpoint", key)
	case strings.HasSuffix(path, "/continuation:finish"):
		return blackboardv2.SynchronizationDeliveryFingerprint("finish", key)
	default:
		return blackboardv2.SynchronizationDeliveryFingerprint("http:"+request.Method+":"+path, key)
	}
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
	writeBlackboardV2JSON(response, http.StatusOK, result, sync)
}

func writeBlackboardV2ConditionalSuccess(response http.ResponseWriter, request *http.Request, revision int, result any, sync *blackboardv2.SynchronizationAttachment) {
	etag := blackboardV2RevisionETag(revision)
	response.Header().Set("ETag", etag)
	response.Header().Set("Cache-Control", "private, no-cache")
	// Pending-only GET delivery acknowledges before this write. A 304 would
	// discard the body (and sync sibling) after acknowledgement with no request
	// identity to redeliver later — always return the body when sync exists.
	if sync == nil && etagMatches(request.Header.Get("If-None-Match"), etag) {
		// 304 responses carry validators but no body, and never an error envelope.
		response.WriteHeader(http.StatusNotModified)
		return
	}
	writeBlackboardV2JSON(response, http.StatusOK, result, sync)
}

func writeBlackboardV2JSON(response http.ResponseWriter, status int, result any, sync *blackboardv2.SynchronizationAttachment) {
	if sync == nil {
		writeJSON(response, status, result)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		writeBlackboardV2Error(response, blackboardV2HTTPError("internal", "unexpected Blackboard v2 failure", "internal"), nil)
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		writeBlackboardV2Error(response, blackboardV2HTTPError("internal", "unexpected Blackboard v2 failure", "internal"), nil)
		return
	}
	syncRaw, err := json.Marshal(sync)
	if err != nil {
		writeBlackboardV2Error(response, blackboardV2HTTPError("internal", "unexpected Blackboard v2 failure", "internal"), nil)
		return
	}
	object["sync"] = syncRaw
	writeJSON(response, status, object)
}

func writeBlackboardV2Error(response http.ResponseWriter, err *blackboardv2.Error, sync *blackboardv2.SynchronizationAttachment) {
	if err == nil {
		err = blackboardV2HTTPError("internal", "unexpected Blackboard v2 failure", "internal")
	}
	// Authenticated semantic errors — including service-origin invalid_schema
	// such as unsupported schema after Continuation binding — may carry
	// same-Project sync. Transport/body-parse invalid_schema and auth failures
	// never reach this path with a non-nil sync: callers pass nil before
	// authenticated semantic dispatch. Still strip codes that must never
	// advertise synchronization (authority, internal, storage contention).
	if sync != nil && (err.Code == "authority_denied" || err.Code == "internal" || err.Code == "storage_busy") {
		sync = nil
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
	if err == nil {
		return blackboardV2HTTPError("internal", "unexpected Blackboard v2 failure", "internal")
	}
	var semantic *blackboardv2.Error
	if errors.As(err, &semantic) {
		return semantic
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "database is locked") || strings.Contains(lower, "database is busy") ||
		strings.Contains(lower, "sqlite_busy") || strings.Contains(lower, "sqlite_locked") {
		return blackboardV2HTTPError("storage_busy", "SQLite writer lock is busy", "storage")
	}
	// Unexpected faults map to a closed internal envelope without raw storage
	// paths, SQL text, or operator secrets.
	return blackboardV2HTTPError("internal", "unexpected Blackboard v2 failure", "internal")
}

func blackboardV2HTTPError(code, message, path string) *blackboardv2.Error {
	return &blackboardv2.Error{Code: code, Message: message, Path: path, Retryable: code == "storage_busy"}
}

func blackboardV2RevisionETag(revision int) string {
	return `"` + strconv.Itoa(revision) + `"`
}

// etagMatches implements honest If-None-Match comparison for a single strong
// revision ETag: exact match, list membership, or `*`.
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for _, part := range strings.Split(header, ",") {
		candidate := strings.TrimSpace(part)
		if strings.HasPrefix(candidate, "W/") {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "W/"))
		}
		if candidate == etag {
			return true
		}
	}
	return false
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
		// Retryable evidence/publication codes already returned 503 above.
		// Remaining domain codes stay closed 422 unless explicitly mapped.
		return http.StatusUnprocessableEntity
	}
}
