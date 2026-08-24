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
	"strings"
	"time"

	"pentest/internal/owner"
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
	HostActivated  bool   `json:"host_activated,omitempty"`
	SandboxNetwork string `json:"sandbox_network,omitempty"`
	// SandboxVPNTun is an opt-in sandbox capability that mounts /dev/net/tun and
	// grants CAP_NET_ADMIN so OpenVPN (or other TUN clients) can create tun0.
	// It cannot combine with host_proxy_only, which drops NET_ADMIN after the
	// egress firewall is installed.
	SandboxVPNTun bool `json:"sandbox_vpn_tun,omitempty"`
	// ContainerCLI selects docker or podman for this Task's sandbox launch.
	// Empty means the daemon default (-container-cli / PENTEST_CONTAINER_CLI).
	ContainerCLI             string                   `json:"container_cli,omitempty"`
	Notes                    string                   `json:"notes,omitempty"`
	Extras                   map[string]string        `json:"extras,omitempty"`
	BlackboardConclusionMode BlackboardConclusionMode `json:"blackboard_conclusion_mode"`
	// Policy is the immutable Task Policy Snapshot captured at Task creation.
	// A zero value disables that limit.
	Policy TaskPolicy `json:"policy"`
}

func (controls RunControls) validate() error {
	if err := controls.Policy.validate(); err != nil {
		return err
	}
	if _, err := NormalizeContainerCLIChoice(controls.ContainerCLI); err != nil {
		return err
	}
	if controls.SandboxVPNTun && strings.TrimSpace(controls.SandboxNetwork) == "host_proxy_only" {
		return ErrSandboxVPNTunHostProxyConflict
	}
	if controls.SandboxVPNTun {
		if extras := controls.Extras; extras != nil {
			if strings.TrimSpace(extras["sandbox_network"]) == "host_proxy_only" {
				return ErrSandboxVPNTunHostProxyConflict
			}
		}
	}
	return nil
}

// NormalizeContainerCLIChoice accepts empty (daemon default), docker, or podman.
func NormalizeContainerCLIChoice(cli string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(cli)) {
	case "":
		return "", nil
	case "docker":
		return "docker", nil
	case "podman":
		return "podman", nil
	default:
		return "", ErrInvalidContainerCLI
	}
}

// ResolveContainerCLI prefers a Task Run Control choice, then the daemon default.
func ResolveContainerCLI(taskChoice, daemonDefault string) string {
	if normalized, err := NormalizeContainerCLIChoice(taskChoice); err == nil && normalized != "" {
		return normalized
	}
	return strings.TrimSpace(daemonDefault)
}

// TaskPolicy defines machine-enforced stop conditions for one Task.
// Each positive value enables its limit. A zero value means no limit.
type TaskPolicy struct {
	MaxAttempts            int `json:"max_attempts,omitempty"`
	MaxWrongSubmissions    int `json:"max_wrong_submissions,omitempty"`
	MaxWallTimeSeconds     int `json:"max_wall_time_seconds,omitempty"`
	MaxConsecutiveFailures int `json:"max_consecutive_failures,omitempty"`
	MaxRatingDrawdown      int `json:"max_rating_drawdown,omitempty"`
	MaxNoProgressSeconds   int `json:"max_no_progress_seconds,omitempty"`
}

