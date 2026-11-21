package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func applyCreateRecord(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	switch change.Type {
	case "entity":
		return applyCreateEntity(ctx, tx, projectID, revision, index, change, now)
	case "objective":
		return applyCreateObjective(ctx, tx, projectID, revision, index, change, now)
	case "attempt":
		return applyCreateAttempt(ctx, tx, projectID, revision, index, change, now)
	case "fact":
		return applyCreateFact(ctx, tx, projectID, revision, index, change, now)
	case "finding":
		return applyCreateFinding(ctx, tx, projectID, revision, index, change, now)
	case "solution":
		return applyCreateSolution(ctx, tx, projectID, revision, index, change, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Blackboard v2 record type in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
}

func applyCreateObjective(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeObjectiveRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateObjectiveRecord(record, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "objective" && objectivesEqual(existing.record.objectiveRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	return insertCurrentWorkRecord(ctx, tx, projectID, revision, change.Key, "objective", record, now)
}

func applyCreateAttempt(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeAttemptRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateAttemptRecord(record, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "attempt" && attemptsEqual(existing.record.attemptRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	return insertCurrentWorkRecord(ctx, tx, projectID, revision, change.Key, "attempt", record, now)
}

func insertCurrentWorkRecord(ctx context.Context, tx *sql.Tx, projectID string, revision int, key, typ string, record any, now string) (int, string, int, bool, error) {
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode %s record: %w", typ, err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)`,
		projectID, key, typ, string(recordJSON), now, now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store Blackboard v2 %s: %w", typ, err)
	}
	return nextRevision, key, 1, true, nil
}

func applyCreateEntity(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeEntityRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateEntityRecord(record, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "entity" && entitiesEqual(existing.record.entityRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode Entity record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
		VALUES (?, ?, 'entity', 1, ?, ?, ?)`,
		projectID, change.Key, string(recordJSON), now, now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store Blackboard v2 Entity: %w", err)
	}
	return nextRevision, change.Key, 1, true, nil
}

func applyCreateFact(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if change.Type != "fact" {
		return revision, "", 0, false, semanticError("semantic_validation", "create supports only Project Facts in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeFactRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateFactRecord(record, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "fact" && factsEqual(existing.record.factRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode Fact record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
		VALUES (?, ?, 'fact', 1, ?, ?, ?)`,
		projectID, change.Key, string(recordJSON), now, now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store Blackboard v2 Fact: %w", err)
	}
	return nextRevision, change.Key, 1, true, nil
}
