package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SessionEntityRecord is the Session Blackboard Entity shape. A Session has
// no Project scope, so scope_status is intentionally absent from this DTO.
type SessionEntityRecord struct {
	Status        string `json:"status"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Locator       string `json:"locator,omitempty"`
	Description   string `json:"description,omitempty"`
	CredentialRef string `json:"credential_ref,omitempty"`
}

type SessionEntityPatch struct {
	Kind          *string `json:"kind,omitempty"`
	Name          *string `json:"name,omitempty"`
	Locator       *string `json:"locator,omitempty"`
	Description   *string `json:"description,omitempty"`
	CredentialRef *string `json:"credential_ref,omitempty"`
}

// SessionFactRecord is a Session Fact. The name is deliberate: it does not
// fabricate Project scope or imply a Project-owned conclusion.
type SessionFactRecord struct {
	Category   string `json:"category"`
	Summary    string `json:"summary"`
	Body       string `json:"body,omitempty"`
	Confidence string `json:"confidence"`
}

type SessionFactPatch struct {
	Category *string `json:"category,omitempty"`
	Summary  *string `json:"summary,omitempty"`
	Body     *string `json:"body,omitempty"`
}

// SessionContinuationSnapshot is the immutable launch or mutable working
// snapshot bound to one trusted Session Continuation.
type SessionContinuationSnapshot struct {
	Schema   string `json:"schema"`
	Revision int    `json:"revision"`
	Bytes    []byte `json:"bytes"`
	SHA256   string `json:"sha256"`
}

// SessionContinuationPin binds exactly one Session owner to one launch pin.
// The launch snapshot never changes; successful owner writes advance only the
// Working snapshot.
type SessionContinuationPin struct {
	ContinuationID string                      `json:"continuation_id"`
	SessionID      string                      `json:"-"`
	Launch         SessionContinuationSnapshot `json:"launch"`
	Working        SessionContinuationSnapshot `json:"working"`
}

// ApplyForSession applies the shared semantic-change-batch/v2 kernel to one
// durable Non-Project Session. Session state is stored in Session-owned
// tables; a Project is never synthesized or consulted.
func (s *Service) ApplyForSession(ctx context.Context, sessionID string, batch ChangeBatch) (ChangeResult, error) {
	return s.applySession(ctx, sessionID, "", nil, batch)
}

// ApplyForSessionAtRevision is the Session counterpart to the Project
// Runtime optimistic-concurrency entry point.
func (s *Service) ApplyForSessionAtRevision(ctx context.Context, sessionID string, expectedBaseRevision int, batch ChangeBatch) (ChangeResult, error) {
	if expectedBaseRevision < 0 {
		return ChangeResult{}, semanticError("semantic_validation", "base_revision must not be negative", "base_revision", nil)
	}
	return s.applySession(ctx, sessionID, "", &expectedBaseRevision, batch)
}

// ApplyForSessionContinuation applies a trusted Session Runtime write. The
// continuation binding is server-derived and is kept out of the semantic
// payload and result DTOs.
func (s *Service) ApplyForSessionContinuation(ctx context.Context, sessionID, continuationID string, batch ChangeBatch) (ChangeResult, error) {
	if strings.TrimSpace(continuationID) == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	return s.applySession(ctx, sessionID, continuationID, nil, batch)
}

func validateSessionChangeCapability(change Change, index int) error {
	path := fmt.Sprintf("changes[%d]", index)
	if change.Op == "merge" || isOneOf(change.Type, "finding", "solution", "evidence") {
		return sessionCapabilityError(path, "Project-only Blackboard capability is unavailable to a Session")
	}
	if isOneOf(change.Status, "verified", "rejected", "false_positive", "missing") {
		return sessionCapabilityError(path+".status", "Project-only lifecycle state is unavailable to a Session")
	}
	if (change.Op == "relate" || change.Op == "unrelate") && change.Relation == "evidences" {
		return sessionCapabilityError(path+".relation", "Evidence relationships are Project-only")
	}
	if change.Type == "entity" || change.Type == "fact" {
		for _, field := range change.Clear {
			if field == "scope_status" {
				return sessionCapabilityError(path+".clear", "Session records do not have Project scope")
			}
		}
	}
	if change.Type == "entity" {
		switch record := change.Record.(type) {
		case SessionEntityRecord:
		case *SessionEntityRecord:
		case EntityRecord:
			if record.ScopeStatus != "" {
				return sessionCapabilityError(path+".record.scope_status", "Session Entities do not have Project scope")
			}
		case *EntityRecord:
			if record != nil && record.ScopeStatus != "" {
				return sessionCapabilityError(path+".record.scope_status", "Session Entities do not have Project scope")
			}
		case EntityPatch:
			if record.ScopeStatus != nil {
				return sessionCapabilityError(path+".record.scope_status", "Session Entities do not have Project scope")
			}
		case *EntityPatch:
			if record != nil && record.ScopeStatus != nil {
				return sessionCapabilityError(path+".record.scope_status", "Session Entities do not have Project scope")
			}
		case json.RawMessage:
			if jsonFieldPresent(record, "scope_status") {
				return sessionCapabilityError(path+".record.scope_status", "Session Entities do not have Project scope")
			}
		}
	}
	if change.Type == "fact" {
		switch record := change.Record.(type) {
		case SessionFactRecord:
		case *SessionFactRecord:
		case FactRecord:
			if record.ScopeStatus != "" {
				return sessionCapabilityError(path+".record.scope_status", "Session Facts do not have Project scope")
			}
		case *FactRecord:
			if record != nil && record.ScopeStatus != "" {
				return sessionCapabilityError(path+".record.scope_status", "Session Facts do not have Project scope")
			}
		case FactPatch:
			if record.ScopeStatus != nil {
				return sessionCapabilityError(path+".record.scope_status", "Session Facts do not have Project scope")
			}
		case *FactPatch:
			if record != nil && record.ScopeStatus != nil {
				return sessionCapabilityError(path+".record.scope_status", "Session Facts do not have Project scope")
			}
		case json.RawMessage:
			if jsonFieldPresent(record, "scope_status") {
				return sessionCapabilityError(path+".record.scope_status", "Session Facts do not have Project scope")
			}
		}
	}
	return nil
}

func validateSessionChangeDTOShape(change Change, index int) error {
	if err := validateChangeShape(change, index); err != nil {
		return err
	}
	path := fmt.Sprintf("changes[%d].record", index)
	switch {
	case change.Op == "create" && change.Type == "entity":
		switch change.Record.(type) {
		case SessionEntityRecord, *SessionEntityRecord:
			_, err := completeSessionEntityRecord(change.Record, path)
			return err
		default:
			_, err := completeEntityRecord(change.Record, path)
			return err
		}
	case change.Op == "create" && change.Type == "fact":
		switch change.Record.(type) {
		case SessionFactRecord, *SessionFactRecord:
			_, err := completeSessionFactRecord(change.Record, path)
			return err
		default:
			_, err := completeFactRecord(change.Record, path)
			return err
		}
	case change.Op == "update" && change.Type == "entity":
		switch change.Record.(type) {
		case SessionEntityPatch, *SessionEntityPatch:
			_, err := partialSessionEntityRecord(change.Record, path)
			return err
		default:
			_, err := partialEntityRecord(change.Record, path)
			return err
		}
	case change.Op == "update" && change.Type == "fact":
		switch change.Record.(type) {
		case SessionFactPatch, *SessionFactPatch:
			_, err := partialSessionFactRecord(change.Record, path)
			return err
		default:
			_, err := partialFactRecord(change.Record, path)
			return err
		}
	default:
		return validateChangeDTOShape(change, index)
	}
}

func completeSessionEntityRecord(value any, path string) (EntityRecord, error) {
	switch record := value.(type) {
	case SessionEntityRecord:
		return EntityRecord{Status: record.Status, Kind: record.Kind, Name: record.Name, Locator: record.Locator, Description: record.Description, CredentialRef: record.CredentialRef}, nil
	case *SessionEntityRecord:
		if record == nil {
			return EntityRecord{}, semanticError("semantic_validation", "Session Entity record is required", path, nil)
		}
		return EntityRecord{Status: record.Status, Kind: record.Kind, Name: record.Name, Locator: record.Locator, Description: record.Description, CredentialRef: record.CredentialRef}, nil
	case json.RawMessage:
		var decoded SessionEntityRecord
		if err := strictDecodeJSON(record, &decoded); err != nil {
			return EntityRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return EntityRecord{Status: decoded.Status, Kind: decoded.Kind, Name: decoded.Name, Locator: decoded.Locator, Description: decoded.Description, CredentialRef: decoded.CredentialRef}, nil
	default:
		return completeEntityRecord(value, path)
	}
}

func partialSessionEntityRecord(value any, path string) (EntityPatch, error) {
	switch patch := value.(type) {
	case SessionEntityPatch:
		if patch.Kind == nil && patch.Name == nil && patch.Locator == nil && patch.Description == nil && patch.CredentialRef == nil {
			return EntityPatch{}, semanticError("semantic_validation", "Session Entity partial record requires at least one property", path, nil)
		}
		return EntityPatch{Kind: patch.Kind, Name: patch.Name, Locator: patch.Locator, Description: patch.Description, CredentialRef: patch.CredentialRef}, nil
	case *SessionEntityPatch:
		if patch == nil {
			return EntityPatch{}, semanticError("semantic_validation", "Session Entity update requires a partial record", path, nil)
		}
		return partialSessionEntityRecord(*patch, path)
	default:
		return partialEntityRecord(value, path)
	}
}

func completeSessionFactRecord(value any, path string) (FactRecord, error) {
	switch record := value.(type) {
	case SessionFactRecord:
		return FactRecord{Category: record.Category, Summary: record.Summary, Body: record.Body, Confidence: record.Confidence}, nil
	case *SessionFactRecord:
		if record == nil {
			return FactRecord{}, semanticError("semantic_validation", "Session Fact record is required", path, nil)
		}
		return FactRecord{Category: record.Category, Summary: record.Summary, Body: record.Body, Confidence: record.Confidence}, nil
	case json.RawMessage:
		var decoded SessionFactRecord
		if err := strictDecodeJSON(record, &decoded); err != nil {
			return FactRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return FactRecord{Category: decoded.Category, Summary: decoded.Summary, Body: decoded.Body, Confidence: decoded.Confidence}, nil
	default:
		return completeFactRecord(value, path)
	}
}

func partialSessionFactRecord(value any, path string) (FactPatch, error) {
	switch patch := value.(type) {
	case SessionFactPatch:
		if patch.Category == nil && patch.Summary == nil && patch.Body == nil {
			return FactPatch{}, semanticError("semantic_validation", "Session Fact partial record requires at least one property", path, nil)
		}
		return FactPatch{Category: patch.Category, Summary: patch.Summary, Body: patch.Body}, nil
	case *SessionFactPatch:
		if patch == nil {
			return FactPatch{}, semanticError("semantic_validation", "Session Fact update requires a partial record", path, nil)
		}
		return partialSessionFactRecord(*patch, path)
	default:
		return partialFactRecord(value, path)
	}
}

func sessionCapabilityError(path, message string) *Error {
	return semanticError("owner_capability_denied", message, path, nil)
}

func validateSessionEntityRecord(record EntityRecord, path string) error {
	if record.Status != "active" {
		return semanticError("semantic_validation", "Entity status must be active", path+".status", nil)
	}
	if !isAcceptedEntityKind(record.Kind) {
		return semanticError("semantic_validation", "Entity kind is not accepted", path+".kind", nil)
	}
	if err := validateConciseText(record.Name, path+".name"); err != nil {
		return err
	}
	if containsSecretMarker(record.Name) {
		return semanticError("semantic_validation", "Entity name must not contain secrets", path+".name", nil)
	}
	if record.Locator != "" {
		if err := validateLocator(record.Locator, path+".locator"); err != nil {
			return err
		}
	}
	if record.Description != "" {
		if err := validateConciseText(record.Description, path+".description"); err != nil {
			return err
		}
		if containsSecretMarker(record.Description) {
			return semanticError("semantic_validation", "Entity description must not contain secrets", path+".description", nil)
		}
	}
	if record.CredentialRef != "" {
		if err := validateCredentialRef(record.CredentialRef, path+".credential_ref"); err != nil {
			return err
		}
	}
	return nil
}

func validateSessionFactRecord(record FactRecord, path string) error {
	if err := validateConciseText(record.Category, path+".category"); err != nil {
		return err
	}
	if err := validateSemanticText(record.Summary, path+".summary"); err != nil {
		return err
	}
	if record.Body != "" && !utf8.ValidString(record.Body) {
		return semanticError("semantic_validation", "Fact body must be valid UTF-8", path+".body", nil)
	}
	if record.Confidence != "tentative" && record.Confidence != "confirmed" {
		return semanticError("semantic_validation", "Fact confidence must be tentative or confirmed", path+".confidence", nil)
	}
	return nil
}

func ensureSessionExists(ctx context.Context, tx *sql.Tx, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return semanticError("not_found", "session not found", "session", nil)
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=?)`, sessionID).Scan(&exists); err != nil {
		return fmt.Errorf("check Session Blackboard owner: %w", err)
	}
	if exists == 0 {
		return semanticError("not_found", "session not found", "session", nil)
	}
	return nil
}

