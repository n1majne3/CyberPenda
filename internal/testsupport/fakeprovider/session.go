// Package fakeprovider provides a deterministic runtime.ProviderSession test double. Only test files import it.
package fakeprovider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// Config controls deterministic fake provider behavior.
type Config struct {
	SessionID         string
	ActiveTurnID      string
	ActiveTurnKind    runtime.RuntimeTurnKind
	Capabilities      runtimeplugin.Capabilities
	ManualAcknowledge bool
	Failures          map[runtime.ProviderSessionMode]error
}

type call struct {
	mode   runtime.ProviderSessionMode
	done   chan struct{}
	result runtime.ProviderSessionResult
	err    error
}

// Session exercises provider session semantics without a runtime
// binary or model credentials.
type Session struct {
	mu           sync.Mutex
	id           string
	capabilities runtimeplugin.Capabilities
	activeTurnID string
	turnNumber   int
	manualAck    bool
	failures     map[runtime.ProviderSessionMode]error
	calls        map[string]*call
	activeCall   string
	continuation string
	acknowledge  map[string]chan struct{}
	closed       bool
	// offline is true when current process/session health is confirmed dead
	// without requiring durable Task status or elapsed time.
	offline bool
	// unexpectedOffline is true only for non-Close process death (MarkOffline).
	// Explicit Close/Stop must not look like unexpected exit.
	unexpectedOffline bool
	// healthUnknown forces indeterminate health for activity tests.
	healthUnknown bool
	// lastRequests records each operation request for acceptance tests.
	lastRequests                       []runtime.ProviderSessionRequest
	observationSink                    runtime.ProviderSessionObserve
	attemptResultSink                  runtime.ProviderSessionAttemptResultSink
	attemptResultValidationFailureSink runtime.ProviderSessionAttemptResultValidationFailureSink
	requestTurnKind                    map[string]runtime.RuntimeTurnKind
	providerTurnKind                   map[string]runtime.RuntimeTurnKind
	requestLineage                     map[string]runtime.ProviderSessionTurnLineage
	providerLineage                    map[string]runtime.ProviderSessionTurnLineage
}

// New returns an idle or active deterministic session.
func New(config Config) *Session {
	id := strings.TrimSpace(config.SessionID)
	if id == "" {
		id = "fake-session"
	}
	activeTurnID := strings.TrimSpace(config.ActiveTurnID)
	activeTurnKind := config.ActiveTurnKind
	if activeTurnID != "" && activeTurnKind == "" {
		activeTurnKind = runtime.RuntimeTurnKindWork
	}
	providerTurnKind := map[string]runtime.RuntimeTurnKind{}
	if activeTurnID != "" {
		providerTurnKind[activeTurnID] = activeTurnKind
	}
	return &Session{
		id: id, capabilities: config.Capabilities,
		activeTurnID: activeTurnID, turnNumber: 1,
		manualAck: config.ManualAcknowledge, failures: config.Failures,
		calls: map[string]*call{}, acknowledge: map[string]chan struct{}{},
		requestTurnKind: map[string]runtime.RuntimeTurnKind{}, providerTurnKind: providerTurnKind,
		requestLineage: map[string]runtime.ProviderSessionTurnLineage{}, providerLineage: map[string]runtime.ProviderSessionTurnLineage{},
	}
}

func (s *Session) ID() string { return s.id }

func (s *Session) SessionID() string { return s.id }

func (s *Session) BindContinuation(continuationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return runtime.ErrProviderSessionClosed
	}
	s.continuation = strings.TrimSpace(continuationID)
	return nil
}

func (s *Session) Capabilities() runtimeplugin.Capabilities { return s.capabilities }

func (s *Session) SetObservationSink(sink runtime.ProviderSessionObserve) {
	s.mu.Lock()
	s.observationSink = sink
	s.mu.Unlock()
}

func (s *Session) SetAttemptResultSink(sink runtime.ProviderSessionAttemptResultSink) {
	s.mu.Lock()
	s.attemptResultSink = sink
	s.mu.Unlock()
}

func (s *Session) SetAttemptResultValidationFailureSink(sink runtime.ProviderSessionAttemptResultValidationFailureSink) {
	s.mu.Lock()
	s.attemptResultValidationFailureSink = sink
	s.mu.Unlock()
}

