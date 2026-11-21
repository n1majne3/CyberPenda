package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	baseRevision := revision
	kernel, err := applySemanticChangeSet(ctx, tx, revision, batch.Changes, now, semanticChangeBackend{
		prepare: func(_ context.Context, _ *sql.Tx, change Change, index int) (Change, error) {
			return change, validateSessionChangeCapability(change, index)
		},
		create: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, string, int, bool, error) {
			return sessionApplyCreate(ctx, tx, sessionID, revision, index, change, now)
		},
		update: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, string, int, bool, error) {
			return sessionApplyUpdate(ctx, tx, sessionID, revision, index, change, now)
		},
		relate: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, RelationVersionTuple, bool, error) {
			return sessionApplyRelate(ctx, tx, sessionID, revision, index, change, now)
		},
		unrelate: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, RelationVersionTuple, error) {
			return sessionApplyUnrelate(ctx, tx, sessionID, revision, index, change, now)
		},
		transition: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, string, int, bool, error) {
			return sessionApplyTransition(ctx, tx, sessionID, revision, index, change, now)
		},
		supersede: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, created map[string]bool, now string) (int, string, int, RelationVersionTuple, bool, error) {
			return sessionApplySupersede(ctx, tx, sessionID, revision, index, change, created, now)
		},
		unsupported: "unsupported Session Blackboard operation",
	})
	if err != nil {
		return ChangeResult{}, err
	}
	revision = kernel.Revision
	if err := sessionValidateFinalTerminalAttempts(ctx, tx, sessionID, kernel.TerminalAttempts); err != nil {
		return ChangeResult{}, err
	}
	result := makeChangeResult(revision, kernel.ChangedRecords, kernel.ChangedRelations)
	snapshot, err := s.sessionSnapshotWithChangesTx(ctx, tx, sessionID, baseRevision, kernel)
	if err != nil {
		return ChangeResult{}, err
	}
	if err := s.persistSessionSnapshotValue(ctx, tx, sessionID, continuationID, now, snapshot); err != nil {
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