func (policy TaskPolicy) validate() error {
	if policy.MaxAttempts < 0 || policy.MaxWrongSubmissions < 0 ||
		policy.MaxWallTimeSeconds < 0 || policy.MaxConsecutiveFailures < 0 ||
		policy.MaxRatingDrawdown < 0 || policy.MaxNoProgressSeconds < 0 {
		return ErrInvalidTaskPolicy
	}
	return nil
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

type BlackboardConclusionErrorCode = owner.BlackboardConclusionErrorCode

// ConclusionValidationDetail is the bounded public reason for one rejected
// closed conclusion result, safe for repair directives and durable state.
type ConclusionValidationDetail = owner.ConclusionValidationDetail

const (
	BlackboardConclusionErrorInvalidResult           = owner.BlackboardConclusionErrorInvalidResult
	BlackboardConclusionErrorToolUseForbidden        = owner.BlackboardConclusionErrorToolUseForbidden
	BlackboardConclusionErrorRepairExhausted         = owner.BlackboardConclusionErrorRepairExhausted
	BlackboardConclusionErrorVersionConflict         = owner.BlackboardConclusionErrorVersionConflict
	BlackboardConclusionErrorRuntimeRecoveryRequired = owner.BlackboardConclusionErrorRuntimeRecoveryRequired
	// BlackboardConclusionErrorWorkTurnNeverSettled is a distinct, non-retryable
	// terminal: a provider work turn never yielded, so the conclusion control
	// turn could not be delivered within the bounded conflict budget. Retrying
	// is futile; the operator must Finish the Task.
	BlackboardConclusionErrorWorkTurnNeverSettled = owner.BlackboardConclusionErrorWorkTurnNeverSettled
	BlackboardConclusionAutomaticTurnLimit        = owner.BlackboardConclusionAutomaticTurnLimit
	// BlackboardConclusionWorkTurnConflictLimit bounds how many explicit retries
	// may re-attempt a conclusion that a non-yielding work turn keeps rejecting.
	// Once exhausted the receipt becomes the non-retryable never-settled terminal
	// instead of endlessly re-emitting the recoverable runtime recovery code.
	BlackboardConclusionWorkTurnConflictLimit = owner.BlackboardConclusionWorkTurnConflictLimit
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
	// ValidationReason, ValidationFieldPath, and ValidationExpected expose the
	// bounded public reason for the last rejected closed result. They are
	// closed tokens only; raw provider output never appears.
	ValidationReason    string `json:"validation_reason,omitempty"`
	ValidationFieldPath string `json:"validation_field_path,omitempty"`
	ValidationExpected  string `json:"validation_expected,omitempty"`
	// RecoveryReason is the closed operator-visible reason for a fail-closed
	// action_required obligation (ADR 0021).
	RecoveryReason string     `json:"recovery_reason,omitempty"`
	RetryAvailable bool       `json:"retry_available"`
	NextEligibleAt *time.Time `json:"next_eligible_at,omitempty"`
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
type BlackboardConclusionReceiptState = owner.BlackboardConclusionReceiptState

const (
	BlackboardConclusionReceiptClean                                = owner.BlackboardConclusionReceiptClean
	BlackboardConclusionReceiptPending                              = owner.BlackboardConclusionReceiptPending
	BlackboardConclusionReceiptDispatchRequested                    = owner.BlackboardConclusionReceiptDispatchRequested
	BlackboardConclusionReceiptAwaitingResult                       = owner.BlackboardConclusionReceiptAwaitingResult
	BlackboardConclusionReceiptRepairDispatchRequested              = owner.BlackboardConclusionReceiptRepairDispatchRequested
	BlackboardConclusionReceiptVersionSyncRequested                 = owner.BlackboardConclusionReceiptVersionSyncRequested
	BlackboardConclusionReceiptVersionRegenerationDispatchRequested = owner.BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	BlackboardConclusionReceiptActionRequired                       = owner.BlackboardConclusionReceiptActionRequired
	BlackboardConclusionReceiptValidated                            = owner.BlackboardConclusionReceiptValidated
	BlackboardConclusionReceiptApplied                              = owner.BlackboardConclusionReceiptApplied
)

// SemanticDebtWatermarks compare terminal Work Tool Results with the latest
// successful semantic persistence that covers them.
type SemanticDebtWatermarks = owner.SemanticDebtWatermarks

// ConclusionDispatchKind is the immutable attempt category of one Conclusion
// Dispatch (ADR 0021). Recovery-created dispatches are kind recovery.
type ConclusionDispatchKind = owner.ConclusionDispatchKind

const (
	ConclusionDispatchKindInitial             = owner.ConclusionDispatchKindInitial
	ConclusionDispatchKindRepair              = owner.ConclusionDispatchKindRepair
	ConclusionDispatchKindVersionRegeneration = owner.ConclusionDispatchKindVersionRegeneration
	ConclusionDispatchKindRetry               = owner.ConclusionDispatchKindRetry
	ConclusionDispatchKindRecovery            = owner.ConclusionDispatchKindRecovery
)

// ConclusionDirectiveKind is the semantic control instruction carried by a
// dispatch. Recovery changes delivery lineage, not directive semantics.
type ConclusionDirectiveKind = owner.ConclusionDirectiveKind

const (
	ConclusionDirectiveKindInitial             = owner.ConclusionDirectiveKindInitial
	ConclusionDirectiveKindRepair              = owner.ConclusionDirectiveKindRepair
	ConclusionDirectiveKindVersionRegeneration = owner.ConclusionDirectiveKindVersionRegeneration
)

// ConclusionDispatchState is the delivery lifecycle of one immutable dispatch.
type ConclusionDispatchState = owner.ConclusionDispatchState

const (
	ConclusionDispatchRequested      = owner.ConclusionDispatchRequested
	ConclusionDispatchAwaitingResult = owner.ConclusionDispatchAwaitingResult
	ConclusionDispatchValidated      = owner.ConclusionDispatchValidated
	ConclusionDispatchApplied        = owner.ConclusionDispatchApplied
	ConclusionDispatchActionRequired = owner.ConclusionDispatchActionRequired
	ConclusionDispatchSuperseded     = owner.ConclusionDispatchSuperseded
	ConclusionDispatchLateTerminal   = owner.ConclusionDispatchLateTerminal
)

// ConclusionRecoveryReason is the closed operator-visible fail-closed reason.
type ConclusionRecoveryReason = owner.ConclusionRecoveryReason

const (
	ConclusionRecoveryRuntimeOwnershipNotProven      = owner.ConclusionRecoveryRuntimeOwnershipNotProven
	ConclusionRecoveryWritableReplacementUnavailable = owner.ConclusionRecoveryWritableReplacementUnavailable
	ConclusionRecoveryAcceptanceAmbiguous            = owner.ConclusionRecoveryAcceptanceAmbiguous
	ConclusionRecoveryDispatchFailed                 = owner.ConclusionRecoveryDispatchFailed
	ConclusionRecoveryLegacyCorrelationUnproven      = owner.ConclusionRecoveryLegacyCorrelationUnproven
)

// BlackboardConclusionReceipt is the durable coordinator record for one
// completed assisted work Runtime Turn. Structured result bytes remain
// internal and are never projected on Task APIs.
type BlackboardConclusionReceipt struct {
	ID                            string                           `json:"id"`
	TaskID                        string                           `json:"task_id"`
	ContinuationID                string                           `json:"continuation_id"`
	SourceRequestID               string                           `json:"source_request_id"`
	SourceRequestCorrelationExact bool                             `json:"source_request_correlation_exact"`
	SourceSessionID               string                           `json:"source_session_id"`
	SourceTurnID                  string                           `json:"source_turn_id"`
	InternalState                 BlackboardConclusionReceiptState `json:"internal_state"`
	SourceWorkWatermark           int                              `json:"source_work_watermark"`
	SemanticPersistenceWatermark  int                              `json:"semantic_persistence_watermark"`
	DispatchRequestID             string                           `json:"dispatch_request_id,omitempty"`
	ControlTurnID                 string                           `json:"control_turn_id,omitempty"`
	BaseRevision                  *int                             `json:"base_revision,omitempty"`
	SynchronizedRevision          *int                             `json:"synchronized_revision,omitempty"`
	SourceSelection               TurnSelection                    `json:"source_selection"`
	CanonicalResultJSON           []byte                           `json:"-"`
	CanonicalResultSHA256         string                           `json:"canonical_result_sha256,omitempty"`
	ApplyIdempotencyKey           string                           `json:"apply_idempotency_key,omitempty"`
	AppliedRevision               *int                             `json:"applied_revision,omitempty"`
	AutomaticTurnCount            int                              `json:"automatic_turn_count"`
	RepairCount                   int                              `json:"repair_count"`
	VersionRegenerationCount      int                              `json:"version_regeneration_count"`
	ExplicitRetryCount            int                              `json:"explicit_retry_count"`
	OperatorRetryKey              string                           `json:"-"`
	SendAttemptCount              int                              `json:"send_attempt_count"`
	SendStartedAt                 *time.Time                       `json:"send_started_at,omitempty"`
	NextEligibleAt                *time.Time                       `json:"next_eligible_at,omitempty"`
	ErrorCode                     BlackboardConclusionErrorCode    `json:"error_code,omitempty"`
	// RecoveryReason is the closed operator-visible reason for a fail-closed
	// action_required obligation (ADR 0021). It is never free-form text.
	RecoveryReason string `json:"recovery_reason,omitempty"`
	// ActiveDispatchID and DispatchKind expose the active Conclusion Dispatch
	// so recovery can create a new dispatch without rewriting history.
	ActiveDispatchID    string                  `json:"-"`
	DispatchKind        ConclusionDispatchKind  `json:"-"`
	DirectiveKind       ConclusionDirectiveKind `json:"-"`
	ValidationReason    string                  `json:"validation_reason,omitempty"`
	ValidationFieldPath string                  `json:"validation_field_path,omitempty"`
	ValidationExpected  string                  `json:"validation_expected,omitempty"`
	CreatedAt           time.Time               `json:"created_at"`
	UpdatedAt           time.Time               `json:"updated_at"`
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
		view.RecoveryReason = receipt.RecoveryReason
		view.ValidationReason = receipt.ValidationReason
		view.ValidationFieldPath = receipt.ValidationFieldPath
		view.ValidationExpected = receipt.ValidationExpected
		if receipt.ErrorCode == BlackboardConclusionErrorWorkTurnNeverSettled {
			// A never-settled work turn is terminal: retrying cannot win because
			// the provider never yields the active turn. Surface it as not
			// retryable so the operator is directed to Finish instead.
			view.RetryAvailable = false
			break
		}
		if receipt.RecoveryReason == string(ConclusionRecoveryAcceptanceAmbiguous) {
			// An acceptance-ambiguous provider delivery is never resent: the
			// obligation is terminal-actionable and offers no generic Retry.
			view.RetryAvailable = false
			break
		}
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
	Type             Type            `json:"type"`
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

// OwnerContract exposes the explicit Project-capable owner binding consumed by
// owner-neutral Runtime and Blackboard kernels. Task always carries a Project
// identity; callers cannot construct a projectless Task contract.
func (t Task) OwnerContract(workdir string) owner.Contract {
	return owner.NewTaskContract(t.ID, t.ProjectID, workdir)
}

// CreateRequest is the input to Service.Create.
type CreateRequest struct {
	ProjectID        string
	Type             Type
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

// Type is the immutable Task semantic classification captured at Task Launch.
type Type string

const (
	TypePentest      Type = "pentest"
	TypeCTFChallenge Type = "ctf_challenge"
)

var ErrInvalidTaskType = errors.New("Task Type must be pentest or ctf_challenge")

var ErrTaskTypeProjectKindMismatch = errors.New("Task Type must match the current Project Kind")

var ErrInvalidBlackboardConclusionMode = errors.New("Blackboard conclusion mode must be interactive or assisted")

var ErrInvalidTaskPolicy = errors.New("Task Policy limits must be zero or positive")

// ErrSandboxVPNTunHostProxyConflict reports that sandbox VPN TUN cannot run
// with the host_proxy_only network shape.
var ErrSandboxVPNTunHostProxyConflict = errors.New("sandbox VPN TUN cannot combine with host_proxy_only network")

// ErrInvalidContainerCLI reports an unsupported Task-level container CLI choice.
var ErrInvalidContainerCLI = errors.New("container CLI must be docker or podman")

var ErrInvalidBlackboardConclusionReceipt = errors.New("invalid Blackboard conclusion checkpoint receipt")

var ErrBlackboardConclusionRetryCooldown = errors.New("Blackboard conclusion retry is not yet eligible")

// ErrBlackboardConclusionWorkTurnNeverSettled reports a retry against a
// non-retryable terminal reached when a provider work turn never yielded.
var ErrBlackboardConclusionWorkTurnNeverSettled = errors.New("Blackboard conclusion work turn never settled")

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
	if err := req.RunControls.validate(); err != nil {
		return Task{}, err
	}

	// Capture the scope snapshot from the live project. If a project service is
	// wired, read it; otherwise the snapshot is empty (caller is responsible for
	// providing scope out-of-band, e.g. the HTTP layer).
	var snapshot ScopeSnapshot
	var projectKind string
	if s.projects != nil {
		proj, err := s.projects.Get(req.ProjectID)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				return Task{}, ErrProjectNotFound
			}
			return Task{}, fmt.Errorf("read project scope: %w", err)
		}
		snapshot = proj.Scope
		projectKind = proj.Kind
	}
	if req.Type != TypePentest && req.Type != TypeCTFChallenge {
		return Task{}, ErrInvalidTaskType
	}
	if projectKind != "" && string(req.Type) != projectKind {
		return Task{}, ErrTaskTypeProjectKindMismatch
	}

	now := time.Now().UTC()
	created := Task{
		ID:                   newID(),
		ProjectID:            req.ProjectID,
		Type:                 req.Type,
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
		`INSERT INTO tasks (id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		created.ID, created.ProjectID, string(created.Type), created.Goal, string(created.Status), string(created.Runner),
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
		`SELECT id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at FROM tasks WHERE id = ? AND deleted_at = ''`,
		id,
	))
}

// ListForProject returns tasks for a project ordered by creation time.
func (s *Service) ListForProject(projectID string) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at
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

// taskSelectColumns lists the columns scanned by the shared Task projections.
// Keep it in sync with the table columns used by scanTask.
const taskSelectColumns = `id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at`

// recentPerProjectSQL builds the bounded per-Project recent Task query. The
// exclusion placeholders keep busy Tasks out of the ordinary summary; the
// navigation index idx_tasks_project_activity serves both the filter and the
// ordering, so a Project's history beyond the fixed summary is never read
// (#201).
func recentPerProjectSQL(limit, excludeCount int) string {
	query := `SELECT ` + taskSelectColumns + ` FROM tasks WHERE project_id = ? AND deleted_at = ''`
	if excludeCount > 0 {
		query += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat(`?,`, excludeCount), `,`) + `)`
	}
	return query + ` ORDER BY updated_at DESC, created_at DESC LIMIT ?`
}

// ListRecentPerProject returns, for each requested Project, the `limit` most
// recently updated non-deleted Tasks, excluding the given Task ids. The
// navigation projection passes the live busy Task ids so the ordinary summary
// stays exactly `limit` entries; the selected Task stays inside the query and
// is deduplicated by the caller when recency already included it (#201).
//
// Ordering is updated_at DESC then created_at DESC. Each Project is one
// bounded indexed query with LIMIT, so query work and returned rows never grow
// with a Project's total historical Task count.
func (s *Service) ListRecentPerProject(projectIDs []string, limit int, excludeIDs ...string) (map[string][]Task, error) {
	if limit <= 0 {
		limit = 1
	}
	byProject := make(map[string][]Task, len(projectIDs))
	for _, projectID := range projectIDs {
		args := make([]any, 0, len(excludeIDs)+2)
		args = append(args, projectID)
		for _, id := range excludeIDs {
			args = append(args, id)
		}
		args = append(args, limit)
		rows, err := s.db.Query(recentPerProjectSQL(limit, len(excludeIDs)), args...)
		if err != nil {
			return nil, fmt.Errorf("list recent tasks: %w", err)
		}
		var tasks []Task
		for rows.Next() {
			found, err := scanTask(rows)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan task: %w", err)
			}
			tasks = append(tasks, found)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list recent tasks: %w", err)
		}
		rows.Close()
		if tasks != nil {
			byProject[projectID] = tasks
		}
	}
	return byProject, nil
}

// LatestUpdate returns the newest updated_at across every Task row, including
// soft-deleted rows so a deletion advances the navigation epoch. The Project
// Navigation Projection folds it into its opaque revision so an unchanged
// refresh can be answered without serializing the projection (#201). The
// idx_tasks_updated_at index answers the MAX in one indexed read.
func (s *Service) LatestUpdate() (time.Time, error) {
	var value string
	if err := s.db.QueryRow(`SELECT MAX(updated_at) FROM tasks`).Scan(&value); err != nil {
		return time.Time{}, fmt.Errorf("latest task update: %w", err)
	}
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse latest task update: %w", err)
	}
	return parsed, nil
}

// Delete removes a terminal task from normal Task surfaces while retaining its
// durable row for Blackboard provenance and historical joins. The row's
// updated_at advances with the deletion so the navigation revision changes and
// a cached Sidebar projection cannot keep showing a deleted Task (#201).
func (s *Service) Delete(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`UPDATE tasks SET deleted_at = ?, updated_at = ?
		 WHERE id = ? AND deleted_at = '' AND status NOT IN (?, ?, ?)`,
		now, now, id,
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

	err := row.Scan(&found.ID, &found.ProjectID, &found.Type, &found.Goal, &status, &runner, &found.RuntimeProfileID, &runControlsJSON, &scopeJSON, &createdAt, &updatedAt)
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

// AppendEventTx appends a structured task event inside a caller-owned
// transaction. Seq is assigned monotonically per task within the transaction
// so the event can be committed atomically with another owner-neutral record
// (for example a durable Accepted Steering request).
func (s *Service) AppendEventTx(tx *sql.Tx, taskID string, kind EventKind, payload EventPayload) (Event, error) {
	return appendTaskEventTx(tx, taskID, kind, payload, time.Now().UTC())
}

// appendTaskEventTx stores one structured task event inside the caller-owned
// transaction. Seq is assigned monotonically per task within the transaction.
func appendTaskEventTx(tx *sql.Tx, taskID string, kind EventKind, payload EventPayload, now time.Time) (Event, error) {
	if payload == nil {
		payload = EventPayload{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id = ?`, taskID).Scan(&maxSeq); err != nil {
		return Event{}, fmt.Errorf("read max seq: %w", err)
	}
	event := Event{
		ID: newID(), TaskID: taskID, Kind: kind, Payload: payload, Seq: int(maxSeq.Int64) + 1, CreatedAt: now,
	}
	if _, err := tx.Exec(
		`INSERT INTO task_events (id, task_id, continuation_id, seq, kind, payload_json, created_at) VALUES (?, ?, NULLIF(?,''), ?, ?, ?, ?)`,
		event.ID, event.TaskID, "", event.Seq, string(event.Kind), string(payloadJSON), event.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return Event{}, fmt.Errorf("store event: %w", err)
	}
	return event, nil
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

	return scanEvents(rows)
}

