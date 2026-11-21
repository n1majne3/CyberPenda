// Package session owns the durable Non-Project Session aggregate. A Session is
// intentionally separate from Task: it has no Project identity, Scope, or
// Project artifacts, while its owner-local Events and managed Workdir remain
// durable for the Session's complete lifetime.
package session

import (
	"errors"
	"io"
	"path/filepath"
	"pentest/internal/runtimeconfig"
	"strings"
	"time"

	"pentest/internal/owner"
)

// Lifecycle is the only durable Session state. Runtime liveness and turn
// failures are separate projections and never change this value implicitly.
type Lifecycle string

const (
	LifecycleOpen     Lifecycle = "open"
	LifecycleArchived Lifecycle = "archived"
)

// MaxTitleRunes bounds the operator-visible Session Title derived from the
// first non-empty input line.
const MaxTitleRunes = 80

// MaxAttachmentFileBytes and MaxAttachmentCount keep the domain safe even when
// callers other than the HTTP multipart adapter create a Session.
const (
	MaxAttachmentFileBytes int64 = 100 << 20
	MaxAttachmentCount           = 25
)

type EventKind string

const (
	EventKindConversation         EventKind = "conversation"
	EventKindAttachment           EventKind = "attachment"
	EventKindLifecycle            EventKind = "lifecycle"
	EventKindRuntimeOutput        EventKind = "runtime_output"
	EventKindSteering             EventKind = "steering"
	EventKindPermission           EventKind = "permission"
	EventKindTurn                 EventKind = "turn"
	EventKindBlackboardConclusion EventKind = "blackboard_conclusion"
)

// EventPayload is intentionally structured and compact. Raw files remain in
// the managed Workdir; an attachment Event stores only its safe reference and
// digest metadata.
type EventPayload map[string]any

// Event is one ordered, owner-local Session timeline entry.
type Event struct {
	ID        string       `json:"id"`
	SessionID string       `json:"session_id"`
	Seq       int          `json:"seq"`
	Kind      EventKind    `json:"kind"`
	Payload   EventPayload `json:"payload"`
	CreatedAt time.Time    `json:"created_at"`
}

type Runner string

const (
	RunnerSandbox Runner = "sandbox"
	RunnerHost    Runner = "host"
)

// RuntimeStatus is the lifecycle of one Session-scoped Runtime Continuation.
// It is deliberately separate from Session Lifecycle and Runtime Activity.
type RuntimeStatus string

const (
	RuntimeStatusPending     RuntimeStatus = "pending"
	RuntimeStatusRunning     RuntimeStatus = "running"
	RuntimeStatusCompleted   RuntimeStatus = "completed"
	RuntimeStatusFailed      RuntimeStatus = "failed"
	RuntimeStatusStopped     RuntimeStatus = "stopped"
	RuntimeStatusInterrupted RuntimeStatus = "interrupted"
)

