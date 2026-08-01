package blackboardv2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"pentest/internal/blackboardv2grammar"
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

// SessionEntityPatch is the Session Blackboard Entity update shape.
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

// SessionFactPatch is the Session Fact update shape.
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

// ApplyForSessionContinuationAtRevision is the Session counterpart to the
// Project Runtime optimistic-concurrency entry point. Exact replays are
// resolved before the base-revision precondition inside applySession.
func (s *Service) ApplyForSessionContinuationAtRevision(ctx context.Context, sessionID, continuationID string, expectedBaseRevision int, batch ChangeBatch) (ChangeResult, error) {
	if strings.TrimSpace(continuationID) == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if expectedBaseRevision < 0 {
		return ChangeResult{}, semanticError("semantic_validation", "base_revision must not be negative", "base_revision", nil)
	}
	return s.applySession(ctx, sessionID, continuationID, &expectedBaseRevision, batch)
}

// ReadSessionCurrent reads current Session-owned semantic state by local Key.
func (s *Service) ReadSessionCurrent(ctx context.Context, sessionID, key string) (CurrentDetail, error) {
	if err := validateKey(key, "key"); err != nil {
		return CurrentDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CurrentDetail{}, fmt.Errorf("begin Session Blackboard read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionExists(ctx, tx, sessionID); err != nil {
		return CurrentDetail{}, err
	}
	revision, err := sessionCurrentRevisionOrZero(ctx, tx, sessionID)
	if err != nil {
		return CurrentDetail{}, err
	}
	found, err := loadSessionCurrentRecord(ctx, tx, sessionID, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CurrentDetail{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), "key", map[string]any{"key": key})
		}
		return CurrentDetail{}, err
	}
	relationships, err := loadSessionCurrentRelationshipsForKey(ctx, tx, sessionID, found.key)
	if err != nil {
		return CurrentDetail{}, err
	}
	return CurrentDetail{
		Schema: recordSchema, Revision: revision, Key: found.key, Type: found.typ,
		Version: found.version, Record: found.record, Relationships: relationships,
	}, nil
}

// ReadSessionHistory reads deterministic Session Semantic History using the
// same signed cursor contract as Project Blackboard. The cursor is bound to
// the Session owner instead of a Project owner.
func (s *Service) ReadSessionHistory(ctx context.Context, sessionID, key string, options HistoryOptions) (SemanticHistory, error) {
	if err := validateKey(key, "key"); err != nil {
		return SemanticHistory{}, err
	}
	if options.Limit < 0 || options.Limit > 100 {
		return SemanticHistory{}, semanticError("semantic_validation", "history limit must be between 1 and 100", "limit", nil)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("begin Session Blackboard history read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionExists(ctx, tx, sessionID); err != nil {
		return SemanticHistory{}, err
	}
	cursorKey, err := loadHistoryCursorKey(ctx, tx)
	if err != nil {
		return SemanticHistory{}, err
	}
	cursor, err := parseCursor(options.Cursor, sessionID, cursorKey)
	if err != nil {
		return SemanticHistory{}, err
	}
	limit := options.Limit
	if cursor.present {
		if limit == 0 {
			limit = cursor.limit
		} else if limit != cursor.limit {
			return SemanticHistory{}, invalidHistoryCursorError("page_size_mismatch")
		}
	} else if limit == 0 {
		limit = 20
	}
	revision, err := sessionCurrentRevisionOrZero(ctx, tx, sessionID)
	if err != nil {
		return SemanticHistory{}, err
	}
	if cursor.present && cursor.key != key {
		return SemanticHistory{}, invalidHistoryCursorError("key_mismatch")
	}
	if cursor.present && cursor.revision != revision {
		return SemanticHistory{}, semanticError("semantic_validation", "history cursor is stale", "cursor", map[string]any{
			"reason": "stale_cursor", "cursor_revision": float64(cursor.revision),
			"current_revision": float64(revision), "next_action": "restart_history_read",
		})
	}
	var currentExists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM blackboard_v2_session_records WHERE session_id=? AND key=?)`, sessionID, key).Scan(&currentExists); err != nil {
		return SemanticHistory{}, fmt.Errorf("check Session Blackboard current record: %w", err)
	}
	total, err := countSessionHistoryItems(ctx, tx, sessionID, key)
	if err != nil {
		return SemanticHistory{}, err
	}
	if currentExists == 0 && total == 0 {
		return SemanticHistory{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), "key", map[string]any{"key": key})
	}
	offset := cursor.offset
	if cursor.present && offset >= total {
		return SemanticHistory{}, invalidHistoryCursorError("offset_out_of_range")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT identity_key, kind, version, type, record_json, from_key, relation, to_key, reason
		FROM (
			SELECT key AS identity_key, 'record' AS kind, version, type, record_json,
			       '' AS from_key, '' AS relation, '' AS to_key, '' AS reason,
			       recorded_at AS sort_time, 0 AS sort_group
			FROM blackboard_v2_session_record_history
			WHERE session_id=? AND key=?
			UNION ALL
			SELECT from_key || char(0) || relation || char(0) || to_key AS identity_key,
			       'relationship' AS kind, version, '' AS type, '' AS record_json,
			       from_key, relation, to_key, reason, recorded_at AS sort_time, 1 AS sort_group
			FROM blackboard_v2_session_relationship_history
			WHERE session_id=? AND (from_key=? OR to_key=?)
		)
		ORDER BY sort_group ASC, sort_time ASC, version ASC, identity_key ASC,
		         relation ASC, from_key ASC, to_key ASC
		LIMIT ? OFFSET ?`, sessionID, key, sessionID, key, key, limit, offset)
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("read Session Blackboard history: %w", err)
	}
	defer rows.Close()
	items := make([]HistoryItem, 0, limit)
	for rows.Next() {
		var identityKey, kind, typ, raw, from, relation, to, reason string
		var version int
		if err := rows.Scan(&identityKey, &kind, &version, &typ, &raw, &from, &relation, &to, &reason); err != nil {
			return SemanticHistory{}, fmt.Errorf("scan Session Blackboard history: %w", err)
		}
		if kind == "record" {
			record, err := decodeStoredRecord(typ, raw)
			if err != nil {
				return SemanticHistory{}, fmt.Errorf("decode Session Blackboard history record: %w", err)
			}
			items = append(items, HistoryItem{Kind: kind, Key: identityKey, Version: version, Type: typ, Record: &record})
		} else {
			items = append(items, HistoryItem{Kind: kind, Version: version, From: from, Relation: relation, To: to, Reason: reason})
		}
	}
	if err := rows.Err(); err != nil {
		return SemanticHistory{}, fmt.Errorf("iterate Session Blackboard history: %w", err)
	}
	next := ""
	if offset+len(items) < total {
		next, err = makeCursor(revision, key, limit, offset+len(items), sessionID, cursorKey)
		if err != nil {
			return SemanticHistory{}, err
		}
	}
	return SemanticHistory{Schema: historySchema, Revision: revision, Key: key, Items: items, NextCursor: next}, nil
}

