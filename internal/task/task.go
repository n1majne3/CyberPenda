// Package task owns the task domain: a user-goal-driven project run executed by
// one runtime profile through one runner. A task captures an immutable scope
// snapshot at launch, plus run controls. Task events form the structured
// timeline; raw output stays in logs or evidence artifacts, never in events.
package task

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
)

// Runner names the execution boundary for a task. The sandbox runner is the
// default; the host runner requires explicit activation.
type Runner string

const (
	RunnerSandbox Runner = "sandbox"
	RunnerHost    Runner = "host"
)

// Status is the lifecycle state of a task.
type Status string

const (
	StatusPending     Status = "pending"
	StatusRunning     Status = "running"
	StatusPaused      Status = "paused"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusStopped     Status = "stopped"
	StatusInterrupted Status = "interrupted"
)

// RunControls are the structured task launch settings: runner is stored
// separately because it gates execution boundary visibility.
type RunControls struct {
	HostActivated  bool              `json:"host_activated,omitempty"`
	SandboxNetwork string            `json:"sandbox_network,omitempty"`
	Notes          string            `json:"notes,omitempty"`
	Extras         map[string]string `json:"extras,omitempty"`
}

// ScopeSnapshot is an immutable copy of the project scope captured when a task
// starts. It records historical authorization and does not change when the
// current scope later changes.
type ScopeSnapshot = project.Scope

// EventKind classifies a task event. Events are structured and small; raw output
// stays in logs or evidence artifacts.
type EventKind string

const (
	EventKindRuntimeOutput EventKind = "runtime_output"
	EventKindStatus        EventKind = "status"
	EventKindSteering      EventKind = "steering"
	EventKindConversation  EventKind = "conversation"
	EventKindLifecycle     EventKind = "lifecycle"
)

// EventPayload is the structured payload of a task event. Keep it compact.
type EventPayload map[string]any

// Event is one structured timeline entry for a task.
type Event struct {
	ID             string       `json:"id"`
	TaskID         string       `json:"task_id"`
	ContinuationID string       `json:"continuation_id,omitempty"`
	Seq            int          `json:"seq"`
	Kind           EventKind    `json:"kind"`
	Payload        EventPayload `json:"payload"`
	CreatedAt      time.Time    `json:"created_at"`
}

// RuntimeConfigVersion is a historical task-specific runtime configuration
// captured for a runtime continuation. A runtime-profile switch inside a task
// creates a new version, not a new task.
type RuntimeConfigVersion struct {
	ID               string         `json:"id"`
	TaskID           string         `json:"task_id"`
	Version          int            `json:"version"`
	RuntimeProfileID string         `json:"runtime_profile_id"`
	Config           map[string]any `json:"config"`
	CreatedAt        time.Time      `json:"created_at"`
}

// ContinuationSnapshotPin is the immutable runtime-configuration and Blackboard
// snapshot metadata captured when a Continuation is created.
type ContinuationSnapshotPin struct {
	RuntimeConfigVersionID              string `json:"runtime_config_version_id,omitempty"`
	BlackboardGraphRevision             int    `json:"blackboard_graph_revision,omitempty"`
	BlackboardRendererVersion           string `json:"blackboard_renderer_version,omitempty"`
	BlackboardEstimatorVersion          string `json:"blackboard_estimator_version,omitempty"`
	BlackboardProjectionHash            string `json:"blackboard_projection_hash,omitempty"`
	BlackboardProjectionBytes           int    `json:"blackboard_projection_bytes,omitempty"`
	BlackboardProjectionEstimatedTokens int    `json:"blackboard_projection_estimated_tokens,omitempty"`
}

