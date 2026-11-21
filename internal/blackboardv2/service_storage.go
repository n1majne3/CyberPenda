package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"pentest/internal/blackboardv2grammar"
)

type storedRecord struct {
	key     string
	typ     string
	version int
	record  Record
}

func ensureProjectState(ctx context.Context, tx *sql.Tx, projectID string) error {
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_project_state (project_id, revision)
		VALUES (?, 0)
		ON CONFLICT(project_id) DO NOTHING`, projectID,
	); err != nil {
		return fmt.Errorf("ensure Blackboard v2 Project state: %w", err)
	}
	return nil
}

func ensureProjectExists(ctx context.Context, tx *sql.Tx, projectID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&exists); err != nil {
		return fmt.Errorf("check Project for Blackboard v2 state: %w", err)
	}
	if exists == 0 {
		return semanticError("not_found", "project not found", "project", map[string]any{"project": projectID})
	}
	return nil
}

func currentRevision(ctx context.Context, tx *sql.Tx, projectID string) (int, error) {
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM blackboard_v2_project_state WHERE project_id = ?`, projectID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("read Blackboard v2 revision: %w", err)
	}
	return revision, nil
}

func currentRevisionOrZero(ctx context.Context, tx *sql.Tx, projectID string) (int, error) {
	revision, err := currentRevision(ctx, tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

func incrementRevision(ctx context.Context, tx *sql.Tx, projectID string, current int) (int, error) {
	next := current + 1
	if _, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_project_state SET revision = ? WHERE project_id = ?`, next, projectID); err != nil {
		return 0, fmt.Errorf("advance Blackboard v2 revision: %w", err)
	}
	return next, nil
}

func loadCurrentRecord(ctx context.Context, tx *sql.Tx, projectID, key string) (storedRecord, error) {
	canonicalKey, _, err := resolveKeyRedirect(ctx, tx, projectID, key)
	if err != nil {
		return storedRecord{}, err
	}
	return loadCurrentRecordDirect(ctx, tx, projectID, canonicalKey)
}

func loadCurrentRecordDirect(ctx context.Context, tx *sql.Tx, projectID, key string) (storedRecord, error) {
	var found storedRecord
	var raw string
	err := tx.QueryRowContext(ctx, `
		SELECT key, type, version, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND key = ?`,
		projectID, key,
	).Scan(&found.key, &found.typ, &found.version, &raw)
	if err != nil {
		return storedRecord{}, err
	}
	record, err := decodeStoredRecord(found.typ, raw)
	if err != nil {
		return storedRecord{}, fmt.Errorf("decode Blackboard v2 record: %w", err)
	}
	found.record = record
	return found, nil
}

func decodeStoredRecord(typ, raw string) (Record, error) {
	switch typ {
	case "entity":
		var record EntityRecord
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromEntity(record), nil
	case "objective":
		var record ObjectiveRecord
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromObjective(record), nil
	case "attempt":
		var record AttemptRecord
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromAttempt(record), nil
	case "fact":
		var record FactRecord
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromFact(record), nil
	case "finding":
		var record findingOutputRecord
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromFindingOutput(record), nil
	case "solution":
		var record SolutionRecord
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromSolution(record), nil
	case "evidence":
		var record EvidenceRecord
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromEvidence(record), nil
	default:
		return Record{}, fmt.Errorf("unsupported Blackboard v2 record type %q", typ)
	}
}

func loadCurrentRelationshipsForKey(ctx context.Context, tx *sql.Tx, projectID, key string) ([]RelationshipTuple, error) {
	all, err := loadAllCurrentRelationships(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	relationships := make([]RelationshipTuple, 0)
	for _, relationship := range all {
		if relationship[0] == key || relationship[2] == key {
			relationships = append(relationships, relationship)
		}
	}
	return relationships, nil
}

func loadAllCurrentRelationships(ctx context.Context, tx *sql.Tx, projectID string) ([]RelationshipTuple, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT rel.from_key, rel.relation, rel.to_key, rel.reason, source.type, target.type
		FROM blackboard_v2_relationships AS rel
		LEFT JOIN blackboard_v2_records AS source
		  ON source.project_id=rel.project_id AND source.key=rel.from_key
		LEFT JOIN blackboard_v2_records AS target
		  ON target.project_id=rel.project_id AND target.key=rel.to_key
		WHERE rel.project_id = ?
		ORDER BY rel.from_key ASC, rel.relation ASC, rel.to_key ASC, rel.reason ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("read Blackboard v2 snapshot relationships: %w", err)
	}
	defer rows.Close()
	return scanRelationshipTuples(rows)
}

type relationshipRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanRelationshipTuples(rows relationshipRows) ([]RelationshipTuple, error) {
	relationships := []RelationshipTuple{}
	stored := make([]persistedRelationship, 0)
	for rows.Next() {
		var from, relation, to, reason string
		var fromType, toType sql.NullString
		if err := rows.Scan(&from, &relation, &to, &reason, &fromType, &toType); err != nil {
			return nil, fmt.Errorf("scan Blackboard v2 relationship: %w", err)
		}
		rule, known := blackboardv2grammar.Lookup(relation)
		if !known || relation == "supersedes" {
			return nil, persistedRelationshipError(from, relation, to, "relation")
		}
		if from == to {
			return nil, persistedRelationshipError(from, relation, to, "self_link")
		}
		if !fromType.Valid || !toType.Valid || !rule.Allows(fromType.String, toType.String) {
			return nil, persistedRelationshipError(from, relation, to, "endpoint")
		}
		if violation := blackboardv2grammar.ReasonViolation(relation, reason); violation != "" {
			return nil, persistedRelationshipError(from, relation, to, violation)
		}
		stored = append(stored, persistedRelationship{from: from, relation: relation, to: to, fromType: fromType.String, toType: toType.String})
		if reason == "" {
			relationships = append(relationships, RelationshipTuple{from, relation, to})
		} else {
			relationships = append(relationships, RelationshipTuple{from, relation, to, reason})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Blackboard v2 relationships: %w", err)
	}
	if relation, cyclic := persistedRelationshipCycle(stored); cyclic {
		return nil, persistedRelationshipError("", relation, "", "cycle")
	}
	return relationships, nil
}

type persistedRelationship struct {
	from, relation, to string
	fromType, toType   string
}

func persistedRelationshipError(from, relation, to, violation string) error {
	details := map[string]any{"relation": relation, "violation": violation}
	if from != "" {
		details["from"] = from
	}
	if to != "" {
		details["to"] = to
	}
	return semanticError("semantic_validation", "persisted relationship is outside Blackboard v2 relationship grammar", "relations", details)
}

func persistedRelationshipCycle(relationships []persistedRelationship) (string, bool) {
	for _, rule := range blackboardv2grammar.Rules() {
		if !isOneOf(rule.CyclePolicy, "acyclic_per_endpoint_family", "acyclic", "project_fact_to_project_fact_acyclic") {
			continue
		}
		adjacency := make(map[string][]string)
		nodes := make(map[string]bool)
		for _, relationship := range relationships {
			if relationship.relation != rule.Relation {
				continue
			}
			if rule.CyclePolicy == "project_fact_to_project_fact_acyclic" && (relationship.fromType != "fact" || relationship.toType != "fact") {
				continue
			}
			adjacency[relationship.from] = append(adjacency[relationship.from], relationship.to)
			nodes[relationship.from], nodes[relationship.to] = true, true
		}
		keys := make([]string, 0, len(nodes))
		for key := range nodes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for key := range adjacency {
			sort.Strings(adjacency[key])
		}
		state := make(map[string]uint8, len(nodes))
		var visit func(string) bool
		visit = func(key string) bool {
			if state[key] == 1 {
				return true
			}
			if state[key] == 2 {
				return false
			}
			state[key] = 1
			for _, target := range adjacency[key] {
				if visit(target) {
					return true
				}
			}
			state[key] = 2
			return false
		}
		for _, key := range keys {
			if visit(key) {
				return rule.Relation, true
			}
		}
	}
	return "", false
}
