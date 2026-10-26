package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// ProviderSessionCapability names one independently negotiated provider
// control operation. It is intentionally separate from one-shot Adapter.Run.
type ProviderSessionCapability string

const (
	ProviderSessionCapabilityPersistentSession    ProviderSessionCapability = "persistent_session"
	ProviderSessionCapabilitySendTurn             ProviderSessionCapability = "send_turn"
	ProviderSessionCapabilityInterruptTurn        ProviderSessionCapability = "interrupt_turn"
	ProviderSessionCapabilityInterruptThenReplace ProviderSessionCapability = "interrupt_then_replace"
	ProviderSessionCapabilityInTurnSteer          ProviderSessionCapability = "in_turn_steer"
	ProviderSessionCapabilityPermissionResponse   ProviderSessionCapability = "permission_response"
	ProviderSessionCapabilityResumeSession        ProviderSessionCapability = "resume_session"
)

// ProviderSessionMode identifies the provider-native operation represented by
// an event or result.
type ProviderSessionMode string

const (
	ProviderSessionModeSendTurn             ProviderSessionMode = "send_turn"
	ProviderSessionModeInterruptTurn        ProviderSessionMode = "interrupt_turn"
	ProviderSessionModeInterruptThenReplace ProviderSessionMode = "interrupt_then_replace"
	ProviderSessionModeInTurnSteer          ProviderSessionMode = "in_turn_steer"
	ProviderSessionModePermissionResponse   ProviderSessionMode = "permission_response"
)

var (
	// ErrProviderSessionControlConflict reports a different control operation
	// already in progress for this owner-bound session.
	ErrProviderSessionControlConflict = errors.New("provider session control conflict")
	// ErrProviderSessionClosed reports an operation against a closed session.
	ErrProviderSessionClosed = errors.New("provider session is closed")
	// ErrInvalidProviderSessionRequest reports missing stable request identity.
	ErrInvalidProviderSessionRequest = errors.New("invalid provider session request")
	// ErrProviderTurnUnavailable reports an in-turn operation without a live
	// provider Runtime Turn to target.
	ErrProviderTurnUnavailable = errors.New("provider Runtime Turn is unavailable")
	// ErrProviderTurnChanged reports an in-turn operation whose expected Turn
	// is no longer the provider session's active Turn.
	ErrProviderTurnChanged = errors.New("provider Runtime Turn changed")
	// ErrProviderTurnNotSteerable reports an active provider Turn whose kind
	// explicitly rejects same-turn operator input, such as review or compact.
	ErrProviderTurnNotSteerable = errors.New("active provider Runtime Turn is not steerable")
	// ErrProviderMethodUnavailable reports a provider App Server that does not
	// implement the requested native method.
	ErrProviderMethodUnavailable = errors.New("provider method is unavailable")
	// ErrProviderSessionRequestConflict reports reuse of a request id with a
	// different operation payload.
	ErrProviderSessionRequestConflict = errors.New("provider session request id is already bound to different content")
)

// ProviderTurnChangedError preserves the redacted expected/active identities
// needed for deterministic steering recovery without exposing provider text.
type ProviderTurnChangedError struct {
	ExpectedTurnID string
	ActiveTurnID   string
}

func (e *ProviderTurnChangedError) Error() string {
	return fmt.Sprintf("expected provider Turn %q, active Turn is %q", e.ExpectedTurnID, e.ActiveTurnID)
}

func (e *ProviderTurnChangedError) Is(target error) bool { return target == ErrProviderTurnChanged }

// ProviderSessionRequestConflictError identifies an idempotency-key payload
// mismatch without exposing the payload itself.
type ProviderSessionRequestConflictError struct {
	RequestID string
}

func (e *ProviderSessionRequestConflictError) Error() string {
	return fmt.Sprintf("provider session request %q conflicts with prior content", e.RequestID)
}

func (e *ProviderSessionRequestConflictError) Is(target error) bool {
	return target == ErrProviderSessionRequestConflict || target == ErrProviderSessionControlConflict
}

// UnsupportedProviderSessionCapabilityError makes unsupported interactive
// controls distinguishable from provider or transport failures.
type UnsupportedProviderSessionCapabilityError struct {
	Capability ProviderSessionCapability
}

func (e *UnsupportedProviderSessionCapabilityError) Error() string {
	return fmt.Sprintf("provider session does not support %s", e.Capability)
}

// ProviderSessionOperationError is a typed, redacted provider operation
// failure. Cause is available to server diagnostics and is never placed in the
// normalized Task Event payload by this contract.
type ProviderSessionOperationError struct {
	Mode  ProviderSessionMode
	Cause error
}

func (e *ProviderSessionOperationError) Error() string {
	return fmt.Sprintf("provider session %s failed", e.Mode)
}

func (e *ProviderSessionOperationError) Unwrap() error { return e.Cause }

