package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

func (s *Service) applyRetainedEvidence(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest, requestHash, managedPath string, metadata retainedEvidenceMetadata, durablyReserved bool) (ChangeResult, error) {
	operator, operatorRequest := operatorAuthorityFromContext(ctx)
	receiptKey := "retain-evidence-v2:" + continuationID + ":" + request.IdempotencyKey
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin retained Evidence semantic change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectState(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
	}
	if replay, ok, err := idempotencyReplay(ctx, tx, projectID, continuationID, receiptKey, requestHash); err != nil {
		return ChangeResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit retained Evidence replay: %w", err)
		}
		if !operatorRequest {
			if err := s.rematerializeContinuationWorkingSnapshot(ctx, continuationID); err != nil {
				return ChangeResult{}, fmt.Errorf("recover retained Evidence Working Snapshot: %w", err)
			}
		}
		return replay, nil
	}
	request, err = resolveRetainedEvidenceRedirectsTx(ctx, tx, projectID, request)
	if err != nil {
		return ChangeResult{}, err
	}
	if durablyReserved {
		internalPath, err := plannedEvidenceInternalPath(projectID, metadata.sha256, filepath.Base(managedPath))
		if err != nil {
			return ChangeResult{}, err
		}
		var payloadDigest, payloadState string
		var payloadSize int64
		err = tx.QueryRowContext(ctx, `SELECT sha256,size_bytes,state FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, projectID, internalPath).Scan(&payloadDigest, &payloadSize, &payloadState)
		if errors.Is(err, sql.ErrNoRows) || payloadState == "gc" {
			return ChangeResult{}, &Error{Code: "evidence_payload_gc_in_progress", Message: "Evidence payload cleanup is in progress", Path: "source_path", Retryable: true}
		}
		if err != nil {
			return ChangeResult{}, fmt.Errorf("validate retained Evidence payload claim: %w", err)
		}
		if payloadDigest != metadata.sha256 || payloadSize != metadata.size {
			return ChangeResult{}, semanticError("evidence_integrity_failed", "retained Evidence payload claim failed integrity validation", "source_path", nil)
		}
		var reservationCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=? AND managed_internal_path=?`, projectID, continuationID, request.IdempotencyKey, requestHash, internalPath).Scan(&reservationCount); err != nil {
			return ChangeResult{}, fmt.Errorf("validate retained Evidence reservation: %w", err)
		}
		if reservationCount != 1 {
			return ChangeResult{}, &Error{Code: "evidence_reservation_changed", Message: "Evidence reservation changed during retention", Path: "idempotency_key", Retryable: true}
		}
	}
	if !operatorRequest {
		status, err := continuationProjectStatus(ctx, tx, projectID, continuationID)
		if err != nil {
			return ChangeResult{}, err
		}
		if !durablyReserved && !continuationCanWrite(status) {
			return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
		}
	}
	attempt, err := loadCurrentRecord(ctx, tx, projectID, request.Attempt)
	historicalAttempt := false
	if errors.Is(err, sql.ErrNoRows) && durablyReserved {
		if operatorRequest {
			if err := requireOperatorAttemptOwner(ctx, tx, projectID, request.Attempt, operator.actorID, "attempt"); err != nil {
				return ChangeResult{}, err
			}
		} else {
			if err := requireAttemptOwner(ctx, tx, projectID, request.Attempt, continuationID, "attempt"); err != nil {
				return ChangeResult{}, err
			}
		}
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT record_json FROM blackboard_v2_record_history WHERE project_id=? AND key=? AND type='attempt' ORDER BY version DESC LIMIT 1`, projectID, request.Attempt).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ChangeResult{}, semanticError("semantic_validation", "durably reserved producing Attempt has no terminal history", "attempt", nil)
			}
			return ChangeResult{}, fmt.Errorf("read reserved producing Attempt history: %w", err)
		}
		var terminal AttemptRecord
		if err := decodeJSON([]byte(raw), &terminal); err != nil {
			return ChangeResult{}, fmt.Errorf("decode reserved producing Attempt history: %w", err)
		}
		if !isOneOf(terminal.Status, "succeeded", "failed", "blocked", "inconclusive", "interrupted") {
			return ChangeResult{}, semanticError("semantic_validation", "durably reserved producing Attempt is not terminal", "attempt", nil)
		}
		historicalAttempt = true
	} else {
		if errors.Is(err, sql.ErrNoRows) {
			return ChangeResult{}, semanticError("not_found", fmt.Sprintf("%s was not found", request.Attempt), "attempt", map[string]any{"key": request.Attempt})
		}
		if err != nil {
			return ChangeResult{}, err
		}
		if attempt.typ != "attempt" {
			return ChangeResult{}, semanticError("semantic_validation", "attempt must reference an Attempt", "attempt", nil)
		}
		if operatorRequest {
			if err := requireOperatorAttemptOwner(ctx, tx, projectID, request.Attempt, operator.actorID, "attempt"); err != nil {
				return ChangeResult{}, err
			}
		} else {
			if err := requireAttemptOwner(ctx, tx, projectID, request.Attempt, continuationID, "attempt"); err != nil {
				return ChangeResult{}, err
			}
		}
		if attempt.record.attemptRecord().Status != "open" {
			return ChangeResult{}, semanticError("semantic_validation", "producing Attempt must be open for a new retain", "attempt", nil)
		}
	}
	record := EvidenceRecord{Status: "available", ArtifactType: request.ArtifactType, Summary: request.Summary, MediaType: request.MediaType, SourcePath: request.SourcePath, ManagedPath: managedPath, SHA256: metadata.sha256, Size: metadata.size, CapturedAt: request.CapturedAt}
	if err := validateEvidenceRecord(record, "record"); err != nil {
		return ChangeResult{}, err
	}
	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	changedRecords := make(map[string]int)
	existing, err := loadCurrentRecord(ctx, tx, projectID, request.Key)
	if errors.Is(err, sql.ErrNoRows) {
		if request.Version != 0 {
			return ChangeResult{}, semanticError("not_found", fmt.Sprintf("%s was not found", request.Key), "key", map[string]any{"key": request.Key})
		}
		if used, err := historicalKeyExists(ctx, tx, projectID, request.Key); err != nil {
			return ChangeResult{}, err
		} else if used {
			return ChangeResult{}, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", request.Key), "key", map[string]any{"key": request.Key})
		}
		raw, err := json.Marshal(record)
		if err != nil {
			return ChangeResult{}, err
		}
		revision, err = incrementRevision(ctx, tx, projectID, revision)
		if err != nil {
			return ChangeResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_v2_records(project_id,key,type,version,record_json,created_at,updated_at) VALUES(?,?,'evidence',1,?,?,?)`, projectID, request.Key, string(raw), now, now); err != nil {
			return ChangeResult{}, fmt.Errorf("store Blackboard v2 Evidence: %w", err)
		}
		changedRecords[request.Key] = 1
	} else {
		if err != nil {
			return ChangeResult{}, err
		}
		if existing.typ != "evidence" {
			return ChangeResult{}, semanticError("key_conflict", fmt.Sprintf("%s already exists", request.Key), "key", map[string]any{"key": request.Key})
		}
		if request.Version == 0 {
			return ChangeResult{}, semanticError("semantic_validation", "current Evidence version is required for replacement", "version", nil)
		}
		if request.Version != existing.version {
			return ChangeResult{}, semanticError("version_conflict", fmt.Sprintf("%s changed", request.Key), "version", map[string]any{"key": request.Key, "expected_version": float64(request.Version), "current_version": float64(existing.version), "next_action": "read_current_record"})
		}
		if !evidenceEqual(existing.record.evidenceRecord(), record) {
			nextRevision, _, version, _, err := replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, record, now)
			if err != nil {
				return ChangeResult{}, err
			}
			revision = nextRevision
			changedRecords[request.Key] = version
		}
	}
	changedRelations := make(map[string]RelationVersionTuple)
	if historicalAttempt {
		nextRevision, tuple, changed, err := applyHistoricalProduced(ctx, tx, projectID, revision, request.Attempt, request.Key, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRelations[relationKey(tuple)] = tuple
		}
	} else {
		nextRevision, tuple, changed, err := applyRelate(ctx, tx, projectID, revision, 0, Change{Op: "relate", From: request.Attempt, Relation: "produced", To: request.Key}, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRelations[relationKey(tuple)] = tuple
		}
	}
	for index, link := range request.Links {
		nextRevision, tuple, changed, err := applyRelate(ctx, tx, projectID, revision, index+1, Change{Op: "relate", From: request.Key, Relation: link[0], To: link[1]}, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRelations[relationKey(tuple)] = tuple
		}
	}
	if operatorRequest {
		if version, changed := changedRecords[request.Key]; changed {
			if err := recordOperatorEvidenceOrigin(ctx, tx, projectID, request.Key, version, operator, request.SourcePath, now); err != nil {
				return ChangeResult{}, err
			}
		}
	}
	workingAdvanced := false
	if !operatorRequest {
		_, _, workingAdvanced, err = s.advanceContinuationWorkingSnapshotTx(ctx, tx, projectID, continuationID)
		if err != nil {
			return ChangeResult{}, err
		}
	}
	result := makeChangeResult(revision, changedRecords, changedRelations)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_v2_idempotency_receipts(project_id,idempotency_key,request_hash,result_json,created_at,continuation_id) VALUES(?,?,?,?,?,?)`, projectID, receiptKey, requestHash, string(resultJSON), now, continuationID); err != nil {
		return ChangeResult{}, fmt.Errorf("store retained Evidence receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit retained Evidence semantic change: %w", err)
	}
	if workingAdvanced {
		if err := s.rematerializeContinuationWorkingSnapshot(ctx, continuationID); err != nil {
			return ChangeResult{}, fmt.Errorf("replace retained Evidence Working Snapshot: %w", err)
		}
	}
	return result, nil
}

func applyHistoricalProduced(ctx context.Context, tx *sql.Tx, projectID string, revision int, attemptKey, evidenceKey, now string) (int, RelationVersionTuple, bool, error) {
	var version int
	err := tx.QueryRowContext(ctx, `SELECT version FROM blackboard_v2_relationship_history WHERE project_id=? AND from_key=? AND relation='produced' AND to_key=? ORDER BY version DESC LIMIT 1`, projectID, attemptKey, evidenceKey).Scan(&version)
	if err == nil {
		return revision, RelationVersionTuple{attemptKey, "produced", evidenceKey, version}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("read historical Evidence production: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_v2_relationship_history(project_id,from_key,relation,to_key,version,reason,recorded_at) VALUES(?,?,'produced',?,1,'',?)`, projectID, attemptKey, evidenceKey, now); err != nil {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("store historical Evidence production: %w", err)
	}
	return nextRevision, RelationVersionTuple{attemptKey, "produced", evidenceKey, 1}, true, nil
}

func (s *Service) failEvidence(point EvidenceFailurePoint) error {
	if s.evidenceConfig.Failures == nil {
		return nil
	}
	return s.evidenceConfig.Failures.FailAfter(point)
}
