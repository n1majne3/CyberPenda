package task

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// UpdateStatus sets the task lifecycle status and bumps updated_at. Steering
// actions that change run controls apply only at continuation boundaries and
// are recorded as events by the caller.
func (s *Service) UpdateStatus(taskID string, status Status) (Task, error) {
	found, err := s.Get(taskID)
	if err != nil {
		return Task{}, err
	}
	found.Status = status
	found.UpdatedAt = time.Now().UTC()

	_, err = s.db.Exec(`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`,
		string(found.Status), found.UpdatedAt.Format(time.RFC3339Nano), found.ID)
	if err != nil {
		return Task{}, fmt.Errorf("update status: %w", err)
	}
	return found, nil
}

// ReconcileInterruptedStateExcept preserves Tasks whose Runtime ownership was
// proven by the startup coordinator while interrupting every other active Task.
func (s *Service) ReconcileInterruptedStateExcept(ownedTaskIDs []string) (ReconcileInterruptedResult, error) {
	owned := make(map[string]struct{}, len(ownedTaskIDs))
	for _, taskID := range ownedTaskIDs {
		if taskID = strings.TrimSpace(taskID); taskID != "" {
			owned[taskID] = struct{}{}
		}
	}
	rows, err := s.db.Query(
		`SELECT id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at, (SELECT blackboard_protocol FROM projects WHERE projects.id=tasks.project_id)
		 FROM tasks WHERE status IN (?, ?, ?)`,
		string(StatusRunning), string(StatusPending), string(StatusPaused))
	if err != nil {
		return ReconcileInterruptedResult{}, fmt.Errorf("query active tasks: %w", err)
	}
	defer rows.Close()
	var active []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return ReconcileInterruptedResult{}, fmt.Errorf("scan active task: %w", err)
		}
		if _, excluded := owned[task.ID]; excluded {
			continue
		}
		active = append(active, task)
	}
	if err := rows.Err(); err != nil {
		return ReconcileInterruptedResult{}, fmt.Errorf("iterate active tasks: %w", err)
	}

	containerContinuations, err := s.runtimeContinuationsWithIdentities(owned)
	if err != nil {
		return ReconcileInterruptedResult{}, err
	}

	var changed []Task
	for _, t := range active {
		updated, err := s.UpdateStatus(t.ID, StatusInterrupted)
		if err != nil {
			return ReconcileInterruptedResult{Tasks: changed, Continuations: containerContinuations}, fmt.Errorf("interrupt task %s: %w", t.ID, err)
		}
		if err := s.interruptActiveContinuations(t.ID); err != nil {
			return ReconcileInterruptedResult{Tasks: changed, Continuations: containerContinuations}, fmt.Errorf("interrupt continuations for task %s: %w", t.ID, err)
		}
		changed = append(changed, updated)
	}
	if err := s.interruptStaleActiveContinuations(); err != nil {
		return ReconcileInterruptedResult{Tasks: changed, Continuations: containerContinuations}, err
	}
	return ReconcileInterruptedResult{Tasks: changed, Continuations: containerContinuations}, nil
}

func (s *Service) runtimeContinuationsWithIdentities(excludedTaskIDs map[string]struct{}) ([]TaskContinuation, error) {
	rows, err := s.db.Query(
		`SELECT `+continuationSelectColumns+`
			 FROM task_continuations
			 WHERE trim(container_id) <> ''
			   AND status IN (?, ?, ?)`,
		string(StatusPending),
		string(StatusRunning),
		string(StatusPaused),
	)
	if err != nil {
		return nil, fmt.Errorf("query runtime continuations with identities: %w", err)
	}
	defer rows.Close()
	var continuations []TaskContinuation
	for rows.Next() {
		continuation, err := scanContinuation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan runtime continuation with identity: %w", err)
		}
		if _, excluded := excludedTaskIDs[continuation.TaskID]; excluded {
			continue
		}
		continuations = append(continuations, continuation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runtime continuations with identities: %w", err)
	}
	return continuations, nil
}

func (s *Service) interruptActiveContinuations(taskID string) error {
	return s.interruptContinuations(
		`SELECT id,task_id FROM task_continuations
		 WHERE task_id = ? AND status IN (?, ?, ?)`,
		taskID,
		string(StatusPending),
		string(StatusRunning),
		string(StatusPaused),
	)
}

