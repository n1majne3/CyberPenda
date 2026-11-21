package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

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
	return SemanticHealth{
		Schema: healthSchema, Revision: projection.Snapshot.Revision, Status: healthStatusFromAnomalies(anomalies),
		Attention: attention, Anomalies: anomalies,
	}, nil
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
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, reason
		FROM blackboard_v2_session_relationships WHERE session_id=? AND (from_key=? OR to_key=?)
		ORDER BY from_key ASC, relation ASC, to_key ASC, reason ASC`, sessionID, key, key)
	if err != nil {
		return nil, fmt.Errorf("read Session Blackboard relationships: %w", err)
	}
	defer rows.Close()
	return scanSessionCurrentRelationships(rows)
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
	return scanSessionCurrentRelationships(rows)
}

func scanSessionCurrentRelationships(rows *sql.Rows) ([]RelationshipTuple, error) {
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
