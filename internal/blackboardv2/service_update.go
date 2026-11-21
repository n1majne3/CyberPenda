package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func applyUpdateRecord(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	switch change.Type {
	case "entity":
		return applyUpdateEntity(ctx, tx, projectID, revision, index, change, now)
	case "objective":
		return applyUpdateObjective(ctx, tx, projectID, revision, index, change, now)
	case "attempt":
		return applyUpdateAttempt(ctx, tx, projectID, revision, index, change, now)
	case "fact":
		return applyUpdateFact(ctx, tx, projectID, revision, index, change, now)
	case "finding":
		return applyUpdateFinding(ctx, tx, projectID, revision, index, change, now)
	case "solution":
		return applyUpdateSolution(ctx, tx, projectID, revision, index, change, now)
	case "evidence":
		return applyUpdateEvidence(ctx, tx, projectID, revision, index, change, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Blackboard v2 record type in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
}

func applyUpdateEvidence(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "evidence", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	patch, err := partialEvidenceRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	next := existing.record.evidenceRecord()
	if patch.Summary != nil {
		next.Summary = *patch.Summary
	}
	if patch.MediaType != nil {
		next.MediaType = *patch.MediaType
	}
	if patch.CapturedAt != nil {
		next.CapturedAt = *patch.CapturedAt
	}
	for _, field := range change.Clear {
		switch field {
		case "media_type":
			next.MediaType = ""
		case "captured_at":
			next.CapturedAt = ""
		default:
			return revision, "", 0, false, semanticError("semantic_validation", "unsupported Evidence clear field", path+".clear", map[string]any{"field": field})
		}
	}
	if err := validateEvidenceRecord(next, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if evidenceEqual(existing.record.evidenceRecord(), next) {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
}

func applyUpdateEntity(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
		}
		return revision, "", 0, false, err
	}
	if existing.typ != "entity" {
		return revision, "", 0, false, semanticError("semantic_validation", "record type mismatch", fmt.Sprintf("changes[%d].type", index), map[string]any{"key": change.Key})
	}
	if change.Version != existing.version {
		return revision, "", 0, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			fmt.Sprintf("changes[%d].version", index),
			map[string]any{
				"key":              change.Key,
				"expected_version": float64(change.Version),
				"current_version":  float64(existing.version),
				"next_action":      "read_current_record",
			},
		)
	}
	patch, err := partialEntityRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord, err := applyEntityPatch(existing.record.entityRecord(), patch, change.Clear, fmt.Sprintf("changes[%d].clear", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateEntityRecord(nextRecord, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	if entitiesEqual(existing.record.entityRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	historyJSON, err := json.Marshal(existing.record.entityRecord())
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode prior Entity record: %w", err)
	}
	nextJSON, err := json.Marshal(nextRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode updated Entity record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'entity', ?, ?)`,
		projectID, change.Key, existing.version, string(historyJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store prior Blackboard v2 Entity: %w", err)
	}
	nextVersion := existing.version + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_records
		SET version = ?, record_json = ?, updated_at = ?
		WHERE project_id = ? AND key = ?`,
		nextVersion, string(nextJSON), now, projectID, change.Key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("update Blackboard v2 Entity: %w", err)
	}
	return nextRevision, change.Key, nextVersion, true, nil
}

func applyUpdateObjective(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "objective", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	if len(change.Clear) != 0 {
		return revision, "", 0, false, semanticError("semantic_validation", "Objective update does not accept clear", path+".clear", nil)
	}
	patch, err := partialObjectiveRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord := existing.record.objectiveRecord()
	if patch.Objective != nil {
		nextRecord.Objective = *patch.Objective
	}
	if err := validateObjectiveRecord(nextRecord, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if objectivesEqual(existing.record.objectiveRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, nextRecord, now)
}

func applyUpdateAttempt(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "attempt", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	if len(change.Clear) != 0 {
		return revision, "", 0, false, semanticError("semantic_validation", "Attempt update does not accept clear", path+".clear", nil)
	}
	patch, err := partialAttemptRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord := existing.record.attemptRecord()
	if patch.Summary != nil {
		nextRecord.Summary = *patch.Summary
	}
	if err := validateAttemptRecord(nextRecord, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if attemptsEqual(existing.record.attemptRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, nextRecord, now)
}

func currentRecordForUpdate(ctx context.Context, tx *sql.Tx, projectID string, change Change, typ, path string) (storedRecord, error) {
	if err := validateKey(change.Key, path+".key"); err != nil {
		return storedRecord{}, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedRecord{}, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
		}
		return storedRecord{}, err
	}
	if existing.typ != typ {
		return storedRecord{}, semanticError("semantic_validation", "record type mismatch", path+".type", map[string]any{"key": change.Key})
	}
	if change.Version != existing.version {
		return storedRecord{}, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			path+".version",
			map[string]any{"key": change.Key, "expected_version": float64(change.Version), "current_version": float64(existing.version), "next_action": "read_current_record"},
		)
	}
	return existing, nil
}

func replaceCurrentWorkRecord(ctx context.Context, tx *sql.Tx, projectID string, revision int, existing storedRecord, nextRecord any, now string) (int, string, int, bool, error) {
	historyJSON, err := json.Marshal(existing.record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode prior %s record: %w", existing.typ, err)
	}
	nextJSON, err := json.Marshal(nextRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode updated %s record: %w", existing.typ, err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, existing.key, existing.version, existing.typ, string(historyJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store prior Blackboard v2 %s: %w", existing.typ, err)
	}
	nextVersion := existing.version + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_records
		SET version = ?, record_json = ?, updated_at = ?
		WHERE project_id = ? AND key = ?`,
		nextVersion, string(nextJSON), now, projectID, existing.key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("update Blackboard v2 %s: %w", existing.typ, err)
	}
	return nextRevision, existing.key, nextVersion, true, nil
}

func applyUpdateFact(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if change.Type != "fact" {
		return revision, "", 0, false, semanticError("semantic_validation", "update supports only Project Facts in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
		}
		return revision, "", 0, false, err
	}
	if existing.typ != "fact" {
		return revision, "", 0, false, semanticError("semantic_validation", "record type mismatch", fmt.Sprintf("changes[%d].type", index), map[string]any{"key": change.Key})
	}
	if change.Version != existing.version {
		return revision, "", 0, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			fmt.Sprintf("changes[%d].version", index),
			map[string]any{
				"key":              change.Key,
				"expected_version": float64(change.Version),
				"current_version":  float64(existing.version),
				"next_action":      "read_current_record",
			},
		)
	}
	patch, err := partialFactRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord, err := applyFactPatch(existing.record.factRecord(), patch, change.Clear, fmt.Sprintf("changes[%d].clear", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateFactRecord(nextRecord, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	if factsEqual(existing.record.factRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	historyJSON, err := json.Marshal(existing.record.factRecord())
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode prior Fact record: %w", err)
	}
	nextJSON, err := json.Marshal(nextRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode updated Fact record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'fact', ?, ?)`,
		projectID, change.Key, existing.version, string(historyJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store prior Blackboard v2 Fact: %w", err)
	}
	nextVersion := existing.version + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_records
		SET version = ?, record_json = ?, updated_at = ?
		WHERE project_id = ? AND key = ?`,
		nextVersion, string(nextJSON), now, projectID, change.Key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("update Blackboard v2 Fact: %w", err)
	}
	return nextRevision, change.Key, nextVersion, true, nil
}
