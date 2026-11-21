package blackboardv2

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// finalizeClaimedSyncDelivery atomically acknowledges the Working Snapshot (when
// still advancing), stores the durable attachment receipt, and publishes exact
// snapshot bytes. Finish may have already advanced and closed the Continuation;
// in that case the receipt is finalized from committed working state.
func (s *Service) finalizeClaimedSyncDelivery(ctx context.Context, projectID, taskID, continuationID, requestFingerprint string, fromRevision int, preLive bool) (*SynchronizationAttachment, error) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin trusted sync delivery finalize: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var receiptStatus string
	var receiptFrom int
	var attachmentRaw string
	err = tx.QueryRowContext(ctx, `
		SELECT status,from_revision,attachment_json
		FROM blackboard_v2_sync_delivery_receipts
		WHERE continuation_id=? AND request_fingerprint=?`, continuationID, requestFingerprint,
	).Scan(&receiptStatus, &receiptFrom, &attachmentRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("trusted sync delivery claim missing at finalize")
	}
	if err != nil {
		return nil, fmt.Errorf("read trusted sync delivery claim at finalize: %w", err)
	}
	if receiptStatus == "finalized" {
		var attachment SynchronizationAttachment
		if err := json.Unmarshal([]byte(attachmentRaw), &attachment); err != nil {
			return nil, fmt.Errorf("decode finalized trusted sync delivery: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		if preLive {
			if err := s.rematerializeContinuationWorkingSnapshotLocked(ctx, continuationID); err != nil {
				return nil, fmt.Errorf("republish Working Snapshot for sync delivery replay: %w", err)
			}
		}
		return &attachment, nil
	}
	if receiptStatus != "claimed" {
		return nil, fmt.Errorf("trusted sync delivery receipt status %q", receiptStatus)
	}
	if fromRevision < 0 {
		fromRevision = receiptFrom
	}

	var boundTaskID, contStatus string
	var acknowledged, newer int
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
	).Scan(&boundTaskID, &contStatus, &acknowledged, &workingBytes, &newer)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return nil, fmt.Errorf("validate trusted sync delivery finalize: %w", err)
	}
	if taskID != "" && taskID != boundTaskID {
		return nil, semanticError("authority_denied", "trusted Continuation does not own this Task interface", "", nil)
	}

	live := continuationCanWrite(contStatus) && newer == 0
	// Prefer current Project Snapshot while live; after Finish/close use committed
	// Working Snapshot bytes so exact replay remains available.
	var deliveredBytes []byte
	if live {
		projection, projErr := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
		if projErr != nil {
			return nil, projErr
		}
		if projection.Snapshot.Revision < acknowledged {
			return nil, fmt.Errorf("acknowledged Blackboard revision moved beyond current Project state")
		}
		if projection.Snapshot.Revision > acknowledged {
			result, execErr := tx.ExecContext(ctx, `
				UPDATE blackboard_v2_continuation_state
				SET last_acknowledged_revision=?,working_snapshot_bytes=?,updated_at=?
				WHERE continuation_id=? AND last_acknowledged_revision=?`,
				projection.Snapshot.Revision, projection.Bytes, time.Now().UTC().Format(time.RFC3339Nano), continuationID, acknowledged,
			)
			if execErr != nil {
				return nil, fmt.Errorf("advance synchronized Working Blackboard Snapshot: %w", execErr)
			}
			if changed, rowsErr := result.RowsAffected(); rowsErr != nil {
				return nil, rowsErr
			} else if changed != 1 {
				return nil, fmt.Errorf("stale Working Blackboard Snapshot synchronization")
			}
			workingBytes = append([]byte(nil), projection.Bytes...)
			acknowledged = projection.Snapshot.Revision
		} else if !bytes.Equal(workingBytes, projection.Bytes) {
			// Already acknowledged at current revision (own write absorbed the notice).
			// Keep committed Working Snapshot bytes as the durable attachment payload.
			workingBytes = append([]byte(nil), projection.Bytes...)
		}
		deliveredBytes = append([]byte(nil), workingBytes...)
	} else {
		if len(workingBytes) == 0 {
			return nil, fmt.Errorf("closed Working Snapshot missing for sync delivery finalize")
		}
		deliveredBytes = append([]byte(nil), workingBytes...)
	}
	var snapshot RuntimeSnapshot
	if err := json.Unmarshal(deliveredBytes, &snapshot); err != nil {
		return nil, fmt.Errorf("decode Working Snapshot for sync delivery finalize: %w", err)
	}

	// from_revision is the last acknowledged revision observed when the notice
	// was reserved. Prefer the claim-time value for stable response-loss replay.
	deliveredFrom := receiptFrom
	if deliveredFrom < 0 || deliveredFrom > snapshot.Revision {
		deliveredFrom = acknowledged
		if fromRevision >= 0 && fromRevision <= snapshot.Revision {
			deliveredFrom = fromRevision
		}
	}
	attachment := SynchronizationAttachment{
		Reason: "another_task_changed_shared_project_knowledge", FromRevision: deliveredFrom,
		Revision: snapshot.Revision, Snapshot: snapshot,
	}
	attachmentJSON, err := json.Marshal(attachment)
	if err != nil {
		return nil, fmt.Errorf("encode trusted sync delivery receipt: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_sync_delivery_receipts
		SET status='finalized',revision=?,attachment_json=?,working_snapshot_bytes=?,updated_at=?
		WHERE continuation_id=? AND request_fingerprint=? AND status='claimed'`,
		snapshot.Revision, string(attachmentJSON), deliveredBytes, now, continuationID, requestFingerprint,
	)
	if err != nil {
		return nil, fmt.Errorf("finalize trusted sync delivery receipt: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if changed != 1 {
		return nil, fmt.Errorf("stale trusted sync delivery finalize")
	}

	// Publish exact Snapshot bytes before commit when the Continuation still owns
	// the Task Working Snapshot. Closed Continuations must not rematerialize.
	if live {
		workingPath := filepath.Join(s.runtimeRoot, boundTaskID, "workdir", ".pentest", "blackboard.json")
		previousBytes, previousErr := os.ReadFile(workingPath)
		previousExists := previousErr == nil
		if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read prior Working Snapshot before synchronized publication: %w", previousErr)
		}
		if err := materializeWorkingSnapshot(s.runtimeRoot, boundTaskID, deliveredBytes); err != nil {
			restorePriorWorkingSnapshotFile(s.runtimeRoot, boundTaskID, workingPath, previousBytes, previousExists)
			return nil, fmt.Errorf("publish synchronized Working Blackboard Snapshot: %w", err)
		}
		if err := tx.Commit(); err != nil {
			restorePriorWorkingSnapshotFile(s.runtimeRoot, boundTaskID, workingPath, previousBytes, previousExists)
			return nil, fmt.Errorf("commit trusted sync delivery finalize: %w", err)
		}
		return &attachment, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit trusted sync delivery finalize: %w", err)
	}
	return &attachment, nil
}

func (s *Service) rematerializeContinuationWorkingSnapshotLocked(ctx context.Context, continuationID string) error {
	var taskID string
	var workingBytes []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT continuation.task_id,state.working_snapshot_bytes
		FROM task_continuations AS continuation
		JOIN blackboard_v2_continuation_state AS state ON state.continuation_id=continuation.id
		WHERE continuation.id=?`, continuationID,
	).Scan(&taskID, &workingBytes)
	if err != nil {
		return err
	}
	return materializeWorkingSnapshot(s.runtimeRoot, taskID, workingBytes)
}
