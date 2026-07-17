package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const MaxInterruptedAttemptCheckpoints = 20

// InterruptedAttemptCheckpoint is the bounded semantic resume allowlist. It
// deliberately excludes storage identity, provenance, raw output, and owner.
type InterruptedAttemptCheckpoint struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

// InterruptedAttemptCheckpoints reads the final truthful summary of Attempts
// interrupted by one durably reconciled Continuation.
func (s *Service) InterruptedAttemptCheckpoints(ctx context.Context, projectID, continuationID string) ([]InterruptedAttemptCheckpoint, error) {
	var status, reconciliation string
	err := s.db.QueryRowContext(ctx, `
		SELECT continuation.status,continuation.blackboard_reconciliation_status
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id=continuation.task_id
		WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID,
	).Scan(&status, &reconciliation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return nil, fmt.Errorf("validate interrupted checkpoint authority: %w", err)
	}
	if !isTerminalContinuationStatus(status) || reconciliation != "completed" {
		return nil, semanticError("reconciliation_incomplete", "interrupted checkpoints require durable Continuation reconciliation", "", nil)
	}
	if status != "interrupted" && status != "failed" && status != "stopped" {
		return []InterruptedAttemptCheckpoint{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT history.key,history.record_json
		FROM blackboard_v2_record_history AS history
		JOIN blackboard_v2_attempt_origins AS origin
		  ON origin.project_id=history.project_id AND origin.key=history.key
		WHERE history.project_id=? AND history.type='attempt' AND origin.continuation_id=?
		  AND json_extract(history.record_json,'$.status')='interrupted'
		  AND history.version=(
		    SELECT MAX(latest.version) FROM blackboard_v2_record_history AS latest
		    WHERE latest.project_id=history.project_id AND latest.key=history.key
		  )
		ORDER BY history.key LIMIT ?`, projectID, continuationID, MaxInterruptedAttemptCheckpoints,
	)
	if err != nil {
		return nil, fmt.Errorf("read interrupted Attempt checkpoints: %w", err)
	}
	defer rows.Close()
	checkpoints := make([]InterruptedAttemptCheckpoint, 0)
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("scan interrupted Attempt checkpoint: %w", err)
		}
		record, err := decodeStoredRecord("attempt", raw)
		if err != nil {
			return nil, fmt.Errorf("decode interrupted Attempt checkpoint: %w", err)
		}
		checkpoint := InterruptedAttemptCheckpoint{Key: key, Summary: record.attemptRecord().Summary}
		if err := validateKey(checkpoint.Key, "key"); err != nil {
			return nil, err
		}
		if err := validateSemanticText(checkpoint.Summary, "summary"); err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interrupted Attempt checkpoints: %w", err)
	}
	return checkpoints, nil
}
