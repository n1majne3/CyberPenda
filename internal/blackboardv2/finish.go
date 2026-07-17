package blackboardv2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const finishResultSchema = "continuation-finish/v2"

type FinishFailurePoint string

const FinishFailureBeforeCommit FinishFailurePoint = "before_commit"

type FinishFailureInjector func(FinishFailurePoint) error

func (s *Service) SetFinishFailureInjector(injector FinishFailureInjector) {
	s.finishFail = injector
}

// FinishContinuationRequest is the complete closed Runtime request. Trusted
// Project and Continuation identity are supplied by the caller's principal.
type FinishContinuationRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

func (request *FinishContinuationRequest) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return err
	}
	for field := range fields {
		if field != "idempotency_key" {
			return fmt.Errorf("unknown FinishContinuationRequest field %q", field)
		}
	}
	key, err := decodeRequiredString(fields, "idempotency_key")
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("idempotency_key must not be empty")
	}
	*request = FinishContinuationRequest{IdempotencyKey: key}
	return nil
}

// FinishContinuationResult is the closed v2 success result. It points at the
// exact acknowledged Working Snapshot retained by the closed Continuation.
type FinishContinuationResult struct {
	Schema          string          `json:"schema"`
	Status          string          `json:"status"`
	Revision        int             `json:"revision"`
	WorkingSnapshot WorkingSnapshot `json:"working_snapshot"`
}

// FinishContinuation atomically rejects owned open Attempts, closes the
// current Continuation, and stores an immutable exact-replay receipt.
func (s *Service) FinishContinuation(ctx context.Context, projectID, continuationID string, request FinishContinuationRequest) (FinishContinuationResult, error) {
	if projectID == "" || continuationID == "" {
		return FinishContinuationResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if request.IdempotencyKey == "" {
		return FinishContinuationResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	requestHash := finishRequestHash(request)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("begin Blackboard v2 Finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var taskID, status string
	var number int
	err = tx.QueryRowContext(ctx, `
		SELECT continuation.task_id,continuation.number,continuation.status
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id=continuation.task_id
		JOIN blackboard_v2_continuation_pins AS pin ON pin.continuation_id=continuation.id
		WHERE continuation.id=? AND task.project_id=?`,
		continuationID, projectID,
	).Scan(&taskID, &number, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return FinishContinuationResult{}, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("validate Blackboard v2 Finish principal: %w", err)
	}

	stored, found, err := readFinishReceipt(ctx, tx, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if found {
		if stored.idempotencyKey != request.IdempotencyKey || stored.requestHash != requestHash {
			return FinishContinuationResult{}, semanticError("finish_conflict", "Continuation was already finished with different semantics", "idempotency_key", nil)
		}
		if err := tx.Commit(); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("commit Blackboard v2 Finish replay: %w", err)
		}
		return stored.result, nil
	}

	var receiptOwner string
	err = tx.QueryRowContext(ctx, `
		SELECT continuation_id FROM blackboard_v2_continuation_finishes
		WHERE project_id=? AND idempotency_key=?`, projectID, request.IdempotencyKey,
	).Scan(&receiptOwner)
	if err == nil && receiptOwner != continuationID {
		return FinishContinuationResult{}, semanticError("authority_denied", "Finish idempotency receipt belongs to another trusted origin", "idempotency_key", nil)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FinishContinuationResult{}, fmt.Errorf("read Blackboard v2 Finish key owner: %w", err)
	}
	if !continuationCanWrite(status) {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	var newer int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_continuations WHERE task_id=? AND number>?`, taskID, number).Scan(&newer); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("validate current Blackboard v2 Continuation: %w", err)
	}
	if newer != 0 {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation no longer owns the Task Working Snapshot", "", nil)
	}

	openAttempts, err := openAttemptsForContinuation(ctx, tx, projectID, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if len(openAttempts) != 0 {
		return FinishContinuationResult{}, semanticError(
			"continuation_open_attempts",
			"Finish requires every current-Continuation Attempt to be terminal",
			"attempts",
			map[string]any{"open_attempts": openAttempts},
		)
	}

	var revision int
	var workingBytes []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT last_acknowledged_revision,working_snapshot_bytes
		FROM blackboard_v2_continuation_state WHERE continuation_id=?`, continuationID,
	).Scan(&revision, &workingBytes); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("read acknowledged Working Snapshot for Finish: %w", err)
	}
	if err := verifySnapshotEnvelope(workingBytes, revision); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("verify acknowledged Working Snapshot for Finish: %w", err)
	}
	result := FinishContinuationResult{
		Schema: finishResultSchema, Status: "finished", Revision: revision,
		WorkingSnapshot: WorkingSnapshot{Path: workingPath, Revision: revision},
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("encode Blackboard v2 Finish result: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_continuation_finishes
		(continuation_id,project_id,idempotency_key,request_hash,result_json,finished_at)
		VALUES (?,?,?,?,?,?)`,
		continuationID, projectID, request.IdempotencyKey, requestHash, string(resultJSON), now,
	); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("store Blackboard v2 Finish receipt: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE task_continuations
		SET status='completed',updated_at=?,ended_at=?,
		    blackboard_reconciliation_status='completed',
		    blackboard_reconciliation_mutation_id='',blackboard_reconciled_at=?
		WHERE id=? AND status=?`, now, now, now, continuationID, status,
	)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("close Blackboard v2 Continuation: %w", err)
	}
	if changed, err := updated.RowsAffected(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("count closed Blackboard v2 Continuation: %w", err)
	} else if changed != 1 {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	if s.finishFail != nil {
		if err := s.finishFail(FinishFailureBeforeCommit); err != nil {
			return FinishContinuationResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("commit Blackboard v2 Finish: %w", err)
	}
	return result, nil
}

type finishReceipt struct {
	idempotencyKey string
	requestHash    string
	result         FinishContinuationResult
}

func readFinishReceipt(ctx context.Context, tx *sql.Tx, continuationID string) (finishReceipt, bool, error) {
	var receipt finishReceipt
	var resultJSON string
	err := tx.QueryRowContext(ctx, `
		SELECT idempotency_key,request_hash,result_json
		FROM blackboard_v2_continuation_finishes WHERE continuation_id=?`, continuationID,
	).Scan(&receipt.idempotencyKey, &receipt.requestHash, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return finishReceipt{}, false, nil
	}
	if err != nil {
		return finishReceipt{}, false, fmt.Errorf("read Blackboard v2 Finish receipt: %w", err)
	}
	if err := decodeJSON([]byte(resultJSON), &receipt.result); err != nil {
		return finishReceipt{}, false, fmt.Errorf("decode Blackboard v2 Finish receipt: %w", err)
	}
	return receipt, true, nil
}

func openAttemptsForContinuation(ctx context.Context, tx *sql.Tx, projectID, continuationID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT record.key
		FROM blackboard_v2_records AS record
		JOIN blackboard_v2_attempt_origins AS origin
		  ON origin.project_id=record.project_id AND origin.key=record.key
		WHERE record.project_id=? AND record.type='attempt'
		  AND origin.continuation_id=?
		  AND json_extract(record.record_json,'$.status')='open'
		ORDER BY record.key`, projectID, continuationID,
	)
	if err != nil {
		return nil, fmt.Errorf("read open Attempts for Blackboard v2 Finish: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan open Attempt for Blackboard v2 Finish: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open Attempts for Blackboard v2 Finish: %w", err)
	}
	sort.Strings(keys)
	return keys, nil
}

func finishRequestHash(request FinishContinuationRequest) string {
	raw, _ := json.Marshal(request)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
