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

// RetainEvidenceForOperator retains one file selected from a Disabled Task by
// an authenticated operator. The source Continuation identifies the confined
// Workdir only; it receives no Blackboard authority or synchronization.
func (s *Service) RetainEvidenceForOperator(ctx context.Context, projectID, taskID, continuationID, actorID string, request RetainEvidenceRequest) (ChangeResult, error) {
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	continuationID = strings.TrimSpace(continuationID)
	actorID = strings.TrimSpace(actorID)
	if projectID == "" || taskID == "" || continuationID == "" || actorID == "" {
		return ChangeResult{}, semanticError("authority_denied", "operator, Project, Task, and source Continuation identity are required", "authorization", nil)
	}
	ctx = withOperatorAuthority(ctx, operatorAuthority{
		actorID: actorID, sourceTaskID: taskID, sourceContinuationID: continuationID,
	})
	return s.RetainEvidenceForContinuation(ctx, projectID, continuationID, request)
}

// RetainTaskEvidenceForOperator fixes the source Continuation and actor on the
// first Task-scoped idempotency reservation. A retry after Runtime replacement
// therefore reuses the original receipt and cannot read replacement output or
// change Trusted Origin.
func (s *Service) RetainTaskEvidenceForOperator(ctx context.Context, projectID, taskID, candidateContinuationID, actorID string, request RetainEvidenceRequest) (ChangeResult, error) {
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	candidateContinuationID = strings.TrimSpace(candidateContinuationID)
	actorID = strings.TrimSpace(actorID)
	if projectID == "" || taskID == "" || candidateContinuationID == "" || actorID == "" {
		return ChangeResult{}, semanticError("authority_denied", "operator, Project, Task, and source Continuation identity are required", "authorization", nil)
	}
	if err := validateRetainEvidenceRequest(request); err != nil {
		return ChangeResult{}, err
	}
	actualTaskID, _, err := s.continuationTaskStatus(ctx, projectID, candidateContinuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if actualTaskID != taskID {
		return ChangeResult{}, semanticError("authority_denied", "selected Task output does not match its source Continuation", "authorization", nil)
	}
	requestHash, err := retainedEvidenceRequestHash(request)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("hash operator Evidence request: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin operator Evidence source reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blackboard_v2_operator_evidence_requests
		(project_id,source_task_id,idempotency_key,request_hash,actor_id,source_continuation_id,created_at)
		VALUES(?,?,?,?,?,?,?)`, projectID, taskID, request.IdempotencyKey, requestHash, actorID, candidateContinuationID, now); err != nil {
		return ChangeResult{}, fmt.Errorf("reserve operator Evidence source: %w", err)
	}
	var boundHash, boundActorID, boundContinuationID string
	if err := tx.QueryRowContext(ctx, `SELECT request_hash,actor_id,source_continuation_id
		FROM blackboard_v2_operator_evidence_requests
		WHERE project_id=? AND source_task_id=? AND idempotency_key=?`,
		projectID, taskID, request.IdempotencyKey).Scan(&boundHash, &boundActorID, &boundContinuationID); err != nil {
		return ChangeResult{}, fmt.Errorf("read operator Evidence source reservation: %w", err)
	}
	if boundHash != requestHash {
		return ChangeResult{}, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": request.IdempotencyKey})
	}
	if boundContinuationID != candidateContinuationID {
		var retainedRequestCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM blackboard_v2_evidence_requests
			WHERE project_id=? AND continuation_id=? AND idempotency_key=?`,
			projectID, boundContinuationID, request.IdempotencyKey).Scan(&retainedRequestCount); err != nil {
			return ChangeResult{}, fmt.Errorf("read bound Evidence request: %w", err)
		}
		if retainedRequestCount == 0 {
			return ChangeResult{}, semanticError(
				"evidence_source_forbidden",
				"the original source Continuation has no retained request to replay",
				"source_path",
				nil,
			)
		}
	}
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit operator Evidence source reservation: %w", err)
	}
	return s.RetainEvidenceForOperator(ctx, projectID, taskID, boundContinuationID, boundActorID, request)
}

