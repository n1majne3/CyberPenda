package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

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
	// already in progress for this Task-owned session.
	ErrProviderSessionControlConflict = errors.New("provider session control conflict")
	// ErrProviderSessionClosed reports an operation against a closed session.
	ErrProviderSessionClosed = errors.New("provider session is closed")
	// ErrInvalidProviderSessionRequest reports missing stable request identity.
	ErrInvalidProviderSessionRequest = errors.New("invalid provider session request")
	// ErrProviderSessionRequestConflict reports reuse of a request id with a
	// different operation payload.
	ErrProviderSessionRequestConflict = errors.New("provider session request id is already bound to different content")
)

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

// ProviderSessionRequest is a Task-bound provider control request. RequestID
// is the idempotency key. Message remains on the control channel and is not
// copied into provider lifecycle events.
type ProviderSessionRequest struct {
	RequestID           string
	Message             string
	ProviderTurnID      string
	PermissionRequestID string
	PermissionDecision  string
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

// ProviderSessionEmit records a normalized Task/Continuation event. Protocol
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

// ProviderSessionContinuationBinder updates the request-level Continuation pin
// on a Task-owned transport without replacing the provider session.
type ProviderSessionContinuationBinder interface {
	BindContinuation(string) error
}

// FakeProviderSessionConfig controls deterministic fake provider behavior.
type FakeProviderSessionConfig struct {
	SessionID         string
	ActiveTurnID      string
	Capabilities      runtimeplugin.Capabilities
	ManualAcknowledge bool
	Failures          map[ProviderSessionMode]error
}

type providerSessionCall struct {
	mode   ProviderSessionMode
	done   chan struct{}
	result ProviderSessionResult
	err    error
}

// FakeProviderSession exercises provider session semantics without a runtime
// binary or model credentials.
type FakeProviderSession struct {
	mu           sync.Mutex
	id           string
	capabilities runtimeplugin.Capabilities
	activeTurnID string
	turnNumber   int
	manualAck    bool
	failures     map[ProviderSessionMode]error
	calls        map[string]*providerSessionCall
	activeCall   string
	continuation string
	acknowledge  map[string]chan struct{}
	closed       bool
}

// NewFakeProviderSession returns an idle or active deterministic session.
func NewFakeProviderSession(config FakeProviderSessionConfig) *FakeProviderSession {
	id := strings.TrimSpace(config.SessionID)
	if id == "" {
		id = "fake-session"
	}
	return &FakeProviderSession{
		id: id, capabilities: config.Capabilities,
		activeTurnID: strings.TrimSpace(config.ActiveTurnID), turnNumber: 1,
		manualAck: config.ManualAcknowledge, failures: config.Failures,
		calls: map[string]*providerSessionCall{}, acknowledge: map[string]chan struct{}{},
	}
}

func (s *FakeProviderSession) ID() string { return s.id }

func (s *FakeProviderSession) SessionID() string { return s.id }

func (s *FakeProviderSession) BindContinuation(continuationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrProviderSessionClosed
	}
	s.continuation = strings.TrimSpace(continuationID)
	return nil
}

func (s *FakeProviderSession) Capabilities() runtimeplugin.Capabilities { return s.capabilities }

func (s *FakeProviderSession) SendTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.operate(ctx, ProviderSessionModeSendTurn, ProviderSessionCapabilitySendTurn, request, emit)
}

func (s *FakeProviderSession) InterruptTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.operate(ctx, ProviderSessionModeInterruptTurn, ProviderSessionCapabilityInterruptTurn, request, emit)
}

func (s *FakeProviderSession) InterruptThenReplace(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.operate(ctx, ProviderSessionModeInterruptThenReplace, ProviderSessionCapabilityInterruptThenReplace, request, emit)
}

func (s *FakeProviderSession) SteerInTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.operate(ctx, ProviderSessionModeInTurnSteer, ProviderSessionCapabilityInTurnSteer, request, emit)
}

// InTurnSteer is an operation-name alias matching the capability name.
func (s *FakeProviderSession) InTurnSteer(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.SteerInTurn(ctx, request, emit)
}

func (s *FakeProviderSession) RespondPermission(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.operate(ctx, ProviderSessionModePermissionResponse, ProviderSessionCapabilityPermissionResponse, request, emit)
}

func (s *FakeProviderSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeCall != "" {
		return ErrProviderSessionControlConflict
	}
	s.closed = true
	return nil
}

// Acknowledge releases a manually gated fake provider acknowledgement.
func (s *FakeProviderSession) Acknowledge(requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ack, ok := s.acknowledge[requestID]
	if !ok {
		return fmt.Errorf("%w: request %q is not awaiting acknowledgement", ErrInvalidProviderSessionRequest, requestID)
	}
	delete(s.acknowledge, requestID)
	close(ack)
	return nil
}