// SessionRuntimeSnapshot returns the complete current Session projection. It
// contains only Session-supported work, knowledge, and relationships.
func (s *Service) SessionRuntimeSnapshot(ctx context.Context, sessionID string) (RuntimeSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("begin Session Blackboard snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
}

// ProjectSessionRuntimeSnapshot returns the canonical bytes and attention
// measurement for a Session Working Snapshot.
func (s *Service) ProjectSessionRuntimeSnapshot(ctx context.Context, sessionID string) (RuntimeSnapshotProjection, error) {
	snapshot, err := s.SessionRuntimeSnapshot(ctx, sessionID)
	if err != nil {
		return RuntimeSnapshotProjection{}, err
	}
	return projectRuntimeSnapshot(snapshot)
}

// SessionSemanticHealth reuses the v2 health diagnosis while omitting
// Project-only records, redirect grants, evidence checks, and proposals.
func (s *Service) SessionSemanticHealth(ctx context.Context, sessionID string) (SemanticHealth, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SemanticHealth{}, fmt.Errorf("begin Session semantic health: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionExists(ctx, tx, sessionID); err != nil {
		return SemanticHealth{}, err
	}
	snapshot, err := s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	if err != nil {
		return SemanticHealth{}, err
	}
	_, storedRelations, err := loadSessionRelationshipsForHealth(ctx, tx, sessionID)
	if err != nil {
		return SemanticHealth{}, err
	}
	typeByKey := make(map[string]string)
	for key := range snapshot.Work.Objectives {
		typeByKey[key] = "objective"
	}
	for key := range snapshot.Work.Attempts {
		typeByKey[key] = "attempt"
	}
	for key := range snapshot.Knowledge.Entities {
		typeByKey[key] = "entity"
	}
	for key := range snapshot.Knowledge.Facts {
		typeByKey[key] = "fact"
	}
	projection, err := projectRuntimeSnapshot(snapshot)
	if err != nil {
		return SemanticHealth{}, err
	}
	anomalies := relationshipIntegrityAnomalies(storedRelations, typeByKey)
	anomalies = append(anomalies, attentionAnomalies(projection, true)...)
	anomalies = append(anomalies, snapshotSemanticAnomalies(snapshot)...)
	anomalies = dedupeHealthAnomalies(anomalies)
	sortHealthAnomalies(anomalies)
	attention := HealthAttention{
		Bytes: projection.ByteCount, EstimatedTokens: projection.EstimatedTokens,
		State: projection.AttentionState, Complete: projection.Complete, Launchable: projection.Launchable,
		// Session health reuses the shared byte/token attention limits, but
		// Project-only consolidation is not a Session capability.
		ConsolidationOffered: false, ConsolidationRequired: false,
	}
	proposals := make([]HealthProposal, 0)
	return SemanticHealth{
		Schema: healthSchema, Revision: projection.Snapshot.Revision, Status: healthStatusFromAnomalies(anomalies),
		Attention: attention, Anomalies: anomalies, Proposals: proposals,
	}, nil
}

// BindSessionContinuation captures the exact current Session snapshot as an
// immutable launch pin. Rebinding an existing continuation returns its
// original pin and never overwrites it.
func (s *Service) BindSessionContinuation(ctx context.Context, sessionID, continuationID string) (SessionContinuationPin, error) {
	if strings.TrimSpace(continuationID) == "" {
		return SessionContinuationPin{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("begin Session Continuation pin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lifecycle, err := ensureSessionState(ctx, tx, sessionID)
	if err != nil {
		return SessionContinuationPin{}, err
	}
	if lifecycle != "open" {
		return SessionContinuationPin{}, semanticError("owner_archived", "archived Session cannot bind a new Continuation Blackboard pin", "", nil)
	}
	if err := validateSessionContinuation(ctx, tx, sessionID, continuationID); err != nil {
		return SessionContinuationPin{}, err
	}
	if pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID); err != nil {
		return SessionContinuationPin{}, err
	} else if pin.ContinuationID != "" {
		if err := tx.Commit(); err != nil {
			return SessionContinuationPin{}, fmt.Errorf("commit Session Continuation pin replay: %w", err)
		}
		return pin, nil
	}
	snapshot, err := s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	if err != nil {
		return SessionContinuationPin{}, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("encode Session launch snapshot: %w", err)
	}
	digest := sessionSnapshotDigest(encoded)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_snapshots(session_id, revision, snapshot_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET revision=excluded.revision, snapshot_json=excluded.snapshot_json, updated_at=excluded.updated_at`,
		sessionID, snapshot.Revision, string(encoded), now); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("store Session snapshot at Continuation bind: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_continuation_pins
		(continuation_id, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		 working_revision, working_snapshot_json, working_snapshot_sha256, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, continuationID, sessionID, snapshot.Revision, string(encoded), digest,
		snapshot.Revision, string(encoded), digest, now, now); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("store Session Continuation launch pin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("commit Session Continuation launch pin: %w", err)
	}
	return SessionContinuationPin{
		ContinuationID: continuationID, SessionID: sessionID,
		Launch:  SessionContinuationSnapshot{Schema: snapshot.Schema, Revision: snapshot.Revision, Bytes: append([]byte(nil), encoded...), SHA256: digest},
		Working: SessionContinuationSnapshot{Schema: snapshot.Schema, Revision: snapshot.Revision, Bytes: append([]byte(nil), encoded...), SHA256: digest},
	}, nil
}

// RebindSessionContinuation carries the immutable launch snapshot and mutable
// Working Snapshot ownership across a persistent interrupt-and-replace turn.
// The old pin remains as history, while the replacement becomes the only
// trusted owner allowed to write after the old Continuation is settled.
func (s *Service) RebindSessionContinuation(ctx context.Context, sessionID, oldContinuationID, nextContinuationID string) error {
	if strings.TrimSpace(oldContinuationID) == "" || strings.TrimSpace(nextContinuationID) == "" || oldContinuationID == nextContinuationID {
		return semanticError("authority_denied", "distinct Session Continuation identities are required", "continuation_id", nil)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Session Continuation rebind: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lifecycle, err := ensureSessionState(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if lifecycle != "open" {
		return semanticError("owner_archived", "archived Session cannot rebind a Continuation Blackboard pin", "", nil)
	}
	if _, err := sessionContinuationStatus(ctx, tx, sessionID, oldContinuationID); err != nil {
		return err
	}
	if err := validateSessionContinuation(ctx, tx, sessionID, nextContinuationID); err != nil {
		return err
	}
	if err := s.ensureSessionContinuationPinTx(ctx, tx, sessionID, oldContinuationID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO blackboard_v2_session_continuation_pins
		(continuation_id, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		 working_revision, working_snapshot_json, working_snapshot_sha256, created_at, updated_at)
		SELECT ?, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		       working_revision, working_snapshot_json, working_snapshot_sha256, ?, ?
		FROM blackboard_v2_session_continuation_pins
		WHERE session_id=? AND continuation_id=?`, nextContinuationID, now, now, sessionID, oldContinuationID); err != nil {
		return fmt.Errorf("carry Session Continuation Blackboard pin: %w", err)
	}
	var copied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_session_continuation_pins WHERE session_id=? AND continuation_id=?`, sessionID, nextContinuationID).Scan(&copied); err != nil {
		return fmt.Errorf("verify Session Continuation Blackboard pin: %w", err)
	}
	if copied != 1 {
		return semanticError("authority_denied", "Session Continuation Blackboard pin is unavailable", "continuation_id", nil)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Session Continuation rebind: %w", err)
	}
	return nil
}

// ReadSessionContinuationPin returns the server-bound launch and Working
// snapshots without exposing Session identity inside either snapshot JSON.
func (s *Service) ReadSessionContinuationPin(ctx context.Context, sessionID, continuationID string) (SessionContinuationPin, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("begin Session Continuation pin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionExists(ctx, tx, sessionID); err != nil {
		return SessionContinuationPin{}, err
	}
	pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID)
	if err != nil {
		return SessionContinuationPin{}, err
	}
	if pin.ContinuationID == "" {
		return SessionContinuationPin{}, semanticError("not_found", "Session Continuation pin not found", "", nil)
	}
	return pin, nil
}

// FinishSessionContinuation closes the trusted Session Continuation while
// retaining the Session aggregate and its Blackboard. The finish receipt is
// owner-bound and exactly replayable.
func (s *Service) FinishSessionContinuation(ctx context.Context, sessionID, continuationID, idempotencyKey string) (FinishContinuationResult, error) {
	if strings.TrimSpace(continuationID) == "" {
		return FinishContinuationResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return FinishContinuationResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("begin Session Continuation finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := ensureSessionState(ctx, tx, sessionID); err != nil {
		return FinishContinuationResult{}, err
	}
	requestHash := sessionFinishRequestHash(idempotencyKey)
	var storedKey, storedHash, raw string
	err = tx.QueryRowContext(ctx, `
		SELECT idempotency_key, request_hash, result_json FROM blackboard_v2_session_finish_receipts
		WHERE session_id=? AND continuation_id=?
		ORDER BY created_at ASC, idempotency_key ASC LIMIT 1`, sessionID, continuationID).Scan(&storedKey, &storedHash, &raw)
	if err == nil {
		if storedKey != idempotencyKey || storedHash != requestHash {
			return FinishContinuationResult{}, semanticError("finish_conflict", "Continuation was already finished with different semantics", "idempotency_key", nil)
		}
		var result FinishContinuationResult
		if err := decodeJSON([]byte(raw), &result); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("decode Session Continuation finish replay: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("commit Session Continuation finish replay: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return FinishContinuationResult{}, fmt.Errorf("read Session Continuation finish receipt: %w", err)
	}
	var receiptOwner string
	err = tx.QueryRowContext(ctx, `
		SELECT continuation_id FROM blackboard_v2_session_finish_receipts
		WHERE session_id=? AND idempotency_key=? LIMIT 1`, sessionID, idempotencyKey).Scan(&receiptOwner)
	if err == nil && receiptOwner != continuationID {
		return FinishContinuationResult{}, semanticError("authority_denied", "Finish idempotency receipt belongs to another trusted origin", "idempotency_key", nil)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FinishContinuationResult{}, fmt.Errorf("read Session Continuation finish key owner: %w", err)
	}
	if err := validateSessionContinuation(ctx, tx, sessionID, continuationID); err != nil {
		return FinishContinuationResult{}, err
	}
	pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if pin.ContinuationID == "" {
		return FinishContinuationResult{}, semanticError("authority_denied", "Session Continuation has no Blackboard launch pin", "continuation_id", nil)
	}
	currentOwner, pinned, err := sessionContinuationOwnsCurrentPath(ctx, tx, sessionID, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if pinned && !currentOwner {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation no longer owns the Session Working Snapshot", "", nil)
	}
	var openAttempts int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM blackboard_v2_session_records
		WHERE session_id=? AND type='attempt' AND json_extract(record_json, '$.status')='open'`, sessionID).Scan(&openAttempts); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("check open Session Attempts: %w", err)
	}
	if openAttempts != 0 {
		return FinishContinuationResult{}, semanticError("continuation_open_attempts", "Session Continuation cannot finish while an Attempt is open", "", nil)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.persistSessionSnapshot(ctx, tx, sessionID, continuationID, now); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("synchronize Session Working Snapshot for Finish: %w", err)
	}
	revision, err := sessionCurrentRevision(ctx, tx, sessionID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	result := FinishContinuationResult{
		Schema: finishResultSchema, Status: "finished", Revision: revision,
		WorkingSnapshot: WorkingSnapshot{Path: workingPath, Revision: revision},
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("encode Session Continuation finish result: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE session_continuations SET status='completed', updated_at=?, ended_at=?
		WHERE id=? AND session_id=? AND status IN ('pending','running')`, now, now, continuationID, sessionID)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("finish Session Continuation: %w", err)
	}
	if affected, err := updated.RowsAffected(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("count Session Continuation finish: %w", err)
	} else if affected != 1 {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Session writes", "", nil)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_finish_receipts
		(session_id, continuation_id, idempotency_key, request_hash, result_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, continuationID, idempotencyKey, requestHash, string(resultJSON), now); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("store Session Continuation finish receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("commit Session Continuation finish: %w", err)
	}
	return result, nil
}

// CheckpointSessionAttemptForContinuation records a compact owner-local
// summary for a running Session Attempt using the shared semantic kernel.
func (s *Service) CheckpointSessionAttemptForContinuation(ctx context.Context, sessionID, continuationID string, request CheckpointAttemptRequest) (ChangeResult, error) {
	if strings.TrimSpace(continuationID) == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if request.IdempotencyKey == "" {
		return ChangeResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if err := validateKey(request.Key, "key"); err != nil {
		return ChangeResult{}, err
	}
	if request.Version < 1 {
		return ChangeResult{}, semanticError("semantic_validation", "version must be positive", "version", nil)
	}
	if err := validateSemanticText(request.Summary, "summary"); err != nil {
		return ChangeResult{}, err
	}
	return s.ApplyForSessionContinuation(ctx, sessionID, continuationID, ChangeBatch{
		Schema: changeBatchSchema, IdempotencyKey: request.IdempotencyKey,
		Changes: []Change{{Op: "update", Key: request.Key, Version: request.Version, Type: "attempt", Record: AttemptPatch{Summary: &request.Summary}}},
	})
}

func sessionFinishRequestHash(idempotencyKey string) string {
	digest := sha256.Sum256([]byte("session-continuation-finish\x00" + idempotencyKey))
	return hex.EncodeToString(digest[:])
}

func (s *Service) applySession(ctx context.Context, sessionID, continuationID string, expectedBaseRevision *int, batch ChangeBatch) (ChangeResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if strings.TrimSpace(sessionID) == "" {
		return ChangeResult{}, semanticError("not_found", "session not found", "session", nil)
	}
	if batch.Schema != changeBatchSchema {
		return ChangeResult{}, semanticError("invalid_schema", "unsupported semantic change schema", "", nil)
	}
	if batch.IdempotencyKey == "" {
		return ChangeResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if batch.Changes == nil {
		return ChangeResult{}, semanticError("semantic_validation", "changes must be a non-null array", "changes", nil)
	}
	for index, change := range batch.Changes {
		if err := validateSessionChangeDTOShape(change, index); err != nil {
			return ChangeResult{}, err
		}
	}
	requestHash, err := canonicalRequestHash(batch)
	if err != nil {
		return ChangeResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin Session Blackboard change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	lifecycle, err := ensureSessionState(ctx, tx, sessionID)
	if err != nil {
		return ChangeResult{}, err
	}
	if replay, ok, err := sessionIdempotencyReplay(ctx, tx, sessionID, continuationID, batch.IdempotencyKey, requestHash); err != nil {
		return ChangeResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit Session Blackboard replay: %w", err)
		}
		return replay, nil
	}
	if continuationID != "" {
		if err := validateSessionContinuation(ctx, tx, sessionID, continuationID); err != nil {
			return ChangeResult{}, err
		}
		currentOwner, pinned, err := sessionContinuationOwnsCurrentPath(ctx, tx, sessionID, continuationID)
		if err != nil {
			return ChangeResult{}, err
		}
		if pinned && !currentOwner {
			return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation no longer owns the Session Working Snapshot", "", nil)
		}
		if err := s.ensureSessionContinuationPinTx(ctx, tx, sessionID, continuationID); err != nil {
			return ChangeResult{}, err
		}
	}
	if lifecycle != "open" {
		return ChangeResult{}, semanticError("owner_archived", "Session is archived and does not accept new Blackboard writes", "", nil)
	}
	revision, err := sessionCurrentRevision(ctx, tx, sessionID)
	if err != nil {
		return ChangeResult{}, err
	}
	if expectedBaseRevision != nil && revision != *expectedBaseRevision {
		return ChangeResult{}, semanticError("version_conflict", "Session Blackboard revision changed", "base_revision", map[string]any{
			"expected_revision": float64(*expectedBaseRevision), "current_revision": float64(revision),
			"next_action": "synchronize_runtime_blackboard",
		})
	}
	changedRecords := make(map[string]int)
	changedRelations := make(map[string]RelationVersionTuple)
	createdThisBatch := make(map[string]bool)
	terminalAttempts := make(map[string]terminalAttemptValidation)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, change := range batch.Changes {
		if err := validateSessionChangeCapability(change, index); err != nil {
			return ChangeResult{}, err
		}
		var changed bool
		switch change.Op {
		case "create":
			var key string
			var version int
			var next int
			next, key, version, changed, err = sessionApplyCreate(ctx, tx, sessionID, revision, index, change, now)
			if err == nil && changed {
				revision, changedRecords[key] = next, version
				createdThisBatch[key] = true
			}
		case "update":
			var key string
			var version int
			var next int
			next, key, version, changed, err = sessionApplyUpdate(ctx, tx, sessionID, revision, index, change, now)
			if err == nil && changed {
				revision, changedRecords[key] = next, version
			}
		case "relate":
			var tuple RelationVersionTuple
			var next int
			next, tuple, changed, err = sessionApplyRelate(ctx, tx, sessionID, revision, index, change, now)
			if err == nil && changed {
				revision, changedRelations[relationKey(tuple)] = next, tuple
			}
		case "unrelate":
			var tuple RelationVersionTuple
			var next int
			next, tuple, err = sessionApplyUnrelate(ctx, tx, sessionID, revision, index, change, now)
			if err == nil {
				revision, changedRelations[relationKey(tuple)] = next, tuple
				changed = true
			}
		case "transition":
			var key string
			var version int
			var next int
			next, key, version, changed, err = sessionApplyTransition(ctx, tx, sessionID, revision, index, change, now)
			if err == nil && changed {
				revision, changedRecords[key] = next, version
				if isOneOf(change.Status, "succeeded", "failed", "blocked", "inconclusive") {
					terminalAttempts[key] = terminalAttemptValidation{status: change.Status, path: fmt.Sprintf("changes[%d].status", index)}
				}
			}
		case "supersede":
			var key string
			var version int
			var tuple RelationVersionTuple
			var next int
			next, key, version, tuple, changed, err = sessionApplySupersede(ctx, tx, sessionID, revision, index, change, createdThisBatch, now)
			if err == nil && changed {
				revision, changedRecords[key], changedRelations[relationKey(tuple)] = next, version, tuple
			}
		default:
			err = semanticError("semantic_validation", "unsupported Session Blackboard operation", fmt.Sprintf("changes[%d].op", index), nil)
		}
		if err != nil {
			return ChangeResult{}, err
		}
	}
	if err := sessionValidateFinalTerminalAttempts(ctx, tx, sessionID, terminalAttempts); err != nil {
		return ChangeResult{}, err
	}
	result := makeChangeResult(revision, changedRecords, changedRelations)
	if err := s.persistSessionSnapshot(ctx, tx, sessionID, continuationID, now); err != nil {
		return ChangeResult{}, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("encode Session Blackboard result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_idempotency_receipts
		(session_id, idempotency_key, continuation_id, request_hash, result_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, batch.IdempotencyKey, continuationID, requestHash, string(resultJSON), now); err != nil {
		return ChangeResult{}, fmt.Errorf("store Session Blackboard idempotency receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit Session Blackboard change: %w", err)
	}
	return result, nil
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

func sessionApplyCreate(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	var typ string
	var record any
	var err error
	switch change.Type {
	case "entity":
		var entity EntityRecord
		entity, err = completeSessionEntityRecord(change.Record, path+".record")
		if err == nil {
			err = validateSessionEntityRecord(entity, path+".record")
		}
		typ, record = "entity", entity
	case "objective":
		var objective ObjectiveRecord
		objective, err = completeObjectiveRecord(change.Record, path+".record")
		if err == nil {
			err = validateObjectiveRecord(objective, path+".record")
		}
		typ, record = "objective", objective
	case "attempt":
		var attempt AttemptRecord
		attempt, err = completeAttemptRecord(change.Record, path+".record")
		if err == nil {
			err = validateAttemptRecord(attempt, path+".record")
		}
		typ, record = "attempt", attempt
	case "fact":
		var fact FactRecord
		fact, err = completeSessionFactRecord(change.Record, path+".record")
		if err == nil {
			err = validateSessionFactRecord(fact, path+".record")
		}
		typ, record = "fact", fact
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Session Blackboard record type", path+".type", nil)
	}
	if err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Key)
	if err == nil {
		if existing.typ == typ && sessionRecordsEqual(existing.record, record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_v2_session_record_history WHERE session_id=? AND key=?)`, sessionID, change.Key).Scan(&used); err != nil {
		return revision, "", 0, false, fmt.Errorf("check Session Blackboard historical Key: %w", err)
	}
	if used != 0 {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	return sessionInsertRecord(ctx, tx, sessionID, revision, change.Key, typ, record, now)
}

func sessionApplyUpdate(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if err != nil {
		return revision, "", 0, false, err
	}
	if existing.typ != change.Type {
		return revision, "", 0, false, semanticError("semantic_validation", "record type mismatch", path+".type", map[string]any{"key": change.Key})
	}
	if change.Version != existing.version {
		return revision, "", 0, false, sessionVersionConflict(path+".version", change.Key, change.Version, existing.version)
	}
	var next any
	switch existing.typ {
	case "entity":
		patch, patchErr := partialSessionEntityRecord(change.Record, path+".record")
		if patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		if len(change.Clear) > 0 {
			// applyEntityPatch owns the closed clear-field vocabulary.
		}
		entity, patchErr := applyEntityPatch(existing.record.entityRecord(), patch, change.Clear, path+".clear")
		if patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		if patchErr = validateSessionEntityRecord(entity, path+".record"); patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		next = entity
	case "objective":
		if len(change.Clear) != 0 {
			return revision, "", 0, false, semanticError("semantic_validation", "Objective update does not accept clear", path+".clear", nil)
		}
		patch, patchErr := partialObjectiveRecord(change.Record, path+".record")
		if patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		objective := existing.record.objectiveRecord()
		if patch.Objective != nil {
			objective.Objective = *patch.Objective
		}
		if patchErr = validateObjectiveRecord(objective, path+".record"); patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		next = objective
	case "attempt":
		if len(change.Clear) != 0 {
			return revision, "", 0, false, semanticError("semantic_validation", "Attempt update does not accept clear", path+".clear", nil)
		}
		patch, patchErr := partialAttemptRecord(change.Record, path+".record")
		if patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		attempt := existing.record.attemptRecord()
		if patch.Summary != nil {
			attempt.Summary = *patch.Summary
		}
		if patchErr = validateAttemptRecord(attempt, path+".record"); patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		next = attempt
	case "fact":
		patch, patchErr := partialSessionFactRecord(change.Record, path+".record")
		if patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		fact, patchErr := applyFactPatch(existing.record.factRecord(), patch, change.Clear, path+".clear")
		if patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		if patchErr = validateSessionFactRecord(fact, path+".record"); patchErr != nil {
			return revision, "", 0, false, patchErr
		}
		next = fact
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Session Blackboard record type", path+".type", nil)
	}
	if sessionRecordsEqual(existing.record, next) {
		return revision, change.Key, existing.version, false, nil
	}
	return sessionReplaceCurrentRecord(ctx, tx, sessionID, revision, existing, next, now)
}

func sessionVersionConflict(path, key string, expected, current int) *Error {
	return semanticError("version_conflict", fmt.Sprintf("%s changed", key), path, map[string]any{
		"key": key, "expected_version": float64(expected), "current_version": float64(current), "next_action": "read_current_record",
	})
}

func sessionRecordsEqual(existing Record, next any) bool {
	switch record := next.(type) {
	case EntityRecord:
		return entitiesEqual(existing.entityRecord(), record)
	case ObjectiveRecord:
		return objectivesEqual(existing.objectiveRecord(), record)
	case AttemptRecord:
		return attemptsEqual(existing.attemptRecord(), record)
	case FactRecord:
		return factsEqual(existing.factRecord(), record)
	default:
		return false
	}
}

func sessionInsertRecord(ctx context.Context, tx *sql.Tx, sessionID string, revision int, key, typ string, record any, now string) (int, string, int, bool, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode Session %s record: %w", typ, err)
	}
	next, err := sessionIncrementRevision(ctx, tx, sessionID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_records
		(session_id, key, type, version, record_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)`, sessionID, key, typ, string(encoded), now, now); err != nil {
		return revision, "", 0, false, fmt.Errorf("store Session Blackboard %s: %w", typ, err)
	}
	return next, key, 1, true, nil
}

func sessionReplaceCurrentRecord(ctx context.Context, tx *sql.Tx, sessionID string, revision int, existing storedRecord, nextRecord any, now string) (int, string, int, bool, error) {
	prior, err := json.Marshal(existing.record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode prior Session %s: %w", existing.typ, err)
	}
	nextJSON, err := json.Marshal(nextRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode updated Session %s: %w", existing.typ, err)
	}
	nextRevision, err := sessionIncrementRevision(ctx, tx, sessionID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_record_history
		(session_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, existing.key, existing.version, existing.typ, string(prior), now); err != nil {
		return revision, "", 0, false, fmt.Errorf("store prior Session %s: %w", existing.typ, err)
	}
	nextVersion := existing.version + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_session_records SET version=?, record_json=?, updated_at=?
		WHERE session_id=? AND key=?`, nextVersion, string(nextJSON), now, sessionID, existing.key); err != nil {
		return revision, "", 0, false, fmt.Errorf("update Session Blackboard %s: %w", existing.typ, err)
	}
	return nextRevision, existing.key, nextVersion, true, nil
}

func sessionApplyRelate(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, now string) (int, RelationVersionTuple, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.From, path+".from"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if err := validateKey(change.To, path+".to"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if change.From == change.To {
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship self-links are invalid", path+".to", nil)
	}
	if change.Relation == "evidences" {
		return revision, RelationVersionTuple{}, false, sessionCapabilityError(path+".relation", "Evidence relationships are Project-only")
	}
	if err := validateRelationshipReason(change.Relation, change.Reason, path+".reason"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	fromRecord, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.From)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.From), path+".from", map[string]any{"key": change.From})
	}
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	toRecord, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.To)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.To), path+".to", map[string]any{"key": change.To})
	}
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if err := validateRelationshipEndpoint(change.Relation, fromRecord.typ, toRecord.typ, path+".relation"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	rule, _ := blackboardv2grammar.Lookup(change.Relation)
	if rule.CyclePolicy == "project_fact_to_project_fact_acyclic" && fromRecord.typ == "fact" && toRecord.typ == "fact" {
		wouldCycle, err := sessionFactSupportsWouldCycle(ctx, tx, sessionID, change.From, change.To)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if wouldCycle {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "Fact supports relationships must be acyclic", path+".to", nil)
		}
	}
	if isOneOf(rule.CyclePolicy, "acyclic_per_endpoint_family", "acyclic") {
		wouldCycle, err := sessionRelationshipWouldCycle(ctx, tx, sessionID, change.Relation, change.From, change.To)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if wouldCycle {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", fmt.Sprintf("%s relationships must be acyclic", change.Relation), path+".to", nil)
		}
	}
	var currentVersion int
	var currentReason string
	err = tx.QueryRowContext(ctx, `
		SELECT version, reason FROM blackboard_v2_session_relationships
		WHERE session_id=? AND from_key=? AND relation=? AND to_key=?`, sessionID, change.From, change.Relation, change.To).Scan(&currentVersion, &currentReason)
	if err == nil {
		if currentReason == change.Reason {
			return revision, RelationVersionTuple{change.From, change.Relation, change.To, currentVersion}, false, nil
		}
		if change.Version == 0 {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "current relationship version is required when reason changes", path+".version", nil)
		}
		if change.Version != currentVersion {
			return revision, RelationVersionTuple{}, false, sessionVersionConflict(path+".version", change.From+" "+change.Relation+" "+change.To, change.Version, currentVersion)
		}
		next, err := sessionIncrementRevision(ctx, tx, sessionID, revision)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_session_relationship_history
			(session_id, from_key, relation, to_key, version, reason, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, change.From, change.Relation, change.To, currentVersion, currentReason, now); err != nil {
			return revision, RelationVersionTuple{}, false, fmt.Errorf("store prior Session relationship: %w", err)
		}
		nextVersion := currentVersion + 1
		if _, err := tx.ExecContext(ctx, `
			UPDATE blackboard_v2_session_relationships SET version=?, reason=?, updated_at=?
			WHERE session_id=? AND from_key=? AND relation=? AND to_key=?`, nextVersion, change.Reason, now, sessionID, change.From, change.Relation, change.To); err != nil {
			return revision, RelationVersionTuple{}, false, fmt.Errorf("update Session relationship reason: %w", err)
		}
		return next, RelationVersionTuple{change.From, change.Relation, change.To, nextVersion}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("read Session relationship: %w", err)
	}
	maxVersion, err := sessionMaxRelationshipVersion(ctx, tx, sessionID, change.From, change.Relation, change.To)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if change.Version != 0 {
		if maxVersion != 0 {
			return revision, RelationVersionTuple{}, false, semanticError("version_conflict", "relationship changed", path+".version", map[string]any{
				"from": change.From, "relation": change.Relation, "to": change.To, "expected_version": float64(change.Version), "current_version": float64(maxVersion), "current_state": "removed", "next_action": "read_current_record",
			})
		}
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship version is not accepted for a new relation", path+".version", nil)
	}
	nextVersion := maxVersion + 1
	next, err := sessionIncrementRevision(ctx, tx, sessionID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_relationships
		(session_id, from_key, relation, to_key, version, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sessionID, change.From, change.Relation, change.To, nextVersion, change.Reason, now, now); err != nil {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("store Session relationship: %w", err)
	}
	return next, RelationVersionTuple{change.From, change.Relation, change.To, nextVersion}, true, nil
}

