package blackboardv2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FinishSessionContinuation closes the trusted Session Continuation while
// retaining the Session aggregate and its Blackboard. The finish receipt is
// owner-bound and exactly replayable.
func (s *Service) FinishSessionContinuation(ctx context.Context, sessionID, continuationID, idempotencyKey string) (FinishContinuationResult, error) {
	if strings.TrimSpace(continuationID) == "" {
		return FinishContinuationResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return FinishContinuationResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("begin Session Continuation finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := ensureSessionState(ctx, tx, sessionID); err != nil {
		return FinishContinuationResult{}, err
	}
	requestHash := sessionFinishRequestHash(idempotencyKey)
	var storedKey, storedHash, raw string
	err = tx.QueryRowContext(ctx, `
		SELECT idempotency_key, request_hash, result_json FROM blackboard_v2_session_finish_receipts
		WHERE session_id=? AND continuation_id=?
		ORDER BY created_at ASC, idempotency_key ASC LIMIT 1`, sessionID, continuationID).Scan(&storedKey, &storedHash, &raw)
	if err == nil {
		if storedKey != idempotencyKey || storedHash != requestHash {
			return FinishContinuationResult{}, semanticError("finish_conflict", "Continuation was already finished with different semantics", "idempotency_key", nil)
		}
		var result FinishContinuationResult
		if err := decodeJSON([]byte(raw), &result); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("decode Session Continuation finish replay: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return FinishContinuationResult{}, fmt.Errorf("commit Session Continuation finish replay: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return FinishContinuationResult{}, fmt.Errorf("read Session Continuation finish receipt: %w", err)
	}
	var receiptOwner string
	err = tx.QueryRowContext(ctx, `
		SELECT continuation_id FROM blackboard_v2_session_finish_receipts
		WHERE session_id=? AND idempotency_key=? LIMIT 1`, sessionID, idempotencyKey).Scan(&receiptOwner)
	if err == nil && receiptOwner != continuationID {
		return FinishContinuationResult{}, semanticError("authority_denied", "Finish idempotency receipt belongs to another trusted origin", "idempotency_key", nil)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FinishContinuationResult{}, fmt.Errorf("read Session Continuation finish key owner: %w", err)
	}
	if err := validateSessionContinuation(ctx, tx, sessionID, continuationID); err != nil {
		return FinishContinuationResult{}, err
	}
	pin, err := readSessionContinuationPinTx(ctx, tx, sessionID, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if pin.ContinuationID == "" {
		return FinishContinuationResult{}, semanticError("authority_denied", "Session Continuation has no Blackboard launch pin", "continuation_id", nil)
	}
	currentOwner, pinned, err := sessionContinuationOwnsCurrentPath(ctx, tx, sessionID, continuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if pinned && !currentOwner {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation no longer owns the Session Working Snapshot", "", nil)
	}
	var openAttempts int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM blackboard_v2_session_records
		WHERE session_id=? AND type='attempt' AND json_extract(record_json, '$.status')='open'`, sessionID).Scan(&openAttempts); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("check open Session Attempts: %w", err)
	}
	if openAttempts != 0 {
		return FinishContinuationResult{}, semanticError("continuation_open_attempts", "Session Continuation cannot finish while an Attempt is open", "", nil)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.persistSessionSnapshot(ctx, tx, sessionID, continuationID, now); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("synchronize Session Working Snapshot for Finish: %w", err)
	}
	revision, err := sessionCurrentRevision(ctx, tx, sessionID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	result := FinishContinuationResult{
		Schema: finishResultSchema, Status: "finished", Revision: revision,
		WorkingSnapshot: WorkingSnapshot{Path: workingPath, Revision: revision},
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("encode Session Continuation finish result: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE session_continuations SET status='completed', updated_at=?, ended_at=?
		WHERE id=? AND session_id=? AND status IN ('pending','running')`, now, now, continuationID, sessionID)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("finish Session Continuation: %w", err)
	}
	if affected, err := updated.RowsAffected(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("count Session Continuation finish: %w", err)
	} else if affected != 1 {
		return FinishContinuationResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Session writes", "", nil)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_finish_receipts
		(session_id, continuation_id, idempotency_key, request_hash, result_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, continuationID, idempotencyKey, requestHash, string(resultJSON), now); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("store Session Continuation finish receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("commit Session Continuation finish: %w", err)
	}
	return result, nil
}

// CheckpointSessionAttemptForContinuation records a compact owner-local
// summary for a running Session Attempt using the shared semantic kernel.
func (s *Service) CheckpointSessionAttemptForContinuation(ctx context.Context, sessionID, continuationID string, request CheckpointAttemptRequest) (ChangeResult, error) {
	if strings.TrimSpace(continuationID) == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if request.IdempotencyKey == "" {
		return ChangeResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if err := validateKey(request.Key, "key"); err != nil {
		return ChangeResult{}, err
	}
	if request.Version < 1 {
		return ChangeResult{}, semanticError("semantic_validation", "version must be positive", "version", nil)
	}
	if err := validateSemanticText(request.Summary, "summary"); err != nil {
		return ChangeResult{}, err
	}
	return s.ApplyForSessionContinuation(ctx, sessionID, continuationID, ChangeBatch{
		Schema: changeBatchSchema, IdempotencyKey: request.IdempotencyKey,
		Changes: []Change{{Op: "update", Key: request.Key, Version: request.Version, Type: "attempt", Record: AttemptPatch{Summary: &request.Summary}}},
	})
}

func sessionFinishRequestHash(idempotencyKey string) string {
	digest := sha256.Sum256([]byte("session-continuation-finish\x00" + idempotencyKey))
	return hex.EncodeToString(digest[:])
}