func (s *FakeProviderSession) operate(ctx context.Context, mode ProviderSessionMode, capability ProviderSessionCapability, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" {
		return ProviderSessionResult{}, ErrInvalidProviderSessionRequest
	}
	if !hasProviderSessionCapability(s.capabilities, capability) {
		s.mu.Lock()
		turnID := s.activeTurnID
		s.mu.Unlock()
		emitSessionEvent(emit, mode, "unsupported", request.RequestID, s.id, turnID)
		return ProviderSessionResult{}, &UnsupportedProviderSessionCapabilityError{Capability: capability}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ProviderSessionResult{}, ErrProviderSessionClosed
	}
	if prior, ok := s.calls[request.RequestID]; ok {
		if prior.mode != mode {
			s.mu.Unlock()
			return ProviderSessionResult{}, ErrProviderSessionControlConflict
		}
		s.mu.Unlock()
		select {
		case <-prior.done:
			return prior.result, prior.err
		case <-ctx.Done():
			return ProviderSessionResult{}, ctx.Err()
		}
	}
	if s.activeCall != "" {
		s.mu.Unlock()
		return ProviderSessionResult{}, ErrProviderSessionControlConflict
	}
	call := &providerSessionCall{mode: mode, done: make(chan struct{})}
	s.calls[request.RequestID] = call
	s.activeCall = request.RequestID
	turnID := s.activeTurnID
	if turnID == "" {
		turnID = s.nextTurnIDLocked()
		s.activeTurnID = turnID
	}
	var ack chan struct{}
	if s.manualAck && modeNeedsAcknowledgement(mode) {
		ack = make(chan struct{})
		s.acknowledge[request.RequestID] = ack
	}
	failure := s.failures[mode]
	s.mu.Unlock()

	finish := func(result ProviderSessionResult, err error, retryable bool) (ProviderSessionResult, error) {
		s.mu.Lock()
		call.result, call.err = result, err
		if s.activeCall == request.RequestID {
			s.activeCall = ""
		}
		delete(s.acknowledge, request.RequestID)
		// A caller-local cancellation only means that this wait ended; the
		// provider operation may still be replayed by the Task bridge. Keep
		// provider rejections terminal, but allow the same idempotency key to
		// observe a later acknowledgement after a local timeout.
		if retryable {
			delete(s.calls, request.RequestID)
		}
		close(call.done)
		s.mu.Unlock()
		return result, err
	}

	emitSessionEvent(emit, mode, "requested", request.RequestID, s.id, turnID)
	if failure != nil {
		emitSessionEvent(emit, mode, "failed", request.RequestID, s.id, turnID)
		return finish(ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: failure}, false)
	}
	if ack != nil {
		select {
		case <-ack:
		case <-ctx.Done():
			return finish(ProviderSessionResult{}, ctx.Err(), true)
		}
	}

	switch mode {
	case ProviderSessionModeInterruptThenReplace:
		emitSessionEvent(emit, mode, "acknowledged", request.RequestID, s.id, turnID)
		emitSessionEvent(emit, mode, "settled", request.RequestID, s.id, turnID)
		s.mu.Lock()
		turnID = s.nextTurnIDLocked()
		s.activeTurnID = turnID
		s.mu.Unlock()
		emitSessionEvent(emit, mode, "started", request.RequestID, s.id, turnID)
	case ProviderSessionModeInterruptTurn:
		emitSessionEvent(emit, mode, "acknowledged", request.RequestID, s.id, turnID)
		emitSessionEvent(emit, mode, "settled", request.RequestID, s.id, turnID)
	case ProviderSessionModeSendTurn:
		emitSessionEvent(emit, mode, "started", request.RequestID, s.id, turnID)
	case ProviderSessionModeInTurnSteer, ProviderSessionModePermissionResponse:
		emitSessionEvent(emit, mode, "acknowledged", request.RequestID, s.id, turnID)
	}
	return finish(ProviderSessionResult{RequestID: request.RequestID, SessionID: s.id, ProviderTurnID: turnID, Mode: mode, Outcome: sessionResultOutcome(mode)}, nil, false)
}

func providerSessionContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *FakeProviderSession) nextTurnIDLocked() string {
	s.turnNumber++
	return fmt.Sprintf("%s-turn-%d", s.id, s.turnNumber)
}

func modeNeedsAcknowledgement(mode ProviderSessionMode) bool {
	return mode == ProviderSessionModeInterruptTurn || mode == ProviderSessionModeInterruptThenReplace || mode == ProviderSessionModeInTurnSteer || mode == ProviderSessionModePermissionResponse
}

func sessionResultOutcome(mode ProviderSessionMode) string {
	if mode == ProviderSessionModeSendTurn || mode == ProviderSessionModeInterruptThenReplace {
		return "started"
	}
	if mode == ProviderSessionModeInterruptTurn {
		return "settled"
	}
	return "acknowledged"
}

func emitSessionEvent(emit ProviderSessionEmit, mode ProviderSessionMode, outcome, requestID, sessionID, providerTurnID string) {
	if emit == nil {
		return
	}
	kind := task.EventKindLifecycle
	if mode == ProviderSessionModeInterruptTurn || mode == ProviderSessionModeInterruptThenReplace || mode == ProviderSessionModeInTurnSteer {
		kind = task.EventKindSteering
	}
	emit(kind, task.EventPayload{
		"request_id": requestID, "session_id": sessionID, "provider_turn_id": providerTurnID,
		"mode": string(mode), "outcome": outcome,
	})
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
