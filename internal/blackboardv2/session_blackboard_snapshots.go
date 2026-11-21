package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

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

// SessionRuntimeSnapshotProjection returns the canonical bytes and attention
// measurement for a Session Working Snapshot.
func (s *Service) SessionRuntimeSnapshotProjection(ctx context.Context, sessionID string) (RuntimeSnapshotProjection, error) {
	snapshot, err := s.SessionRuntimeSnapshot(ctx, sessionID)
	if err != nil {
		return RuntimeSnapshotProjection{}, err
	}
	return projectRuntimeSnapshot(snapshot)
}

// ProjectSessionRuntimeSnapshot is retained as a compatibility alias for
// callers that used the original owner-neutral projection name.
func (s *Service) ProjectSessionRuntimeSnapshot(ctx context.Context, sessionID string) (RuntimeSnapshotProjection, error) {
	return s.SessionRuntimeSnapshotProjection(ctx, sessionID)
}

func (s *Service) persistSessionSnapshot(ctx context.Context, tx *sql.Tx, sessionID, continuationID, now string) error {
	snapshot, err := s.sessionRuntimeSnapshotTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	return s.persistSessionSnapshotValue(ctx, tx, sessionID, continuationID, now, snapshot)
}

func (s *Service) persistSessionSnapshotValue(ctx context.Context, tx *sql.Tx, sessionID, continuationID, now string, snapshot RuntimeSnapshot) error {
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