func ensureSessionState(ctx context.Context, tx *sql.Tx, sessionID string) (string, error) {
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM sessions WHERE id=?`, sessionID).Scan(&lifecycle); errors.Is(err, sql.ErrNoRows) {
		return "", semanticError("not_found", "session not found", "session", nil)
	} else if err != nil {
		return "", fmt.Errorf("read Session Blackboard owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_state(session_id, revision) VALUES (?, 0)
		ON CONFLICT(session_id) DO NOTHING`, sessionID); err != nil {
		return "", fmt.Errorf("ensure Session Blackboard state: %w", err)
	}
	return lifecycle, nil
}

func sessionCurrentRevision(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM blackboard_v2_session_state WHERE session_id=?`, sessionID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("read Session Blackboard revision: %w", err)
	}
	return revision, nil
}

func sessionCurrentRevisionOrZero(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	revision, err := sessionCurrentRevision(ctx, tx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

func sessionIncrementRevision(ctx context.Context, tx *sql.Tx, sessionID string, current int) (int, error) {
	next := current + 1
	result, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_session_state SET revision=? WHERE session_id=? AND revision=?`, next, sessionID, current)
	if err != nil {
		return 0, fmt.Errorf("advance Session Blackboard revision: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return 0, fmt.Errorf("count Session Blackboard revision advance: %w", err)
	} else if affected != 1 {
		return 0, semanticError("version_conflict", "Session Blackboard revision changed", "revision", nil)
	}
	return next, nil
}

