package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CreateContinuation records a Task-domain Continuation without launching a
// Runtime. Production launch uses the Blackboard v2 continuity coordinator.
func (s *Service) CreateContinuation(taskID, runtimeProfileID, runtimeProvider string, runner Runner) (TaskContinuation, error) {
	return s.createContinuation(taskID, runtimeProfileID, runtimeProvider, runner, "")
}

// CreateReplacementContinuation creates a new turn boundary for a persistent
// Task session while retaining the prior runtime configuration pin and any
// discovered provider/container metadata.
func (s *Service) CreateReplacementContinuation(previous TaskContinuation) (TaskContinuation, error) {
	return s.createReplacementContinuation(previous, ReconciliationPending)
}

func (s *Service) createReplacementContinuation(previous TaskContinuation, reconciliationStatus ReconciliationStatus) (TaskContinuation, error) {
	next, err := s.createContinuationWithReconciliationStatus(
		previous.TaskID, previous.RuntimeProfileID, previous.RuntimeProvider, previous.Runner,
		previous.RuntimeConfigVersionID, reconciliationStatus,
	)
	if err != nil {
		return TaskContinuation{}, err
	}
	if previous.ContainerID != "" || previous.NativeSessionID != "" || previous.NativeSessionPath != "" {
		next, err = s.UpdateContinuationRuntimeMetadata(next.ID, previous.ContainerID, previous.NativeSessionID, previous.NativeSessionPath)
		if err != nil {
			return TaskContinuation{}, err
		}
	}
	return next, nil
}

// CreateReplacementContinuationWithoutBlackboard creates the ordinary Runtime
// turn boundary for a Disabled owner and records that no Blackboard
// reconciliation is required for that boundary.
func (s *Service) CreateReplacementContinuationWithoutBlackboard(previous TaskContinuation) (TaskContinuation, error) {
	return s.createReplacementContinuation(previous, ReconciliationCompleted)
}

func (s *Service) createContinuation(taskID, runtimeProfileID, runtimeProvider string, runner Runner, runtimeConfigVersionID string) (TaskContinuation, error) {
	return s.createContinuationWithReconciliationStatus(
		taskID, runtimeProfileID, runtimeProvider, runner, runtimeConfigVersionID, ReconciliationPending,
	)
}

