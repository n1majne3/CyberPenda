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
	"strings"
	"time"
)

const finishResultSchema = "continuation-finish/v2"

// FinishIntentStatus is the closed tool-result vocabulary for a deferred
// Blackboard Finish Intent (ADR 0022). A Runtime observes status
// "intent_recorded" while the Work Runtime Turn can still produce work; the
// continuation closes only at settlement, when status becomes "finished".
const (
	FinishStatusFinished       = "finished"
	FinishStatusIntentRecorded = "intent_recorded"
)

// FinishIntentProvenance carries the Work Turn correlation captured when a
// Blackboard Finish Intent is recorded. Later source work in the same Turn is
// compared against this provenance to invalidate the intent.
type FinishIntentProvenance struct {
	SourceTurnID        string
	SourceWorkWatermark int
}

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
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
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
		var revision int
		var workingBytes []byte
		if err := tx.QueryRowContext(ctx, `SELECT last_acknowledged_revision,working_snapshot_bytes FROM blackboard_v2_continuation_state WHERE continuation_id=?`, continuationID).Scan(&revision, &workingBytes); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("read replayed Finish Working Snapshot: %w", err)
		}
		if revision != stored.result.Revision || stored.result.WorkingSnapshot.Revision != revision {
			return FinishContinuationResult{}, fmt.Errorf("stored Finish result does not match acknowledged Working Snapshot")
		}
		// Exact Finish replay returns the original result only. It must not
		// attach live synchronization or rematerialize a closed Working Snapshot.
		_ = taskID
		_ = workingBytes
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
	var pendingEvidence int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM blackboard_v2_evidence_requests
		WHERE project_id=? AND continuation_id=? AND status<>'completed'`, projectID, continuationID,
	).Scan(&pendingEvidence); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("read pending Evidence writes for Finish: %w", err)
	}
	if pendingEvidence != 0 {
		return FinishContinuationResult{}, semanticError("continuation_pending_writes", "Finish requires pending Evidence writes to complete", "", map[string]any{"pending_evidence": pendingEvidence})
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

	projection, err := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("project current Runtime Snapshot for Finish: %w", err)
	}
	workingBytes := projection.Bytes
	result, err := s.closeProjectFinishContinuationTx(ctx, tx, projectID, continuationID, status, request.IdempotencyKey, requestHash, func() error {
		if s.finishFail != nil {
			return s.finishFail(FinishFailureBeforeCommit)
		}
		return nil
	})
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if err := materializeWorkingSnapshot(s.runtimeRoot, taskID, workingBytes); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("publish closed Working Snapshot: %w", err)
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

// FinishIntent is the in-memory view of one durable Blackboard Finish Intent
// row. Valid is true only while no later source work in the Work Runtime Turn
// has invalidated the intent.
type FinishIntent struct {
	ContinuationID      string
	ProjectID           string
	IdempotencyKey      string
	RequestHash         string
	SourceTurnID        string
	SourceWorkWatermark int
	Valid               bool
	InvalidatedAt       string
	RecordedAt          string
}

// RecordFinishIntent records a Blackboard Finish Intent during a Work Runtime
// Turn without closing the Continuation (ADR 0022). The Continuation's write
// protocol stays open until the Turn settles and the intent is still valid and
// covered. Exact replay by idempotency key returns the recorded intent. Later
// source work in the same Turn invalidates the intent (see InvalidateFinishIntent).
func (s *Service) RecordFinishIntent(ctx context.Context, projectID, continuationID string, request FinishContinuationRequest, provenance FinishIntentProvenance) (FinishContinuationResult, error) {
	if projectID == "" || continuationID == "" {
		return FinishContinuationResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if request.IdempotencyKey == "" {
		return FinishContinuationResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if strings.TrimSpace(provenance.SourceTurnID) == "" {
		return FinishContinuationResult{}, semanticError("semantic_validation", "finish intent requires source Work Turn provenance", "source_turn_id", nil)
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	requestHash := finishRequestHash(request)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("begin Blackboard v2 Finish Intent: %w", err)
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
		return FinishContinuationResult{}, fmt.Errorf("validate Blackboard v2 Finish Intent principal: %w", err)
	}

	// Exact replay: an existing VALID recorded intent with the same idempotency
	// key and request hash returns its original result without rewriting the
	// row. An INVALIDATED intent is replaced by a fresh intent, because the prior
	// finish no longer represents the Work Turn's current source work (ADR 0022).
	existing, found, err := readFinishIntentRow(ctx, tx, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if found && existing.Valid {
		if existing.IdempotencyKey != request.IdempotencyKey || existing.RequestHash != requestHash {
			return FinishContinuationResult{}, semanticError("finish_conflict", "Continuation already recorded a Finish Intent with different semantics", "idempotency_key", nil)
		}
		var revision int
		if err := tx.QueryRowContext(ctx, `SELECT last_acknowledged_revision FROM blackboard_v2_continuation_state WHERE continuation_id=?`, continuationID).Scan(&revision); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("read Finish Intent acknowledged revision: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("commit Blackboard v2 Finish Intent replay: %w", err)
		}
		return finishIntentResult(revision), nil
	}
	if found && !existing.Valid {
		// Replace the invalidated intent so a new finish call records a fresh
		// intent under the current Work Turn provenance.
		if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_finish_intents WHERE continuation_id=? AND invalidated=1`, continuationID); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("replace invalidated Blackboard v2 Finish Intent: %w", err)
		}
	}

	// A finish receipt already exists (the Continuation was closed by an earlier
	// settlement). Replaying a finish intent against a closed Continuation is a
	// closed_continuation authority error so the tool response stays truthful.
	receipt, hasReceipt, err := readFinishReceipt(ctx, tx, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if hasReceipt {
		if receipt.idempotencyKey == request.IdempotencyKey && receipt.requestHash == requestHash {
			if err := tx.Commit(); err != nil {
				return FinishContinuationResult{}, fmt.Errorf("commit Blackboard v2 Finish Intent replay: %w", err)
			}
			return receipt.result, nil
		}
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}

	var ownerConflict string
	err = tx.QueryRowContext(ctx, `SELECT continuation_id FROM blackboard_v2_finish_intents WHERE project_id=? AND idempotency_key=?`, projectID, request.IdempotencyKey).Scan(&ownerConflict)
	if err == nil && ownerConflict != continuationID {
		return FinishContinuationResult{}, semanticError("authority_denied", "Finish intent idempotency key belongs to another trusted origin", "idempotency_key", nil)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FinishContinuationResult{}, fmt.Errorf("read Blackboard v2 Finish Intent key owner: %w", err)
	}
	if !continuationCanWrite(status) {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	var newer int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_continuations WHERE task_id=? AND number>?`, taskID, number).Scan(&newer); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("validate current Blackboard v2 Continuation for Finish Intent: %w", err)
	}
	if newer != 0 {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation no longer owns the Task Working Snapshot", "", nil)
	}

	projection, err := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("project current Runtime Snapshot for Finish Intent: %w", err)
	}
	revision := projection.Snapshot.Revision
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_finish_intents
		(continuation_id,project_id,idempotency_key,request_hash,source_turn_id,source_work_watermark,invalidated,invalidated_at,recorded_at)
		VALUES (?,?,?,?,?, ?,0,'',?)`,
		continuationID, projectID, request.IdempotencyKey, requestHash,
		provenance.SourceTurnID, provenance.SourceWorkWatermark, now,
	); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("store Blackboard v2 Finish Intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("commit Blackboard v2 Finish Intent: %w", err)
	}
	return finishIntentResult(revision), nil
}