func (s *Service) interruptStaleActiveContinuations() error {
	return s.interruptContinuations(
		`SELECT id,task_id FROM task_continuations
		 WHERE status IN (?, ?, ?)
		   AND task_id IN (
		       SELECT id FROM tasks WHERE status NOT IN (?, ?, ?)
		   )`,
		string(StatusPending),
		string(StatusRunning),
		string(StatusPaused),
		string(StatusPending),
		string(StatusRunning),
		string(StatusPaused),
	)
}

func (s *Service) interruptContinuations(query string, args ...any) error {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("query active continuations: %w", err)
	}
	type continuationRef struct{ id, taskID string }
	var continuations []continuationRef
	for rows.Next() {
		var continuation continuationRef
		if err := rows.Scan(&continuation.id, &continuation.taskID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan active continuation: %w", err)
		}
		continuations = append(continuations, continuation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate active continuations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close active continuations: %w", err)
	}
	for _, continuation := range continuations {
		if _, err := s.interruptContinuationForRestart(continuation.taskID, continuation.id); err != nil {
			return fmt.Errorf("interrupt continuation %s: %w", continuation.id, err)
		}
	}
	return nil
}

func (s *Service) interruptContinuationForRestart(taskID, continuationID string) (TaskContinuation, error) {
	found, err := s.Continuation(continuationID)
	if err != nil {
		return TaskContinuation{}, err
	}
	now := time.Now().UTC()
	payloadJSON, err := json.Marshal(EventPayload{"phase": "interrupted", "reason": "daemon_restart"})
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("encode restart interruption Event: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("begin restart interruption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id=?`, taskID).Scan(&maxSeq); err != nil {
		return TaskContinuation{}, fmt.Errorf("read restart interruption Event sequence: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO task_events (id,task_id,continuation_id,seq,kind,payload_json,created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		newID(), taskID, continuationID, int(maxSeq.Int64)+1, string(EventKindLifecycle), string(payloadJSON), now.Format(time.RFC3339Nano),
	); err != nil {
		return TaskContinuation{}, fmt.Errorf("store restart interruption Event: %w", err)
	}
	result, err := tx.Exec(
		`UPDATE task_continuations SET status=?,updated_at=?,ended_at=?
		 WHERE id=? AND task_id=? AND status IN (?,?,?)`,
		string(StatusInterrupted), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), continuationID, taskID,
		string(StatusPending), string(StatusRunning), string(StatusPaused),
	)
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("store restart Continuation interruption: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return TaskContinuation{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return TaskContinuation{}, fmt.Errorf("commit restart Continuation interruption: %w", err)
	}
	found.Status = StatusInterrupted
	found.UpdatedAt = now
	found.EndedAt = &now
	return s.notifyTerminalContinuation(found, "daemon_restart")
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}

func scanContinuation(row scanner) (TaskContinuation, error) {
	var found TaskContinuation
	var runner string
	var status string
	var startedAt string
	var updatedAt string
	var endedAt string
	var blackboardReconciledAt string
	var runtimeConfigVersionID sql.NullString

	err := row.Scan(
		&found.ID,
		&found.TaskID,
		&found.Number,
		&found.RuntimeProfileID,
		&found.RuntimeProvider,
		&runner,
		&status,
		&found.ContainerID,
		&found.NativeSessionID,
		&found.NativeSessionPath,
		&startedAt,
		&updatedAt,
		&endedAt,
		&runtimeConfigVersionID,
		&found.BlackboardReconciliationStatus,
		&found.BlackboardReconciliationMutationID,
		&blackboardReconciledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskContinuation{}, ErrNotFound
	}
	if err != nil {
		return TaskContinuation{}, err
	}
	found.Runner = Runner(runner)
	found.Status = Status(status)
	if runtimeConfigVersionID.Valid {
		found.RuntimeConfigVersionID = runtimeConfigVersionID.String
	}
	if found.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
		return TaskContinuation{}, fmt.Errorf("parse started_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return TaskContinuation{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if endedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, endedAt)
		if err != nil {
			return TaskContinuation{}, fmt.Errorf("parse ended_at: %w", err)
		}
		found.EndedAt = &parsed
	}
	if blackboardReconciledAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, blackboardReconciledAt)
		if err != nil {
			return TaskContinuation{}, fmt.Errorf("parse blackboard_reconciled_at: %w", err)
		}
		found.BlackboardReconciledAt = &parsed
	}
	return found, nil
}

func isTerminalStatus(status Status) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusStopped, StatusInterrupted:
		return true
	default:
		return false
	}
}
