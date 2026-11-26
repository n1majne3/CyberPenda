package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HarnessSteeringDirective is one unconsumed task-local directive selected in
// Task Event order. EventID remains server authority and is never projected.
type HarnessSteeringDirective struct {
	EventID   string
	Directive string
}

// UnconsumedHarnessSteering returns requested directives that have no durable
// steering_applied marker yet.
func (s *Service) UnconsumedHarnessSteering(ctx context.Context, taskID string) ([]HarnessSteeringDirective, error) {
	_ = ctx
	events, err := s.Events(taskID)
	if err != nil {
		return nil, err
	}
	consumed := make(map[string]bool)
	for _, event := range events {
		if event.Kind != EventKindSteering || event.Payload["phase"] != "steering_applied" {
			continue
		}
		if requestedID, ok := event.Payload["requested_event_id"].(string); ok && requestedID != "" {
			consumed[requestedID] = true
		}
	}
	directives := make([]HarnessSteeringDirective, 0)
	for _, event := range events {
		if event.Kind != EventKindSteering || event.Payload["phase"] != "steering_requested" || consumed[event.ID] {
			continue
		}
		directive, _ := event.Payload["directive"].(string)
		if strings.TrimSpace(directive) != "" {
			directives = append(directives, HarnessSteeringDirective{EventID: event.ID, Directive: directive})
		}
	}
	return directives, nil
}

// RecordRuntimeConfig captures a new task runtime configuration version. The
// first call for a task is version 1; each subsequent call (e.g. after a profile
// switch) increments the version. This models a runtime continuation, not a new
// task.
func (s *Service) RecordRuntimeConfig(taskID, runtimeProfileID string, config map[string]any) (RuntimeConfigVersion, error) {
	if _, err := s.Get(taskID); err != nil {
		return RuntimeConfigVersion{}, err
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("encode config: %w", err)
	}

	now := time.Now().UTC()
	version := RuntimeConfigVersion{
		ID:               newID(),
		TaskID:           taskID,
		RuntimeProfileID: runtimeProfileID,
		Config:           config,
		CreatedAt:        now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var maxVersion sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM task_runtime_config_versions WHERE task_id = ?`, taskID).Scan(&maxVersion); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("read max version: %w", err)
	}
	version.Version = int(maxVersion.Int64) + 1

	if _, err := tx.Exec(
		`INSERT INTO task_runtime_config_versions (id, task_id, version, runtime_profile_id, config_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		version.ID, version.TaskID, version.Version, version.RuntimeProfileID, string(configJSON), version.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("store config version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("commit config version: %w", err)
	}
	return version, nil
}

// ContinuationLaunchRequest is the Task-domain portion of one atomic
// Continuation launch. The Blackboard v2 coordinator owns the surrounding
// transaction and Snapshot pin.
type ContinuationLaunchRequest struct {
	ProjectID        string
	TaskID           string
	RuntimeProfileID string
	RuntimeProvider  string
	Runner           Runner
	RuntimeConfig    map[string]any
	SteeringEventIDs []string
	// Native session metadata may cross a replacement Continuation boundary;
	// ContainerID may not, because the prior Runtime has already been proven absent.
	NativeSessionID   string
	NativeSessionPath string
}

// CreateContinuationLaunchTx stores the runtime configuration version and its
// pinned Continuation through the caller-owned launch transaction.
func (s *Service) CreateContinuationLaunchTx(ctx context.Context, tx *sql.Tx, req ContinuationLaunchRequest) (RuntimeConfigVersion, TaskContinuation, error) {
	if req.Runner != RunnerSandbox && req.Runner != RunnerHost {
		return RuntimeConfigVersion{}, TaskContinuation{}, ErrUnsupportedRunner
	}
	var projectID, runControlsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT project_id,run_controls_json FROM tasks WHERE id=?`, req.TaskID).Scan(&projectID, &runControlsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RuntimeConfigVersion{}, TaskContinuation{}, ErrNotFound
		}
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("read launch task: %w", err)
	}
	var runControls RunControls
	if err := json.Unmarshal([]byte(runControlsJSON), &runControls); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("decode launch task Run Controls: %w", err)
	}
	blackboardDisabled := runControls.BlackboardMode == BlackboardModeDisabled
	if projectID != req.ProjectID {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("launch task does not belong to project")
	}
	var latestStatus, latestReconciliation string
	err := tx.QueryRowContext(ctx, `
		SELECT status,blackboard_reconciliation_status FROM task_continuations
		WHERE task_id=? ORDER BY number DESC LIMIT 1`, req.TaskID,
	).Scan(&latestStatus, &latestReconciliation)
	if err == nil && !isTerminalStatus(Status(latestStatus)) {
		return RuntimeConfigVersion{}, TaskContinuation{}, ErrActiveContinuation
	}
	if !blackboardDisabled && err == nil && (Status(latestStatus) == StatusFailed || Status(latestStatus) == StatusStopped || Status(latestStatus) == StatusInterrupted) && latestReconciliation != string(ReconciliationCompleted) {
		return RuntimeConfigVersion{}, TaskContinuation{}, ErrContinuationReconciliationIncomplete
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("read current Continuation before launch: %w", err)
	}

	configJSON, err := json.Marshal(req.RuntimeConfig)
	if err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("encode launch runtime config: %w", err)
	}
	now := time.Now().UTC()
	config := RuntimeConfigVersion{
		ID: newID(), TaskID: req.TaskID, RuntimeProfileID: req.RuntimeProfileID,
		Config: req.RuntimeConfig, CreatedAt: now,
	}
	var maxConfigVersion sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM task_runtime_config_versions WHERE task_id=?`, req.TaskID).Scan(&maxConfigVersion); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("read max launch config version: %w", err)
	}
	config.Version = int(maxConfigVersion.Int64) + 1
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO task_runtime_config_versions (id,task_id,version,runtime_profile_id,config_json,created_at) VALUES (?,?,?,?,?,?)`,
		config.ID, config.TaskID, config.Version, config.RuntimeProfileID, string(configJSON), config.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("store launch runtime config: %w", err)
	}

	continuation := TaskContinuation{
		RuntimeConfigVersionID: config.ID,
		ID:                     newID(), TaskID: req.TaskID, RuntimeProfileID: req.RuntimeProfileID,
		RuntimeProvider: req.RuntimeProvider, Runner: req.Runner,
		NativeSessionID: strings.TrimSpace(req.NativeSessionID), NativeSessionPath: strings.TrimSpace(req.NativeSessionPath),
		Status: StatusPending, BlackboardReconciliationStatus: ReconciliationPending,
		StartedAt: now, UpdatedAt: now,
	}
	var maxContinuationNumber sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(number) FROM task_continuations WHERE task_id=?`, req.TaskID).Scan(&maxContinuationNumber); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("read max launch continuation number: %w", err)
	}
	continuation.Number = int(maxContinuationNumber.Int64) + 1
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO task_continuations (id,task_id,number,runtime_profile_id,runtime_provider,runner,status,container_id,native_session_id,native_session_path,started_at,updated_at,ended_at,runtime_config_version_id,blackboard_reconciliation_status)
		 VALUES (?,?,?,?,?,?,?,'',?,?,?,?,'',?,?)`,
		continuation.ID, continuation.TaskID, continuation.Number, continuation.RuntimeProfileID,
		continuation.RuntimeProvider, string(continuation.Runner), string(continuation.Status),
		continuation.NativeSessionID, continuation.NativeSessionPath,
		continuation.StartedAt.Format(time.RFC3339Nano), continuation.UpdatedAt.Format(time.RFC3339Nano),
		continuation.RuntimeConfigVersionID, string(continuation.BlackboardReconciliationStatus),
	); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("store launch continuation: %w", err)
	}
	if err := consumeHarnessSteeringTx(ctx, tx, req.TaskID, continuation.ID, req.SteeringEventIDs, now); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, err
	}
	return config, continuation, nil
}

// CreateContinuationLaunchWithoutBlackboard stores the ordinary Task Runtime
// configuration and Continuation identity without a Blackboard launch
// transaction. Blackboard-enabled launches use CreateContinuationLaunchTx
// through their continuity coordinator so the Snapshot pin and grant still
// commit atomically.
func (s *Service) CreateContinuationLaunchWithoutBlackboard(ctx context.Context, req ContinuationLaunchRequest) (RuntimeConfigVersion, TaskContinuation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("begin Task Continuation launch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	config, continuation, err := s.CreateContinuationLaunchTx(ctx, tx, req)
	if err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE task_continuations SET blackboard_reconciliation_status=? WHERE id=?`,
		string(ReconciliationCompleted), continuation.ID,
	); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("settle omitted Blackboard reconciliation: %w", err)
	}
	continuation.BlackboardReconciliationStatus = ReconciliationCompleted
	if err := tx.Commit(); err != nil {
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("commit Task Continuation launch: %w", err)
	}
	return config, continuation, nil
}

