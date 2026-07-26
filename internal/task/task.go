// Package task owns the task domain: a user-goal-driven project run executed by
// one runtime profile through one runner. A task captures an immutable scope
// snapshot at launch, plus run controls. Task events form the structured
// timeline; raw output stays in logs or evidence artifacts, never in events.
package task

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	HostActivated            bool                     `json:"host_activated,omitempty"`
	SandboxNetwork           string                   `json:"sandbox_network,omitempty"`
	Notes                    string                   `json:"notes,omitempty"`
	Extras                   map[string]string        `json:"extras,omitempty"`
	BlackboardConclusionMode BlackboardConclusionMode `json:"blackboard_conclusion_mode"`
}

// BlackboardConclusionMode selects whether the operator alone prompts the
// Runtime to persist conclusions or the Harness assists at work-Turn bounds.
type BlackboardConclusionMode string

const (
	BlackboardConclusionModeInteractive BlackboardConclusionMode = "interactive"
	BlackboardConclusionModeAssisted    BlackboardConclusionMode = "assisted"
)

func normalizeBlackboardConclusionMode(mode BlackboardConclusionMode) (BlackboardConclusionMode, error) {
	switch mode {
	case "", BlackboardConclusionModeInteractive:
		return BlackboardConclusionModeInteractive, nil
	case BlackboardConclusionModeAssisted:
		return BlackboardConclusionModeAssisted, nil
	default:
		return "", ErrInvalidBlackboardConclusionMode
	}
}

// BlackboardConclusionState is the durable semantic-debt projection for a
// Task. Later orchestration slices add concluding and action-required states.
type BlackboardConclusionState string

const (
	BlackboardConclusionStateClean          BlackboardConclusionState = "clean"
	BlackboardConclusionStatePending        BlackboardConclusionState = "pending"
	BlackboardConclusionStateConcluding     BlackboardConclusionState = "concluding"
	BlackboardConclusionStateActionRequired BlackboardConclusionState = "action_required"
)

type BlackboardConclusionErrorCode string

const (
	BlackboardConclusionErrorInvalidResult    BlackboardConclusionErrorCode = "semantic_conclusion_invalid_result"
	BlackboardConclusionErrorToolUseForbidden BlackboardConclusionErrorCode = "conclude_tool_use_forbidden"
	BlackboardConclusionErrorRepairExhausted  BlackboardConclusionErrorCode = "semantic_conclusion_repair_exhausted"
	BlackboardConclusionErrorVersionConflict  BlackboardConclusionErrorCode = "semantic_conclusion_version_conflict"
	BlackboardConclusionAutomaticTurnLimit                                  = 2
)

// BlackboardConclusion is the compact Task read view for the latest assisted
// Work Turn checkpoint and any conclusion progress it triggered.
type BlackboardConclusion struct {
	Mode                         BlackboardConclusionMode      `json:"mode"`
	State                        BlackboardConclusionState     `json:"state"`
	SourceTurnID                 string                        `json:"source_turn_id,omitempty"`
	SourceWorkWatermark          int                           `json:"source_work_watermark"`
	SemanticPersistenceWatermark int                           `json:"semantic_persistence_watermark"`
	AppliedRevision              *int                          `json:"applied_revision,omitempty"`
	ErrorCode                    BlackboardConclusionErrorCode `json:"error_code,omitempty"`
	RetryAvailable               bool                          `json:"retry_available"`
	NextEligibleAt               *time.Time                    `json:"next_eligible_at,omitempty"`
}

// ScopeSnapshot is an immutable copy of the project scope captured when a task
// starts. It records historical authorization and does not change when the
// current scope later changes.
type ScopeSnapshot = project.Scope

// EventKind classifies a task event. Events are structured and small; raw output
// stays in logs or evidence artifacts.
type EventKind string

const (
	EventKindRuntimeOutput        EventKind = "runtime_output"
	EventKindStatus               EventKind = "status"
	EventKindSteering             EventKind = "steering"
	EventKindConversation         EventKind = "conversation"
	EventKindLifecycle            EventKind = "lifecycle"
	EventKindBlackboardConclusion EventKind = "blackboard_conclusion"
)

// EventPayload is the structured payload of a task event. Keep it compact.
type EventPayload map[string]any

// Event is one structured timeline entry for a task.
type Event struct {
	ID             string       `json:"id"`
	TaskID         string       `json:"task_id"`
	ContinuationID string       `json:"continuation_id,omitempty"`
	AttemptNodeID  string       `json:"attempt_node_id,omitempty"`
	Seq            int          `json:"seq"`
	Kind           EventKind    `json:"kind"`
	Payload        EventPayload `json:"payload"`
	CreatedAt      time.Time    `json:"created_at"`
}

// BlackboardConclusionReceiptState is the internal durable state machine. It
// is projected to the smaller operator-facing BlackboardConclusionState.
type BlackboardConclusionReceiptState string

const (
	BlackboardConclusionReceiptClean                                BlackboardConclusionReceiptState = "clean"
	BlackboardConclusionReceiptPending                              BlackboardConclusionReceiptState = "pending"
	BlackboardConclusionReceiptDispatchRequested                    BlackboardConclusionReceiptState = "dispatch_requested"
	BlackboardConclusionReceiptAwaitingResult                       BlackboardConclusionReceiptState = "awaiting_result"
	BlackboardConclusionReceiptRepairDispatchRequested              BlackboardConclusionReceiptState = "repair_dispatch_requested"
	BlackboardConclusionReceiptVersionSyncRequested                 BlackboardConclusionReceiptState = "version_sync_requested"
	BlackboardConclusionReceiptVersionRegenerationDispatchRequested BlackboardConclusionReceiptState = "version_regeneration_dispatch_requested"
	BlackboardConclusionReceiptActionRequired                       BlackboardConclusionReceiptState = "action_required"
	BlackboardConclusionReceiptValidated                            BlackboardConclusionReceiptState = "validated"
	BlackboardConclusionReceiptApplied                              BlackboardConclusionReceiptState = "applied"
)

// SemanticDebtWatermarks compare terminal Work Tool Results with the latest
// successful semantic persistence that covers them.
type SemanticDebtWatermarks struct {
	SourceWork          int
	SemanticPersistence int
}

func (watermarks SemanticDebtWatermarks) valid() bool {
	return watermarks.SemanticPersistence >= 0 && watermarks.SourceWork >= watermarks.SemanticPersistence
}

// BlackboardConclusionReceipt is the durable coordinator record for one
// completed assisted work Runtime Turn. Structured result bytes remain
// internal and are never projected on Task APIs.
type BlackboardConclusionReceipt struct {
	ID                           string                           `json:"id"`
	TaskID                       string                           `json:"task_id"`
	ContinuationID               string                           `json:"continuation_id"`
	SourceSessionID              string                           `json:"source_session_id"`
	SourceTurnID                 string                           `json:"source_turn_id"`
	InternalState                BlackboardConclusionReceiptState `json:"internal_state"`
	SourceWorkWatermark          int                              `json:"source_work_watermark"`
	SemanticPersistenceWatermark int                              `json:"semantic_persistence_watermark"`
	DispatchRequestID            string                           `json:"dispatch_request_id,omitempty"`
	ControlTurnID                string                           `json:"control_turn_id,omitempty"`
	BaseRevision                 *int                             `json:"base_revision,omitempty"`
	SourceSelection              TurnSelection                    `json:"source_selection"`
	CanonicalResultJSON          []byte                           `json:"-"`
	CanonicalResultSHA256        string                           `json:"canonical_result_sha256,omitempty"`
	ApplyIdempotencyKey          string                           `json:"apply_idempotency_key,omitempty"`
	AppliedRevision              *int                             `json:"applied_revision,omitempty"`
	AutomaticTurnCount           int                              `json:"automatic_turn_count"`
	RepairCount                  int                              `json:"repair_count"`
	VersionRegenerationCount     int                              `json:"version_regeneration_count"`
	ExplicitRetryCount           int                              `json:"explicit_retry_count"`
	OperatorRetryKey             string                           `json:"-"`
	NextEligibleAt               *time.Time                       `json:"next_eligible_at,omitempty"`
	ErrorCode                    BlackboardConclusionErrorCode    `json:"error_code,omitempty"`
	CreatedAt                    time.Time                        `json:"created_at"`
	UpdatedAt                    time.Time                        `json:"updated_at"`
}