func (s *Service) createContinuationWithReconciliationStatus(taskID, runtimeProfileID, runtimeProvider string, runner Runner, runtimeConfigVersionID string, reconciliationStatus ReconciliationStatus) (TaskContinuation, error) {
	if _, err := s.Get(taskID); err != nil {
		return TaskContinuation{}, err
	}
	now := time.Now().UTC()
	continuation := TaskContinuation{
		RuntimeConfigVersionID:         runtimeConfigVersionID,
		ID:                             newID(),
		TaskID:                         taskID,
		RuntimeProfileID:               runtimeProfileID,
		RuntimeProvider:                runtimeProvider,
		Runner:                         runner,
		Status:                         StatusPending,
		BlackboardReconciliationStatus: reconciliationStatus,
		StartedAt:                      now,
		UpdatedAt:                      now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if runtimeConfigVersionID != "" {
		var pinnedRuntimeProfileID string
		if err := tx.QueryRow(`SELECT runtime_profile_id FROM task_runtime_config_versions WHERE id=? AND task_id=?`, runtimeConfigVersionID, taskID).Scan(&pinnedRuntimeProfileID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return TaskContinuation{}, fmt.Errorf("runtime config version does not belong to task")
			}
			return TaskContinuation{}, fmt.Errorf("read runtime config version: %w", err)
		}
		if pinnedRuntimeProfileID != runtimeProfileID {
			return TaskContinuation{}, fmt.Errorf("runtime config version belongs to runtime profile %q, not %q", pinnedRuntimeProfileID, runtimeProfileID)
		}
	}

	var maxNumber sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(number) FROM task_continuations WHERE task_id = ?`, taskID).Scan(&maxNumber); err != nil {
		return TaskContinuation{}, fmt.Errorf("read max continuation number: %w", err)
	}
	continuation.Number = int(maxNumber.Int64) + 1

	if _, err := tx.Exec(
		`INSERT INTO task_continuations (id, task_id, number, runtime_profile_id, runtime_provider, runner, status, container_id, native_session_id, native_session_path, started_at, updated_at, ended_at, runtime_config_version_id, blackboard_reconciliation_status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, '', ?, ?)`,
		continuation.ID, continuation.TaskID, continuation.Number, continuation.RuntimeProfileID,
		continuation.RuntimeProvider, string(continuation.Runner), string(continuation.Status),
		continuation.StartedAt.Format(time.RFC3339Nano), continuation.UpdatedAt.Format(time.RFC3339Nano),
		continuation.RuntimeConfigVersionID, string(continuation.BlackboardReconciliationStatus),
	); err != nil {
		return TaskContinuation{}, fmt.Errorf("store continuation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TaskContinuation{}, fmt.Errorf("commit continuation: %w", err)
	}
	return continuation, nil
}

// ActiveContinuation returns the currently active Runtime Continuation for a
// Task, if one exists.
func (s *Service) ActiveContinuation(taskID string) (*TaskContinuation, error) {
	if _, err := s.Get(taskID); err != nil {
		return nil, err
	}
	found, err := scanContinuation(s.db.QueryRow(
		`SELECT `+continuationSelectColumns+`
		 FROM task_continuations
		 WHERE task_id = ? AND status IN (?, ?, ?)
		 ORDER BY number DESC LIMIT 1`,
		taskID, string(StatusPending), string(StatusRunning), string(StatusPaused),
	))
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

// LatestContinuation returns the latest recorded Runtime Continuation for a
// Task, whether active or terminal.
func (s *Service) LatestContinuation(taskID string) (*TaskContinuation, error) {
	if _, err := s.Get(taskID); err != nil {
		return nil, err
	}
	found, err := scanContinuation(s.db.QueryRow(
		`SELECT `+continuationSelectColumns+`
		 FROM task_continuations
		 WHERE task_id = ? ORDER BY number DESC LIMIT 1`,
		taskID,
	))
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

func (s *Service) Continuation(continuationID string) (TaskContinuation, error) {
	return scanContinuation(s.db.QueryRow(
		`SELECT `+continuationSelectColumns+` FROM task_continuations WHERE id = ?`,
		continuationID,
	))
}

// TerminalContinuations returns every durably terminal Continuation so daemon
// startup can recover semantic reconciliation crash windows idempotently.
func (s *Service) TerminalContinuations() ([]TaskContinuation, error) {
	rows, err := s.db.Query(
		`SELECT `+continuationSelectColumns+`
		 FROM task_continuations
		 WHERE status IN (?,?,?,?) AND blackboard_reconciliation_status<>'legacy_not_applicable'
		 ORDER BY started_at,id`,
		string(StatusCompleted), string(StatusFailed), string(StatusStopped), string(StatusInterrupted),
	)
	if err != nil {
		return nil, fmt.Errorf("query terminal Continuations: %w", err)
	}
	defer rows.Close()
	var continuations []TaskContinuation
	for rows.Next() {
		continuation, err := scanContinuation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan terminal Continuation: %w", err)
		}
		continuations = append(continuations, continuation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate terminal Continuations: %w", err)
	}
	return continuations, nil
}

// MarkContinuationReconciliation stores the durable outcome only after the
// owning semantic reconciliation has completed.
func (s *Service) MarkContinuationReconciliation(ctx context.Context, continuationID string, status ReconciliationStatus, mutationID string, reconciledAt time.Time) (TaskContinuation, error) {
	if status != ReconciliationCompleted && status != ReconciliationFailed {
		return TaskContinuation{}, fmt.Errorf("invalid Continuation reconciliation status %q", status)
	}
	stamp := reconciledAt.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("begin Continuation reconciliation marker: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE task_continuations
		SET blackboard_reconciliation_status=?,blackboard_reconciliation_mutation_id=?,blackboard_reconciled_at=?,updated_at=?
		WHERE id=?`, string(status), mutationID, stamp, stamp, continuationID)
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("store Continuation reconciliation marker: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return TaskContinuation{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return TaskContinuation{}, fmt.Errorf("commit Continuation reconciliation marker: %w", err)
	}
	return s.Continuation(continuationID)
}