// TaskContinuation is one Runtime Continuation for a Task. It tracks the
// Runtime-specific run instance that later Stop/Resume controls will own.
type TaskContinuation struct {
	ContinuationSnapshotPin
	ID                string     `json:"id"`
	TaskID            string     `json:"task_id"`
	Number            int        `json:"number"`
	RuntimeProfileID  string     `json:"runtime_profile_id"`
	RuntimeProvider   string     `json:"runtime_provider"`
	Runner            Runner     `json:"runner"`
	Status            Status     `json:"status"`
	ContainerID       string     `json:"container_id,omitempty"`
	NativeSessionID   string     `json:"native_session_id,omitempty"`
	NativeSessionPath string     `json:"native_session_path,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
}

const continuationSelectColumns = `id, task_id, number, runtime_profile_id, runtime_provider, runner, status, container_id, native_session_id, native_session_path, started_at, updated_at, ended_at, runtime_config_version_id, blackboard_graph_revision, blackboard_renderer_version, blackboard_estimator_version, blackboard_projection_hash, blackboard_projection_bytes, blackboard_projection_estimated_tokens`

type RuntimeControls struct {
	NativeResumeAvailable   bool   `json:"native_resume_available"`
	NativeResumeReason      string `json:"native_resume_reason,omitempty"`
	HandoffResumeAvailable  bool   `json:"handoff_resume_available"`
	QueueSteerAvailable     bool   `json:"queue_steer_available"`
	InterruptSteerAvailable bool   `json:"interrupt_steer_available"`
	InterruptSteerReason    string `json:"interrupt_steer_reason,omitempty"`
	NativeSessionCaptured   bool   `json:"native_session_captured"`
	SameRuntimeProviderOnly bool   `json:"same_runtime_provider_only"`
	RuntimeProvider         string `json:"runtime_provider,omitempty"`
}

// ReconcileInterruptedResult describes active runtime state interrupted during
// daemon startup reconciliation.
type ReconcileInterruptedResult struct {
	Tasks         []Task
	Continuations []TaskContinuation
}

type SummaryVersion struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	Version     int       `json:"version"`
	Summary     string    `json:"summary"`
	SubmittedBy string    `json:"submitted_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Task is a single user-goal-driven run within a project.
type Task struct {
	ID                 string            `json:"id"`
	ProjectID          string            `json:"project_id"`
	Goal               string            `json:"goal"`
	Status             Status            `json:"status"`
	Runner             Runner            `json:"runner"`
	RuntimeProfileID   string            `json:"runtime_profile_id"`
	RunControls        RunControls       `json:"run_controls"`
	ScopeSnapshot      ScopeSnapshot     `json:"scope_snapshot"`
	RuntimeControls    RuntimeControls   `json:"runtime_controls"`
	ActiveContinuation *TaskContinuation `json:"active_continuation,omitempty"`
	LatestContinuation *TaskContinuation `json:"latest_continuation,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// CreateRequest is the input to Service.Create.
type CreateRequest struct {
	ProjectID        string
	Goal             string
	RuntimeProfileID string
	Runner           Runner
	RunControls      RunControls
}

// ErrNotFound is returned when no task matches the requested id.
var ErrNotFound = errors.New("task not found")

// ErrMissingGoal is returned when a task has no non-empty goal.
var ErrMissingGoal = errors.New("task goal is required")

// ErrProjectNotFound is returned when the project referenced by a task does not
// exist.
var ErrProjectNotFound = errors.New("project not found")

// ErrUnsupportedRunner is returned when the runner is neither sandbox nor host.
var ErrUnsupportedRunner = errors.New("runner must be sandbox or host")

var ErrMissingSummary = errors.New("task summary is required")

// Service implements task business rules against SQLite. It depends on the
// project service only to read the scope at launch; it does not mutate projects.
type GoalProjector interface {
	ProjectTaskGoal(taskID string) error
}

// ContinuationTerminalMarker closes capabilities whose lifecycle is bound to
// a Continuation when that Continuation reaches a terminal Task status.
type ContinuationTerminalMarker interface {
	MarkContinuationTerminal(context.Context, string) error
}

// Service owns durable Task state. Goal projection is an optional system
// adapter so graph data can remain dark until the graph store cutover.
type Service struct {
	db             *store.DB
	projects       *project.Service
	goalProjector  GoalProjector
	terminalMarker ContinuationTerminalMarker
}

// NewService returns a Service backed by the given database. It reads project
// scope through the provided project service to capture the launch snapshot.
func NewService(db *store.DB, projects ...*project.Service) *Service {
	svc := &Service{db: db}
	// Optional dependency injection: callers may pass a project service so the
	// task service can capture scope snapshots. If omitted, scope snapshots are
	// empty and the HTTP layer supplies the project service before launch.
	if len(projects) > 0 {
		svc.projects = projects[0]
	}
	return svc
}

// SetProjectService wires the project service used to read launch scope. This
// keeps the constructor simple while allowing the daemon to assemble services
// in any order.
func (s *Service) SetProjectService(projects *project.Service) {
	s.projects = projects
}

// SetContinuationTerminalMarker wires the lifecycle projection that closes
// Continuation-scoped capabilities in the same production assembly.
func (s *Service) SetContinuationTerminalMarker(marker ContinuationTerminalMarker) {
	s.terminalMarker = marker
}

// SetGoalProjector wires the system-owned Task Goal projection. Production
// leaves this unset until graph cutover; graph tests and migration wiring set it.
func (s *Service) SetGoalProjector(projector GoalProjector) {
	s.goalProjector = projector
}

func (s *Service) projectGoal(taskID string) error {
	if s.goalProjector == nil {
		return nil
	}
	if err := s.goalProjector.ProjectTaskGoal(taskID); err != nil {
		return fmt.Errorf("project task goal: %w", err)
	}
	return nil
}

// Create launches a new task: it validates the goal and runner, captures an
// immutable scope snapshot from the project, and persists the task.
func (s *Service) Create(req CreateRequest) (Task, error) {
	if req.Goal == "" {
		return Task{}, ErrMissingGoal
	}
	if req.Runner != RunnerSandbox && req.Runner != RunnerHost {
		return Task{}, ErrUnsupportedRunner
	}

	// Capture the scope snapshot from the live project. If a project service is
	// wired, read it; otherwise the snapshot is empty (caller is responsible for
	// providing scope out-of-band, e.g. the HTTP layer).
	var snapshot ScopeSnapshot
	if s.projects != nil {
		proj, err := s.projects.Get(req.ProjectID)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				return Task{}, ErrProjectNotFound
			}
			return Task{}, fmt.Errorf("read project scope: %w", err)
		}
		snapshot = proj.Scope
	}

	now := time.Now().UTC()
	created := Task{
		ID:               newID(),
		ProjectID:        req.ProjectID,
		Goal:             req.Goal,
		Status:           StatusPending,
		Runner:           req.Runner,
		RuntimeProfileID: req.RuntimeProfileID,
		RunControls:      req.RunControls,
		ScopeSnapshot:    snapshot,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	runControlsJSON, err := json.Marshal(created.RunControls)
	if err != nil {
		return Task{}, fmt.Errorf("encode run controls: %w", err)
	}
	scopeJSON, err := json.Marshal(created.ScopeSnapshot)
	if err != nil {
		return Task{}, fmt.Errorf("encode scope snapshot: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO tasks (id, project_id, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		created.ID, created.ProjectID, created.Goal, string(created.Status), string(created.Runner),
		created.RuntimeProfileID, string(runControlsJSON), string(scopeJSON),
		created.CreatedAt.Format(time.RFC3339Nano), created.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Task{}, fmt.Errorf("store task: %w", err)
	}
	if err := s.projectGoal(created.ID); err != nil {
		return created, err
	}
	return created, nil
}

// Get loads a single task by id.
func (s *Service) Get(id string) (Task, error) {
	return scanTask(s.db.QueryRow(
		`SELECT id, project_id, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at FROM tasks WHERE id = ?`,
		id,
	))
}

// ListForProject returns tasks for a project ordered by creation time.
func (s *Service) ListForProject(projectID string) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at
		 FROM tasks WHERE project_id = ? ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		found, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, found)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (Task, error) {
	var found Task
	var status string
	var runner string
	var runControlsJSON string
	var scopeJSON string
	var createdAt string
	var updatedAt string

	err := row.Scan(&found.ID, &found.ProjectID, &found.Goal, &status, &runner, &found.RuntimeProfileID, &runControlsJSON, &scopeJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	found.Status = Status(status)
	found.Runner = Runner(runner)
	if err := json.Unmarshal([]byte(runControlsJSON), &found.RunControls); err != nil {
		return Task{}, fmt.Errorf("decode run controls: %w", err)
	}
	if err := json.Unmarshal([]byte(scopeJSON), &found.ScopeSnapshot); err != nil {
		return Task{}, fmt.Errorf("decode scope snapshot: %w", err)
	}
	if found.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Task{}, fmt.Errorf("parse created_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Task{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return found, nil
}

// AppendEvent appends a structured event to the task timeline. Seq is assigned
// monotonically per task. The task must exist.
func (s *Service) AppendEvent(taskID string, kind EventKind, payload EventPayload) (Event, error) {
	return s.appendEvent(taskID, "", kind, payload)
}

// AppendContinuationEvent appends a Runtime Event bound to one Continuation.
// The Continuation must belong to the Task.
func (s *Service) AppendContinuationEvent(taskID, continuationID string, kind EventKind, payload EventPayload) (Event, error) {
	return s.appendEvent(taskID, continuationID, kind, payload)
}

func (s *Service) appendEvent(taskID, continuationID string, kind EventKind, payload EventPayload) (Event, error) {
	if _, err := s.Get(taskID); err != nil {
		return Event{}, err
	}
	if continuationID != "" {
		var ownerTaskID string
		if err := s.db.QueryRow(`SELECT task_id FROM task_continuations WHERE id=?`, continuationID).Scan(&ownerTaskID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Event{}, ErrNotFound
			}
			return Event{}, fmt.Errorf("load event Continuation: %w", err)
		}
		if ownerTaskID != taskID {
			return Event{}, ErrNotFound
		}
	}

	if payload == nil {
		payload = EventPayload{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}

	now := time.Now().UTC()
	event := Event{
		ID:             newID(),
		TaskID:         taskID,
		ContinuationID: continuationID,
		Kind:           kind,
		Payload:        payload,
		CreatedAt:      now,
	}

	// Compute next seq within a transaction so concurrent appends stay ordered.
	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id = ?`, taskID).Scan(&maxSeq); err != nil {
		return Event{}, fmt.Errorf("read max seq: %w", err)
	}
	event.Seq = int(maxSeq.Int64) + 1

	if _, err := tx.Exec(
		`INSERT INTO task_events (id, task_id, continuation_id, seq, kind, payload_json, created_at) VALUES (?, ?, NULLIF(?,''), ?, ?, ?, ?)`,
		event.ID, event.TaskID, event.ContinuationID, event.Seq, string(event.Kind), string(payloadJSON), event.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return Event{}, fmt.Errorf("store event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit event: %w", err)
	}
	return event, nil
}