func finishIntentResult(revision int) FinishContinuationResult {
	return FinishContinuationResult{
		Schema: finishResultSchema, Status: FinishStatusIntentRecorded, Revision: revision,
		WorkingSnapshot: WorkingSnapshot{Path: workingPath, Revision: revision},
	}
}

// closeProjectFinishContinuationTx performs the shared atomic close used by both
// an interactive Finish and a deferred Finish Intent settlement: synchronize the
// current Runtime Snapshot, write the immutable finish receipt, and mark the
// Continuation completed. It does not commit; the caller owns the transaction.
// The preClose hook runs after the receipt is stored but before the
// Continuation status update, so an interactive crash-injector can fail there.
func (s *Service) closeProjectFinishContinuationTx(ctx context.Context, tx *sql.Tx, projectID, continuationID, status, idempotencyKey, requestHash string, preClose func() error) (FinishContinuationResult, error) {
	projection, err := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("project current Runtime Snapshot for Finish: %w", err)
	}
	revision := projection.Snapshot.Revision
	workingBytes := projection.Bytes
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_continuation_state
		SET last_acknowledged_revision=?,working_snapshot_bytes=?,updated_at=?
		WHERE continuation_id=?`, revision, workingBytes, time.Now().UTC().Format(time.RFC3339Nano), continuationID,
	); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("synchronize current Working Snapshot for Finish: %w", err)
	}
	result := FinishContinuationResult{
		Schema: finishResultSchema, Status: FinishStatusFinished, Revision: revision,
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
		continuationID, projectID, idempotencyKey, requestHash, string(resultJSON), now,
	); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("store Blackboard v2 Finish receipt: %w", err)
	}
	if preClose != nil {
		if err := preClose(); err != nil {
			return FinishContinuationResult{}, err
		}
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
	return result, nil
}

// InvalidateFinishIntent marks the latest valid Blackboard Finish Intent for a
// Continuation invalidated. Later source work in the same Work Runtime Turn
// calls this so the recorded close can no longer settle. A new finish call is
// required after invalidation. It is safe to call when no valid intent exists.
func (s *Service) InvalidateFinishIntent(ctx context.Context, projectID, continuationID string) error {
	if projectID == "" || continuationID == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Blackboard v2 Finish Intent invalidation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_finish_intents
		SET invalidated=1, invalidated_at=?
		WHERE continuation_id=? AND project_id=? AND invalidated=0`, now, continuationID, projectID)
	if err != nil {
		return fmt.Errorf("invalidate Blackboard v2 Finish Intent: %w", err)
	}
	if changed, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("count invalidated Blackboard v2 Finish Intent: %w", err)
	} else if changed == 0 {
		// No valid intent exists; invalidation is a no-op.
		return tx.Rollback()
	}
	return tx.Commit()
}

