package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"pentest/internal/blackboardv2grammar"
)

func applyRelate(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, RelationVersionTuple, bool, error) {
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
	if err := validateRelationshipReason(change.Relation, change.Reason, path+".reason"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	fromRecord, err := loadCurrentRecord(ctx, tx, projectID, change.From)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.From), path+".from", map[string]any{"key": change.From})
		}
		return revision, RelationVersionTuple{}, false, err
	}
	toRecord, err := loadCurrentRecord(ctx, tx, projectID, change.To)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.To), path+".to", map[string]any{"key": change.To})
		}
		return revision, RelationVersionTuple{}, false, err
	}
	if err := validateRelationshipEndpoint(change.Relation, fromRecord.typ, toRecord.typ, path+".relation"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	rule, _ := blackboardv2grammar.Lookup(change.Relation)
	if rule.CyclePolicy == "project_fact_to_project_fact_acyclic" && fromRecord.typ == "fact" && toRecord.typ == "fact" {
		wouldCycle, err := factSupportsWouldCycle(ctx, tx, projectID, change.From, change.To)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if wouldCycle {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "Fact supports relationships must be acyclic", path+".to", nil)
		}
	}
	if isOneOf(rule.CyclePolicy, "acyclic_per_endpoint_family", "acyclic") {
		wouldCycle, err := relationshipWouldCycle(ctx, tx, projectID, change.Relation, change.From, change.To)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if wouldCycle {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", fmt.Sprintf("%s relationships must be acyclic", change.Relation), path+".to", nil)
		}
	}

	var existingVersion int
	var existingReason string
	err = tx.QueryRowContext(ctx, `
		SELECT version, reason
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND from_key = ? AND relation = ? AND to_key = ?`,
		projectID, change.From, change.Relation, change.To,
	).Scan(&existingVersion, &existingReason)
	if err == nil {
		if existingReason == change.Reason {
			return revision, RelationVersionTuple{change.From, change.Relation, change.To, existingVersion}, false, nil
		}
		if change.Version == 0 {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "current relationship version is required when reason changes", path+".version", nil)
		}
		if change.Version != existingVersion {
			return revision, RelationVersionTuple{}, false, semanticError("version_conflict", "relationship changed", path+".version", map[string]any{
				"from": change.From, "relation": change.Relation, "to": change.To, "expected_version": float64(change.Version), "current_version": float64(existingVersion), "next_action": "read_current_record",
			})
		}
		nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, change.From, change.Relation, change.To, existingVersion, existingReason, now,
		); err != nil {
			return revision, RelationVersionTuple{}, false, fmt.Errorf("store prior Blackboard v2 relationship: %w", err)
		}
		nextVersion := existingVersion + 1
		if _, err := tx.ExecContext(ctx, `
			UPDATE blackboard_v2_relationships
			SET version = ?, reason = ?, updated_at = ?
			WHERE project_id = ? AND from_key = ? AND relation = ? AND to_key = ?`,
			nextVersion, change.Reason, now, projectID, change.From, change.Relation, change.To,
		); err != nil {
			return revision, RelationVersionTuple{}, false, fmt.Errorf("update Blackboard v2 relationship reason: %w", err)
		}
		return nextRevision, RelationVersionTuple{change.From, change.Relation, change.To, nextVersion}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("read Blackboard v2 relationship: %w", err)
	}
	maxVersion, err := maxRelationshipVersion(ctx, tx, projectID, change.From, change.Relation, change.To)
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
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_relationships (project_id, from_key, relation, to_key, version, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, change.From, change.Relation, change.To, nextVersion, change.Reason, now, now,
	); err != nil {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("store Blackboard v2 relationship: %w", err)
	}
	return nextRevision, RelationVersionTuple{change.From, change.Relation, change.To, nextVersion}, true, nil
}

