package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"pentest/internal/blackboardv2grammar"
)

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

// Each reachability check runs in one statement. UNION deduplicates visited
// keys, including when retained data already contains a cycle. The transaction
// sees relationships added by earlier operations in the same Change Batch.
func sessionRelationshipWouldCycle(ctx context.Context, tx *sql.Tx, sessionID, relation, fromKey, toKey string) (bool, error) {
	var cycle int
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE reachable(key) AS (
  SELECT ?
  UNION
  SELECT rel.to_key FROM blackboard_v2_session_relationships rel
  JOIN reachable ON rel.from_key=reachable.key
  WHERE rel.session_id=? AND rel.relation=?
 ) SELECT EXISTS(SELECT 1 FROM reachable WHERE key=?)`, toKey, sessionID, relation, fromKey).Scan(&cycle)
	if err != nil {
		return false, fmt.Errorf("read Session %s relationships: %w", relation, err)
	}
	return cycle != 0, nil
}

func sessionFactSupportsWouldCycle(ctx context.Context, tx *sql.Tx, sessionID, fromKey, toKey string) (bool, error) {
	if fromKey == toKey {
		return true, nil
	}
	var cycle int
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE reachable(key) AS (
  SELECT ?
  UNION
  SELECT rel.to_key FROM blackboard_v2_session_relationships rel
  JOIN reachable ON rel.from_key=reachable.key
  JOIN blackboard_v2_session_records source
   ON source.session_id=rel.session_id AND source.key=rel.from_key AND source.type='fact'
  JOIN blackboard_v2_session_records target
   ON target.session_id=rel.session_id AND target.key=rel.to_key AND target.type='fact'
  WHERE rel.session_id=? AND rel.relation='supports'
 ) SELECT EXISTS(SELECT 1 FROM reachable WHERE key=?)`, toKey, sessionID, fromKey).Scan(&cycle)
	if err != nil {
		return false, fmt.Errorf("read Session Fact supports relationships: %w", err)
	}
	return cycle != 0, nil
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

func sessionIncomingInvalidatingFactKeys(ctx context.Context, tx *sql.Tx, sessionID, objectiveKey string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT rel.from_key
		FROM blackboard_v2_session_relationships AS rel
		JOIN blackboard_v2_session_records AS source
		  ON source.session_id=rel.session_id AND source.key=rel.from_key
		WHERE rel.session_id=? AND rel.relation='invalidates' AND rel.to_key=? AND source.type='fact'
		ORDER BY rel.from_key ASC`, sessionID, objectiveKey)
	if err != nil {
		return nil, fmt.Errorf("read Session Objective invalidation proof: %w", err)
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan Session Objective invalidation proof: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Session Objective invalidation proof: %w", err)
	}
	return keys, nil
}