// RuntimeTurnSelection is the per-turn provider, model, and reasoning choice.
// It never mutates the selected Runtime Profile.
type RuntimeTurnSelection struct {
	ModelProviderID string `json:"model_provider_id,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// RuntimeActivity reports current process/session health independently of
// whether a Session is open or archived.
type RuntimeActivity struct {
	Liveness     string `json:"liveness"`
	TurnActivity string `json:"turn_activity,omitempty"`
	Warning      string `json:"warning,omitempty"`
}

type RuntimeControls struct {
	NativeResumeAvailable   bool                  `json:"native_resume_available"`
	NativeSteerAvailable    bool                  `json:"native_steer_available"`
	NativeSteerMode         string                `json:"native_steer_mode,omitempty"`
	NativeSteerState        string                `json:"native_steer_state,omitempty"`
	NativeSteerRequestID    string                `json:"native_steer_request_id,omitempty"`
	NativeSteerErrorCode    string                `json:"native_steer_error_code,omitempty"`
	NativeSteerError        string                `json:"native_steer_error,omitempty"`
	QueueSteerAvailable     bool                  `json:"queue_steer_available"`
	InterruptSteerAvailable bool                  `json:"interrupt_steer_available"`
	NativeSessionCaptured   bool                  `json:"native_session_captured"`
	RuntimeProvider         string                `json:"runtime_provider,omitempty"`
	TurnSelection           *RuntimeTurnSelection `json:"turn_selection,omitempty"`
	ProviderPermissions     []ProviderPermission  `json:"provider_permissions,omitempty"`
	RecoveryState           string                `json:"recovery_state,omitempty"`
	RecoveryReason          string                `json:"recovery_reason,omitempty"`
}

// RunControls are the owner-local launch controls that remain stable across
// Session Runtime Continuations. Sessions expose the same Blackboard Mode
// vocabulary as Tasks without acquiring a Project identity or Project scope.
type RunControls struct {
	BlackboardMode BlackboardMode `json:"blackboard_mode"`
	// ContainerCLI is docker or podman for Session sandbox launches. Empty
	// means the daemon default (-container-cli / PENTEST_CONTAINER_CLI).
	ContainerCLI   string `json:"container_cli,omitempty"`
	SandboxNetwork string `json:"sandbox_network,omitempty"`
	SandboxVPNTun  bool   `json:"sandbox_vpn_tun,omitempty"`
}

type BlackboardMode string

const (
	BlackboardModeInteractive  BlackboardMode = "interactive"
	BlackboardModeWorkingGraph BlackboardMode = "working_graph"
	BlackboardModeDisabled     BlackboardMode = "disabled"
)

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
	BlackboardConclusionErrorWorkTurnNeverSettled    = owner.BlackboardConclusionErrorWorkTurnNeverSettled
	BlackboardConclusionAutomaticTurnLimit           = owner.BlackboardConclusionAutomaticTurnLimit
	BlackboardConclusionWorkTurnConflictLimit        = owner.BlackboardConclusionWorkTurnConflictLimit
)

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

// BlackboardConclusion is the compact Session read view for the latest
// assisted Work Runtime Turn checkpoint and any conclusion progress it
// triggered. Result bytes and provider correlation remain private.
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

// ProviderPermission is a redacted provider approval request owned by one
// Session and one active continuation.
type ProviderPermission struct {
	RequestID           string    `json:"request_id"`
	PermissionRequestID string    `json:"permission_request_id"`
	SessionID           string    `json:"session_id,omitempty"`
	ProviderTurnID      string    `json:"provider_turn_id,omitempty"`
	Provider            string    `json:"provider,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// RuntimeConfigVersion is durable Session configuration history. Secrets are
// never stored here; callers persist only the resolved non-secret selection.
type RuntimeConfigVersion struct {
	ID               string         `json:"id"`
	SessionID        string         `json:"session_id"`
	Version          int            `json:"version"`
	RuntimeProfileID string         `json:"runtime_profile_id,omitempty"`
	Config           map[string]any `json:"config"`
	CreatedAt        time.Time      `json:"created_at"`
}

type Continuation struct {
	ID                string        `json:"id"`
	SessionID         string        `json:"session_id"`
	Number            int           `json:"number"`
	RuntimeProfileID  string        `json:"runtime_profile_id,omitempty"`
	RuntimeProvider   string        `json:"runtime_provider"`
	Runner            Runner        `json:"runner"`
	Status            RuntimeStatus `json:"status"`
	ContainerID       string        `json:"container_id,omitempty"`
	NativeSessionID   string        `json:"native_session_id,omitempty"`
	NativeSessionPath string        `json:"native_session_path,omitempty"`
	RuntimeConfigID   string        `json:"runtime_config_version_id,omitempty"`
	StartedAt         time.Time     `json:"started_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	EndedAt           *time.Time    `json:"ended_at,omitempty"`
}

type CreateContinuationRequest struct {
	RuntimeProfileID  string
	RuntimeProvider   string
	Runner            Runner
	RuntimeConfig     map[string]any
	RuntimeConfigID   string
	ContainerID       string
	NativeSessionID   string
	NativeSessionPath string
}

// Session is the durable Non-Project Session aggregate. Workdir is kept out of
// the JSON representation because it is a server-local path, not operator
// state or a Project artifact reference.
type Session struct {
	BlackboardProtocol   string                 `json:"blackboard_protocol"`
	ID                   string                 `json:"id"`
	Title                string                 `json:"title"`
	Lifecycle            Lifecycle              `json:"lifecycle"`
	Workdir              string                 `json:"-"`
	RunControls          RunControls            `json:"run_controls"`
	BlackboardConclusion BlackboardConclusion   `json:"blackboard_conclusion"`
	RuntimeActivity      RuntimeActivity        `json:"runtime_activity"`
	RuntimeControls      RuntimeControls        `json:"runtime_controls"`
	RuntimeConfiguration *runtimeconfig.Summary `json:"runtime_configuration,omitempty"`
	ActiveContinuation   *Continuation          `json:"active_continuation,omitempty"`
	LatestContinuation   *Continuation          `json:"latest_continuation,omitempty"`
	CreatedAt            time.Time              `json:"created_at"`
	UpdatedAt            time.Time              `json:"updated_at"`
	LastActivityAt       time.Time              `json:"last_activity_at"`
}

// OwnerContract returns the explicit Session capability contract consumed by
// later Runtime and Blackboard adapters. It cannot carry a Project identity.
func (s Session) OwnerContract() owner.Contract {
	return owner.NewSessionContract(s.ID, s.Workdir)
}

// Attachment is an operator-supplied file staged into a Session Workdir. Open
// is called exactly once during creation; the service owns closing the stream.
type Attachment struct {
	Name string
	Size int64
	Open func() (io.ReadCloser, error)
}

// ConversationInput is one operator message and its optional attachments.
// It can be committed with a new Continuation so recovery never exposes a
// Continuation without the input that initiated it.
type ConversationInput struct {
	Role        string
	Text        string
	Attachments []Attachment
}

// AttachmentReference is the typed, safe Workdir-relative projection stored by
// an attachment Event. Runtime adapters consume this instead of interpreting
// the Event payload map themselves.
type AttachmentReference struct {
	RelativePath   string
	ContinuationID string
}

func (event Event) AttachmentReference() (AttachmentReference, bool) {
	if event.Kind != EventKindAttachment {
		return AttachmentReference{}, false
	}
	relativePath, _ := event.Payload["relative_path"].(string)
	clean := filepath.Clean(strings.TrimSpace(relativePath))
	if clean == "." || filepath.IsAbs(clean) || filepath.Base(clean) != clean {
		return AttachmentReference{}, false
	}
	continuationID, _ := event.Payload["continuation_id"].(string)
	return AttachmentReference{RelativePath: clean, ContinuationID: strings.TrimSpace(continuationID)}, true
}

type CreateRequest struct {
	Input          string
	Attachments    []Attachment
	BlackboardMode BlackboardMode
	InitialRuntime *CreateContinuationRequest
}

var (
	// ErrNotFound deliberately has no owner details so cross-owner lookups do
	// not reveal whether another aggregate exists.
	ErrNotFound                   = errors.New("session not found")
	ErrMissingInput               = errors.New("session initial input is required")
	ErrMissingTitle               = errors.New("session title is required")
	ErrInvalidLifecycle           = errors.New("invalid session lifecycle")
	ErrInvalidLimit               = errors.New("session list limit must not be negative")
	ErrAlreadyArchived            = errors.New("session is already archived")
	ErrNotArchived                = errors.New("session is not archived")
	ErrOpenSession                = errors.New("open session cannot be deleted")
	ErrInvalidAttachment          = errors.New("invalid session attachment")
	ErrInvalidWorkdir             = errors.New("session workdir is outside the managed root")
	ErrSessionNotOpen             = errors.New("session is not open")
	ErrActiveContinuation         = errors.New("session already has an active continuation")
	ErrInvalidRunner              = errors.New("runner must be sandbox or host")
	ErrContinuationNotFound       = errors.New("session continuation not found")
	ErrContinuationStatusConflict = errors.New("session continuation status conflicts with its terminal state")
	ErrMissingRuntimeProfile      = errors.New("session runtime profile is required")
	ErrInvalidBlackboardMode      = errors.New("Session Blackboard Mode must be interactive, working_graph, or disabled")
)