func maxRelationshipVersion(ctx context.Context, tx *sql.Tx, projectID, from, relation, to string) (int, error) {
	var version int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0)
		FROM (
			SELECT version FROM blackboard_v2_relationships WHERE project_id=? AND from_key=? AND relation=? AND to_key=?
			UNION ALL
			SELECT version FROM blackboard_v2_relationship_history WHERE project_id=? AND from_key=? AND relation=? AND to_key=?
		)`, projectID, from, relation, to, projectID, from, relation, to).Scan(&version); err != nil {
		return 0, fmt.Errorf("read Blackboard v2 relationship identity version: %w", err)
	}
	return version, nil
}

func validateRelationshipReason(relation, reason, path string) error {
	violation := blackboardv2grammar.ReasonViolation(relation, reason)
	if violation == "" {
		return nil
	}
	details := map[string]any{"relation": relation, "violation": violation}
	switch violation {
	case blackboardv2grammar.ReasonViolationForbidden:
		return semanticError("semantic_validation", "relationship reason is not allowed for this relation", path, details)
	case blackboardv2grammar.ReasonViolationRedundant:
		return semanticError("semantic_validation", "relationship reason must add semantic information beyond the relation type", path, details)
	default:
		return semanticError("semantic_validation", "concise semantic text is required and must fit the v2 limit", path, details)
	}
}

func currentOutgoingRelationshipCount(ctx context.Context, tx *sql.Tx, projectID, fromKey, relation string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND from_key = ? AND relation = ?`,
		projectID, fromKey, relation,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count current %s relationships: %w", relation, err)
	}
	return count, nil
}

func historicalOutgoingRelationshipCount(ctx context.Context, tx *sql.Tx, projectID, fromKey, relation string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_v2_relationship_history
		WHERE project_id = ? AND from_key = ? AND relation = ?`,
		projectID, fromKey, relation,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count historical %s relationships: %w", relation, err)
	}
	return count, nil
}

func hasIncomingSatisfies(ctx context.Context, tx *sql.Tx, projectID, objectiveKey string) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM blackboard_v2_relationships AS rel
			JOIN blackboard_v2_records AS source
			  ON source.project_id = rel.project_id AND source.key = rel.from_key
			WHERE rel.project_id = ? AND rel.relation = 'satisfies' AND rel.to_key = ?
			  AND source.type IN ('fact', 'finding', 'solution')
		)`,
		projectID, objectiveKey,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check Objective satisfaction: %w", err)
	}
	return exists == 1, nil
}

