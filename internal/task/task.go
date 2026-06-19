// Package task owns the task domain: a user-goal-driven project run executed by
// one runtime profile through one runner. A task captures an immutable scope
// snapshot at launch, plus run controls. Task events form the structured
// timeline; raw output stays in logs or evidence artifacts, never in events.
package task

import (
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
// default; the host runner requires explicit activation or YOLO mode.
type Runner string

const (
	RunnerSandbox Runner = "sandbox"
	RunnerHost    Runner = "host"
)

// Status is the lifecycle state of a task.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusStopped   Status = "stopped"
)

// RunControls are the structured task launch settings: runner is stored
// separately because it gates execution boundary visibility.
type RunControls struct {
	YOLO            bool              `json:"yolo,omitempty"`
	HostActivated   bool              `json:"host_activated,omitempty"`
	Notes           string            `json:"notes,omitempty"`
	Extras          map[string]string `json:"extras,omitempty"`
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
	ID        string       `json:"id"`
	TaskID    string       `json:"task_id"`
	Seq       int          `json:"seq"`
	Kind      EventKind    `json:"kind"`
	Payload   EventPayload `json:"payload"`
	CreatedAt time.Time    `json:"created_at"`
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
	ID               string        `json:"id"`
	ProjectID        string        `json:"project_id"`
	Goal             string        `json:"goal"`
	Status           Status        `json:"status"`
	Runner           Runner        `json:"runner"`
	RuntimeProfileID string        `json:"runtime_profile_id"`
	RunControls      RunControls   `json:"run_controls"`
	ScopeSnapshot    ScopeSnapshot `json:"scope_snapshot"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
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
type Service struct {
	db       *store.DB
	projects *project.Service
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
	if _, err := s.Get(taskID); err != nil {
		return Event{}, err
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
		ID:        newID(),
		TaskID:    taskID,
		Kind:      kind,
		Payload:   payload,
		CreatedAt: now,
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
		`INSERT INTO task_events (id, task_id, seq, kind, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.TaskID, event.Seq, string(event.Kind), string(payloadJSON), event.CreatedAt.Format(time.RFC3339Nano),
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
		`SELECT id, task_id, seq, kind, payload_json, created_at FROM task_events WHERE task_id = ? ORDER BY seq ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var kind string
		var payloadJSON string
		var createdAt string
		if err := rows.Scan(&event.ID, &event.TaskID, &event.Seq, &kind, &payloadJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
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
	return found, nil
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}