// View projects internal coordinator progress into the compact Task API
// vocabulary. Canonical result bytes and dispatch idempotency stay private.
func (receipt BlackboardConclusionReceipt) View(mode BlackboardConclusionMode) BlackboardConclusion {
	return receipt.ViewAt(mode, time.Now().UTC())
}

func (receipt BlackboardConclusionReceipt) ViewAt(mode BlackboardConclusionMode, now time.Time) BlackboardConclusion {
	view := BlackboardConclusion{
		Mode: mode, SourceTurnID: receipt.SourceTurnID,
		SourceWorkWatermark:          receipt.SourceWorkWatermark,
		SemanticPersistenceWatermark: receipt.SemanticPersistenceWatermark,
	}
	switch receipt.InternalState {
	case BlackboardConclusionReceiptClean:
		view.State = BlackboardConclusionStateClean
	case BlackboardConclusionReceiptPending:
		view.State = BlackboardConclusionStatePending
	case BlackboardConclusionReceiptApplied:
		view.State = BlackboardConclusionStateClean
		if receipt.AppliedRevision != nil {
			view.AppliedRevision = intPointer(*receipt.AppliedRevision)
		}
	case BlackboardConclusionReceiptActionRequired:
		view.State = BlackboardConclusionStateActionRequired
		view.ErrorCode = receipt.ErrorCode
		if receipt.NextEligibleAt != nil {
			eligible := receipt.NextEligibleAt.UTC()
			view.NextEligibleAt = &eligible
			view.RetryAvailable = !now.Before(eligible)
		}
	default:
		view.State = BlackboardConclusionStateConcluding
	}
	return view
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

// TaskContinuation is one Runtime Continuation for a Task. It tracks the
// Runtime-specific run instance that later Stop/Resume controls will own.
type TaskContinuation struct {
	RuntimeConfigVersionID             string               `json:"runtime_config_version_id,omitempty"`
	ID                                 string               `json:"id"`
	TaskID                             string               `json:"task_id"`
	Number                             int                  `json:"number"`
	RuntimeProfileID                   string               `json:"runtime_profile_id"`
	RuntimeProvider                    string               `json:"runtime_provider"`
	Runner                             Runner               `json:"runner"`
	Status                             Status               `json:"status"`
	ContainerID                        string               `json:"container_id,omitempty"`
	NativeSessionID                    string               `json:"native_session_id,omitempty"`
	NativeSessionPath                  string               `json:"native_session_path,omitempty"`
	StartedAt                          time.Time            `json:"started_at"`
	UpdatedAt                          time.Time            `json:"updated_at"`
	EndedAt                            *time.Time           `json:"ended_at,omitempty"`
	BlackboardReconciliationStatus     ReconciliationStatus `json:"blackboard_reconciliation_status"`
	BlackboardReconciliationMutationID string               `json:"blackboard_reconciliation_mutation_id,omitempty"`
	BlackboardReconciledAt             *time.Time           `json:"blackboard_reconciled_at,omitempty"`
}

// ReconciliationStatus is the durable normal/unexpected reconciliation marker
// for one Runtime Continuation.
type ReconciliationStatus string

const (
	ReconciliationPending   ReconciliationStatus = "pending"
	ReconciliationCompleted ReconciliationStatus = "completed"
	ReconciliationFailed    ReconciliationStatus = "failed"
)

const continuationSelectColumns = `id, task_id, number, runtime_profile_id, runtime_provider, runner, status, container_id, native_session_id, native_session_path, started_at, updated_at, ended_at, runtime_config_version_id, blackboard_reconciliation_status, blackboard_reconciliation_mutation_id, blackboard_reconciled_at`

type RuntimeControls struct {
	NativeResumeAvailable bool   `json:"native_resume_available"`
	NativeResumeReason    string `json:"native_resume_reason,omitempty"`
	NativeSteerAvailable  bool   `json:"native_steer_available"`
	NativeSteerMode       string `json:"native_steer_mode,omitempty"`
	NativeSteerState      string `json:"native_steer_state,omitempty"`
	NativeSteerRequestID  string `json:"native_steer_request_id,omitempty"`
	NativeSteerReason     string `json:"native_steer_reason,omitempty"`
	ResumeAvailable       bool   `json:"resume_available"`
	// FinishAvailable is true only when Runtime Activity is live and idle.
	// Operator Task Finish is gated by current session health, not Task status.
	FinishAvailable         bool   `json:"finish_available"`
	QueueSteerAvailable     bool   `json:"queue_steer_available"`
	InterruptSteerAvailable bool   `json:"interrupt_steer_available"`
	InterruptSteerReason    string `json:"interrupt_steer_reason,omitempty"`
	NativeSessionCaptured   bool   `json:"native_session_captured"`
	SameRuntimeProviderOnly bool   `json:"same_runtime_provider_only"`
	RuntimeProvider         string `json:"runtime_provider,omitempty"`
	// ProjectedModelProviderIDs is the fixed-at-launch set of global Model
	// Providers projected into a Pi runtime (ADR 0015). Empty/omitted means
	// native cross-provider is fail-closed; operator must restart.
	ProjectedModelProviderIDs []string `json:"projected_model_provider_ids,omitempty"`
	// TurnSelection is the preceding Runtime Turn Selection retained for the
	// Task Conversation composer. Every turn still sends a complete selection.
	TurnSelection       *TurnSelection              `json:"turn_selection,omitempty"`
	ProviderPermissions []ProviderPermissionRequest `json:"provider_permissions,omitempty"`
	RecoveryState       string                      `json:"recovery_state,omitempty"`
	RecoveryReason      string                      `json:"recovery_reason,omitempty"`
}

// TurnSelection is the Model Provider, model, and Requested Reasoning Effort
// resolved for a Runtime Turn.
type TurnSelection struct {
	ModelProviderID string `json:"model_provider_id,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// ProviderPermissionRequest is a redacted pending approval exposed to the
// authenticated Task UI. Provider wire details never cross this boundary.
type ProviderPermissionRequest struct {
	RequestID           string    `json:"request_id"`
	PermissionRequestID string    `json:"permission_request_id"`
	SessionID           string    `json:"session_id,omitempty"`
	ProviderTurnID      string    `json:"provider_turn_id,omitempty"`
	Provider            string    `json:"provider,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// ReconcileInterruptedResult describes active runtime state interrupted during
// daemon startup reconciliation.
type ReconcileInterruptedResult struct {
	Tasks         []Task
	Continuations []TaskContinuation
}

// RuntimeActivity is the operator-visible current Runtime health, independent
// of durable Task lifecycle. It is computed from daemon-owned process/session
// health and terminal notifications, not from stored session identity, Task
// Events, or elapsed time.
type RuntimeActivity struct {
	// Liveness is one of live, offline, orphaned, or unknown.
	Liveness string `json:"liveness"`
	// TurnActivity is busy or idle while Liveness is live; empty otherwise.
	TurnActivity string `json:"turn_activity,omitempty"`
	// Warning explains unknown liveness without mutating Task lifecycle.
	Warning string `json:"warning,omitempty"`
}

// Task is a single user-goal-driven run within a project.
type Task struct {
	ID               string          `json:"id"`
	ProjectID        string          `json:"project_id"`
	Goal             string          `json:"goal"`
	Status           Status          `json:"status"`
	Runner           Runner          `json:"runner"`
	RuntimeProfileID string          `json:"runtime_profile_id"`
	RunControls      RunControls     `json:"run_controls"`
	ScopeSnapshot    ScopeSnapshot   `json:"scope_snapshot"`
	RuntimeControls  RuntimeControls `json:"runtime_controls"`
	// RuntimeActivity is current process/session health, not Task status.
	RuntimeActivity      RuntimeActivity      `json:"runtime_activity"`
	BlackboardConclusion BlackboardConclusion `json:"blackboard_conclusion"`
	ActiveContinuation   *TaskContinuation    `json:"active_continuation,omitempty"`
	LatestContinuation   *TaskContinuation    `json:"latest_continuation,omitempty"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
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

// ErrActiveTask is returned when deletion is requested while a task may still
// launch or continue runtime work.
var ErrActiveTask = errors.New("active task cannot be deleted")

// ErrUnsupportedRunner is returned when the runner is neither sandbox nor host.
var ErrUnsupportedRunner = errors.New("runner must be sandbox or host")

var ErrInvalidBlackboardConclusionMode = errors.New("Blackboard conclusion mode must be interactive or assisted")

var ErrInvalidBlackboardConclusionReceipt = errors.New("invalid Blackboard conclusion checkpoint receipt")

var ErrBlackboardConclusionRetryCooldown = errors.New("Blackboard conclusion retry is not yet eligible")

// ErrContinuationStatusConflict prevents a late lifecycle observer from
// overwriting a Continuation that already reached a different terminal state.
var ErrContinuationStatusConflict = errors.New("continuation status conflicts with its terminal state")

// ErrActiveContinuation prevents Resume from creating a second current pin
// before the previous Continuation reaches a terminal state.
var ErrActiveContinuation = errors.New("task already has an active continuation")

// ErrContinuationReconciliationIncomplete prevents Resume from bypassing
// durable interruption reconciliation owned by the prior Continuation.
var ErrContinuationReconciliationIncomplete = errors.New("prior continuation reconciliation is incomplete")

// ErrSteeringSelectionConflict reports a stale or foreign steering selection.
var ErrSteeringSelectionConflict = errors.New("Harness Steering selection is stale or invalid")

// Service implements task business rules against SQLite. It depends on the
// project service only to read the scope at launch; it does not mutate projects.
// ContinuationTerminalMarker closes capabilities whose lifecycle is bound to
// a Continuation when that Continuation reaches a terminal Task status.
type ContinuationTerminalMarker interface {
	MarkContinuationTerminal(context.Context, string) error
}

// ContinuationReconciler closes open semantic work after the Task domain has
// durably made a Continuation terminal.
type ContinuationReconciler interface {
	ReconcileTerminalContinuation(context.Context, string, string) error
}

// Service owns durable Task state.
type Service struct {
	db             *store.DB
	projects       *project.Service
	terminalMarker ContinuationTerminalMarker
	reconciler     ContinuationReconciler
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

// SetContinuationReconciler wires post-terminal semantic reconciliation.
// adapter. The Task domain still owns the Continuation status transition.
func (s *Service) SetContinuationReconciler(reconciler ContinuationReconciler) {
	s.reconciler = reconciler
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
	mode, err := normalizeBlackboardConclusionMode(req.RunControls.BlackboardConclusionMode)
	if err != nil {
		return Task{}, err
	}
	req.RunControls.BlackboardConclusionMode = mode

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
		ID:                   newID(),
		ProjectID:            req.ProjectID,
		Goal:                 req.Goal,
		Status:               StatusPending,
		Runner:               req.Runner,
		RuntimeProfileID:     req.RuntimeProfileID,
		RunControls:          req.RunControls,
		BlackboardConclusion: BlackboardConclusion{Mode: mode, State: BlackboardConclusionStateClean},
		ScopeSnapshot:        snapshot,
		CreatedAt:            now,
		UpdatedAt:            now,
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
		`SELECT id, project_id, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at FROM tasks WHERE id = ? AND deleted_at = ''`,
		id,
	))
}

// ListForProject returns tasks for a project ordered by creation time.
func (s *Service) ListForProject(projectID string) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at
		 FROM tasks WHERE project_id = ? AND deleted_at = '' ORDER BY created_at ASC`,
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

// Delete removes a terminal task from normal Task surfaces while retaining its
// durable row for Blackboard provenance and historical joins.
func (s *Service) Delete(id string) error {
	result, err := s.db.Exec(
		`UPDATE tasks SET deleted_at = ?
		 WHERE id = ? AND deleted_at = '' AND status NOT IN (?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), id,
		string(StatusPending), string(StatusRunning), string(StatusPaused),
	)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete task rows affected: %w", err)
	}
	if updated == 1 {
		return nil
	}

	var status string
	var deletedAt string
	if err := s.db.QueryRow(`SELECT status, deleted_at FROM tasks WHERE id = ?`, id).Scan(&status, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read task deletion state: %w", err)
	}
	if deletedAt != "" {
		return ErrNotFound
	}
	return ErrActiveTask
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
	mode, err := normalizeBlackboardConclusionMode(found.RunControls.BlackboardConclusionMode)
	if err != nil {
		return Task{}, err
	}
	found.RunControls.BlackboardConclusionMode = mode
	found.BlackboardConclusion = BlackboardConclusion{Mode: mode, State: BlackboardConclusionStateClean}
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
		`SELECT id, task_id, continuation_id, attempt_node_id, seq, kind, payload_json, created_at FROM task_events WHERE task_id = ? ORDER BY seq ASC`,
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
		var attemptNodeID sql.NullString
		var kind string
		var payloadJSON string
		var createdAt string
		if err := rows.Scan(&event.ID, &event.TaskID, &continuationID, &attemptNodeID, &event.Seq, &kind, &payloadJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.ContinuationID = continuationID.String
		event.AttemptNodeID = attemptNodeID.String
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

// RecordBlackboardConclusionCheckpoint creates the one durable semantic-debt
// receipt for a completed assisted Work Runtime Turn. The receipt and its
// compact Task Event are committed together; replay returns the original.
func (s *Service) RecordBlackboardConclusionCheckpoint(taskID, continuationID, sourceSessionID, sourceTurnID string, sourceSelection TurnSelection, watermarks SemanticDebtWatermarks) (BlackboardConclusionReceipt, bool, error) {
	taskID = strings.TrimSpace(taskID)
	continuationID = strings.TrimSpace(continuationID)
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	sourceTurnID = strings.TrimSpace(sourceTurnID)
	sourceSelection.ModelProviderID = strings.TrimSpace(sourceSelection.ModelProviderID)
	sourceSelection.Model = strings.TrimSpace(sourceSelection.Model)
	sourceSelection.ReasoningEffort = strings.TrimSpace(sourceSelection.ReasoningEffort)
	if taskID == "" || continuationID == "" || sourceSessionID == "" || sourceTurnID == "" ||
		sourceSelection.ModelProviderID == "" || sourceSelection.Model == "" || !watermarks.valid() {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	found, err := s.Get(taskID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if found.RunControls.BlackboardConclusionMode != BlackboardConclusionModeAssisted {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}

	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Blackboard conclusion receipt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var ownerTaskID string
	if err := tx.QueryRow(`SELECT task_id FROM task_continuations WHERE id=?`, continuationID).Scan(&ownerTaskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BlackboardConclusionReceipt{}, false, ErrNotFound
		}
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("load Blackboard conclusion Continuation: %w", err)
	}
	if ownerTaskID != taskID {
		return BlackboardConclusionReceipt{}, false, ErrNotFound
	}

	prior, err := scanBlackboardConclusionReceipt(tx.QueryRow(`
		SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE task_id=? AND continuation_id=? AND source_turn_id=?`, taskID, continuationID, sourceTurnID))
	if err == nil {
		return prior, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("load Blackboard conclusion checkpoint receipt: %w", err)
	}

	now := time.Now().UTC()
	receiptState := BlackboardConclusionReceiptPending
	phase := "pending_detected"
	if watermarks.SourceWork <= watermarks.SemanticPersistence {
		receiptState = BlackboardConclusionReceiptClean
		phase = "persistence_current"
	}
	receipt := BlackboardConclusionReceipt{
		ID: newID(), TaskID: taskID, ContinuationID: continuationID,
		SourceSessionID: sourceSessionID, SourceTurnID: sourceTurnID,
		InternalState: receiptState, SourceWorkWatermark: watermarks.SourceWork,
		SemanticPersistenceWatermark: watermarks.SemanticPersistence,
		SourceSelection:              sourceSelection,
		CreatedAt:                    now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`
		INSERT INTO assisted_conclusion_receipts
		(id,task_id,continuation_id,source_session_id,source_turn_id,state,source_work_watermark,semantic_persistence_watermark,
		 source_model_provider_id,source_model,source_reasoning_effort,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.TaskID, receipt.ContinuationID, receipt.SourceSessionID,
		receipt.SourceTurnID, string(receipt.InternalState), receipt.SourceWorkWatermark, receipt.SemanticPersistenceWatermark,
		receipt.SourceSelection.ModelProviderID, receipt.SourceSelection.Model, receipt.SourceSelection.ReasoningEffort,
		receipt.CreatedAt.Format(time.RFC3339Nano), receipt.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Blackboard conclusion checkpoint receipt: %w", err)
	}

	payload := EventPayload{
		"phase": phase, "receipt_id": receipt.ID, "source_turn_id": receipt.SourceTurnID,
		"source_work_watermark":          receipt.SourceWorkWatermark,
		"semantic_persistence_watermark": receipt.SemanticPersistenceWatermark,
	}
	if err := appendBlackboardConclusionEventTx(tx, receipt, payload, now); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Blackboard conclusion Event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Blackboard conclusion checkpoint receipt: %w", err)
	}
	return receipt, true, nil
}

// ClaimBlackboardConclusionDispatch atomically persists deterministic provider
// and Blackboard idempotency lineage before any external SendTurn. Won is true
// only for the caller that moved the receipt out of pending.
func (s *Service) ClaimBlackboardConclusionDispatch(receiptID string, baseRevision int) (BlackboardConclusionReceipt, bool, error) {
	receiptID = strings.TrimSpace(receiptID)
	if receiptID == "" || baseRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Blackboard conclusion dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := loadBlackboardConclusionReceiptByID(tx, receiptID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	dispatchID, applyKey := blackboardConclusionRequestLineage(receipt.ContinuationID, receipt.SourceTurnID)
	if receipt.InternalState != BlackboardConclusionReceiptPending {
		if receipt.DispatchRequestID == dispatchID && receipt.ApplyIdempotencyKey == applyKey && receipt.BaseRevision != nil && *receipt.BaseRevision == baseRevision {
			return receipt, false, nil
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	result, err := tx.Exec(`UPDATE assisted_conclusion_receipts
		SET state=?,dispatch_request_id=?,base_revision=?,apply_idempotency_key=?,automatic_turn_count=1,updated_at=?
		WHERE id=? AND state=?`, string(BlackboardConclusionReceiptDispatchRequested), dispatchID, baseRevision, applyKey,
		now.Format(time.RFC3339Nano), receipt.ID, string(BlackboardConclusionReceiptPending))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Blackboard conclusion dispatch: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Blackboard conclusion dispatch lost update")
	}
	receipt.InternalState = BlackboardConclusionReceiptDispatchRequested
	receipt.DispatchRequestID = dispatchID
	receipt.ApplyIdempotencyKey = applyKey
	receipt.BaseRevision = intPointer(baseRevision)
	receipt.AutomaticTurnCount = 1
	receipt.UpdatedAt = now
	if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "dispatch_requested", "receipt_id": receipt.ID, "source_turn_id": receipt.SourceTurnID,
		"request_id": dispatchID, "base_revision": baseRevision, "turn_kind": "control",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Blackboard conclusion dispatch: %w", err)
	}
	return receipt, true, nil
}

// MarkBlackboardConclusionAwaiting records provider acceptance of the Conclude
// Turn. Replaying the same correlation is idempotent.
func (s *Service) MarkBlackboardConclusionAwaiting(dispatchRequestID, controlTurnID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	controlTurnID = strings.TrimSpace(controlTurnID)
	if dispatchRequestID == "" || controlTurnID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	from := BlackboardConclusionReceiptDispatchRequested
	current, err := s.BlackboardConclusionByDispatchRequestID(dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if current.InternalState == BlackboardConclusionReceiptRepairDispatchRequested {
		from = BlackboardConclusionReceiptRepairDispatchRequested
	} else if current.InternalState == BlackboardConclusionReceiptVersionRegenerationDispatchRequested {
		from = BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	}
	return s.advanceBlackboardConclusion(dispatchRequestID, from, BlackboardConclusionReceiptAwaitingResult,
		func(receipt BlackboardConclusionReceipt) bool { return receipt.ControlTurnID == controlTurnID },
		func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,control_turn_id=?,next_eligible_at=NULL,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptAwaitingResult), controlTurnID, now.Format(time.RFC3339Nano), receipt.ID,
				string(from)); err != nil {
				return err
			}
			receipt.InternalState = BlackboardConclusionReceiptAwaitingResult
			receipt.ControlTurnID = controlTurnID
			receipt.NextEligibleAt = nil
			return appendBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "awaiting_result", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"source_turn_id": receipt.SourceTurnID, "control_turn_id": controlTurnID, "turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionFailure durably resolves a failed control Turn.
// One invalid initial result may claim a single automatic repair; forbidden
// tool use and every later invalid result require explicit operator action.
func (s *Service) HandleBlackboardConclusionFailure(dispatchRequestID string, code BlackboardConclusionErrorCode, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 || (code != BlackboardConclusionErrorInvalidResult && code != BlackboardConclusionErrorToolUseForbidden) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Blackboard conclusion failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+` FROM assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired {
		return receipt, false, nil
	}
	if receipt.InternalState != BlackboardConclusionReceiptAwaitingResult {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if code == BlackboardConclusionErrorInvalidResult && receipt.AutomaticTurnCount < BlackboardConclusionAutomaticTurnLimit && receipt.RepairCount == 0 && receipt.VersionRegenerationCount == 0 && receipt.ExplicitRetryCount == 0 {
		repairNumber := receipt.RepairCount + 1
		requestID := blackboardConclusionAttemptRequestID("repair", receipt.ContinuationID, receipt.SourceTurnID, repairNumber, "")
		nextEligible := now.Add(cooldown)
		result, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,dispatch_request_id=?,control_turn_id=NULL,
			automatic_turn_count=automatic_turn_count+1,repair_count=repair_count+1,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
			string(BlackboardConclusionReceiptRepairDispatchRequested), requestID, string(code), nextEligible.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), receipt.ID, string(BlackboardConclusionReceiptAwaitingResult))
		if err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Blackboard conclusion repair: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
		}
		receipt.InternalState = BlackboardConclusionReceiptRepairDispatchRequested
		receipt.DispatchRequestID = requestID
		receipt.ControlTurnID = ""
		receipt.AutomaticTurnCount++
		receipt.RepairCount++
		receipt.ErrorCode = code
		receipt.NextEligibleAt = &nextEligible
		receipt.UpdatedAt = now
		if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{"phase": "repair_requested", "receipt_id": receipt.ID, "request_id": requestID, "error_code": string(code), "automatic_turn_count": receipt.AutomaticTurnCount, "repair_count": receipt.RepairCount, "turn_kind": "control"}, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return receipt, true, nil
	}
	actionCode := code
	if code == BlackboardConclusionErrorInvalidResult && receipt.RepairCount > 0 && receipt.VersionRegenerationCount == 0 {
		actionCode = BlackboardConclusionErrorRepairExhausted
	}
	nextEligible := now.Add(cooldown)
	result, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), string(actionCode), nextEligible.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), receipt.ID, string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("require Blackboard conclusion action: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState = BlackboardConclusionReceiptActionRequired
	receipt.ErrorCode = actionCode
	receipt.NextEligibleAt = &nextEligible
	receipt.UpdatedAt = now
	if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{"phase": "action_required", "receipt_id": receipt.ID, "request_id": dispatchRequestID, "error_code": string(actionCode), "next_eligible_at": nextEligible.Format(time.RFC3339Nano)}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// ClaimBlackboardConclusionVersionSync records the intent to synchronize the
// continuation before any version-regeneration Turn can be dispatched. The
// validated canonical result remains available until synchronization resolves.
func (s *Service) ClaimBlackboardConclusionVersionSync(dispatchRequestID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return s.advanceBlackboardConclusion(dispatchRequestID, BlackboardConclusionReceiptValidated, BlackboardConclusionReceiptVersionSyncRequested,
		func(BlackboardConclusionReceipt) bool { return true },
		func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,error_code=?,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptVersionSyncRequested), string(BlackboardConclusionErrorVersionConflict),
				now.Format(time.RFC3339Nano), receipt.ID, string(BlackboardConclusionReceiptValidated)); err != nil {
				return err
			}
			receipt.InternalState = BlackboardConclusionReceiptVersionSyncRequested
			receipt.ErrorCode = BlackboardConclusionErrorVersionConflict
			return appendBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "version_sync_requested", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"base_revision": receipt.BaseRevision, "turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionVersionConflict discards a validated result whose
// revision guard lost a real race and claims one fresh semantic generation.
// It never rewrites the old result's claimed base revision.
func (s *Service) HandleBlackboardConclusionVersionConflict(dispatchRequestID string, currentRevision int, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || currentRevision < 0 || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Blackboard conclusion version conflict: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired &&
		receipt.ErrorCode == BlackboardConclusionErrorVersionConflict && receipt.BaseRevision != nil && *receipt.BaseRevision == currentRevision {
		return receipt, false, nil
	}
	if receipt.InternalState != BlackboardConclusionReceiptVersionSyncRequested || receipt.BaseRevision == nil || currentRevision <= *receipt.BaseRevision {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	if receipt.VersionRegenerationCount == 0 && receipt.AutomaticTurnCount < BlackboardConclusionAutomaticTurnLimit && receipt.ExplicitRetryCount == 0 {
		requestID := blackboardConclusionAttemptRequestID("version", receipt.ContinuationID, receipt.SourceTurnID, 1, fmt.Sprintf("%d", currentRevision))
		result, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,dispatch_request_id=?,control_turn_id=NULL,
			base_revision=?,canonical_result_json=NULL,canonical_result_sha256=NULL,automatic_turn_count=automatic_turn_count+1,
			version_regeneration_count=1,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
			string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), requestID, currentRevision,
			string(BlackboardConclusionErrorVersionConflict), nextEligible.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			receipt.ID, string(BlackboardConclusionReceiptVersionSyncRequested))
		if err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Blackboard conclusion version regeneration: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
		}
		receipt.InternalState = BlackboardConclusionReceiptVersionRegenerationDispatchRequested
		receipt.DispatchRequestID = requestID
		receipt.ControlTurnID = ""
		receipt.BaseRevision = intPointer(currentRevision)
		receipt.CanonicalResultJSON = nil
		receipt.CanonicalResultSHA256 = ""
		receipt.AutomaticTurnCount++
		receipt.VersionRegenerationCount = 1
		receipt.ErrorCode = BlackboardConclusionErrorVersionConflict
		receipt.NextEligibleAt = &nextEligible
		receipt.UpdatedAt = now
		if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{
			"phase": "version_regeneration_requested", "receipt_id": receipt.ID, "request_id": requestID,
			"base_revision": currentRevision, "error_code": string(BlackboardConclusionErrorVersionConflict),
			"automatic_turn_count": receipt.AutomaticTurnCount, "version_regeneration_count": receipt.VersionRegenerationCount,
			"turn_kind": "control",
		}, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return receipt, true, nil
	}

	result, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,base_revision=?,canonical_result_json=NULL,
		canonical_result_sha256=NULL,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), currentRevision, string(BlackboardConclusionErrorVersionConflict),
		nextEligible.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), receipt.ID, string(BlackboardConclusionReceiptVersionSyncRequested))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("require Blackboard conclusion version-conflict action: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState = BlackboardConclusionReceiptActionRequired
	receipt.BaseRevision = intPointer(currentRevision)
	receipt.CanonicalResultJSON = nil
	receipt.CanonicalResultSHA256 = ""
	receipt.ErrorCode = BlackboardConclusionErrorVersionConflict
	receipt.NextEligibleAt = &nextEligible
	receipt.UpdatedAt = now
	if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
		"base_revision": currentRevision, "error_code": string(BlackboardConclusionErrorVersionConflict),
		"next_eligible_at": nextEligible.Format(time.RFC3339Nano), "reason": "version_regeneration_exhausted",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// MarkBlackboardConclusionVersionConflictActionRequired rejects a validated
// result whose record or change versions are semantically incompatible with
// the apply contract. Unlike a real base-revision race, it cannot safely claim
// a newer base and therefore requires operator action immediately.
func (s *Service) MarkBlackboardConclusionVersionConflictActionRequired(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	return s.MarkBlackboardConclusionApplyActionRequired(dispatchRequestID, BlackboardConclusionErrorVersionConflict, now, cooldown)
}

// MarkBlackboardConclusionApplyActionRequired fails closed when a validated
// result cannot be applied or its pre-regeneration synchronization fails. The
// caller supplies only a stable public code; raw apply errors are never stored.
func (s *Service) MarkBlackboardConclusionApplyActionRequired(dispatchRequestID string, code BlackboardConclusionErrorCode, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 || !validBlackboardConclusionErrorCode(code) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin incompatible Blackboard conclusion version: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired && receipt.ErrorCode == code {
		return receipt, false, nil
	}
	from := receipt.InternalState
	if from != BlackboardConclusionReceiptValidated && from != BlackboardConclusionReceiptVersionSyncRequested {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	result, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,canonical_result_json=NULL,
		canonical_result_sha256=NULL,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), string(code),
		nextEligible.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), receipt.ID, string(from))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("require incompatible Blackboard conclusion version action: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState = BlackboardConclusionReceiptActionRequired
	receipt.CanonicalResultJSON = nil
	receipt.CanonicalResultSHA256 = ""
	receipt.ErrorCode = code
	receipt.NextEligibleAt = &nextEligible
	receipt.UpdatedAt = now
	if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
		"base_revision": receipt.BaseRevision, "error_code": string(code),
		"next_eligible_at": nextEligible.Format(time.RFC3339Nano), "reason": "apply_failed",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// MarkBlackboardConclusionRecoveryActionRequired closes crash windows after a
// repair, version sync/regeneration, or operator-authorized retry was durably
// claimed but could not finish. Initial conclusion dispatches are deliberately
// excluded because their independent #164 recovery policy cannot be inferred.
func (s *Service) MarkBlackboardConclusionRecoveryActionRequired(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Blackboard conclusion dispatch recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired {
		return receipt, false, nil
	}
	eligibleRepair := receipt.InternalState == BlackboardConclusionReceiptRepairDispatchRequested
	eligibleVersionSync := receipt.InternalState == BlackboardConclusionReceiptVersionSyncRequested
	eligibleVersionRegeneration := receipt.InternalState == BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	eligibleRetry := receipt.InternalState == BlackboardConclusionReceiptDispatchRequested && receipt.ExplicitRetryCount > 0
	eligibleAwaiting := receipt.InternalState == BlackboardConclusionReceiptAwaitingResult &&
		(receipt.RepairCount > 0 || receipt.VersionRegenerationCount > 0 || receipt.ExplicitRetryCount > 0)
	if !eligibleRepair && !eligibleVersionSync && !eligibleVersionRegeneration && !eligibleRetry && !eligibleAwaiting {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	errorCode := receipt.ErrorCode
	if !validBlackboardConclusionErrorCode(errorCode) {
		errorCode = BlackboardConclusionErrorInvalidResult
	}
	nextEligible := now.Add(cooldown)
	result, err := tx.Exec(`UPDATE assisted_conclusion_receipts
		SET state=?,canonical_result_json=NULL,canonical_result_sha256=NULL,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), string(errorCode), nextEligible.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), receipt.ID, string(receipt.InternalState))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("recover Blackboard conclusion dispatch: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState = BlackboardConclusionReceiptActionRequired
	receipt.CanonicalResultJSON = nil
	receipt.CanonicalResultSHA256 = ""
	receipt.ErrorCode = errorCode
	receipt.NextEligibleAt = &nextEligible
	receipt.UpdatedAt = now
	if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
		"error_code": string(errorCode), "next_eligible_at": nextEligible.Format(time.RFC3339Nano),
		"reason": "dispatch_recovery",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// ReconcileStrandedBlackboardConclusionRecoveries makes durably claimed repair,
// version sync/regeneration, and explicit retry work operator-actionable after
// daemon restart. It excludes indistinguishable initial #164 dispatch claims.
func (s *Service) ReconcileStrandedBlackboardConclusionRecoveries(now time.Time, cooldown time.Duration) ([]BlackboardConclusionReceipt, error) {
	if cooldown < 0 {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	rows, err := s.db.Query(`SELECT dispatch_request_id FROM assisted_conclusion_receipts
		WHERE state IN (?,?,?) OR (state=? AND explicit_retry_count>0) OR (state=? AND (repair_count>0 OR version_regeneration_count>0 OR explicit_retry_count>0))
		ORDER BY created_at,id`, string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested), string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptDispatchRequested),
		string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list stranded Blackboard conclusion recoveries: %w", err)
	}
	var dispatchIDs []string
	for rows.Next() {
		var dispatchID string
		if err := rows.Scan(&dispatchID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stranded Blackboard conclusion recovery: %w", err)
		}
		dispatchIDs = append(dispatchIDs, dispatchID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate stranded Blackboard conclusion recoveries: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close stranded Blackboard conclusion recoveries: %w", err)
	}
	reconciled := make([]BlackboardConclusionReceipt, 0, len(dispatchIDs))
	for _, dispatchID := range dispatchIDs {
		receipt, changed, err := s.MarkBlackboardConclusionRecoveryActionRequired(dispatchID, now, cooldown)
		if err != nil {
			return nil, err
		}
		if changed {
			reconciled = append(reconciled, receipt)
		}
	}
	return reconciled, nil
}

func validBlackboardConclusionErrorCode(code BlackboardConclusionErrorCode) bool {
	return code == BlackboardConclusionErrorInvalidResult ||
		code == BlackboardConclusionErrorToolUseForbidden ||
		code == BlackboardConclusionErrorRepairExhausted ||
		code == BlackboardConclusionErrorVersionConflict
}

// RetryBlackboardConclusion atomically claims one operator-authorized retry.
// Its idempotency key remains internal and does not reopen automatic repair.
func (s *Service) RetryBlackboardConclusion(receiptID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	receiptID, idempotencyKey = strings.TrimSpace(receiptID), strings.TrimSpace(idempotencyKey)
	if receiptID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := loadBlackboardConclusionReceiptByID(tx, receiptID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	receipt, won, err := retryBlackboardConclusionTx(tx, receipt, idempotencyKey, now)
	if err != nil || !won {
		return receipt, won, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// RetryLatestBlackboardConclusion atomically applies a Task-scoped operator
// retry key to the latest durable conclusion debt. A key previously consumed
// by any older receipt replays that original receipt and never targets newer
// work observed between an HTTP read and this transaction.
func (s *Service) RetryLatestBlackboardConclusion(taskID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	taskID, idempotencyKey = strings.TrimSpace(taskID), strings.TrimSpace(idempotencyKey)
	if taskID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var priorReceiptID string
	err = tx.QueryRow(`SELECT receipt_id FROM assisted_conclusion_retry_keys
		WHERE task_id=? AND idempotency_key=?`, taskID, idempotencyKey).Scan(&priorReceiptID)
	if err == nil {
		receipt, loadErr := loadBlackboardConclusionReceiptByID(tx, priorReceiptID)
		if loadErr != nil {
			return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(loadErr)
		}
		return receipt, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("read Blackboard conclusion retry idempotency history: %w", err)
	}
	receipt, err := scanBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, taskID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	receipt, won, err := retryBlackboardConclusionTx(tx, receipt, idempotencyKey, now)
	if err != nil || !won {
		return receipt, won, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

func retryBlackboardConclusionTx(tx *sql.Tx, receipt BlackboardConclusionReceipt, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	var priorReceiptID string
	err := tx.QueryRow(`SELECT receipt_id FROM assisted_conclusion_retry_keys
		WHERE task_id=? AND idempotency_key=?`, receipt.TaskID, idempotencyKey).Scan(&priorReceiptID)
	if err == nil {
		prior, loadErr := loadBlackboardConclusionReceiptByID(tx, priorReceiptID)
		if loadErr != nil {
			return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(loadErr)
		}
		return prior, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("read Blackboard conclusion retry idempotency history: %w", err)
	}
	if receipt.InternalState != BlackboardConclusionReceiptActionRequired {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if receipt.NextEligibleAt == nil || now.Before(*receipt.NextEligibleAt) {
		return BlackboardConclusionReceipt{}, false, ErrBlackboardConclusionRetryCooldown
	}
	retryNumber := receipt.ExplicitRetryCount + 1
	requestID := blackboardConclusionAttemptRequestID("retry", receipt.ContinuationID, receipt.SourceTurnID, retryNumber, idempotencyKey)
	result, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,dispatch_request_id=?,control_turn_id=NULL,
		explicit_retry_count=explicit_retry_count+1,operator_retry_key=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptDispatchRequested), requestID, idempotencyKey, now.Format(time.RFC3339Nano), receipt.ID, string(BlackboardConclusionReceiptActionRequired))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("retry Blackboard conclusion: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState = BlackboardConclusionReceiptDispatchRequested
	receipt.DispatchRequestID = requestID
	receipt.ControlTurnID = ""
	receipt.ExplicitRetryCount++
	receipt.OperatorRetryKey = idempotencyKey
	receipt.UpdatedAt = now
	if _, err := tx.Exec(`INSERT INTO assisted_conclusion_retry_keys
		(task_id,receipt_id,idempotency_key,dispatch_request_id,created_at) VALUES (?,?,?,?,?)`,
		receipt.TaskID, receipt.ID, idempotencyKey, requestID, now.Format(time.RFC3339Nano)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Blackboard conclusion retry idempotency history: %w", err)
	}
	if err := appendBlackboardConclusionEventTx(tx, receipt, EventPayload{"phase": "retry_requested", "receipt_id": receipt.ID, "request_id": requestID, "explicit_retry_count": receipt.ExplicitRetryCount, "turn_kind": "control"}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// BlackboardConclusionByDispatchRequestID resolves durable coordinator state
// from the provider request correlation.
func (s *Service) BlackboardConclusionByDispatchRequestID(dispatchRequestID string) (BlackboardConclusionReceipt, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, ErrInvalidBlackboardConclusionReceipt
	}
	receipt, err := scanBlackboardConclusionReceipt(s.db.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, blackboardConclusionLookupError(err)
	}
	return receipt, nil
}

// MarkBlackboardConclusionValidated persists canonical closed result bytes
// before Blackboard application. The hash is computed by the Service.
func (s *Service) MarkBlackboardConclusionValidated(dispatchRequestID string, canonicalResult []byte) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || len(canonicalResult) == 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	canonicalResult = append([]byte(nil), canonicalResult...)
	sum := sha256.Sum256(canonicalResult)
	hash := hex.EncodeToString(sum[:])
	return s.advanceBlackboardConclusion(dispatchRequestID, BlackboardConclusionReceiptAwaitingResult, BlackboardConclusionReceiptValidated,
		func(receipt BlackboardConclusionReceipt) bool {
			return receipt.CanonicalResultSHA256 == hash && string(receipt.CanonicalResultJSON) == string(canonicalResult)
		}, func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE assisted_conclusion_receipts
				SET state=?,canonical_result_json=?,canonical_result_sha256=?,error_code=NULL,next_eligible_at=NULL,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptValidated), canonicalResult, hash, now.Format(time.RFC3339Nano), receipt.ID,
				string(BlackboardConclusionReceiptAwaitingResult)); err != nil {
				return err
			}
			receipt.InternalState = BlackboardConclusionReceiptValidated
			receipt.CanonicalResultJSON = canonicalResult
			receipt.CanonicalResultSHA256 = hash
			receipt.ErrorCode = ""
			receipt.NextEligibleAt = nil
			return appendBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "result_validated", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"source_turn_id": receipt.SourceTurnID, "control_turn_id": receipt.ControlTurnID, "result_hash": hash,
			}, now)
		})
}

// MarkBlackboardConclusionApplied completes the receipt with the exact
// Blackboard revision returned by ApplyForContinuation.
func (s *Service) MarkBlackboardConclusionApplied(dispatchRequestID string, appliedRevision int) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || appliedRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return s.advanceBlackboardConclusion(dispatchRequestID, BlackboardConclusionReceiptValidated, BlackboardConclusionReceiptApplied,
		func(receipt BlackboardConclusionReceipt) bool {
			return receipt.AppliedRevision != nil && *receipt.AppliedRevision == appliedRevision
		},
		func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE assisted_conclusion_receipts SET state=?,applied_revision=?,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptApplied), appliedRevision, now.Format(time.RFC3339Nano), receipt.ID,
				string(BlackboardConclusionReceiptValidated)); err != nil {
				return err
			}
			receipt.InternalState = BlackboardConclusionReceiptApplied
			receipt.AppliedRevision = intPointer(appliedRevision)
			return appendBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "applied", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"source_turn_id": receipt.SourceTurnID, "control_turn_id": receipt.ControlTurnID, "applied_revision": appliedRevision,
			}, now)
		})
}