// FinishIntentForContinuation returns the latest recorded Blackboard Finish
// Intent for a Continuation, including its validity state. The boolean is false
// when no intent has been recorded.
func (s *Service) FinishIntentForContinuation(ctx context.Context, projectID, continuationID string) (FinishIntent, bool, error) {
	if projectID == "" || continuationID == "" {
		return FinishIntent{}, false, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FinishIntent{}, false, fmt.Errorf("begin read Finish Intent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	intent, found, err := readFinishIntentRow(ctx, tx, continuationID)
	if err != nil {
		return FinishIntent{}, false, err
	}
	intent.ProjectID = projectID
	return intent, found, nil
}

func readFinishIntentRow(ctx context.Context, tx *sql.Tx, continuationID string) (FinishIntent, bool, error) {
	var intent FinishIntent
	var invalidated int
	err := tx.QueryRowContext(ctx, `
		SELECT continuation_id,idempotency_key,request_hash,source_turn_id,source_work_watermark,invalidated,invalidated_at,recorded_at
		FROM blackboard_v2_finish_intents WHERE continuation_id=?`, continuationID,
	).Scan(&intent.ContinuationID, &intent.IdempotencyKey, &intent.RequestHash,
		&intent.SourceTurnID, &intent.SourceWorkWatermark, &invalidated,
		&intent.InvalidatedAt, &intent.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FinishIntent{}, false, nil
	}
	if err != nil {
		return FinishIntent{}, false, fmt.Errorf("read Blackboard v2 Finish Intent: %w", err)
	}
	intent.Valid = invalidated == 0
	return intent, true, nil
}

// SettleFinishIntent closes the Continuation's Blackboard write protocol at
// Work Runtime Turn settlement when the latest Finish Intent is still valid and
// the Turn's semantic debt is covered. It performs the same atomic close as an
// interactive Finish, writes the immutable finish receipt, and removes the
// consumed intent row. When the intent is invalid, already settled, or absent,
// settlement is a no-op and leaves write authority open.
func (s *Service) SettleFinishIntent(ctx context.Context, projectID, continuationID string) (settled bool, err error) {
	if projectID == "" || continuationID == "" {
		return false, nil
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin Blackboard v2 Finish Intent settlement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	intent, found, err := readFinishIntentRow(ctx, tx, continuationID)
	if err != nil {
		return false, err
	}
	if !found || !intent.Valid {
		return false, nil
	}
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
		return false, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return false, fmt.Errorf("validate Blackboard v2 Finish Intent settlement principal: %w", err)
	}
	if !continuationCanWrite(status) {
		// Already closed; nothing to settle.
		return false, nil
	}
	var pendingEvidence int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM blackboard_v2_evidence_requests
		WHERE project_id=? AND continuation_id=? AND status<>'completed'`, projectID, continuationID,
	).Scan(&pendingEvidence); err != nil {
		return false, fmt.Errorf("read pending Evidence writes for Finish settlement: %w", err)
	}
	if pendingEvidence != 0 {
		return false, semanticError("continuation_pending_writes", "Finish requires pending Evidence writes to complete", "", map[string]any{"pending_evidence": pendingEvidence})
	}
	openAttempts, err := openAttemptsForContinuation(ctx, tx, projectID, continuationID)
	if err != nil {
		return false, err
	}
	if len(openAttempts) != 0 {
		return false, semanticError(
			"continuation_open_attempts",
			"Finish requires every current-Continuation Attempt to be terminal",
			"attempts",
			map[string]any{"open_attempts": openAttempts},
		)
	}
	projection, err := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return false, fmt.Errorf("project current Runtime Snapshot for Finish settlement: %w", err)
	}
	workingBytes := projection.Bytes
	if _, err := s.closeProjectFinishContinuationTx(ctx, tx, projectID, continuationID, status, intent.IdempotencyKey, intent.RequestHash, nil); err != nil {
		return false, err
	}
	if err := materializeWorkingSnapshot(s.runtimeRoot, taskID, workingBytes); err != nil {
		return false, fmt.Errorf("publish settled Working Snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Blackboard v2 Finish Intent settlement: %w", err)
	}
	return true, nil
}