// Events returns the task timeline ordered by sequence.
func (s *Service) Events(taskID string) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, task_id, continuation_id, seq, kind, payload_json, created_at FROM task_events WHERE task_id = ? ORDER BY seq ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var continuationID sql.NullString
		var kind string
		var payloadJSON string
		var createdAt string
		if err := rows.Scan(&event.ID, &event.TaskID, &continuationID, &event.Seq, &kind, &payloadJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.ContinuationID = continuationID.String
		event.Kind = EventKind(kind)
		if err := json.Unmarshal([]byte(payloadJSON), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode event payload: %w", err)
		}
		if event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return events, nil
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

// CreateContinuation records the next legacy Runtime Continuation for a Task
// before the harness launches it. Graph-native launch uses
// CreateContinuationWithSnapshotPin.
func (s *Service) CreateContinuation(taskID, runtimeProfileID, runtimeProvider string, runner Runner) (TaskContinuation, error) {
	return s.createContinuation(taskID, runtimeProfileID, runtimeProvider, runner, ContinuationSnapshotPin{})
}

// CreateContinuationWithSnapshotPin records immutable runtime configuration and
// canonical Blackboard snapshot metadata in the Continuation's creation row.
func (s *Service) CreateContinuationWithSnapshotPin(taskID, runtimeProfileID, runtimeProvider string, runner Runner, pin ContinuationSnapshotPin) (TaskContinuation, error) {
	if pin.RuntimeConfigVersionID == "" || pin.BlackboardGraphRevision < 0 || pin.BlackboardRendererVersion == "" || pin.BlackboardEstimatorVersion == "" || pin.BlackboardProjectionHash == "" || pin.BlackboardProjectionBytes <= 0 || pin.BlackboardProjectionEstimatedTokens <= 0 {
		return TaskContinuation{}, fmt.Errorf("invalid continuation snapshot pin")
	}
	return s.createContinuation(taskID, runtimeProfileID, runtimeProvider, runner, pin)
}

func (s *Service) createContinuation(taskID, runtimeProfileID, runtimeProvider string, runner Runner, pin ContinuationSnapshotPin) (TaskContinuation, error) {
	if _, err := s.Get(taskID); err != nil {
		return TaskContinuation{}, err
	}
	if err := s.projectGoal(taskID); err != nil {
		return TaskContinuation{}, err
	}

	now := time.Now().UTC()
	continuation := TaskContinuation{
		ContinuationSnapshotPin: pin,
		ID:                      newID(),
		TaskID:                  taskID,
		RuntimeProfileID:        runtimeProfileID,
		RuntimeProvider:         runtimeProvider,
		Runner:                  runner,
		Status:                  StatusPending,
		StartedAt:               now,
		UpdatedAt:               now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if pin.RuntimeConfigVersionID != "" {
		var pinnedRuntimeProfileID string
		if err := tx.QueryRow(`SELECT runtime_profile_id FROM task_runtime_config_versions WHERE id=? AND task_id=?`, pin.RuntimeConfigVersionID, taskID).Scan(&pinnedRuntimeProfileID); err != nil {
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

	var runtimeConfigVersionID any
	var graphRevision, projectionBytes, estimatedTokens any
	if pin.RuntimeConfigVersionID != "" {
		runtimeConfigVersionID = pin.RuntimeConfigVersionID
		graphRevision, projectionBytes, estimatedTokens = pin.BlackboardGraphRevision, pin.BlackboardProjectionBytes, pin.BlackboardProjectionEstimatedTokens
	}
	if _, err := tx.Exec(
		`INSERT INTO task_continuations (id, task_id, number, runtime_profile_id, runtime_provider, runner, status, container_id, native_session_id, native_session_path, started_at, updated_at, ended_at, runtime_config_version_id, blackboard_graph_revision, blackboard_renderer_version, blackboard_estimator_version, blackboard_projection_hash, blackboard_projection_bytes, blackboard_projection_estimated_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`,
		continuation.ID, continuation.TaskID, continuation.Number, continuation.RuntimeProfileID,
		continuation.RuntimeProvider, string(continuation.Runner), string(continuation.Status),
		continuation.StartedAt.Format(time.RFC3339Nano), continuation.UpdatedAt.Format(time.RFC3339Nano),
		runtimeConfigVersionID, graphRevision, pin.BlackboardRendererVersion, pin.BlackboardEstimatorVersion, pin.BlackboardProjectionHash, projectionBytes, estimatedTokens,
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

	now := time.Now().UTC()
	found.Status = status
	found.UpdatedAt = now
	endedAt := ""
	if isTerminalStatus(status) {
		found.EndedAt = &now
		endedAt = now.Format(time.RFC3339Nano)
	} else {
		found.EndedAt = nil
	}

	_, err = s.db.Exec(
		`UPDATE task_continuations SET status = ?, updated_at = ?, ended_at = ? WHERE id = ?`,
		string(found.Status), found.UpdatedAt.Format(time.RFC3339Nano), endedAt, found.ID,
	)
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("update continuation status: %w", err)
	}
	if isTerminalStatus(status) && s.terminalMarker != nil {
		if err := s.terminalMarker.MarkContinuationTerminal(context.Background(), found.ID); err != nil {
			return TaskContinuation{}, fmt.Errorf("mark continuation capabilities terminal: %w", err)
		}
	}
	return found, nil
}

// UpdateContinuationRuntimeMetadata stores best-effort runtime ownership data
// discovered for a continuation, such as container and native session ids.
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

func (s *Service) PutSummary(taskID, summary, submittedBy string) (SummaryVersion, error) {
	if _, err := s.Get(taskID); err != nil {
		return SummaryVersion{}, err
	}
	if summary == "" {
		return SummaryVersion{}, ErrMissingSummary
	}

	now := time.Now().UTC()
	version := SummaryVersion{
		ID:          newID(),
		TaskID:      taskID,
		Summary:     summary,
		SubmittedBy: submittedBy,
		CreatedAt:   now,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return SummaryVersion{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var maxVersion sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM task_summary_versions WHERE task_id = ?`, taskID).Scan(&maxVersion); err != nil {
		return SummaryVersion{}, fmt.Errorf("read max summary version: %w", err)
	}
	version.Version = int(maxVersion.Int64) + 1

	if _, err := tx.Exec(
		`INSERT INTO task_summary_versions (id, task_id, version, summary, submitted_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		version.ID, version.TaskID, version.Version, version.Summary, version.SubmittedBy, version.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return SummaryVersion{}, fmt.Errorf("store task summary: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SummaryVersion{}, fmt.Errorf("commit task summary: %w", err)
	}
	return version, nil
}

func (s *Service) SummaryVersions(taskID string) ([]SummaryVersion, error) {
	if _, err := s.Get(taskID); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT id, task_id, version, summary, submitted_by, created_at
		 FROM task_summary_versions WHERE task_id = ? ORDER BY version ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("list task summaries: %w", err)
	}
	defer rows.Close()

	var versions []SummaryVersion
	for rows.Next() {
		var version SummaryVersion
		var createdAt string
		if err := rows.Scan(&version.ID, &version.TaskID, &version.Version, &version.Summary, &version.SubmittedBy, &createdAt); err != nil {
			return nil, fmt.Errorf("scan task summary: %w", err)
		}
		if version.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list task summaries: %w", err)
	}
	return versions, nil
}

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
	if err := s.projectGoal(found.ID); err != nil {
		return found, err
	}
	return found, nil
}

// ReconcileInterruptedStatuses marks every task in an active state (running,
// created, paused) as interrupted. It is intended to run at daemon startup:
// those tasks belonged to a previous daemon instance whose in-memory harness
// state is gone, so nothing is actually running them. It returns the tasks it
// changed so the caller can log and emit lifecycle events.
func (s *Service) ReconcileInterruptedStatuses() ([]Task, error) {
	result, err := s.ReconcileInterruptedState()
	if err != nil {
		return result.Tasks, err
	}
	return result.Tasks, nil
}

// ReconcileInterruptedState marks every task and continuation in an active
// state as interrupted. It is intended to run at daemon startup: those runtime
// records belonged to a previous daemon instance whose in-memory harness state
// is gone. It returns the records it changed so callers can log and clean up
// runtime-owned resources such as sandbox containers.
func (s *Service) ReconcileInterruptedState() (ReconcileInterruptedResult, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at
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
		active = append(active, task)
	}
	if err := rows.Err(); err != nil {
		return ReconcileInterruptedResult{}, fmt.Errorf("iterate active tasks: %w", err)
	}

	containerContinuations, err := s.sandboxContinuationsWithContainers()
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

func (s *Service) sandboxContinuationsWithContainers() ([]TaskContinuation, error) {
	rows, err := s.db.Query(
		`SELECT `+continuationSelectColumns+`
		 FROM task_continuations
		 WHERE runner = ? AND trim(container_id) <> ''
		   AND status IN (?, ?, ?)`,
		string(RunnerSandbox),
		string(StatusPending),
		string(StatusRunning),
		string(StatusPaused),
	)
	if err != nil {
		return nil, fmt.Errorf("query sandbox continuations with containers: %w", err)
	}
	defer rows.Close()
	var continuations []TaskContinuation
	for rows.Next() {
		continuation, err := scanContinuation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sandbox continuation with container: %w", err)
		}
		continuations = append(continuations, continuation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sandbox continuations with containers: %w", err)
	}
	return continuations, nil
}

func (s *Service) interruptActiveContinuations(taskID string) error {
	return s.interruptContinuations(
		`SELECT id FROM task_continuations
		 WHERE task_id = ? AND status IN (?, ?, ?)`,
		taskID,
		string(StatusPending),
		string(StatusRunning),
		string(StatusPaused),
	)
}

func (s *Service) interruptStaleActiveContinuations() error {
	return s.interruptContinuations(
		`SELECT id FROM task_continuations
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
	var continuationIDs []string
	for rows.Next() {
		var continuationID string
		if err := rows.Scan(&continuationID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan active continuation: %w", err)
		}
		continuationIDs = append(continuationIDs, continuationID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate active continuations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close active continuations: %w", err)
	}
	for _, continuationID := range continuationIDs {
		if _, err := s.UpdateContinuationStatus(continuationID, StatusInterrupted); err != nil {
			return fmt.Errorf("interrupt continuation %s: %w", continuationID, err)
		}
	}
	return nil
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
	var runtimeConfigVersionID sql.NullString
	var graphRevision, projectionBytes, estimatedTokens sql.NullInt64

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
		&graphRevision,
		&found.BlackboardRendererVersion,
		&found.BlackboardEstimatorVersion,
		&found.BlackboardProjectionHash,
		&projectionBytes,
		&estimatedTokens,
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
	if graphRevision.Valid {
		found.BlackboardGraphRevision = int(graphRevision.Int64)
	}
	if projectionBytes.Valid {
		found.BlackboardProjectionBytes = int(projectionBytes.Int64)
	}
	if estimatedTokens.Valid {
		found.BlackboardProjectionEstimatedTokens = int(estimatedTokens.Int64)
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
