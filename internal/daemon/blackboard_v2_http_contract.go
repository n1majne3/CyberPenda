package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2input"
)

func requireBlackboardV2IdempotencyKey(request *http.Request) (string, *blackboardv2.Error) {
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", blackboardV2HTTPError("semantic_validation", "Idempotency-Key is required", "idempotency_key")
	}
	return key, nil
}

func decodeBlackboardV2HTTPContractJSON(request *http.Request, schemaName string, transportFields map[string]string, sessionOwner bool, target any) *blackboardv2.Error {
	contractRaw, prepareErr := prepareBlackboardV2HTTPContractJSON(request, transportFields)
	if prepareErr != nil {
		return prepareErr
	}
	return blackboardv2input.DecodeContractInput(schemaName, sessionOwner, contractRaw, target)
}

func prepareBlackboardV2HTTPContractJSON(request *http.Request, transportFields map[string]string) ([]byte, *blackboardv2.Error) {
	raw, readErr := readBlackboardV2HTTPBody(request)
	if readErr != nil {
		return nil, readErr
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, blackboardV2HTTPError("invalid_schema", "request body must contain one JSON object", "body")
	}
	for _, field := range []string{"actor_id", "continuation_id", "origin", "owner_id", "project_id", "session_id", "task_id"} {
		if _, exists := fields[field]; exists {
			return nil, &blackboardv2.Error{Code: "invalid_schema", Message: "HTTP body must not contain authority fields", Path: field, Details: map[string]any{"reason": "additional_field"}}
		}
	}
	transportNames := make([]string, 0, len(transportFields))
	for field := range transportFields {
		transportNames = append(transportNames, field)
	}
	sort.Strings(transportNames)
	for _, field := range transportNames {
		value := transportFields[field]
		if _, exists := fields[field]; exists {
			return nil, &blackboardv2.Error{Code: "invalid_schema", Message: "HTTP body must not contain transport-owned fields", Path: field, Details: map[string]any{"reason": "additional_field"}}
		}
		encodedValue, _ := json.Marshal(value)
		fields[field] = encodedValue
	}
	contractRaw, err := json.Marshal(fields)
	if err != nil {
		return nil, blackboardV2HTTPError("invalid_schema", "request body cannot be encoded", "body")
	}
	return contractRaw, nil
}

func readBlackboardV2HTTPBody(request *http.Request) ([]byte, *blackboardv2.Error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, blackboardV2HTTPInputLimit+1))
	if err != nil {
		return nil, blackboardV2HTTPError("invalid_schema", "read request body: "+err.Error(), "body")
	}
	if len(raw) > blackboardV2HTTPInputLimit {
		return nil, blackboardV2HTTPError("invalid_schema", "request body exceeds 4 MiB", "body")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, blackboardV2HTTPError("invalid_schema", "request body is required", "body")
	}
	return raw, nil
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