// LatestBlackboardConclusion returns the newest durable receipt for a Task.
func (s *Service) LatestBlackboardConclusion(taskID string) (*BlackboardConclusionReceipt, error) {
	if _, err := s.Get(taskID); err != nil {
		return nil, err
	}
	receipt, err := scanBlackboardConclusionReceipt(s.db.QueryRow(`
		SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest Blackboard conclusion receipt: %w", err)
	}
	return &receipt, nil
}

// ValidatedBlackboardConclusions returns durable apply intents for daemon
// startup replay. It is read-only and preserves canonical and idempotency
// lineage exactly as stored.
func (s *Service) ValidatedBlackboardConclusions() ([]BlackboardConclusionReceipt, error) {
	rows, err := s.db.Query(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE state=? ORDER BY created_at,id`, string(BlackboardConclusionReceiptValidated))
	if err != nil {
		return nil, fmt.Errorf("list validated Blackboard conclusions: %w", err)
	}
	defer rows.Close()
	receipts := make([]BlackboardConclusionReceipt, 0)
	for rows.Next() {
		receipt, err := scanBlackboardConclusionReceipt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan validated Blackboard conclusion: %w", err)
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate validated Blackboard conclusions: %w", err)
	}
	return receipts, nil
}

func scanBlackboardConclusionReceipt(row scanner) (BlackboardConclusionReceipt, error) {
	var receipt BlackboardConclusionReceipt
	var state, createdAt, updatedAt string
	var dispatchRequestID, controlTurnID, applyKey, resultHash, operatorRetryKey, nextEligibleAt, errorCode sql.NullString
	var baseRevision, appliedRevision sql.NullInt64
	var canonicalResult []byte
	if err := row.Scan(&receipt.ID, &receipt.TaskID, &receipt.ContinuationID, &receipt.SourceSessionID,
		&receipt.SourceTurnID, &state, &receipt.SourceWorkWatermark, &receipt.SemanticPersistenceWatermark, &dispatchRequestID, &controlTurnID,
		&baseRevision, &receipt.SourceSelection.ModelProviderID, &receipt.SourceSelection.Model, &receipt.SourceSelection.ReasoningEffort,
		&canonicalResult, &resultHash, &applyKey, &appliedRevision, &receipt.AutomaticTurnCount, &receipt.RepairCount, &receipt.VersionRegenerationCount,
		&receipt.ExplicitRetryCount, &operatorRetryKey, &nextEligibleAt, &errorCode, &createdAt, &updatedAt); err != nil {
		return BlackboardConclusionReceipt{}, err
	}
	receipt.InternalState = BlackboardConclusionReceiptState(state)
	receipt.DispatchRequestID = dispatchRequestID.String
	receipt.ControlTurnID = controlTurnID.String
	receipt.CanonicalResultJSON = append([]byte(nil), canonicalResult...)
	receipt.CanonicalResultSHA256 = resultHash.String
	receipt.ApplyIdempotencyKey = applyKey.String
	receipt.OperatorRetryKey = operatorRetryKey.String
	receipt.ErrorCode = BlackboardConclusionErrorCode(errorCode.String)
	if baseRevision.Valid {
		receipt.BaseRevision = intPointer(int(baseRevision.Int64))
	}
	if appliedRevision.Valid {
		receipt.AppliedRevision = intPointer(int(appliedRevision.Int64))
	}
	if nextEligibleAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, nextEligibleAt.String)
		if err != nil {
			return BlackboardConclusionReceipt{}, fmt.Errorf("parse Blackboard conclusion next_eligible_at: %w", err)
		}
		receipt.NextEligibleAt = &parsed
	}
	var err error
	if receipt.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return BlackboardConclusionReceipt{}, fmt.Errorf("parse Blackboard conclusion created_at: %w", err)
	}
	if receipt.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return BlackboardConclusionReceipt{}, fmt.Errorf("parse Blackboard conclusion updated_at: %w", err)
	}
	return receipt, nil
}