// EventProjection selects the event kinds needed by one Runtime Owner history
// projection. This keeps SQL work bounded without loading unrelated history.
type EventProjection string

const (
	EventProjectionTimeline   EventProjection = "timeline"
	EventProjectionTranscript EventProjection = "transcript"
)

// EventWindowQuery is one bounded keyset read. Before and After are exclusive;
// zero means the initial recent window.
type EventWindowQuery struct {
	Projection EventProjection
	BeforeSet  bool
	Before     int
	AfterSet   bool
	After      int
	Limit      int
}

// EventWindow is an ordered bounded event slice plus projection cursor state.
type EventWindow struct {
	Events                 []Event
	Cursor                 int
	HasOlder               bool
	HasNewer               bool
	ScanCursor             int
	PriorContinuation      int
	PriorTranscriptAdapter string
}

// HistoryEventWindow reads only event kinds used by the selected projection.
// Query work is bounded by Limit plus one row, independent of full Task history.
func (s *Service) HistoryEventWindow(taskID string, query EventWindowQuery) (EventWindow, error) {
	if _, err := s.Get(taskID); err != nil {
		return EventWindow{}, err
	}
	if query.Limit < 1 || query.Before < 0 || query.After < 0 || (query.BeforeSet && query.AfterSet) {
		return EventWindow{}, fmt.Errorf("invalid Task Event window query")
	}
	kinds := "('runtime_output','lifecycle','steering','attachment','blackboard_conclusion')"
	if query.Projection == EventProjectionTranscript {
		kinds = "('conversation','runtime_output','lifecycle','steering')"
	} else if query.Projection != EventProjectionTimeline {
		return EventWindow{}, fmt.Errorf("invalid Task Event projection")
	}
	var cursor int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM task_events WHERE task_id=? AND kind IN `+kinds, taskID).Scan(&cursor); err != nil {
		return EventWindow{}, fmt.Errorf("read Task Event projection cursor: %w", err)
	}
	order := "DESC"
	predicate := ""
	args := []any{taskID}
	if query.BeforeSet {
		predicate = " AND seq < ?"
		args = append(args, query.Before)
	} else if query.AfterSet {
		predicate = " AND seq > ?"
		args = append(args, query.After)
		order = "ASC"
	}
	args = append(args, query.Limit+1)
	rows, err := s.db.Query(
		`SELECT id,task_id,continuation_id,attempt_node_id,seq,kind,payload_json,created_at
		 FROM task_events WHERE task_id=? AND kind IN `+kinds+predicate+` ORDER BY seq `+order+` LIMIT ?`, args...,
	)
	if err != nil {
		return EventWindow{}, fmt.Errorf("list Task Event window: %w", err)
	}
	defer rows.Close()
	events, err := scanEvents(rows)
	if err != nil {
		return EventWindow{}, err
	}
	hasOlder := false
	if order == "DESC" {
		if len(events) > query.Limit {
			hasOlder = true
			events = events[:query.Limit]
		}
		for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
			events[left], events[right] = events[right], events[left]
		}
	} else if len(events) > query.Limit {
		events = events[:query.Limit]
	}
	hasNewer := order == "ASC" && len(events) == query.Limit && scanCursorLessThanProjectionTail(events, cursor)
	scanCursor := query.After
	if len(events) > 0 {
		scanCursor = events[len(events)-1].Seq
	}
	window := EventWindow{Events: events, Cursor: cursor, HasOlder: hasOlder, HasNewer: hasNewer, ScanCursor: scanCursor}
	if query.Projection == EventProjectionTranscript && len(events) > 0 {
		if err := s.readTranscriptContextBefore(taskID, events[0].Seq, &window); err != nil {
			return EventWindow{}, err
		}
	}
	return window, nil
}

// HistoryEventSeqByRef resolves one Runtime Owner history detail reference
// (a stable timeline item ID or transcript entry ID) to the Seq of the first
// retained Event whose ID the reference embeds. Projection-derived IDs append
// suffixes to their source Event ID, so an exact match or an Event-ID prefix
// followed by a dash both resolve. ok=false means no Event owns the reference.
func (s *Service) HistoryEventSeqByRef(taskID string, ref string) (int, bool, error) {
	if _, err := s.Get(taskID); err != nil {
		return 0, false, err
	}
	var seq int
	err := s.db.QueryRow(
		`SELECT seq FROM task_events WHERE task_id=? AND (id=? OR instr(?, id || '-')=1) ORDER BY seq ASC LIMIT 1`,
		taskID, ref, ref,
	).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("resolve Task Event reference: %w", err)
	}
	return seq, true, nil
}

func (s *Service) readTranscriptContextBefore(taskID string, seq int, window *EventWindow) error {
	var continuationID string
	err := s.db.QueryRow(`
		SELECT COALESCE(continuation_id,''),COALESCE(json_extract(payload_json,'$.adapter'),'') FROM task_events
		WHERE task_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'
		ORDER BY seq DESC LIMIT 1`, taskID, seq,
	).Scan(&continuationID, &window.PriorTranscriptAdapter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Task Transcript context: %w", err)
	}
	if continuationID != "" {
		var provider string
		err = s.db.QueryRow(`SELECT number,runtime_provider FROM task_continuations WHERE id=? AND task_id=?`, continuationID, taskID).
			Scan(&window.PriorContinuation, &provider)
		if err == nil {
			if provider != "" {
				window.PriorTranscriptAdapter = provider
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read Task Transcript Continuation pin: %w", err)
		}
	}
	// Legacy lifecycle Events can predate durable Continuation pins. Preserve
	// their historical numbering with the old count only on that fallback path.
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM task_events
		WHERE task_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'`, taskID, seq,
	).Scan(&window.PriorContinuation); err != nil {
		return fmt.Errorf("read legacy Task Transcript Continuation context: %w", err)
	}
	return nil
}

func scanCursorLessThanProjectionTail(events []Event, cursor int) bool {
	return len(events) > 0 && events[len(events)-1].Seq < cursor
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
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
		var parseErr error
		if event.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt); parseErr != nil {
			return nil, fmt.Errorf("parse created_at: %w", parseErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return events, nil
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
	return s.ReconcileInterruptedStateExcept(nil)
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
		`SELECT id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at
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