func sessionApplyUnrelate(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, now string) (int, RelationVersionTuple, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if change.Relation == "evidences" {
		return revision, RelationVersionTuple{}, sessionCapabilityError(path+".relation", "Evidence relationships are Project-only")
	}
	for _, item := range []struct{ value, field string }{{change.From, "from"}, {change.To, "to"}} {
		if err := validateKey(item.value, path+"."+item.field); err != nil {
			return revision, RelationVersionTuple{}, err
		}
	}
	if change.From == change.To {
		return revision, RelationVersionTuple{}, semanticError("semantic_validation", "relationship self-links are invalid", path+".to", nil)
	}
	var currentVersion int
	var reason string
	err := tx.QueryRowContext(ctx, `
		SELECT version, reason FROM blackboard_v2_session_relationships
		WHERE session_id=? AND from_key=? AND relation=? AND to_key=?`, sessionID, change.From, change.Relation, change.To).Scan(&currentVersion, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, semanticError("not_found", "relationship was not found", path, nil)
	}
	if err != nil {
		return revision, RelationVersionTuple{}, fmt.Errorf("read Session relationship for removal: %w", err)
	}
	if change.Version != currentVersion {
		return revision, RelationVersionTuple{}, sessionVersionConflict(path+".version", change.From+" "+change.Relation+" "+change.To, change.Version, currentVersion)
	}
	next, err := sessionIncrementRevision(ctx, tx, sessionID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_relationship_history
		(session_id, from_key, relation, to_key, version, reason, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, change.From, change.Relation, change.To, currentVersion, reason, now); err != nil {
		return revision, RelationVersionTuple{}, fmt.Errorf("store removed Session relationship: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_session_relationships
		WHERE session_id=? AND from_key=? AND relation=? AND to_key=?`, sessionID, change.From, change.Relation, change.To); err != nil {
		return revision, RelationVersionTuple{}, fmt.Errorf("remove Session relationship: %w", err)
	}
	return next, RelationVersionTuple{change.From, change.Relation, change.To, currentVersion}, nil
}

func sessionApplyTransition(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if err != nil {
		return revision, "", 0, false, err
	}
	if change.Version != existing.version {
		return revision, "", 0, false, sessionVersionConflict(path+".version", change.Key, change.Version, existing.version)
	}
	var terminal any
	switch existing.typ {
	case "entity":
		if change.Status != "retired" {
			return revision, "", 0, false, semanticError("semantic_validation", "Entity transition status must be retired", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		next := existing.record.entityRecord()
		next.Status = "retired"
		terminal = next
	case "objective":
		if change.Status != "resolved" && change.Status != "abandoned" {
			return revision, "", 0, false, semanticError("semantic_validation", "Objective transition status must be resolved or abandoned", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		if change.Status == "resolved" {
			satisfied, err := sessionHasIncomingSatisfies(ctx, tx, sessionID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !satisfied {
				return revision, "", 0, false, semanticError("semantic_validation", "resolved Objective requires a current incoming satisfies relationship", path+".status", nil)
			}
		}
		next := existing.record.objectiveRecord()
		next.Status, next.ResolutionSummary = change.Status, change.ResolutionSummary
		terminal = next
	case "attempt":
		if !isOneOf(change.Status, "succeeded", "failed", "blocked", "inconclusive") {
			return revision, "", 0, false, semanticError("semantic_validation", "Attempt transition status is not terminal", path+".status", nil)
		}
		if err := validateSemanticText(change.Summary, path+".summary"); err != nil {
			return revision, "", 0, false, err
		}
		next := existing.record.attemptRecord()
		next.Status, next.Summary = change.Status, change.Summary
		terminal = next
	case "fact":
		if change.Status != "tentative" && change.Status != "confirmed" {
			return revision, "", 0, false, semanticError("semantic_validation", "Fact transition status must be tentative or confirmed", path+".status", nil)
		}
		next := existing.record.factRecord()
		if next.Confidence == change.Status {
			return revision, change.Key, existing.version, false, nil
		}
		next.Confidence = change.Status
		if err := validateSessionFactRecord(next, path+".status"); err != nil {
			return revision, "", 0, false, err
		}
		return sessionReplaceCurrentRecord(ctx, tx, sessionID, revision, existing, next, now)
	default:
		return revision, "", 0, false, sessionCapabilityError(path+".key", "Project-only Blackboard record type is unavailable to a Session")
	}
	return sessionTerminalizeRecord(ctx, tx, sessionID, revision, existing, terminal, now)
}

func sessionApplySupersede(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, createdThisBatch map[string]bool, now string) (int, string, int, RelationVersionTuple, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Replacement, path+".replacement"); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if err := validateKey(change.Replaced, path+".replaced"); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if change.Replacement == change.Replaced {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "supersede requires different replacement and replaced records", path+".replaced", nil)
	}
	replacement, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Replacement)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replacement), path+".replacement", map[string]any{"key": change.Replacement})
	}
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	replaced, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Replaced)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replaced), path+".replaced", map[string]any{"key": change.Replaced})
	}
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	rule, _ := blackboardv2grammar.Lookup("supersedes")
	if !rule.Allows(replacement.typ, replaced.typ) || !isOneOf(replacement.typ, "entity", "objective", "fact") {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "Session supersede requires two current records of the same supported type", path, nil)
	}
	replacementVersion := change.ReplacementVersion
	if replacementVersion == 0 {
		if !createdThisBatch[change.Replacement] || replacement.version != 1 {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "replacement_version may be omitted only for a version 1 replacement created earlier in the same batch", path+".replacement_version", nil)
		}
		replacementVersion = 1
	}
	if replacementVersion != replacement.version {
		return revision, "", 0, RelationVersionTuple{}, false, sessionVersionConflict(path+".replacement_version", change.Replacement, replacementVersion, replacement.version)
	}
	if change.ReplacedVersion != replaced.version {
		return revision, "", 0, RelationVersionTuple{}, false, sessionVersionConflict(path+".replaced_version", change.Replaced, change.ReplacedVersion, replaced.version)
	}
	var terminal any
	switch replaced.typ {
	case "entity":
		next := replaced.record.entityRecord()
		next.Status = "superseded"
		terminal = next
	case "objective":
		next := replaced.record.objectiveRecord()
		next.Status = "superseded"
		terminal = next
	case "fact":
		next := replaced.record.factRecord()
		// Session Facts retain their Session-local semantic identity while the
		// terminal history version records that the fact was deprecated. The
		// terminal history shape is shared with Project Facts; it is never a
		// current Session record and therefore bypasses current-record validation.
		next.Confidence = "deprecated"
		terminal = next
	}
	nextRevision, key, nextVersion, changed, err := sessionTerminalizeRecord(ctx, tx, sessionID, revision, replaced, terminal, now)
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_relationship_history
		(session_id, from_key, relation, to_key, version, reason, recorded_at)
		VALUES (?, ?, 'supersedes', ?, 1, '', ?)`, sessionID, change.Replacement, change.Replaced, now); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("store Session supersedes history: %w", err)
	}
	return nextRevision, key, nextVersion, RelationVersionTuple{change.Replacement, "supersedes", change.Replaced, 1}, changed, nil
}

func sessionTerminalizeRecord(ctx context.Context, tx *sql.Tx, sessionID string, revision int, existing storedRecord, terminal any, now string) (int, string, int, bool, error) {
	currentJSON, err := json.Marshal(existing.record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode current Session %s: %w", existing.typ, err)
	}
	terminalJSON, err := json.Marshal(terminal)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode terminal Session %s: %w", existing.typ, err)
	}
	nextVersion := existing.version + 1
	nextRevision, err := sessionIncrementRevision(ctx, tx, sessionID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	for version, encoded := range map[int]string{existing.version: string(currentJSON), nextVersion: string(terminalJSON)} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_session_record_history
			(session_id, key, version, type, record_json, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?)`, sessionID, existing.key, version, existing.typ, encoded, now); err != nil {
			return revision, "", 0, false, fmt.Errorf("store terminal Session %s history: %w", existing.typ, err)
		}
	}
	if err := sessionMoveCurrentRelationshipsToHistory(ctx, tx, sessionID, existing.key, now); err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_session_records WHERE session_id=? AND key=?`, sessionID, existing.key); err != nil {
		return revision, "", 0, false, fmt.Errorf("remove terminal Session %s: %w", existing.typ, err)
	}
	return nextRevision, existing.key, nextVersion, true, nil
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

func validateSessionContinuation(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) error {
	status, err := sessionContinuationStatus(ctx, tx, sessionID, continuationID)
	if err != nil {
		return err
	}
	if !isOneOf(status, "pending", "running") {
		return semanticError("closed_continuation", "trusted Continuation is closed for new Session Blackboard writes", "", nil)
	}
	return nil
}

func sessionContinuationOwnsCurrentPath(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) (current, pinned bool, err error) {
	var status string
	var number int
	err = tx.QueryRowContext(ctx, `
		SELECT continuation.number, continuation.status,
		       EXISTS(SELECT 1 FROM blackboard_v2_session_continuation_pins AS pin WHERE pin.continuation_id=continuation.id)
		FROM session_continuations AS continuation
		WHERE continuation.id=? AND continuation.session_id=?`, continuationID, sessionID).Scan(&number, &status, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, semanticError("authority_denied", "trusted Continuation does not own this Session interface", "", nil)
	}
	if err != nil {
		return false, false, fmt.Errorf("read Session Continuation Working Snapshot owner: %w", err)
	}
	if !pinned || !isOneOf(status, "pending", "running") {
		return false, pinned, nil
	}
	var newer int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM session_continuations
		WHERE session_id=? AND number>?`, sessionID, number).Scan(&newer); err != nil {
		return false, pinned, fmt.Errorf("check newer Session Continuations: %w", err)
	}
	return newer == 0, pinned, nil
}

func sessionContinuationStatus(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `
		SELECT status FROM session_continuations WHERE id=? AND session_id=?`, continuationID, sessionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", semanticError("authority_denied", "trusted Continuation does not own this Session interface", "", nil)
	}
	if err != nil {
		return "", fmt.Errorf("validate Session Continuation: %w", err)
	}
	return status, nil
}

func (s *Service) ensureSessionContinuationPinTx(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) error {
	if pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID); err != nil {
		return err
	} else if pin.ContinuationID != "" {
		return nil
	}
	snapshot, err := s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode Session launch snapshot: %w", err)
	}
	digest := sessionSnapshotDigest(encoded)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_continuation_pins
		(continuation_id, session_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		 working_revision, working_snapshot_json, working_snapshot_sha256, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, continuationID, sessionID, snapshot.Revision, string(encoded), digest,
		snapshot.Revision, string(encoded), digest, now, now); err != nil {
		return fmt.Errorf("store Session Continuation launch pin: %w", err)
	}
	return nil
}

func readSessionContinuationPinTx(ctx context.Context, tx *sql.Tx, sessionID, continuationID string) (SessionContinuationPin, error) {
	var pin SessionContinuationPin
	var launchJSON, workingJSON, launchDigest, workingDigest string
	var launchRevision, workingRevision int
	err := tx.QueryRowContext(ctx, `
		SELECT continuation_id, launch_revision, launch_snapshot_json, launch_snapshot_sha256,
		       working_revision, working_snapshot_json, working_snapshot_sha256
		FROM blackboard_v2_session_continuation_pins
		WHERE session_id=? AND continuation_id=?`, sessionID, continuationID).Scan(
		&pin.ContinuationID, &launchRevision, &launchJSON, &launchDigest,
		&workingRevision, &workingJSON, &workingDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionContinuationPin{}, nil
	}
	if err != nil {
		return SessionContinuationPin{}, fmt.Errorf("read Session Continuation pin: %w", err)
	}
	launchBytes := []byte(launchJSON)
	if err := verifyPinnedSnapshot(snapshotSchema, launchRevision, launchBytes, launchDigest); err != nil {
		return SessionContinuationPin{}, err
	}
	workingBytes := []byte(workingJSON)
	if err := verifySnapshotEnvelope(workingBytes, workingRevision); err != nil {
		return SessionContinuationPin{}, fmt.Errorf("%w: Session Working Snapshot integrity failure", ErrLaunchPinIntegrity)
	}
	workingSum := sha256.Sum256(workingBytes)
	if !strings.EqualFold(hex.EncodeToString(workingSum[:]), workingDigest) {
		return SessionContinuationPin{}, fmt.Errorf("%w: Session Working Snapshot digest mismatch", ErrLaunchPinIntegrity)
	}
	pin.SessionID = sessionID
	pin.Launch = SessionContinuationSnapshot{Schema: snapshotSchema, Revision: launchRevision, Bytes: []byte(launchJSON), SHA256: launchDigest}
	pin.Working = SessionContinuationSnapshot{Schema: snapshotSchema, Revision: workingRevision, Bytes: []byte(workingJSON), SHA256: workingDigest}
	return pin, nil
}

func sessionSnapshotDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
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

func sessionMaxRelationshipVersion(ctx context.Context, tx *sql.Tx, sessionID, from, relation, to string) (int, error) {
	var version int
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) FROM (
			SELECT version FROM blackboard_v2_session_relationships WHERE session_id=? AND from_key=? AND relation=? AND to_key=?
			UNION ALL
			SELECT version FROM blackboard_v2_session_relationship_history WHERE session_id=? AND from_key=? AND relation=? AND to_key=?
		)`, sessionID, from, relation, to, sessionID, from, relation, to).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read Session relationship identity version: %w", err)
	}
	return version, nil
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

