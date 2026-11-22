package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InspectContinuationSynchronization validates the Project, Task, and
// Continuation binding before an adapter decodes or executes semantic input.
// An empty taskID is accepted for adapters whose authenticated principal has
// already bound the Task outside this service call. Closed or superseded
// Continuations retain identity checks but report no pending live sync.
func (s *Service) InspectContinuationSynchronization(ctx context.Context, projectID, taskID, continuationID string) (ContinuationSynchronizationState, error) {
	authority, err := s.AuthorizeContinuationBinding(ctx, projectID, taskID, continuationID, false)
	if err != nil {
		return ContinuationSynchronizationState{}, err
	}
	return authority.Sync, nil
}

// SynchronizationDeliveryFingerprint identifies one authenticated trusted
// request for deterministic response-loss replay of its sync attachment.
// Empty fingerprints are Pending-only (for example GET reads) and are not
// stored for later redelivery — lost Pending-only responses cannot be
// reconstructed once acknowledgement commits.
func SynchronizationDeliveryFingerprint(operation, idempotencyKey string) string {
	operation = strings.TrimSpace(operation)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if operation == "" || idempotencyKey == "" {
		return ""
	}
	return operation + "\x1f" + idempotencyKey
}

// ClaimTrustedSynchronization reserves the current pending notice for one
// request fingerprint before the trusted action runs. Concurrent different
// fingerprints cannot both claim: at most one open claim exists per
// Continuation. Exact retries of an already-claimed or finalized fingerprint
// report claimed=true so adapters re-run the action and finalize/replay.
// Empty fingerprints never claim (Pending-only delivery path).
func (s *Service) ClaimTrustedSynchronization(ctx context.Context, projectID, taskID, continuationID, requestFingerprint string, preSync ContinuationSynchronizationState) (bool, error) {
	requestFingerprint = strings.TrimSpace(requestFingerprint)
	if requestFingerprint == "" {
		return false, nil
	}
	if status, _, ok, err := s.loadSyncDeliveryReceipt(ctx, continuationID, requestFingerprint); err != nil {
		return false, err
	} else if ok {
		_ = status
		return true, nil
	}
	if !preSync.Pending {
		return false, nil
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin trusted sync delivery claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Re-check ownership inside the transaction against races.
	var existingStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM blackboard_v2_sync_delivery_receipts
		WHERE continuation_id=? AND request_fingerprint=?`, continuationID, requestFingerprint,
	).Scan(&existingStatus)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read trusted sync delivery claim: %w", err)
	}
	var boundTaskID, status string
	var acknowledged, revision, newer int
	err = tx.QueryRowContext(ctx, `
		SELECT continuation.task_id,continuation.status,state.last_acknowledged_revision,
		       COALESCE(project_state.revision,0),
		       (SELECT COUNT(*) FROM task_continuations AS newer
		         WHERE newer.task_id=continuation.task_id AND newer.number>continuation.number)
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id=continuation.task_id
		JOIN blackboard_v2_continuation_state AS state ON state.continuation_id=continuation.id
		LEFT JOIN blackboard_v2_project_state AS project_state ON project_state.project_id=task.project_id
		WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID,
	).Scan(&boundTaskID, &status, &acknowledged, &revision, &newer)
	if errors.Is(err, sql.ErrNoRows) {
		return false, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return false, fmt.Errorf("validate trusted sync delivery claim: %w", err)
	}
	if taskID != "" && taskID != boundTaskID {
		return false, semanticError("authority_denied", "trusted Continuation does not own this Task interface", "", nil)
	}
	if !continuationCanWrite(status) || newer != 0 || revision <= acknowledged {
		// Pending was absorbed or Continuation closed before claim; nothing to reserve.
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	fromRevision := preSync.FromRevision
	if fromRevision < 0 || fromRevision > acknowledged {
		fromRevision = acknowledged
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_sync_delivery_receipts
		(continuation_id,request_fingerprint,status,from_revision,revision,attachment_json,working_snapshot_bytes,created_at,updated_at)
		VALUES (?,?, 'claimed', ?, 0, '', x'', ?, ?)`,
		continuationID, requestFingerprint, fromRevision, now, now,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			if err := tx.Commit(); err != nil {
				return false, err
			}
			if status, _, ok, loadErr := s.loadSyncDeliveryReceipt(ctx, continuationID, requestFingerprint); loadErr != nil {
				return false, loadErr
			} else if ok {
				_ = status
				return true, nil
			}
			return false, nil
		}
		return false, fmt.Errorf("claim trusted sync delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit trusted sync delivery claim: %w", err)
	}
	return true, nil
}

// CaptureTrustedSynchronization returns the synchronization attachment that
// must accompany one authenticated trusted response, if any.
//
// Request-scoped protocol (non-empty fingerprint):
//   - Adapters claim the pending notice before the action when Pending and a
//     stable idempotency fingerprint exist.
//   - Finalize acknowledgement, exact Working Snapshot bytes, and the durable
//     receipt atomically. Crash after claim/action before finalize recovers on
//     exact retry, including Finish after closure.
//   - Finalized receipts are preserved per fingerprint so older idempotent
//     retries redeliver their original attachment after later sync deliveries.
//   - Ordinary later/different requests without a receipt stay clean.
//
// Pending-only protocol (empty fingerprint, e.g. GET/read):
//   - Deliver and acknowledge once when pre-action Pending is true and no
//     fingerprint has reserved the notice. Lost responses cannot be replayed
//     because no request identity exists; adapters must still return the body
//     whenever delivery succeeds (never acknowledge-then-discard on 304/errors).
func (s *Service) CaptureTrustedSynchronization(ctx context.Context, projectID, taskID, continuationID string, preSync ContinuationSynchronizationState, preLive bool, requestFingerprint string) (*SynchronizationAttachment, error) {
	requestFingerprint = strings.TrimSpace(requestFingerprint)
	if requestFingerprint != "" {
		status, stored, ok, err := s.loadSyncDeliveryReceipt(ctx, continuationID, requestFingerprint)
		if err != nil {
			return nil, err
		}
		if ok && status == "finalized" {
			if preLive {
				if err := s.rematerializeContinuationWorkingSnapshot(ctx, continuationID); err != nil {
					return nil, fmt.Errorf("republish Working Snapshot for sync delivery replay: %w", err)
				}
			}
			return &stored, nil
		}
		if ok && status == "claimed" {
			return s.finalizeClaimedSyncDelivery(ctx, projectID, taskID, continuationID, requestFingerprint, preSync.FromRevision, preLive)
		}
		// No receipt: try a late claim for adapters that only call Capture, then finalize.
		if preSync.Pending {
			claimed, claimErr := s.ClaimTrustedSynchronization(ctx, projectID, taskID, continuationID, requestFingerprint, preSync)
			if claimErr != nil {
				return nil, claimErr
			}
			if claimed {
				return s.finalizeClaimedSyncDelivery(ctx, projectID, taskID, continuationID, requestFingerprint, preSync.FromRevision, preLive)
			}
		}
		return nil, nil
	}
	// Pending-only: never durable. Skip when a fingerprint has reserved the notice.
	if !preSync.Pending {
		return nil, nil
	}
	if reserved, err := s.hasOpenSyncDeliveryClaim(ctx, continuationID); err != nil {
		return nil, err
	} else if reserved {
		return nil, nil
	}
	attachment, err := s.SynchronizeContinuation(ctx, projectID, taskID, continuationID, preSync.FromRevision)
	if err != nil {
		return nil, err
	}
	return &attachment, nil
}

func (s *Service) loadSyncDeliveryReceipt(ctx context.Context, continuationID, fingerprint string) (status string, attachment SynchronizationAttachment, ok bool, err error) {
	var raw string
	scanErr := s.db.QueryRowContext(ctx, `
		SELECT status,attachment_json
		FROM blackboard_v2_sync_delivery_receipts
		WHERE continuation_id=? AND request_fingerprint=?`, continuationID, fingerprint,
	).Scan(&status, &raw)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return "", SynchronizationAttachment{}, false, nil
	}
	if scanErr != nil {
		return "", SynchronizationAttachment{}, false, fmt.Errorf("read trusted sync delivery receipt: %w", scanErr)
	}
	if status == "finalized" {
		if strings.TrimSpace(raw) == "" {
			return "", SynchronizationAttachment{}, false, fmt.Errorf("finalized trusted sync delivery receipt missing attachment")
		}
		if err := json.Unmarshal([]byte(raw), &attachment); err != nil {
			return "", SynchronizationAttachment{}, false, fmt.Errorf("decode trusted sync delivery receipt: %w", err)
		}
	}
	return status, attachment, true, nil
}

func (s *Service) hasOpenSyncDeliveryClaim(ctx context.Context, continuationID string) (bool, error) {
	var fingerprint string
	err := s.db.QueryRowContext(ctx, `
		SELECT request_fingerprint FROM blackboard_v2_sync_delivery_receipts
		WHERE continuation_id=? AND status='claimed' LIMIT 1`, continuationID,
	).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read open trusted sync delivery claim: %w", err)
	}
	return fingerprint != "", nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

// SynchronizeContinuation advances the authenticated Working Snapshot to the
// exact current Runtime Snapshot and returns the common response attachment.
// Delivery is crash/retry safe: the Working Snapshot is published before the
// acknowledgement commits when advancing, and an already-acknowledged retry
// republishes the exact committed Working Snapshot bytes so a lost response or
// interrupted materialization can recover without leaving a live pending notice.
// If publication or commit fails after the filesystem advances, the prior
// Working Snapshot file bytes (or absence) are restored so durable state and
// disk stay aligned for the next retry.
//
// Transport adapters should prefer CaptureTrustedSynchronization so lost
// authenticated responses can redeliver the same attachment via request fingerprint.
func (s *Service) SynchronizeContinuation(ctx context.Context, projectID, taskID, continuationID string, fromRevision int) (SynchronizationAttachment, error) {
	if fromRevision < 0 {
		return SynchronizationAttachment{}, semanticError("semantic_validation", "synchronization revision must not be negative", "from_revision", nil)
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SynchronizationAttachment{}, fmt.Errorf("begin trusted Continuation synchronization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var boundTaskID string
	var acknowledged int
	var status string
	var newer int
	var workingBytes []byte
	err = tx.QueryRowContext(ctx, `
		SELECT continuation.task_id,continuation.status,state.last_acknowledged_revision,state.working_snapshot_bytes,
		       (SELECT COUNT(*) FROM task_continuations AS newer
		         WHERE newer.task_id=continuation.task_id AND newer.number>continuation.number)
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id=continuation.task_id
		JOIN blackboard_v2_continuation_pins AS pin ON pin.continuation_id=continuation.id
		JOIN blackboard_v2_continuation_state AS state ON state.continuation_id=continuation.id
		WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID,
	).Scan(&boundTaskID, &status, &acknowledged, &workingBytes, &newer)
	if errors.Is(err, sql.ErrNoRows) {
		return SynchronizationAttachment{}, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return SynchronizationAttachment{}, fmt.Errorf("validate trusted Continuation synchronization: %w", err)
	}
	if taskID != "" && taskID != boundTaskID {
		return SynchronizationAttachment{}, semanticError("authority_denied", "trusted Continuation does not own this Task interface", "", nil)
	}
	if !continuationCanWrite(status) || newer != 0 {
		return SynchronizationAttachment{}, semanticError("closed_continuation", "trusted Continuation is closed for synchronization", "", nil)
	}
	projection, err := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return SynchronizationAttachment{}, err
	}
	if projection.Snapshot.Revision < acknowledged {
		return SynchronizationAttachment{}, fmt.Errorf("acknowledged Blackboard revision moved beyond current Project state")
	}
	// from_revision is the last acknowledged revision the adapter observed. Prefer
	// the caller's observed from when it still describes this notice; otherwise
	// report the server-side acknowledgement observed in this transaction.
	deliveredFrom := acknowledged
	if fromRevision >= 0 && fromRevision <= acknowledged {
		deliveredFrom = fromRevision
	}
	if projection.Snapshot.Revision > acknowledged {
		result, err := tx.ExecContext(ctx, `
			UPDATE blackboard_v2_continuation_state
			SET last_acknowledged_revision=?,working_snapshot_bytes=?,updated_at=?
			WHERE continuation_id=? AND last_acknowledged_revision=?`,
			projection.Snapshot.Revision, projection.Bytes, time.Now().UTC().Format(time.RFC3339Nano), continuationID, acknowledged,
		)
		if err != nil {
			return SynchronizationAttachment{}, fmt.Errorf("advance synchronized Working Blackboard Snapshot: %w", err)
		}
		if changed, err := result.RowsAffected(); err != nil {
			return SynchronizationAttachment{}, err
		} else if changed != 1 {
			return SynchronizationAttachment{}, fmt.Errorf("stale Working Blackboard Snapshot synchronization")
		}
		workingBytes = append([]byte(nil), projection.Bytes...)
	}
	// Capture prior on-disk Working Snapshot before publication so a failed
	// materialization or commit can restore exact prior bytes (or absence).
	// Publish-before-commit remains crash-safe when the process dies after disk
	// replace: Pending stays true when advancing, and retries republish.
	workingPath := filepath.Join(s.runtimeRoot, boundTaskID, "workdir", ".pentest", "blackboard.json")
	previousBytes, previousErr := os.ReadFile(workingPath)
	previousExists := previousErr == nil
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return SynchronizationAttachment{}, fmt.Errorf("read prior Working Snapshot before synchronized publication: %w", previousErr)
	}
	restoreWorkingSnapshot := func() {
		restorePriorWorkingSnapshotFile(s.runtimeRoot, boundTaskID, workingPath, previousBytes, previousExists)
	}
	// Publish exact Snapshot bytes before commit so a crash after disk replace
	// and before commit leaves Pending true (when advancing) and retries safely.
	// Already-acknowledged retries republish committed Working Snapshot bytes so
	// lost materialization or a deleted file recovers without reopening Pending.
	if err := materializeWorkingSnapshot(s.runtimeRoot, boundTaskID, workingBytes); err != nil {
		restoreWorkingSnapshot()
		return SynchronizationAttachment{}, fmt.Errorf("publish synchronized Working Blackboard Snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		restoreWorkingSnapshot()
		return SynchronizationAttachment{}, fmt.Errorf("commit trusted Continuation synchronization: %w", err)
	}
	var snapshot RuntimeSnapshot
	if err := json.Unmarshal(workingBytes, &snapshot); err != nil {
		return SynchronizationAttachment{}, fmt.Errorf("decode synchronized Working Blackboard Snapshot: %w", err)
	}
	return SynchronizationAttachment{
		Reason: "another_task_changed_shared_project_knowledge", FromRevision: deliveredFrom,
		Revision: snapshot.Revision, Snapshot: snapshot,
	}, nil
}