// ProviderSessionRequest is an owner-bound provider control request. RequestID
// is the idempotency key. Message remains on the control channel and is not
// copied into provider lifecycle events.
type ProviderSessionRequest struct {
	RequestID           string
	Message             string
	ProviderTurnID      string
	PermissionRequestID string
	PermissionDecision  string
	// ModelProviderID, Model, and RequestedReasoningEffort are the resolved
	// Runtime Turn Selection for this request. Every turn sends them explicitly.
	ModelProviderID          string
	Model                    string
	RequestedReasoningEffort string
	// EffectiveReasoningEffort is set only when a Runtime reports the level it
	// actually applied. It is never inferred from the request.
	EffectiveReasoningEffort string
	// TurnKind is server-owned. Missing values retain compatibility by
	// normalizing to an operator work Runtime Turn.
	TurnKind RuntimeTurnKind
}

// ProviderSessionResult is the stable correlation result for one provider
// operation.
type ProviderSessionResult struct {
	RequestID      string
	SessionID      string
	ProviderTurnID string
	Mode           ProviderSessionMode
	Outcome        string
}

// Payload returns the compact, redacted correlation fields persisted on Task
// and Continuation events.
func (r ProviderSessionResult) Payload() task.EventPayload {
	return task.EventPayload{
		"request_id": r.RequestID, "session_id": r.SessionID, "provider_turn_id": r.ProviderTurnID,
		"mode": string(r.Mode), "outcome": r.Outcome,
	}
}

// ProviderSessionEmit records a normalized owner/Continuation event. Protocol
// wire data and message content are deliberately outside this callback.
type ProviderSessionEmit func(task.EventKind, task.EventPayload)

// ProviderSessionEventSink lets the daemon attach durable handling for
// unsolicited provider notifications. Operation-local callbacks still take
// precedence, so one native control cannot be projected twice.
type ProviderSessionEventSink interface {
	SetEventSink(ProviderSessionEmit)
}

// ProviderSession is the long-lived provider control boundary beside the
// existing one-shot Adapter. Implementations keep provider process/session
// identity stable while turns change inside it.
type ProviderSession interface {
	SessionID() string
	Capabilities() runtimeplugin.Capabilities
	SendTurn(context.Context, ProviderSessionRequest, ProviderSessionEmit) (ProviderSessionResult, error)
	InterruptTurn(context.Context, ProviderSessionRequest, ProviderSessionEmit) (ProviderSessionResult, error)
	InterruptThenReplace(context.Context, ProviderSessionRequest, ProviderSessionEmit) (ProviderSessionResult, error)
	SteerInTurn(context.Context, ProviderSessionRequest, ProviderSessionEmit) (ProviderSessionResult, error)
	RespondPermission(context.Context, ProviderSessionRequest, ProviderSessionEmit) (ProviderSessionResult, error)
	Close(context.Context) error
}

// ProviderSessionTurnState is one atomic snapshot of the provider session's
// control and active Runtime Turn identity.
type ProviderSessionTurnState struct {
	SessionID      string
	ActiveTurnID   string
	ActiveTurnKind RuntimeTurnKind
	ControlBusy    bool
}

// TurnBusy reports whether a control RPC or provider Runtime Turn is active.
func (state ProviderSessionTurnState) TurnBusy() bool {
	return state.ControlBusy || strings.TrimSpace(state.ActiveTurnID) != ""
}

// ProviderSessionTurnStateReporter exposes the active Turn identity required
// to bind a durable same-turn Steering request.
type ProviderSessionTurnStateReporter interface {
	TurnState() ProviderSessionTurnState
}

// ProviderSessionTurnBusyReporter retains compatibility for provider handles
// that report liveness without exposing active Turn identity.
type ProviderSessionTurnBusyReporter interface {
	TurnBusy() bool
}

// ProviderSessionContinuationBinder updates the request-level Continuation pin
// on a Task-owned transport without replacing the provider session.
type ProviderSessionContinuationBinder interface {
	BindContinuation(string) error
}

func providerSessionContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func hasProviderSessionCapability(capabilities runtimeplugin.Capabilities, capability ProviderSessionCapability) bool {
	switch capability {
	case ProviderSessionCapabilityPersistentSession:
		return capabilities.PersistentSession
	case ProviderSessionCapabilitySendTurn:
		return capabilities.SendTurn
	case ProviderSessionCapabilityInterruptTurn:
		return capabilities.InterruptTurn
	case ProviderSessionCapabilityInterruptThenReplace:
		return capabilities.InterruptThenReplace
	case ProviderSessionCapabilityInTurnSteer:
		return capabilities.InTurnSteer
	case ProviderSessionCapabilityPermissionResponse:
		return capabilities.PermissionResponse
	case ProviderSessionCapabilityResumeSession:
		return capabilities.ResumeSession
	default:
		return false
	}
}