// UpdateContinuationStatus updates the lifecycle status of a recorded Runtime
// Continuation.
func (s *Service) UpdateContinuationStatus(continuationID string, status Status) (TaskContinuation, error) {
	found, err := scanContinuation(s.db.QueryRow(
		`SELECT `+continuationSelectColumns+`
		 FROM task_continuations WHERE id = ?`,
		continuationID,
	))
	if err != nil {
		return TaskContinuation{}, err
	}
	if isTerminalStatus(found.Status) {
		if found.Status != status {
			return found, ErrContinuationStatusConflict
		}
		return s.notifyTerminalContinuation(found, string(status))
	}

	now := time.Now().UTC()
	previousStatus := found.Status
	found.Status = status
	found.UpdatedAt = now
	endedAt := ""
	if isTerminalStatus(status) {
		found.EndedAt = &now
		endedAt = now.Format(time.RFC3339Nano)
	} else {
		found.EndedAt = nil
	}

	result, err := s.db.Exec(
		`UPDATE task_continuations SET status = ?, updated_at = ?, ended_at = ? WHERE id = ? AND status = ?`,
		string(found.Status), found.UpdatedAt.Format(time.RFC3339Nano), endedAt, found.ID, string(previousStatus),
	)
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("update continuation status: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return TaskContinuation{}, fmt.Errorf("count continuation status update: %w", err)
	} else if changed != 1 {
		current, readErr := s.Continuation(continuationID)
		if readErr != nil {
			return TaskContinuation{}, readErr
		}
		if current.Status == status {
			return current, nil
		}
		return current, ErrContinuationStatusConflict
	}
	if isTerminalStatus(status) {
		return s.notifyTerminalContinuation(found, string(status))
	}
	return found, nil
}

func (s *Service) notifyTerminalContinuation(found TaskContinuation, reason string) (TaskContinuation, error) {
	if s.terminalMarker != nil {
		if err := s.terminalMarker.MarkContinuationTerminal(context.Background(), found.ID); err != nil {
			return TaskContinuation{}, fmt.Errorf("mark continuation capabilities terminal: %w", err)
		}
	}
	if s.reconciler != nil {
		owner, err := s.Get(found.TaskID)
		if err != nil {
			return found, fmt.Errorf("load terminal Continuation Task: %w", err)
		}
		if owner.RunControls.BlackboardMode != BlackboardModeDisabled {
			if err := s.reconciler.ReconcileTerminalContinuation(context.Background(), found.ID, reason); err != nil {
				return found, fmt.Errorf("reconcile terminal Continuation: %w", err)
			}
		}
	}
	return found, nil
}

func (s *Service) UpdateContinuationRuntimeMetadata(continuationID, containerID, nativeSessionID, nativeSessionPath string) (TaskContinuation, error) {
	found, err := scanContinuation(s.db.QueryRow(
		`SELECT `+continuationSelectColumns+`
		 FROM task_continuations WHERE id = ?`,
		continuationID,
	))
	if err != nil {
		return TaskContinuation{}, err
	}

	if containerID != "" {
		found.ContainerID = containerID
	}
	if nativeSessionID != "" {
		found.NativeSessionID = nativeSessionID
	}
	if nativeSessionPath != "" {
		found.NativeSessionPath = nativeSessionPath
	}
	found.UpdatedAt = time.Now().UTC()

	_, err = s.db.Exec(
		`UPDATE task_continuations
		 SET container_id = ?, native_session_id = ?, native_session_path = ?, updated_at = ?
		 WHERE id = ?`,
		found.ContainerID,
		found.NativeSessionID,
		found.NativeSessionPath,
		found.UpdatedAt.Format(time.RFC3339Nano),
		found.ID,
	)
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("update continuation runtime metadata: %w", err)
	}
	return found, nil
}

// RuntimeConfigVersions returns the captured runtime configuration versions for
// a task, ordered by version.
func (s *Service) RuntimeConfigVersions(taskID string) ([]RuntimeConfigVersion, error) {
	rows, err := s.db.Query(
		`SELECT id, task_id, version, runtime_profile_id, config_json, created_at FROM task_runtime_config_versions WHERE task_id = ? ORDER BY version ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("list config versions: %w", err)
	}
	defer rows.Close()

	var versions []RuntimeConfigVersion
	for rows.Next() {
		var version RuntimeConfigVersion
		var configJSON string
		var createdAt string
		if err := rows.Scan(&version.ID, &version.TaskID, &version.Version, &version.RuntimeProfileID, &configJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan config version: %w", err)
		}
		if err := json.Unmarshal([]byte(configJSON), &version.Config); err != nil {
			return nil, fmt.Errorf("decode config: %w", err)
		}
		if version.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list config versions: %w", err)
	}
	return versions, nil
}