// EmitAttemptResult strictly decodes provider output before exposing a typed,
// canonical semantic result to the Harness. Raw bytes are never forwarded.
func (s *Session) EmitAttemptResult(raw []byte) error {
	validated, err := blackboardconclusion.Decode(raw)
	if err != nil {
		s.mu.Lock()
		requestID, providerTurnID, correlationErr := s.attemptResultCorrelationLocked()
		sink := s.attemptResultValidationFailureSink
		s.mu.Unlock()
		if correlationErr == nil && sink != nil {
			sink(attemptResultValidationFailure(requestID, s.id, providerTurnID, err, blackboardconclusion.ValidationReasonInvalidResult))
		}
		return err
	}
	s.mu.Lock()
	requestID, providerTurnID, err := s.attemptResultCorrelationLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	result := runtime.ProviderSessionAttemptResult{
		RequestID:      requestID,
		SessionID:      s.id,
		ProviderTurnID: providerTurnID,
		Validated:      validated,
	}
	sink := s.attemptResultSink
	s.mu.Unlock()
	if sink != nil {
		sink(result)
	}
	return nil
}

func (s *Session) attemptResultCorrelationLocked() (string, string, error) {
	if s.closed {
		return "", "", runtime.ErrProviderSessionClosed
	}
	if len(s.lastRequests) == 0 {
		return "", "", runtime.ErrInvalidProviderSessionRequest
	}
	requestID := s.lastRequests[len(s.lastRequests)-1].RequestID
	providerTurnID := s.activeTurnID
	if call := s.calls[requestID]; call != nil && strings.TrimSpace(call.result.ProviderTurnID) != "" {
		providerTurnID = call.result.ProviderTurnID
	}
	if strings.TrimSpace(providerTurnID) == "" {
		return "", "", runtime.ErrInvalidProviderSessionRequest
	}
	return requestID, providerTurnID, nil
}

func (s *Session) ResolveProviderSessionTurnKind(requestID, providerTurnID string) (runtime.RuntimeTurnKind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind, ok := s.requestTurnKind[strings.TrimSpace(requestID)]; ok {
		return kind, true
	}
	kind, ok := s.providerTurnKind[strings.TrimSpace(providerTurnID)]
	return kind, ok
}

func (s *Session) ResolveProviderSessionTurnLineage(requestID, providerTurnID string) (runtime.ProviderSessionTurnLineage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lineage, ok := s.requestLineage[strings.TrimSpace(requestID)]; ok {
		return lineage, true
	}
	lineage, ok := s.providerLineage[strings.TrimSpace(providerTurnID)]
	return lineage, ok
}

// EmitObservation deterministically projects bounded provider metadata for
// acceptance tests. Fake-owned session and active Turn correlation fill only
// missing values; all kind-specific fields remain explicitly validated.
func (s *Session) EmitObservation(observation runtime.ProviderSessionObservation) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return runtime.ErrProviderSessionClosed
	}
	if strings.TrimSpace(observation.SessionID) == "" {
		observation.SessionID = s.id
	}
	if strings.TrimSpace(observation.ProviderTurnID) == "" {
		observation.ProviderTurnID = s.activeTurnID
	}
	if strings.TrimSpace(observation.RequestID) == "" && len(s.lastRequests) > 0 {
		observation.RequestID = s.lastRequests[len(s.lastRequests)-1].RequestID
	}
	if err := observation.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	if observation.Kind == runtime.ProviderSessionObservationTurnCompleted && observation.ProviderTurnID == s.activeTurnID {
		s.activeTurnID = ""
	}
	sink := s.observationSink
	s.mu.Unlock()
	if sink != nil {
		sink(observation)
	}
	return nil
}

func (s *Session) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.operate(ctx, runtime.ProviderSessionModeSendTurn, runtime.ProviderSessionCapabilitySendTurn, request, emit)
}

// LastRequests returns a copy of recorded provider control requests.
func (s *Session) LastRequests() []runtime.ProviderSessionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runtime.ProviderSessionRequest(nil), s.lastRequests...)
}