func countSessionHistoryItems(ctx context.Context, tx *sql.Tx, sessionID, key string) (int, error) {
	var total int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM blackboard_v2_session_record_history WHERE session_id=? AND key=?) +
			(SELECT COUNT(*) FROM blackboard_v2_session_relationship_history WHERE session_id=? AND (from_key=? OR to_key=?))`, sessionID, key, sessionID, key, key).Scan(&total); err != nil {
		return 0, fmt.Errorf("count Session Blackboard history: %w", err)
	}
	return total, nil
}

func loadSessionCurrentRelationshipsForKey(ctx context.Context, tx *sql.Tx, sessionID, key string) ([]RelationshipTuple, error) {
	all, err := loadSessionCurrentRelationships(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	filtered := make([]RelationshipTuple, 0)
	for _, tuple := range all {
		if tuple[0] == key || tuple[2] == key {
			filtered = append(filtered, tuple)
		}
	}
	return filtered, nil
}

func loadSessionCurrentRelationships(ctx context.Context, tx *sql.Tx, sessionID string) ([]RelationshipTuple, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, reason
		FROM blackboard_v2_session_relationships WHERE session_id=?
		ORDER BY from_key ASC, relation ASC, to_key ASC, reason ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read Session Blackboard relationships: %w", err)
	}
	defer rows.Close()
	result := make([]RelationshipTuple, 0)
	for rows.Next() {
		var from, relation, to, reason string
		if err := rows.Scan(&from, &relation, &to, &reason); err != nil {
			return nil, fmt.Errorf("scan Session Blackboard relationship: %w", err)
		}
		if reason == "" {
			result = append(result, RelationshipTuple{from, relation, to})
		} else {
			result = append(result, RelationshipTuple{from, relation, to, reason})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Session Blackboard relationships: %w", err)
	}
	return result, nil
}

func loadSessionRelationshipsForHealth(ctx context.Context, tx *sql.Tx, sessionID string) ([]RelationshipTuple, []persistedRelationship, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT rel.from_key, rel.relation, rel.to_key, rel.reason, source.type, target.type
		FROM blackboard_v2_session_relationships AS rel
		LEFT JOIN blackboard_v2_session_records AS source
		  ON source.session_id=rel.session_id AND source.key=rel.from_key
		LEFT JOIN blackboard_v2_session_records AS target
		  ON target.session_id=rel.session_id AND target.key=rel.to_key
		WHERE rel.session_id=?
		ORDER BY rel.from_key ASC, rel.relation ASC, rel.to_key ASC, rel.reason ASC`, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("read Session health relationships: %w", err)
	}
	defer rows.Close()
	tuples := make([]RelationshipTuple, 0)
	stored := make([]persistedRelationship, 0)
	for rows.Next() {
		var from, relation, to, reason string
		var fromType, toType sql.NullString
		if err := rows.Scan(&from, &relation, &to, &reason, &fromType, &toType); err != nil {
			return nil, nil, fmt.Errorf("scan Session health relationship: %w", err)
		}
		if reason == "" {
			tuples = append(tuples, RelationshipTuple{from, relation, to})
		} else {
			tuples = append(tuples, RelationshipTuple{from, relation, to, reason})
		}
		stored = append(stored, persistedRelationship{from: from, relation: relation, to: to, fromType: fromType.String, toType: toType.String})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate Session health relationships: %w", err)
	}
	return tuples, stored, nil
}