const blackboardConclusionReceiptColumns = `id,task_id,continuation_id,source_session_id,source_turn_id,state,
	source_work_watermark,semantic_persistence_watermark,dispatch_request_id,control_turn_id,base_revision,source_model_provider_id,source_model,
	source_reasoning_effort,canonical_result_json,canonical_result_sha256,apply_idempotency_key,applied_revision,automatic_turn_count,
	repair_count,version_regeneration_count,explicit_retry_count,operator_retry_key,next_eligible_at,error_code,created_at,updated_at`

func (s *Service) advanceBlackboardConclusion(dispatchRequestID string, from, to BlackboardConclusionReceiptState,
	replayMatches func(BlackboardConclusionReceipt) bool,
	advance func(*sql.Tx, *BlackboardConclusionReceipt, time.Time) error,
) (BlackboardConclusionReceipt, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Blackboard conclusion %s: %w", to, err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, blackboardConclusionLookupError(err)
	}
	if receipt.InternalState != from {
		if receipt.InternalState == to || receiptStateAfter(receipt.InternalState, to) {
			if replayMatches(receipt) {
				return receipt, false, nil
			}
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	if err := advance(tx, &receipt, now); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("mark Blackboard conclusion %s: %w", to, err)
	}
	receipt.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Blackboard conclusion %s: %w", to, err)
	}
	return receipt, true, nil
}