func consumeHarnessSteeringTx(ctx context.Context, tx *sql.Tx, taskID, continuationID string, eventIDs []string, now time.Time) error {
	if len(eventIDs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(eventIDs))
	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM task_events WHERE task_id=?`, taskID).Scan(&maxSeq); err != nil {
		return fmt.Errorf("read steering Event sequence: %w", err)
	}
	seq := int(maxSeq.Int64)
	for _, eventID := range eventIDs {
		if eventID == "" || seen[eventID] {
			return ErrSteeringSelectionConflict
		}
		seen[eventID] = true
		var kind, payloadJSON string
		if err := tx.QueryRowContext(ctx, `SELECT kind,payload_json FROM task_events WHERE id=? AND task_id=?`, eventID, taskID).Scan(&kind, &payloadJSON); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSteeringSelectionConflict
			}
			return fmt.Errorf("read selected Harness Steering: %w", err)
		}
		var payload EventPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return fmt.Errorf("decode selected Harness Steering: %w", err)
		}
		if kind != string(EventKindSteering) || payload["phase"] != "steering_requested" {
			return ErrSteeringSelectionConflict
		}
		var consumed int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM task_events
			WHERE task_id=? AND kind=? AND json_extract(payload_json,'$.phase')='steering_applied'
			  AND json_extract(payload_json,'$.requested_event_id')=?`, taskID, string(EventKindSteering), eventID,
		).Scan(&consumed); err != nil {
			return fmt.Errorf("validate selected Harness Steering: %w", err)
		}
		if consumed != 0 {
			return ErrSteeringSelectionConflict
		}
		appliedJSON, err := json.Marshal(EventPayload{"phase": "steering_applied", "requested_event_id": eventID})
		if err != nil {
			return fmt.Errorf("encode applied Harness Steering: %w", err)
		}
		seq++
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_events (id,task_id,continuation_id,seq,kind,payload_json,created_at)
			VALUES (?,?,?,?,?,?,?)`, newID(), taskID, continuationID, seq, string(EventKindSteering), string(appliedJSON), now.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("mark Harness Steering applied: %w", err)
		}
	}
	return nil
}