func sessionMoveCurrentRelationshipsToHistory(ctx context.Context, tx *sql.Tx, sessionID, key, recordedAt string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, version, reason
		FROM blackboard_v2_session_relationships
		WHERE session_id=? AND (from_key=? OR to_key=?)
		ORDER BY from_key ASC, relation ASC, to_key ASC`, sessionID, key, key)
	if err != nil {
		return fmt.Errorf("read terminal Session relationships: %w", err)
	}
	type relationship struct {
		from, relation, to, reason string
		version                    int
	}
	items := make([]relationship, 0)
	for rows.Next() {
		var item relationship
		if err := rows.Scan(&item.from, &item.relation, &item.to, &item.version, &item.reason); err != nil {
			rows.Close()
			return fmt.Errorf("scan terminal Session relationship: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate terminal Session relationships: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close terminal Session relationships: %w", err)
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_session_relationship_history
			(session_id, from_key, relation, to_key, version, reason, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, item.from, item.relation, item.to, item.version, item.reason, recordedAt); err != nil {
			return fmt.Errorf("store terminal Session relationship history: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_session_relationships WHERE session_id=? AND (from_key=? OR to_key=?)`, sessionID, key, key); err != nil {
		return fmt.Errorf("remove terminal Session relationships: %w", err)
	}
	return nil
}