func (s *Session) InterruptTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.operate(ctx, runtime.ProviderSessionModeInterruptTurn, runtime.ProviderSessionCapabilityInterruptTurn, request, emit)
}

func (s *Session) InterruptThenReplace(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.operate(ctx, runtime.ProviderSessionModeInterruptThenReplace, runtime.ProviderSessionCapabilityInterruptThenReplace, request, emit)
}

func (s *Session) SteerInTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	s.mu.Lock()
	activeTurnID := strings.TrimSpace(s.activeTurnID)
	s.mu.Unlock()
	if activeTurnID == "" {
		return runtime.ProviderSessionResult{}, &runtime.ProviderSessionOperationError{Mode: runtime.ProviderSessionModeInTurnSteer, Cause: runtime.ErrProviderTurnUnavailable}
	}
	if expected := strings.TrimSpace(request.ProviderTurnID); expected == "" {
		return runtime.ProviderSessionResult{}, &runtime.ProviderSessionOperationError{Mode: runtime.ProviderSessionModeInTurnSteer, Cause: runtime.ErrInvalidProviderSessionRequest}
	} else if expected != activeTurnID {
		return runtime.ProviderSessionResult{}, &runtime.ProviderSessionOperationError{Mode: runtime.ProviderSessionModeInTurnSteer, Cause: &runtime.ProviderTurnChangedError{
			ExpectedTurnID: expected, ActiveTurnID: activeTurnID,
		}}
	}
	return s.operate(ctx, runtime.ProviderSessionModeInTurnSteer, runtime.ProviderSessionCapabilityInTurnSteer, request, emit)
}

// InTurnSteer is an operation-name alias matching the capability name.
func (s *Session) InTurnSteer(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.SteerInTurn(ctx, request, emit)
}

func (s *Session) RespondPermission(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.operate(ctx, runtime.ProviderSessionModePermissionResponse, runtime.ProviderSessionCapabilityPermissionResponse, request, emit)
}

func (s *Session) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeCall != "" {
		return runtime.ErrProviderSessionControlConflict
	}
	s.closed = true
	s.activeTurnID = ""
	s.offline = true
	// Explicit Close is operator Stop/cleanup, not unexpected exit.
	s.unexpectedOffline = false
	return nil
}

// MarkOffline records confirmed unexpected process/session exit without Close.
func (s *Session) MarkOffline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offline = true
	s.unexpectedOffline = true
}

// MarkHealthUnknown forces indeterminate health for Runtime Activity tests.
func (s *Session) MarkHealthUnknown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthUnknown = true
}

// ControlBusy reports whether a native control operation is in flight.
func (s *Session) ControlBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeCall != ""
}

// TurnBusy reports whether a fake control call or provider Runtime Turn is active.
func (s *Session) TurnBusy() bool {
	return s.TurnState().TurnBusy()
}

// TurnState returns one atomic fake provider Turn snapshot.
func (s *Session) TurnState() runtime.ProviderSessionTurnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return runtime.ProviderSessionTurnState{
		SessionID: s.id, ActiveTurnID: s.activeTurnID,
		ActiveTurnKind: s.providerTurnKind[s.activeTurnID], ControlBusy: s.activeCall != "",
	}
}

// SessionClosed reports whether Close has completed on this handle.
func (s *Session) SessionClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// SessionOffline reports confirmed offline health from the current process.
func (s *Session) SessionOffline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offline || s.closed
}

// SessionUnexpectedOffline reports offline caused by process/protocol death,
// not by explicit Close/Stop.
func (s *Session) SessionUnexpectedOffline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unexpectedOffline
}

// SessionHealthUnknown reports indeterminate health without inventing failure.
func (s *Session) SessionHealthUnknown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthUnknown && !s.offline && !s.closed
}

// Acknowledge releases a manually gated fake provider acknowledgement.
func (s *Session) Acknowledge(requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ack, ok := s.acknowledge[requestID]
	if !ok {
		return fmt.Errorf("%w: request %q is not awaiting acknowledgement", runtime.ErrInvalidProviderSessionRequest, requestID)
	}
	delete(s.acknowledge, requestID)
	close(ack)
	return nil
}

