package projectinterface

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/blackboard"
)

// tokenEncoding is unpadded base64url: safe in Authorization headers, query
// strings, and environment variables, and round-trips without escaping.
var tokenEncoding = base64.RawURLEncoding

// Project-interface error codes (runtime protocol §3). The graph contract's
// domain error codes remain canonical and are passed through from the graph
// service; these codes are owned by this module.
const (
	// ErrCodeInvalidRequest is a malformed request envelope, unsupported
	// protocol version, or structural problem that never reaches the graph.
	ErrCodeInvalidRequest = "invalid_request"
	// ErrCodeProvenanceSpoofed is a request-body attempt to supply Project,
	// Task, Continuation, Runtime Profile, Runner, actor, or timestamp fields
	// that the trusted grant owns (runtime protocol §4.1).
	ErrCodeProvenanceSpoofed = "provenance_spoofed"
	// ErrCodeProjectMismatch is a path or declared Project that disagrees with
	// the grant's bound Project (runtime protocol §12.4).
	ErrCodeProjectMismatch = "project_mismatch"
	// ErrCodeProjectNotFound is a bound Project that does not exist.
	ErrCodeProjectNotFound = "project_not_found"
	// ErrCodeGrantNotFound is a missing, unknown, or malformed Continuation
	// Interface Grant bearer token.
	ErrCodeGrantNotFound = "grant_not_found"
	// ErrCodeContinuationClosed is a new Runtime write attempted after the
	// grant finished, was revoked, or became terminal. Exact replay and reads
	// remain available.
	ErrCodeContinuationClosed = "continuation_closed"
	// ErrCodeContinuationOpenAttempts rejects Finish while canonical main
	// Attempts created by the current Continuation remain open.
	ErrCodeContinuationOpenAttempts = "continuation_open_attempts"
	// ErrCodeContinuationFinishConflict rejects reuse of a Finish idempotency
	// key with a changed summary or Objective Outcome.
	ErrCodeContinuationFinishConflict = "continuation_finish_conflict"
	// ErrCodeContinuationContextRequired is a task-only operation lacking a
	// valid Continuation grant.
	ErrCodeContinuationContextRequired = "continuation_context_required"
	// ErrCodeActorForbidden is a bound actor not permitted to request the
	// operation (runtime protocol §5).
	ErrCodeActorForbidden = "actor_forbidden"
	// ErrCodeStorageBusy is retryable SQLite writer lock exhaustion that
	// consumes no idempotency key.
	ErrCodeStorageBusy = "storage_busy"
	// ErrCodeSourceEventNotFound is a referenced Task Event that does not exist.
	ErrCodeSourceEventNotFound = "source_event_not_found"
	// ErrCodeSourceEventMismatch is a Task Event outside the bound Task or
	// Continuation provenance context.
	ErrCodeSourceEventMismatch = "source_event_mismatch"
	// ErrCodeEvidenceSourceForbidden rejects traversal or a resolved source
	// outside the caller's trusted Runtime/operator roots.
	ErrCodeEvidenceSourceForbidden = "evidence_source_forbidden"
	// ErrCodeEvidenceSourceChanged reports a missing or replaced source across
	// a retry of the same Retain Evidence request.
	ErrCodeEvidenceSourceChanged = "evidence_source_changed"
	// ErrCodeSnapshotUnavailable is a concrete launch-readiness failure: the
	// pinned full graph could not be rendered, materialized, or hash-verified.
	ErrCodeSnapshotUnavailable = "snapshot_unavailable"
)

// Error is the stable ProjectInterfaceErrorV1 shape (runtime protocol §3). It is
// the only failure envelope returned by the project-interface module; transport
// adapters map it to their status/exit conventions without reclassification.
type Error struct {
	ProtocolVersion int            `json:"protocol_version"`
	Code            string         `json:"code"`
	Message         string         `json:"message"`
	OperationIndex  *int           `json:"operation_index,omitempty"`
	OpID            string         `json:"op_id,omitempty"`
	Path            string         `json:"path,omitempty"`
	Retryable       bool           `json:"retryable"`
	Details         map[string]any `json:"details,omitempty"`
	RequestID       string         `json:"request_id,omitempty"`
}

func (e *Error) Error() string {
	if e.OpID != "" {
		return fmt.Sprintf("%s (%s): %s", e.Code, e.OpID, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ValidationError builds a non-retryable ProjectInterfaceErrorV1 at the given
// path. Operation-scoped fields are filled by the graph service for domain
// errors and by the module for envelope-level errors.
func ValidationError(code, message, path string) *Error {
	return &Error{ProtocolVersion: RuntimeProtocolVersion, Code: code, Message: message, Path: path}
}

// InternalError builds a 500-mapped ProjectInterfaceErrorV1 for an unexpected
// failure that is not a domain or envelope validation error (runtime protocol
// §12.4: 500 is reserved for unexpected internal/integrity failures).
func InternalError(message string) *Error {
	return &Error{ProtocolVersion: RuntimeProtocolVersion, Code: ErrCodeInternal, Message: message, Path: "internal"}
}

func persistenceError(action string, err error) *Error {
	message := action + ": " + err.Error()
	lower := strings.ToLower(message)
	if strings.Contains(lower, "database is locked") || strings.Contains(lower, "database is busy") ||
		strings.Contains(lower, "sqlite_busy") || strings.Contains(lower, "sqlite_locked") {
		return &Error{
			ProtocolVersion: RuntimeProtocolVersion,
			Code:            ErrCodeStorageBusy,
			Message:         "SQLite writer lock is busy",
			Path:            "storage",
			Retryable:       true,
		}
	}
	return InternalError(message)
}

// ErrCodeInternal is the interface code for unexpected internal/integrity
// failures that are not graph domain validation or request-envelope errors. It
// maps to HTTP 500 (runtime protocol §12.4).
const ErrCodeInternal = "internal"

// AsError extracts a project-interface *Error from err, returning nil when err
// is not one. Graph-service domain errors are converted by mapProjectError
// before they reach callers, so a non-Error here is treated as an internal
// failure by the caller.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var validation *blackboard.ValidationError
	if errors.As(err, &validation) {
		return &Error{
			ProtocolVersion: RuntimeProtocolVersion,
			Code:            validation.Code, Message: validation.Message, Path: validation.Path,
			Retryable: false, Details: validation.Details,
		}
	}
	return nil
}