func sessionRelationshipWouldCycle(ctx context.Context, tx *sql.Tx, sessionID, relation, fromKey, toKey string) (bool, error) {
	if fromKey == toKey {
		return true, nil
	}
	visited := map[string]bool{}
	stack := []string{toKey}
	for len(stack) != 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if key == fromKey {
			return true, nil
		}
		if visited[key] {
			continue
		}
		visited[key] = true
		rows, err := tx.QueryContext(ctx, `
			SELECT to_key FROM blackboard_v2_session_relationships
			WHERE session_id=? AND from_key=? AND relation=? ORDER BY to_key ASC`, sessionID, key, relation)
		if err != nil {
			return false, fmt.Errorf("read Session %s relationships: %w", relation, err)
		}
		for rows.Next() {
			var target string
			if err := rows.Scan(&target); err != nil {
				rows.Close()
				return false, fmt.Errorf("scan Session %s relationships: %w", relation, err)
			}
			stack = append(stack, target)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, fmt.Errorf("iterate Session %s relationships: %w", relation, err)
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("close Session %s relationships: %w", relation, err)
		}
	}
	return false, nil
}

func sessionFactSupportsWouldCycle(ctx context.Context, tx *sql.Tx, sessionID, fromKey, toKey string) (bool, error) {
	if fromKey == toKey {
		return true, nil
	}
	visited := map[string]bool{}
	stack := []string{toKey}
	for len(stack) != 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if key == fromKey {
			return true, nil
		}
		if visited[key] {
			continue
		}
		visited[key] = true
		rows, err := tx.QueryContext(ctx, `
			SELECT rel.to_key
			FROM blackboard_v2_session_relationships AS rel
			JOIN blackboard_v2_session_records AS source
			  ON source.session_id=rel.session_id AND source.key=rel.from_key AND source.type='fact'
			JOIN blackboard_v2_session_records AS target
			  ON target.session_id=rel.session_id AND target.key=rel.to_key AND target.type='fact'
			WHERE rel.session_id=? AND rel.from_key=? AND rel.relation='supports'
			ORDER BY rel.to_key ASC`, sessionID, key)
		if err != nil {
			return false, fmt.Errorf("read Session Fact supports relationships: %w", err)
		}
		for rows.Next() {
			var target string
			if err := rows.Scan(&target); err != nil {
				rows.Close()
				return false, fmt.Errorf("scan Session Fact supports relationship: %w", err)
			}
			stack = append(stack, target)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, fmt.Errorf("iterate Session Fact supports relationships: %w", err)
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("close Session Fact supports relationships: %w", err)
		}
	}
	return false, nil
}