func loadSessionCurrentRecord(ctx context.Context, tx *sql.Tx, sessionID, key string) (storedRecord, error) {
	var found storedRecord
	var raw string
	err := tx.QueryRowContext(ctx, `
		SELECT key, type, version, record_json
		FROM blackboard_v2_session_records WHERE session_id=? AND key=?`, sessionID, key).Scan(&found.key, &found.typ, &found.version, &raw)
	if err != nil {
		return storedRecord{}, err
	}
	record, err := decodeStoredRecord(found.typ, raw)
	if err != nil {
		return storedRecord{}, fmt.Errorf("decode Session Blackboard record: %w", err)
	}
	found.record = record
	return found, nil
}

func sessionIdempotencyReplay(ctx context.Context, tx *sql.Tx, sessionID, continuationID, key, requestHash string) (ChangeResult, bool, error) {
	var storedHash, raw, storedContinuationID string
	err := tx.QueryRowContext(ctx, `
		SELECT request_hash, result_json, continuation_id
		FROM blackboard_v2_session_idempotency_receipts WHERE session_id=? AND idempotency_key=?`, sessionID, key).Scan(&storedHash, &raw, &storedContinuationID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeResult{}, false, nil
	}
	if err != nil {
		return ChangeResult{}, false, fmt.Errorf("read Session Blackboard idempotency receipt: %w", err)
	}
	if storedContinuationID != continuationID {
		return ChangeResult{}, false, semanticError("authority_denied", "idempotency receipt belongs to another trusted origin", "idempotency_key", nil)
	}
	if storedHash != requestHash {
		return ChangeResult{}, false, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", nil)
	}
	var result ChangeResult
	if err := decodeJSON([]byte(raw), &result); err != nil {
		return ChangeResult{}, false, fmt.Errorf("decode Session Blackboard idempotency receipt: %w", err)
	}
	return result, true, nil
}
