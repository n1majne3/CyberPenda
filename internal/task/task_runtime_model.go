package task

import (
	"errors"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtimeconfig"
)

// RuntimeConfigVersion is a historical task-specific runtime configuration
// captured for a runtime continuation. A runtime-profile switch inside a task
// creates a new version, not a new task.
type RuntimeConfigVersion struct {
	ID               string         `json:"id"`
	TaskID           string         `json:"task_id"`
	Version          int            `json:"version"`
	RuntimeProfileID string         `json:"runtime_profile_id,omitempty"`
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
	RuntimeProfileID                   string               `json:"runtime_profile_id,omitempty"`
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
	NativeSteerErrorCode  string `json:"native_steer_error_code,omitempty"`
	NativeSteerError      string `json:"native_steer_error,omitempty"`
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
	Liveness string `json:"liveness"`
	// TurnActivity is busy or idle while Liveness is live; empty otherwise.
	TurnActivity string `json:"turn_activity,omitempty"`
	Warning      string `json:"warning,omitempty"`
}

type Task struct {
	// ChallengeHistoryAvailable is a read-only projection, not stored Task state.
	ChallengeHistoryAvailable bool                   `json:"challenge_history_available,omitempty"`
	BlackboardProtocol        string                 `json:"blackboard_protocol"`
	ID                        string                 `json:"id"`
	ProjectID                 string                 `json:"project_id"`
	Type                      Type                   `json:"type"`
	Goal                      string                 `json:"goal"`
	Status                    Status                 `json:"status"`
	Runner                    Runner                 `json:"runner"`
	RuntimeProfileID          string                 `json:"runtime_profile_id,omitempty"`
	RunControls               RunControls            `json:"run_controls"`
	ScopeSnapshot             ScopeSnapshot          `json:"scope_snapshot"`
	RuntimeControls           RuntimeControls        `json:"runtime_controls"`
	RuntimeConfiguration      *runtimeconfig.Summary `json:"runtime_configuration,omitempty"`
	RuntimeActivity           RuntimeActivity        `json:"runtime_activity"`
	BlackboardConclusion      BlackboardConclusion   `json:"blackboard_conclusion"`
	ActiveContinuation        *TaskContinuation      `json:"active_continuation,omitempty"`
	LatestContinuation        *TaskContinuation      `json:"latest_continuation,omitempty"`
	CreatedAt                 time.Time              `json:"created_at"`
	UpdatedAt                 time.Time              `json:"updated_at"`
}

// OwnerContract exposes the explicit Project-capable owner binding consumed by
// owner-neutral Runtime and Blackboard kernels. Task always carries a Project
// identity; callers cannot construct a projectless Task contract.
func (t Task) OwnerContract(workdir string) owner.Contract {
	return owner.NewTaskContract(t.ID, t.ProjectID, workdir)
}

type CreateRequest struct {
	ProjectID        string
	Type             Type
	Goal             string
	RuntimeProfileID string
	Runner           Runner
	RunControls      RunControls
	// RuntimeConfig is the initial immutable Runtime Configuration Snapshot.
	// When present, Task and version 1 are committed in one transaction.
	RuntimeConfig map[string]any
}

var ErrNotFound = errors.New("task not found")

var ErrMissingGoal = errors.New("task goal is required")

// ErrProjectNotFound is returned when the project referenced by a task does not
// exist.
var ErrProjectNotFound = errors.New("project not found")

// ErrActiveTask is returned when deletion is requested while a task may still
// launch or continue runtime work.
var ErrActiveTask = errors.New("active task cannot be deleted")

var ErrUnsupportedRunner = errors.New("runner must be sandbox or host")

type Type string

const (
	TypePentest      Type = "pentest"
	TypeCTFChallenge Type = "ctf_challenge"
)

var ErrInvalidTaskType = errors.New("Task Type must be pentest or ctf_challenge")

var ErrTaskTypeProjectKindMismatch = errors.New("Task Type must match the current Project Kind")

var ErrInvalidBlackboardMode = errors.New("Blackboard Mode must be interactive, working_graph, or disabled")

var ErrInvalidTaskPolicy = errors.New("Task Policy limits must be zero or positive")

// ErrSandboxVPNTunHostProxyConflict reports that sandbox VPN TUN cannot run
// with the host_proxy_only network shape.
var ErrSandboxVPNTunHostProxyConflict = errors.New("sandbox VPN TUN cannot combine with host_proxy_only network")

var ErrInvalidContainerCLI = errors.New("container CLI must be docker or podman")

// ErrContinuationStatusConflict prevents a late lifecycle observer from
// overwriting a Continuation that already reached a different terminal state.
var ErrContinuationStatusConflict = errors.New("continuation status conflicts with its terminal state")

// ErrActiveContinuation prevents Resume from creating a second current pin
// before the previous Continuation reaches a terminal state.
var ErrActiveContinuation = errors.New("task already has an active continuation")

// ErrContinuationReconciliationIncomplete prevents Resume from bypassing
// durable interruption reconciliation owned by the prior Continuation.
var ErrContinuationReconciliationIncomplete = errors.New("prior continuation reconciliation is incomplete")

var ErrSteeringSelectionConflict = errors.New("Harness Steering selection is stale or invalid")