func (s *Session) operate(ctx context.Context, mode runtime.ProviderSessionMode, capability runtime.ProviderSessionCapability, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.TurnKind = normalizeRuntimeTurnKind(request.TurnKind)
	if request.RequestID == "" {
		return runtime.ProviderSessionResult{}, runtime.ErrInvalidProviderSessionRequest
	}
	s.mu.Lock()
	s.lastRequests = append(s.lastRequests, request)
	s.mu.Unlock()
	if !hasProviderSessionCapability(s.capabilities, capability) {
		s.mu.Lock()
		turnID := s.activeTurnID
		s.mu.Unlock()
		emitSessionEvent(emit, mode, "unsupported", request.RequestID, s.id, turnID)
		return runtime.ProviderSessionResult{}, &runtime.UnsupportedProviderSessionCapabilityError{Capability: capability}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return runtime.ProviderSessionResult{}, runtime.ErrProviderSessionClosed
	}
	if prior, ok := s.calls[request.RequestID]; ok {
		if prior.mode != mode {
			s.mu.Unlock()
			return runtime.ProviderSessionResult{}, runtime.ErrProviderSessionControlConflict
		}
		s.mu.Unlock()
		select {
		case <-prior.done:
			return prior.result, prior.err
		case <-ctx.Done():
			return runtime.ProviderSessionResult{}, ctx.Err()
		}
	}
	if s.activeCall != "" {
		s.mu.Unlock()
		return runtime.ProviderSessionResult{}, runtime.ErrProviderSessionControlConflict
	}
	call := &call{mode: mode, done: make(chan struct{})}
	s.calls[request.RequestID] = call
	s.activeCall = request.RequestID
	turnID := s.activeTurnID
	if turnID == "" {
		turnID = s.nextTurnIDLocked()
		s.activeTurnID = turnID
	}
	s.requestTurnKind[request.RequestID] = request.TurnKind
	lineage := providerSessionTurnLineage(request, turnID)
	s.requestLineage[request.RequestID] = lineage
	if mode != runtime.ProviderSessionModeInTurnSteer {
		s.providerTurnKind[turnID] = request.TurnKind
		s.providerLineage[turnID] = lineage
	}
	var ack chan struct{}
	if s.manualAck && modeNeedsAcknowledgement(mode) {
		ack = make(chan struct{})
		s.acknowledge[request.RequestID] = ack
	}
	failure := s.failures[mode]
	s.mu.Unlock()

	finish := func(result runtime.ProviderSessionResult, err error, retryable bool) (runtime.ProviderSessionResult, error) {
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
		return finish(runtime.ProviderSessionResult{}, &runtime.ProviderSessionOperationError{Mode: mode, Cause: failure}, false)
	}
	if ack != nil {
		select {
		case <-ack:
		case <-ctx.Done():
			return finish(runtime.ProviderSessionResult{}, ctx.Err(), true)
		}
	}

	switch mode {
	case runtime.ProviderSessionModeInterruptThenReplace:
		emitSessionEvent(emit, mode, "acknowledged", request.RequestID, s.id, turnID)
		emitSessionEvent(emit, mode, "settled", request.RequestID, s.id, turnID)
		s.mu.Lock()
		turnID = s.nextTurnIDLocked()
		s.activeTurnID = turnID
		s.providerTurnKind[turnID] = request.TurnKind
		s.mu.Unlock()
		emitSessionEvent(emit, mode, "started", request.RequestID, s.id, turnID)
	case runtime.ProviderSessionModeInterruptTurn:
		emitSessionEvent(emit, mode, "acknowledged", request.RequestID, s.id, turnID)
		emitSessionEvent(emit, mode, "settled", request.RequestID, s.id, turnID)
	case runtime.ProviderSessionModeSendTurn:
		emitSessionEvent(emit, mode, "started", request.RequestID, s.id, turnID)
	case runtime.ProviderSessionModeInTurnSteer, runtime.ProviderSessionModePermissionResponse:
		emitSessionEvent(emit, mode, "acknowledged", request.RequestID, s.id, turnID)
	}
	return finish(runtime.ProviderSessionResult{RequestID: request.RequestID, SessionID: s.id, ProviderTurnID: turnID, Mode: mode, Outcome: sessionResultOutcome(mode)}, nil, false)
}