// RetainEvidenceForContinuation retains one confined payload and atomically
// publishes its semantic Evidence record and relationships.
func (s *Service) RetainEvidenceForContinuation(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest) (ChangeResult, error) {
	if err := validateRetainEvidenceRequest(request); err != nil {
		return ChangeResult{}, err
	}
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	requestHash, err := retainedEvidenceRequestHash(request)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("hash retained Evidence request: %w", err)
	}
	row, exists, err := s.readEvidenceRequest(ctx, projectID, continuationID, request.IdempotencyKey)
	if err != nil {
		return ChangeResult{}, err
	}
	if exists {
		if row.requestHash != requestHash {
			return ChangeResult{}, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": request.IdempotencyKey})
		}
		if row.status == "completed" {
			var replay ChangeResult
			if err := decodeJSON([]byte(row.resultJSON), &replay); err != nil {
				return ChangeResult{}, fmt.Errorf("decode retained Evidence replay: %w", err)
			}
			return replay, nil
		}
		if replay, found, err := s.readRetainedEvidenceSemanticReplay(ctx, projectID, continuationID, request.IdempotencyKey, requestHash); err != nil {
			return ChangeResult{}, err
		} else if found {
			resultJSON, err := json.Marshal(replay)
			if err != nil {
				return ChangeResult{}, fmt.Errorf("encode recovered Evidence result: %w", err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, request.IdempotencyKey); err != nil {
				return ChangeResult{}, fmt.Errorf("complete recovered Evidence request: %w", err)
			}
			return replay, nil
		}
	}
	request, err = s.resolveRetainedEvidenceRedirects(ctx, projectID, request)
	if err != nil {
		return ChangeResult{}, err
	}
	taskID, status, err := s.continuationTaskStatus(ctx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	operator, operatorRequest := operatorAuthorityFromContext(ctx)
	if operatorRequest && (operator.sourceTaskID != taskID || operator.sourceContinuationID != continuationID) {
		return ChangeResult{}, semanticError("authority_denied", "selected Task output does not match its source Continuation", "authorization", nil)
	}
	if !exists && !operatorRequest && !continuationCanWrite(status) {
		return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	if strings.TrimSpace(s.evidenceConfig.RuntimeRoot) == "" || strings.TrimSpace(s.evidenceConfig.ArtifactRoot) == "" {
		return ChangeResult{}, fmt.Errorf("Evidence Runtime Root and Artifact Root must be configured")
	}
	if exists {
		recovered, err := s.recoverOwnedEvidenceGC(ctx, projectID, continuationID, request.IdempotencyKey, row)
		if err != nil {
			return ChangeResult{}, err
		}
		if recovered {
			exists = false
			row = evidenceRequestRow{}
		}
	}
	var source *evidenceSource
	if !exists {
		if err := s.validateRetainedEvidencePreconditions(ctx, projectID, continuationID, request); err != nil {
			return ChangeResult{}, err
		}
		opened, err := s.openRuntimeEvidenceSource(taskID, request.SourcePath)
		if err != nil {
			return ChangeResult{}, err
		}
		source = &opened
		defer source.file.Close()
		defer source.root.Close()
		internalPath, err := plannedEvidenceInternalPath(projectID, source.sha256, source.relativePath)
		if err != nil {
			return ChangeResult{}, err
		}
		if err := s.failEvidence(EvidenceFailureBeforeReservation); err != nil {
			return ChangeResult{}, err
		}
		row, _, err = s.reserveEvidenceRequest(ctx, projectID, continuationID, request.IdempotencyKey, requestHash, internalPath, *source)
		if err != nil {
			return ChangeResult{}, err
		}
		if row.status == "completed" {
			var replay ChangeResult
			if err := decodeJSON([]byte(row.resultJSON), &replay); err != nil {
				return ChangeResult{}, fmt.Errorf("decode raced Evidence replay: %w", err)
			}
			return replay, nil
		}
	}
	semanticPath, err := semanticEvidencePath(projectID, row.internalPath, row.sha256)
	if err != nil {
		return ChangeResult{}, err
	}
	payloadReady, err := s.verifyManagedEvidencePayload(row.internalPath, row.sha256, row.size)
	if err != nil {
		return ChangeResult{}, err
	}
	if !payloadReady {
		tempReady, err := s.verifyJournaledEvidenceTemp(row.tempPath, row.sha256, row.size)
		if err != nil {
			return ChangeResult{}, err
		}
		recoveryExists, offlineRecoveryValid, err := s.evidencePublicationRecoveryExists(ctx, projectID, continuationID, request.IdempotencyKey, row)
		if err != nil {
			return ChangeResult{}, err
		}
		if !tempReady && !offlineRecoveryValid && source == nil {
			opened, err := s.openRuntimeEvidenceSource(taskID, request.SourcePath)
			if err != nil && !recoveryExists {
				s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
				return ChangeResult{}, err
			}
			if err == nil {
				source = &opened
				defer source.file.Close()
				defer source.root.Close()
			}
		}
		if source != nil {
			if err := validateEvidenceReservationRow(row, requestHash, *source); err != nil {
				s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
				return ChangeResult{}, err
			}
		}
		if err := s.ensureEvidencePublished(ctx, projectID, continuationID, request.IdempotencyKey, &row, source); err != nil {
			s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
			return ChangeResult{}, err
		}
	} else if row.status == "reserved" {
		if err := s.syncAndCheckpointPublishedEvidence(ctx, projectID, continuationID, request.IdempotencyKey, row.internalPath, row.tempPath); err != nil {
			return ChangeResult{}, err
		}
	}
	payloadReady, err = s.verifyManagedEvidencePayload(row.internalPath, row.sha256, row.size)
	if err != nil {
		return ChangeResult{}, err
	}
	if !payloadReady {
		return ChangeResult{}, semanticError("evidence_integrity_failed", "managed Evidence payload failed integrity verification before semantic commit", "key", nil)
	}
	result, err := s.applyRetainedEvidence(ctx, projectID, continuationID, request, requestHash, semanticPath, retainedEvidenceMetadata{sha256: row.sha256, size: row.size}, true)
	if err != nil {
		s.cleanupDefinitiveEvidenceFailure(ctx, projectID, continuationID, request.IdempotencyKey, row, err)
		return ChangeResult{}, err
	}
	if err := s.failEvidence(EvidenceFailureAfterGraphCommit); err != nil {
		return ChangeResult{}, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("encode retained Evidence result: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_v2_evidence_requests SET status='completed',result_json=?,updated_at=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), projectID, continuationID, request.IdempotencyKey); err != nil {
		return ChangeResult{}, fmt.Errorf("complete Evidence request: %w", err)
	}
	if err := s.failEvidence(EvidenceFailureAfterResultStore); err != nil {
		return ChangeResult{}, err
	}
	return result, nil
}

func (s *Service) resolveRetainedEvidenceRedirects(ctx context.Context, projectID string, request RetainEvidenceRequest) (RetainEvidenceRequest, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RetainEvidenceRequest{}, fmt.Errorf("begin Evidence redirect resolution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return RetainEvidenceRequest{}, err
	}
	return resolveRetainedEvidenceRedirectsTx(ctx, tx, projectID, request)
}

func resolveRetainedEvidenceRedirectsTx(ctx context.Context, tx *sql.Tx, projectID string, request RetainEvidenceRequest) (RetainEvidenceRequest, error) {
	var err error
	request.Key, _, err = resolveKeyRedirect(ctx, tx, projectID, request.Key)
	if err != nil {
		return RetainEvidenceRequest{}, err
	}
	for index := range request.Links {
		request.Links[index][1], _, err = resolveKeyRedirect(ctx, tx, projectID, request.Links[index][1])
		if err != nil {
			return RetainEvidenceRequest{}, err
		}
	}
	return request, nil
}

func (s *Service) readRetainedEvidenceSemanticReplay(ctx context.Context, projectID, continuationID, idempotencyKey, requestHash string) (ChangeResult, bool, error) {
	receiptKey := "retain-evidence-v2:" + continuationID + ":" + idempotencyKey
	var storedHash, raw, storedContinuationID string
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,result_json,continuation_id FROM blackboard_v2_idempotency_receipts WHERE project_id=? AND idempotency_key=?`, projectID, receiptKey).Scan(&storedHash, &raw, &storedContinuationID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeResult{}, false, nil
	}
	if err != nil {
		return ChangeResult{}, false, fmt.Errorf("read retained Evidence semantic receipt: %w", err)
	}
	if storedContinuationID != continuationID {
		return ChangeResult{}, false, semanticError("authority_denied", "retained Evidence receipt belongs to another trusted origin", "idempotency_key", nil)
	}
	if storedHash != requestHash {
		return ChangeResult{}, false, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": idempotencyKey})
	}
	var result ChangeResult
	if err := decodeJSON([]byte(raw), &result); err != nil {
		return ChangeResult{}, false, fmt.Errorf("decode retained Evidence semantic receipt: %w", err)
	}
	return result, true, nil
}
