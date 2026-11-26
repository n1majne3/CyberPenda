// Package task owns the task domain: a user-goal-driven project run executed by
// one runtime profile through one runner. A task captures an immutable scope
// snapshot at launch, plus run controls. Task events form the structured
// timeline; raw output stays in logs or evidence artifacts, never in events.
package task

import (
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/project"
)

// Runner names the execution boundary for a task. The sandbox runner is the
// default; the host runner requires explicit activation.
type Runner string

const (
	RunnerSandbox Runner = "sandbox"
	RunnerHost    Runner = "host"
)

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
	ContainerCLI   string            `json:"container_cli,omitempty"`
	Notes          string            `json:"notes,omitempty"`
	Extras         map[string]string `json:"extras,omitempty"`
	BlackboardMode BlackboardMode    `json:"blackboard_mode"`
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

func ResolveContainerCLI(taskChoice, daemonDefault string) string {
	if normalized, err := NormalizeContainerCLIChoice(taskChoice); err == nil && normalized != "" {
		return normalized
	}
	return strings.TrimSpace(daemonDefault)
}

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

type BlackboardMode string

const (
	BlackboardModeInteractive  BlackboardMode = "interactive"
	BlackboardModeWorkingGraph BlackboardMode = "working_graph"
	BlackboardModeDisabled     BlackboardMode = "disabled"
)

func normalizeBlackboardMode(mode BlackboardMode) (BlackboardMode, error) {
	switch mode {
	case "", BlackboardModeWorkingGraph:
		return BlackboardModeWorkingGraph, nil
	case BlackboardModeDisabled:
		return BlackboardModeDisabled, nil
	case BlackboardModeInteractive:
		return BlackboardModeInteractive, nil
	default:
		return "", ErrInvalidBlackboardMode
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
	Mode                         BlackboardMode                `json:"mode"`
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

type ConclusionRecoveryReason = owner.ConclusionRecoveryReason

const (
	ConclusionRecoveryRuntimeOwnershipNotProven      = owner.ConclusionRecoveryRuntimeOwnershipNotProven
	ConclusionRecoveryWritableReplacementUnavailable = owner.ConclusionRecoveryWritableReplacementUnavailable
	ConclusionRecoveryAcceptanceAmbiguous            = owner.ConclusionRecoveryAcceptanceAmbiguous
	ConclusionRecoveryDispatchFailed                 = owner.ConclusionRecoveryDispatchFailed
	ConclusionRecoveryLegacyCorrelationUnproven      = owner.ConclusionRecoveryLegacyCorrelationUnproven
)