func sessionHasIncomingSatisfies(ctx context.Context, tx *sql.Tx, sessionID, objectiveKey string) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blackboard_v2_session_relationships AS rel
			JOIN blackboard_v2_session_records AS source
			  ON source.session_id=rel.session_id AND source.key=rel.from_key
			WHERE rel.session_id=? AND rel.relation='satisfies' AND rel.to_key=? AND source.type='fact'
		)`, sessionID, objectiveKey).Scan(&exists); err != nil {
		return false, fmt.Errorf("check Session Objective satisfaction: %w", err)
	}
	return exists == 1, nil
}

func sessionValidateFinalTerminalAttempts(ctx context.Context, tx *sql.Tx, sessionID string, attempts map[string]terminalAttemptValidation) error {
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		validation := attempts[key]
		var tested int
		if err := tx.QueryRowContext(ctx, `
			SELECT (SELECT COUNT(*) FROM blackboard_v2_session_relationships WHERE session_id=? AND from_key=? AND relation='tests') +
			       (SELECT COUNT(*) FROM blackboard_v2_session_relationship_history WHERE session_id=? AND from_key=? AND relation='tests')`, sessionID, key, sessionID, key).Scan(&tested); err != nil {
			return fmt.Errorf("count Session tested targets: %w", err)
		}
		if tested == 0 {
			return semanticError("semantic_validation", "terminal Attempt requires at least one tested target", validation.path, map[string]any{"key": key})
		}
		if validation.status != "succeeded" {
			continue
		}
		var outcomes []string
		rows, err := tx.QueryContext(ctx, `
			SELECT to_key FROM blackboard_v2_session_relationships WHERE session_id=? AND from_key=? AND relation='produced'
			UNION SELECT to_key FROM blackboard_v2_session_relationship_history WHERE session_id=? AND from_key=? AND relation='produced'
			ORDER BY to_key ASC`, sessionID, key, sessionID, key)
		if err != nil {
			return fmt.Errorf("read Session produced outcomes: %w", err)
		}
		for rows.Next() {
			var outcome string
			if err := rows.Scan(&outcome); err != nil {
				rows.Close()
				return fmt.Errorf("scan Session produced outcome: %w", err)
			}
			outcomes = append(outcomes, outcome)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate Session produced outcomes: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close Session produced outcomes: %w", err)
		}
		found := false
		for _, outcome := range outcomes {
			if _, err := loadSessionCurrentRecord(ctx, tx, sessionID, outcome); err == nil {
				found = true
				break
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if !found {
			return semanticError("semantic_validation", "succeeded Attempt requires a current produced outcome", validation.path, map[string]any{"key": key})
		}
	}
	return nil
}

func (s *Service) persistSessionSnapshot(ctx context.Context, tx *sql.Tx, sessionID, continuationID, now string) error {
	snapshot, err := s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode Session Working Snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_snapshots(session_id, revision, snapshot_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET revision=excluded.revision, snapshot_json=excluded.snapshot_json, updated_at=excluded.updated_at`, sessionID, snapshot.Revision, string(encoded), now); err != nil {
		return fmt.Errorf("store Session Working Snapshot: %w", err)
	}
	if continuationID != "" {
		digest := sessionSnapshotDigest(encoded)
		result, err := tx.ExecContext(ctx, `
			UPDATE blackboard_v2_session_continuation_pins
			SET working_revision=?, working_snapshot_json=?, working_snapshot_sha256=?, updated_at=?
			WHERE session_id=? AND continuation_id=?`, snapshot.Revision, string(encoded), digest, now, sessionID, continuationID)
		if err != nil {
			return fmt.Errorf("advance Session Continuation Working Snapshot: %w", err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("count Session Continuation Working Snapshot: %w", err)
		} else if affected != 1 {
			return semanticError("authority_denied", "trusted Continuation does not own this Session interface", "", nil)
		}
	}
	return nil
}