func incomingInvalidatingFactKeys(ctx context.Context, tx *sql.Tx, projectID, objectiveKey string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT rel.from_key
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.relation = 'invalidates' AND rel.to_key = ?
		  AND source.type = 'fact'
		ORDER BY rel.from_key ASC`,
		projectID, objectiveKey,
	)
	if err != nil {
		return nil, fmt.Errorf("read Objective invalidation proof: %w", err)
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan Objective invalidation proof: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Objective invalidation proof: %w", err)
	}
	return keys, nil
}

func validateRelationshipEndpoint(relation, fromType, toType, path string) error {
	rule, ok := blackboardv2grammar.Lookup(relation)
	if !ok {
		return semanticError("semantic_validation", "unsupported Blackboard v2 relationship type", path, map[string]any{"relation": relation})
	}
	if relation == "supersedes" {
		return semanticError("semantic_validation", "supersedes is created only by the supersede operation", path, nil)
	}
	if rule.Allows(fromType, toType) {
		return nil
	}
	details := map[string]any{
		"relation": relation, "from_type": fromType, "to_type": toType,
		"allowed_directions": relationshipAllowedDirections(relation),
		"next_action":        "reverse_or_replace_relationship_endpoints",
	}
	return semanticError("semantic_validation", rule.EndpointError, path, details)
}

func relationshipAllowedDirections(relation string) []string {
	directions := make([]string, 0)
	for _, candidate := range blackboardv2grammar.Cases() {
		if candidate.Relation == relation && candidate.Allowed {
			directions = append(directions, candidate.From+" -> "+candidate.To)
		}
	}
	return directions
}

func isOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func relationshipWouldCycle(ctx context.Context, tx *sql.Tx, projectID, relation, fromKey, toKey string) (bool, error) {
	if fromKey == toKey {
		return true, nil
	}
	// UNION (not UNION ALL) visits each key once, even if retained data has a cycle.
	// Read through the caller's transaction so earlier changes in this batch count.
	var cycle bool
	err := tx.QueryRowContext(ctx, `
  WITH RECURSIVE reachable(key) AS (
   SELECT ?
   UNION
   SELECT rel.to_key FROM reachable
   CROSS JOIN blackboard_v2_relationships AS rel
   WHERE rel.from_key = reachable.key AND rel.project_id = ? AND rel.relation = ?
  )
  SELECT EXISTS(SELECT 1 FROM reachable WHERE key = ?)`, toKey, projectID, relation, fromKey).Scan(&cycle)
	if err != nil {
		return false, fmt.Errorf("read %s relationship reachability: %w", relation, err)
	}
	return cycle, nil
}

func factSupportsWouldCycle(ctx context.Context, tx *sql.Tx, projectID, fromKey, toKey string) (bool, error) {
	if fromKey == toKey {
		return true, nil
	}
	var cycle bool
	err := tx.QueryRowContext(ctx, `
  WITH RECURSIVE reachable(key) AS (
   SELECT ?
   UNION
   SELECT rel.to_key FROM reachable
   CROSS JOIN blackboard_v2_relationships AS rel
   JOIN blackboard_v2_records AS source
    ON source.project_id = rel.project_id AND source.key = rel.from_key AND source.type = 'fact'
   JOIN blackboard_v2_records AS target
    ON target.project_id = rel.project_id AND target.key = rel.to_key AND target.type = 'fact'
   WHERE rel.from_key = reachable.key AND rel.project_id = ? AND rel.relation = 'supports'
  )
  SELECT EXISTS(SELECT 1 FROM reachable WHERE key = ?)`, toKey, projectID, fromKey).Scan(&cycle)
	if err != nil {
		return false, fmt.Errorf("read Fact supports reachability: %w", err)
	}
	return cycle, nil
}

func moveCurrentRelationshipsToHistory(ctx context.Context, tx *sql.Tx, projectID, key, recordedAt string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, version, reason
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)
		ORDER BY from_key ASC, relation ASC, to_key ASC`,
		projectID, key, key,
	)
	if err != nil {
		return fmt.Errorf("read terminal Entity relationships: %w", err)
	}
	type relationship struct {
		from, relation, to, reason string
		version                    int
	}
	relationships := make([]relationship, 0)
	for rows.Next() {
		var item relationship
		if err := rows.Scan(&item.from, &item.relation, &item.to, &item.version, &item.reason); err != nil {
			rows.Close()
			return fmt.Errorf("scan terminal Entity relationship: %w", err)
		}
		relationships = append(relationships, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate terminal Entity relationships: %w", err)
	}
	rows.Close()
	for _, item := range relationships {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, item.from, item.relation, item.to, item.version, item.reason, recordedAt,
		); err != nil {
			return fmt.Errorf("store terminal Entity relationship history: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)`,
		projectID, key, key,
	); err != nil {
		return fmt.Errorf("remove terminal Entity relationships: %w", err)
	}
	return nil
}

func relationKey(tuple RelationVersionTuple) string {
	return fmt.Sprintf("%s\x00%s\x00%s", tuple[0], tuple[1], tuple[2])
}

func isOrdinaryRelation(relation string) bool {
	rule, ok := blackboardv2grammar.Lookup(relation)
	return ok && relation != "supersedes" && rule.ReasonPolicy == blackboardv2grammar.ReasonForbidden
}

func isReasonRelation(relation string) bool {
	rule, ok := blackboardv2grammar.Lookup(relation)
	return ok && rule.ReasonPolicy == blackboardv2grammar.ReasonOptional
}
