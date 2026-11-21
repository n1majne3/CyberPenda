package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReconcileTerminalContinuation implements the server-owned Task lifecycle
// callback without accepting a Runtime-supplied Project identity.
func (s *Service) ReconcileTerminalContinuation(ctx context.Context, continuationID, reason string) error {
	_ = reason
	if strings.TrimSpace(continuationID) == "" {
		return semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	var projectID string
	err := s.db.QueryRowContext(ctx, `
		SELECT task.project_id
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id = continuation.task_id
		WHERE continuation.id = ?`, continuationID,
	).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return semanticError("authority_denied", "trusted Continuation does not exist", "", nil)
	}
	if err != nil {
		return fmt.Errorf("resolve terminal Continuation Project: %w", err)
	}
	_, err = s.ReconcileContinuationAttempts(ctx, projectID, continuationID)
	return err
}

// ReconcileContinuationAttempts is a server-only control path that records an
// unexpected Continuation end by moving its valid open Attempts to interrupted
// history. Clean completion records no inferred outcome.
func (s *Service) ReconcileContinuationAttempts(ctx context.Context, projectID, continuationID string) (ChangeResult, error) {
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin Attempt reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectState(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
	}
	status, err := continuationProjectStatus(ctx, tx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !isTerminalContinuationStatus(status) {
		return ChangeResult{}, semanticError("continuation_not_closed", "Attempt reconciliation requires a terminal Continuation", "", nil)
	}
	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !isUnexpectedTerminalContinuationStatus(status) {
		result := makeChangeResult(revision, nil, nil)
		if err := markContinuationReconciled(ctx, tx, continuationID, status); err != nil {
			return ChangeResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit clean Continuation reconciliation: %w", err)
		}
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT record.key, record.version, record.record_json
		FROM blackboard_v2_records AS record
		JOIN blackboard_v2_attempt_origins AS origin
		  ON origin.project_id = record.project_id AND origin.key = record.key
		WHERE record.project_id = ? AND record.type = 'attempt' AND origin.continuation_id = ?
		ORDER BY record.key ASC`,
		projectID, continuationID,
	)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("read owned Attempts for reconciliation: %w", err)
	}
	owned := make([]storedRecord, 0)
	for rows.Next() {
		var key, raw string
		var version int
		if err := rows.Scan(&key, &version, &raw); err != nil {
			rows.Close()
			return ChangeResult{}, fmt.Errorf("scan owned Attempt for reconciliation: %w", err)
		}
		record, err := decodeStoredRecord("attempt", raw)
		if err != nil {
			rows.Close()
			return ChangeResult{}, fmt.Errorf("decode owned Attempt for reconciliation: %w", err)
		}
		owned = append(owned, storedRecord{key: key, typ: "attempt", version: version, record: record})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ChangeResult{}, fmt.Errorf("iterate owned Attempts for reconciliation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return ChangeResult{}, fmt.Errorf("close owned Attempt reconciliation rows: %w", err)
	}

	invalidAttempts := make([]string, 0)
	for _, existing := range owned {
		tested, err := currentOutgoingRelationshipCount(ctx, tx, projectID, existing.key, "tests")
		if err != nil {
			return ChangeResult{}, err
		}
		if tested == 0 {
			tested, err = historicalOutgoingRelationshipCount(ctx, tx, projectID, existing.key, "tests")
			if err != nil {
				return ChangeResult{}, err
			}
		}
		if tested == 0 {
			invalidAttempts = append(invalidAttempts, existing.key)
		}
	}
	if len(invalidAttempts) != 0 {
		return ChangeResult{}, semanticError("semantic_validation", "Attempt reconciliation found owned Attempts without tested targets", "", map[string]any{
			"reason":      "missing_tested_target",
			"attempts":    invalidAttempts,
			"next_action": "repair_invalid_attempt",
		})
	}

	changedRecords := make(map[string]int)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, existing := range owned {
		terminal := existing.record.attemptRecord()
		terminal.Status = "interrupted"
		if validateSemanticText(terminal.Summary, "summary") != nil {
			terminal.Summary = "Runtime Continuation ended unexpectedly before this Attempt was concluded."
		}
		nextRevision, key, version, changed, err := terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRecords[key] = version
		}
	}
	result := makeChangeResult(revision, changedRecords, nil)
	if err := markContinuationReconciled(ctx, tx, continuationID, status); err != nil {
		return ChangeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit Attempt reconciliation: %w", err)
	}
	return result, nil
}

func markContinuationReconciled(ctx context.Context, tx *sql.Tx, continuationID, status string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE task_continuations
		SET blackboard_reconciliation_status='completed',
		    blackboard_reconciliation_mutation_id='',
		    blackboard_reconciled_at=?
		WHERE id=? AND status=?`,
		time.Now().UTC().Format(time.RFC3339Nano), continuationID, status,
	)
	if err != nil {
		return fmt.Errorf("mark Blackboard v2 Continuation reconciliation: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count Blackboard v2 Continuation reconciliation marker: %w", err)
	} else if changed != 1 {
		return semanticError("continuation_not_closed", "Attempt reconciliation requires the current terminal Continuation state", "", nil)
	}
	return nil
}

func continuationProjectStatus(ctx context.Context, tx *sql.Tx, projectID, continuationID string) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `
		SELECT continuation.status
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id = continuation.task_id
		WHERE continuation.id = ? AND task.project_id = ?`,
		continuationID, projectID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return "", fmt.Errorf("validate trusted Continuation Project: %w", err)
	}
	return status, nil
}

func continuationCanWrite(status string) bool {
	return isOneOf(status, "pending", "running", "paused")
}

func isTerminalContinuationStatus(status string) bool {
	return isOneOf(status, "completed", "failed", "stopped", "interrupted")
}

func isUnexpectedTerminalContinuationStatus(status string) bool {
	return isOneOf(status, "failed", "stopped", "interrupted")
}

func validateContinuationChangeOwnership(ctx context.Context, tx *sql.Tx, projectID, continuationID string, change Change, index int) error {
	keys := make([]string, 0, 2)
	switch change.Op {
	case "update", "transition":
		keys = append(keys, change.Key)
	case "relate", "unrelate":
		keys = append(keys, change.From)
	case "supersede":
		keys = append(keys, change.Replacement, change.Replaced)
	}
	for _, key := range keys {
		record, err := loadCurrentRecord(ctx, tx, projectID, key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if record.typ == "attempt" {
			if err := requireAttemptOwner(ctx, tx, projectID, key, continuationID, fmt.Sprintf("changes[%d]", index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func bindAttemptOrigin(ctx context.Context, tx *sql.Tx, projectID, key, continuationID, now string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_attempt_origins (project_id, key, continuation_id, created_at)
		VALUES (?, ?, ?, ?)`,
		projectID, key, continuationID, now,
	); err != nil {
		return fmt.Errorf("bind Attempt trusted origin: %w", err)
	}
	return nil
}

func requireAttemptOwner(ctx context.Context, tx *sql.Tx, projectID, key, continuationID, path string) error {
	var owner string
	err := tx.QueryRowContext(ctx, `
		SELECT continuation_id
		FROM blackboard_v2_attempt_origins
		WHERE project_id = ? AND key = ?`,
		projectID, key,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != continuationID) {
		return semanticError("authority_denied", "Attempt is owned by another trusted origin", path, map[string]any{"key": key})
	}
	if err != nil {
		return fmt.Errorf("read Attempt trusted origin: %w", err)
	}
	return nil
}