func (s *Service) sessionRuntimeSnapshotTx(ctx context.Context, tx *sql.Tx, sessionID string) (RuntimeSnapshot, error) {
	if err := ensureSessionExists(ctx, tx, sessionID); err != nil {
		return RuntimeSnapshot{}, err
	}
	revision, err := sessionCurrentRevisionOrZero(ctx, tx, sessionID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT key, type, version, record_json
		FROM blackboard_v2_session_records WHERE session_id=? ORDER BY key ASC`, sessionID)
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("read Session Blackboard snapshot records: %w", err)
	}
	defer rows.Close()
	entities := make(map[string]SnapshotEntity)
	objectives := make(map[string]SnapshotObjective)
	attempts := make(map[string]SnapshotAttempt)
	facts := make(map[string]SnapshotFact)
	for rows.Next() {
		var key, typ, raw string
		var version int
		if err := rows.Scan(&key, &typ, &version, &raw); err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("scan Session Blackboard snapshot record: %w", err)
		}
		record, err := decodeStoredRecord(typ, raw)
		if err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("decode Session Blackboard snapshot record: %w", err)
		}
		switch typ {
		case "entity":
			entity := record.entityRecord()
			entities[key] = SnapshotEntity{Version: version, Status: entity.Status, Kind: entity.Kind, Name: entity.Name, Locator: entity.Locator, Description: entity.Description, CredentialRef: entity.CredentialRef}
		case "objective":
			objective := record.objectiveRecord()
			objectives[key] = SnapshotObjective{Version: version, Status: objective.Status, Objective: objective.Objective}
		case "attempt":
			attempt := record.attemptRecord()
			attempts[key] = SnapshotAttempt{Version: version, Status: attempt.Status, Summary: attempt.Summary}
		case "fact":
			fact := record.factRecord()
			facts[key] = SnapshotFact{Version: version, Category: fact.Category, Summary: fact.Summary, Confidence: fact.Confidence}
		}
	}
	if err := rows.Err(); err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("iterate Session Blackboard snapshot records: %w", err)
	}
	work := SnapshotWork{}
	if len(objectives) > 0 {
		work.Objectives = objectives
	}
	if len(attempts) > 0 {
		work.Attempts = attempts
	}
	knowledge := SnapshotKnowledge{}
	if len(entities) > 0 {
		knowledge.Entities = entities
	}
	if len(facts) > 0 {
		knowledge.Facts = facts
	}
	relations, err := loadSessionCurrentRelationships(ctx, tx, sessionID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	return RuntimeSnapshot{Schema: snapshotSchema, Semantics: snapshotSemantics, Revision: revision, Work: work, Knowledge: knowledge, Relations: relations}, nil
}