func (s *Session) nextTurnIDLocked() string {
	s.turnNumber++
	return fmt.Sprintf("%s-turn-%d", s.id, s.turnNumber)
}

func modeNeedsAcknowledgement(mode runtime.ProviderSessionMode) bool {
	return mode == runtime.ProviderSessionModeInterruptTurn || mode == runtime.ProviderSessionModeInterruptThenReplace || mode == runtime.ProviderSessionModeInTurnSteer || mode == runtime.ProviderSessionModePermissionResponse
}

func sessionResultOutcome(mode runtime.ProviderSessionMode) string {
	if mode == runtime.ProviderSessionModeSendTurn || mode == runtime.ProviderSessionModeInterruptThenReplace {
		return "started"
	}
	if mode == runtime.ProviderSessionModeInterruptTurn {
		return "settled"
	}
	return "acknowledged"
}

func emitSessionEvent(emit runtime.ProviderSessionEmit, mode runtime.ProviderSessionMode, outcome, requestID, sessionID, providerTurnID string) {
	if emit == nil {
		return
	}
	kind := task.EventKindLifecycle
	if mode == runtime.ProviderSessionModeInterruptTurn || mode == runtime.ProviderSessionModeInterruptThenReplace || mode == runtime.ProviderSessionModeInTurnSteer {
		kind = task.EventKindSteering
	}
	emit(kind, task.EventPayload{
		"request_id": requestID, "session_id": sessionID, "provider_turn_id": providerTurnID,
		"mode": string(mode), "outcome": outcome,
	})
}

func normalizeRuntimeTurnKind(kind runtime.RuntimeTurnKind) runtime.RuntimeTurnKind {
	if kind == "" {
		return runtime.RuntimeTurnKindWork
	}
	return kind
}

func providerSessionTurnLineage(request runtime.ProviderSessionRequest, providerTurnID string) runtime.ProviderSessionTurnLineage {
	return runtime.ProviderSessionTurnLineage{
		RequestID:                strings.TrimSpace(request.RequestID),
		ProviderTurnID:           strings.TrimSpace(providerTurnID),
		Kind:                     normalizeRuntimeTurnKind(request.TurnKind),
		ModelProviderID:          strings.TrimSpace(request.ModelProviderID),
		Model:                    strings.TrimSpace(request.Model),
		RequestedReasoningEffort: strings.TrimSpace(request.RequestedReasoningEffort),
		EffectiveReasoningEffort: strings.TrimSpace(request.EffectiveReasoningEffort),
	}
}

func hasProviderSessionCapability(capabilities runtimeplugin.Capabilities, capability runtime.ProviderSessionCapability) bool {
	switch capability {
	case runtime.ProviderSessionCapabilityPersistentSession:
		return capabilities.PersistentSession
	case runtime.ProviderSessionCapabilitySendTurn:
		return capabilities.SendTurn
	case runtime.ProviderSessionCapabilityInterruptTurn:
		return capabilities.InterruptTurn
	case runtime.ProviderSessionCapabilityInterruptThenReplace:
		return capabilities.InterruptThenReplace
	case runtime.ProviderSessionCapabilityInTurnSteer:
		return capabilities.InTurnSteer
	case runtime.ProviderSessionCapabilityPermissionResponse:
		return capabilities.PermissionResponse
	case runtime.ProviderSessionCapabilityResumeSession:
		return capabilities.ResumeSession
	default:
		return false
	}
}

func attemptResultValidationFailure(requestID, sessionID, providerTurnID string, err error, reason blackboardconclusion.ValidationReason) runtime.ProviderSessionAttemptResultValidationFailure {
	failure := runtime.ProviderSessionAttemptResultValidationFailure{
		RequestID: requestID, SessionID: sessionID, ProviderTurnID: providerTurnID,
		ValidationErrorCode: runtime.ProviderSessionAttemptResultInvalid,
	}
	if err == nil {
		failure.Reason = reason
		return failure
	}
	detail := blackboardconclusion.DecodeDetailOf(err)
	failure.Reason, failure.FieldPath, failure.Expected = detail.Reason, detail.FieldPath, detail.Expected
	return failure
}