func receiptStateAfter(current, target BlackboardConclusionReceiptState) bool {
	order := map[BlackboardConclusionReceiptState]int{
		BlackboardConclusionReceiptPending: 0, BlackboardConclusionReceiptDispatchRequested: 1,
		BlackboardConclusionReceiptAwaitingResult: 2, BlackboardConclusionReceiptValidated: 3,
		BlackboardConclusionReceiptApplied: 4,
	}
	return order[current] > order[target]
}

func loadBlackboardConclusionReceiptByID(tx *sql.Tx, receiptID string) (BlackboardConclusionReceipt, error) {
	return scanBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+blackboardConclusionReceiptColumns+`
		FROM assisted_conclusion_receipts WHERE id=?`, receiptID))
}

func blackboardConclusionLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("load Blackboard conclusion receipt: %w", err)
}

func blackboardConclusionRequestLineage(continuationID, sourceTurnID string) (string, string) {
	lineage := fmt.Sprintf("%d:%s%d:%s", len(continuationID), continuationID, len(sourceTurnID), sourceTurnID)
	sum := sha256.Sum256([]byte(lineage))
	digest := hex.EncodeToString(sum[:])
	return "conclude:v1:" + digest, "assisted-apply:v1:" + digest
}

func blackboardConclusionAttemptRequestID(kind, continuationID, sourceTurnID string, number int, key string) string {
	lineage := fmt.Sprintf("%s:%d:%s:%d:%s:%d:%s", kind, len(continuationID), continuationID, len(sourceTurnID), sourceTurnID, number, key)
	sum := sha256.Sum256([]byte(lineage))
	return "conclude-" + kind + ":v1:" + hex.EncodeToString(sum[:])
}

func appendBlackboardConclusionEventTx(tx *sql.Tx, receipt BlackboardConclusionReceipt, payload EventPayload, now time.Time) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Blackboard conclusion Event: %w", err)
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id=?`, receipt.TaskID).Scan(&maxSeq); err != nil {
		return fmt.Errorf("read Blackboard conclusion Event sequence: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO task_events (id,task_id,continuation_id,seq,kind,payload_json,created_at)
		VALUES (?,?,?,?,?,?,?)`, newID(), receipt.TaskID, receipt.ContinuationID, int(maxSeq.Int64)+1,
		string(EventKindBlackboardConclusion), string(payloadJSON), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("store Blackboard conclusion Event: %w", err)
	}
	return nil
}

func intPointer(value int) *int { return &value }

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
}

// CreateContinuationLaunchTx stores the runtime configuration version and its
// pinned Continuation through the caller-owned launch transaction.
func (s *Service) CreateContinuationLaunchTx(ctx context.Context, tx *sql.Tx, req ContinuationLaunchRequest) (RuntimeConfigVersion, TaskContinuation, error) {
	if req.Runner != RunnerSandbox && req.Runner != RunnerHost {
		return RuntimeConfigVersion{}, TaskContinuation{}, ErrUnsupportedRunner
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id=?`, req.TaskID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RuntimeConfigVersion{}, TaskContinuation{}, ErrNotFound
		}
		return RuntimeConfigVersion{}, TaskContinuation{}, fmt.Errorf("read launch task: %w", err)
	}
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
	if err == nil && (Status(latestStatus) == StatusFailed || Status(latestStatus) == StatusStopped || Status(latestStatus) == StatusInterrupted) && latestReconciliation != string(ReconciliationCompleted) {
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
		 VALUES (?,?,?,?,?,?,?,'','','',?,?,'',?,?)`,
		continuation.ID, continuation.TaskID, continuation.Number, continuation.RuntimeProfileID,
		continuation.RuntimeProvider, string(continuation.Runner), string(continuation.Status),
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

// CreateContinuation records a Task-domain Continuation without launching a
// Runtime. Production launch uses the Blackboard v2 continuity coordinator.
func (s *Service) CreateContinuation(taskID, runtimeProfileID, runtimeProvider string, runner Runner) (TaskContinuation, error) {
	return s.createContinuation(taskID, runtimeProfileID, runtimeProvider, runner, "")
}

// CreateReplacementContinuation creates a new turn boundary for a persistent
// Task session while retaining the prior runtime configuration pin and any
// discovered provider/container metadata.
func (s *Service) CreateReplacementContinuation(previous TaskContinuation) (TaskContinuation, error) {
	next, err := s.createContinuation(previous.TaskID, previous.RuntimeProfileID, previous.RuntimeProvider, previous.Runner, previous.RuntimeConfigVersionID)
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

func (s *Service) createContinuation(taskID, runtimeProfileID, runtimeProvider string, runner Runner, runtimeConfigVersionID string) (TaskContinuation, error) {
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
		BlackboardReconciliationStatus: ReconciliationPending,
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

// Continuation returns one Runtime Continuation by immutable ID.
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

// ActivePinnedContinuations returns Snapshot-pinned Continuations whose committed
// task-local files may need regeneration after a daemon crash.
func (s *Service) ActivePinnedContinuations() ([]TaskContinuation, error) {
	rows, err := s.db.Query(
		`SELECT `+continuationSelectColumns+`
		 FROM task_continuations
		 WHERE status IN (?,?,?) AND runtime_config_version_id IS NOT NULL
		 ORDER BY started_at,id`,
		string(StatusPending), string(StatusRunning), string(StatusPaused),
	)
	if err != nil {
		return nil, fmt.Errorf("query active pinned Continuations: %w", err)
	}
	defer rows.Close()
	var continuations []TaskContinuation
	for rows.Next() {
		continuation, err := scanContinuation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan active pinned Continuation: %w", err)
		}
		continuations = append(continuations, continuation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active pinned Continuations: %w", err)
	}
	return continuations, nil
}

// MarkContinuationReconciliation stores the durable outcome only after the
// owning semantic reconciliation has completed.
func (s *Service) MarkContinuationReconciliation(ctx context.Context, continuationID string, status ReconciliationStatus, mutationID string, reconciledAt time.Time) (TaskContinuation, error) {
	return s.markContinuationReconciliation(ctx, continuationID, status, mutationID, reconciledAt, nil)
}

// MarkContinuationReconciliationWithEvent atomically stores the durable marker
// and its compact system Task Event.
func (s *Service) MarkContinuationReconciliationWithEvent(ctx context.Context, continuationID string, status ReconciliationStatus, mutationID string, reconciledAt time.Time, payload EventPayload) (TaskContinuation, error) {
	return s.markContinuationReconciliation(ctx, continuationID, status, mutationID, reconciledAt, payload)
}

func (s *Service) markContinuationReconciliation(ctx context.Context, continuationID string, status ReconciliationStatus, mutationID string, reconciledAt time.Time, payload EventPayload) (TaskContinuation, error) {
	if status != ReconciliationCompleted && status != ReconciliationFailed {
		return TaskContinuation{}, fmt.Errorf("invalid Continuation reconciliation status %q", status)
	}
	stamp := reconciledAt.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("begin Continuation reconciliation marker: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if payload != nil {
		var taskID, currentStatus, currentMutationID string
		if err := tx.QueryRowContext(ctx, `
			SELECT task_id,blackboard_reconciliation_status,blackboard_reconciliation_mutation_id
			FROM task_continuations WHERE id=?`, continuationID).Scan(&taskID, &currentStatus, &currentMutationID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return TaskContinuation{}, ErrNotFound
			}
			return TaskContinuation{}, fmt.Errorf("read reconciliation Event Task: %w", err)
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return TaskContinuation{}, fmt.Errorf("encode reconciliation Event: %w", err)
		}
		phase, _ := payload["phase"].(string)
		if currentStatus == string(status) && currentMutationID == mutationID && phase != "" {
			var storedPayloadJSON string
			err := tx.QueryRowContext(ctx, `
				SELECT payload_json FROM task_events
				WHERE continuation_id=? AND kind=? AND json_extract(payload_json,'$.phase')=?
				ORDER BY seq DESC,id DESC LIMIT 1`, continuationID, string(EventKindLifecycle), phase).Scan(&storedPayloadJSON)
			if err == nil && storedPayloadJSON == string(payloadJSON) {
				if err := tx.Commit(); err != nil {
					return TaskContinuation{}, fmt.Errorf("commit repeated Continuation reconciliation: %w", err)
				}
				return s.Continuation(continuationID)
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return TaskContinuation{}, fmt.Errorf("read repeated reconciliation Event: %w", err)
			}
		}
		var maxSeq sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM task_events WHERE task_id=?`, taskID).Scan(&maxSeq); err != nil {
			return TaskContinuation{}, fmt.Errorf("read reconciliation Event sequence: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_events (id,task_id,continuation_id,seq,kind,payload_json,created_at)
			VALUES (?,?,?,?,?,?,?)`,
			newID(), taskID, continuationID, int(maxSeq.Int64)+1, string(EventKindLifecycle), string(payloadJSON), stamp,
		); err != nil {
			return TaskContinuation{}, fmt.Errorf("store reconciliation Event: %w", err)
		}
	}
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
		if err := s.reconciler.ReconcileTerminalContinuation(context.Background(), found.ID, reason); err != nil {
			return found, fmt.Errorf("reconcile terminal Continuation: %w", err)
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
