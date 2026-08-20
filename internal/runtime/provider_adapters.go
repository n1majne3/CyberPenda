package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// ProviderSessionTransport is the narrow protocol seam used by provider
// adapters. SandboxSessionBridge implements it directly; tests can provide an
// in-memory transport without provider binaries or credentials.
type ProviderSessionTransport interface {
	Send(context.Context, SandboxBridgeRequest) (SandboxBridgeResponse, error)
	Close(context.Context) error
}

// ProviderSessionEventHandler is implemented by adapters that can consume
// unsolicited provider notifications delivered through SandboxBridgeConfig's
// ProtocolEmit callback. Implementations emit only normalized correlation
// fields; raw protocol payload remains outside Task events.
type ProviderSessionEventHandler interface {
	HandleEvent(SandboxBridgeEvent, ProviderSessionEmit)
}

type providerWireMethods struct {
	send       string
	interrupt  string
	steer      string
	permission string
	params     func(string, string, ProviderSessionRequest) map[string]any
	// prepareSend optionally issues setup wire methods before the primary send
	// method (for example Pi set_model → set_thinking_level before prompt).
	prepareSend func(context.Context, ProviderSessionTransport, string, string, string, ProviderSessionRequest) error
	turnID      func(map[string]any) string
	sessionID   func(map[string]any) string
}

type providerSessionCallResult struct {
	fingerprint string
	result      ProviderSessionResult
	err         error
}

type providerSessionRequestIdentity struct {
	mode        ProviderSessionMode
	fingerprint string
}

type providerSettlement struct {
	seq uint64
}

// providerSessionAdapter implements the shared lifecycle, idempotency, and
// event semantics. Provider wrappers below only supply native wire mappings.
type providerSessionAdapter struct {
	mu                sync.Mutex
	transport         ProviderSessionTransport
	provider          string
	methods           providerWireMethods
	capabilities      runtimeplugin.Capabilities
	sessionID         string
	activeTurnID      string
	closed            bool
	active            bool
	activeRequestID   string
	activeMode        ProviderSessionMode
	activeFingerprint string
	calls             map[string]providerSessionCallResult
	requests          map[string]providerSessionRequestIdentity
	eventSink         ProviderSessionEmit
	observationSink   ProviderSessionObserve
	requestTurnKind   map[string]RuntimeTurnKind
	providerTurnKind  map[string]RuntimeTurnKind
	requestLineage    map[string]ProviderSessionTurnLineage
	providerLineage   map[string]ProviderSessionTurnLineage
	settlements       map[string]providerSettlement
	settlementSeq     uint64
	settlementChanged chan struct{}
}

func newProviderSessionAdapter(provider string, transport ProviderSessionTransport, sessionID, activeTurnID string, capabilities runtimeplugin.Capabilities, methods providerWireMethods) *providerSessionAdapter {
	if strings.TrimSpace(methods.steer) == "" {
		capabilities.InTurnSteer = false
	}
	return &providerSessionAdapter{
		transport: transport, provider: provider, methods: methods,
		capabilities: capabilities, sessionID: strings.TrimSpace(sessionID),
		activeTurnID: strings.TrimSpace(activeTurnID), calls: map[string]providerSessionCallResult{},
		requests: map[string]providerSessionRequestIdentity{}, settlements: map[string]providerSettlement{},
		requestTurnKind: map[string]RuntimeTurnKind{}, providerTurnKind: map[string]RuntimeTurnKind{},
		requestLineage: map[string]ProviderSessionTurnLineage{}, providerLineage: map[string]ProviderSessionTurnLineage{},
		settlementChanged: make(chan struct{}),
	}
}

func (s *providerSessionAdapter) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *providerSessionAdapter) BindContinuation(continuationID string) error {
	continuationID = strings.TrimSpace(continuationID)
	if continuationID == "" {
		return ErrInvalidProviderSessionRequest
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrProviderSessionClosed
	}
	transport := s.transport
	s.mu.Unlock()
	if binder, ok := transport.(ProviderSessionContinuationBinder); ok {
		return binder.BindContinuation(continuationID)
	}
	return nil
}

func (s *providerSessionAdapter) Capabilities() runtimeplugin.Capabilities { return s.capabilities }

func (s *providerSessionAdapter) SetEventSink(sink ProviderSessionEmit) {
	s.mu.Lock()
	s.eventSink = sink
	s.mu.Unlock()
}

func (s *providerSessionAdapter) SetObservationSink(sink ProviderSessionObserve) {
	s.mu.Lock()
	s.observationSink = sink
	s.mu.Unlock()
}

func (s *providerSessionAdapter) emitObservation(observation ProviderSessionObservation) {
	if observation.Validate() != nil {
		return
	}
	s.mu.Lock()
	sink := s.observationSink
	s.mu.Unlock()
	if sink != nil {
		sink(observation)
	}
}

func (s *providerSessionAdapter) ResolveProviderSessionTurnKind(requestID, providerTurnID string) (RuntimeTurnKind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind, ok := s.requestTurnKind[strings.TrimSpace(requestID)]; ok {
		return kind, true
	}
	kind, ok := s.providerTurnKind[strings.TrimSpace(providerTurnID)]
	return kind, ok
}

func (s *providerSessionAdapter) ResolveProviderSessionTurnLineage(requestID, providerTurnID string) (ProviderSessionTurnLineage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lineage, ok := s.requestLineage[strings.TrimSpace(requestID)]; ok {
		return lineage, true
	}
	lineage, ok := s.providerLineage[strings.TrimSpace(providerTurnID)]
	return lineage, ok
}

func (s *providerSessionAdapter) recordProviderSessionTurnKind(requestID, providerTurnID string, kind RuntimeTurnKind) {
	s.mu.Lock()
	s.requestTurnKind[strings.TrimSpace(requestID)] = kind
	if providerTurnID = strings.TrimSpace(providerTurnID); providerTurnID != "" {
		s.providerTurnKind[providerTurnID] = kind
	}
	s.mu.Unlock()
}

func (s *providerSessionAdapter) recordProviderSessionTurnLineage(request ProviderSessionRequest, providerTurnID string) {
	s.mu.Lock()
	lineage := providerSessionTurnLineage(request, providerTurnID)
	s.requestLineage[strings.TrimSpace(request.RequestID)] = lineage
	if providerTurnID = strings.TrimSpace(providerTurnID); providerTurnID != "" {
		s.providerLineage[providerTurnID] = lineage
	}
	s.mu.Unlock()
}

func (s *providerSessionAdapter) SendTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.run(ctx, ProviderSessionModeSendTurn, ProviderSessionCapabilitySendTurn, request, emit, s.methods.send)
}

func (s *providerSessionAdapter) InterruptTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.run(ctx, ProviderSessionModeInterruptTurn, ProviderSessionCapabilityInterruptTurn, request, emit, s.methods.interrupt)
}

func (s *providerSessionAdapter) InterruptThenReplace(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.TurnKind = normalizeRuntimeTurnKind(request.TurnKind)
	if request.RequestID == "" {
		return ProviderSessionResult{}, ErrInvalidProviderSessionRequest
	}
	if err := s.require(ProviderSessionCapabilityInterruptThenReplace); err != nil {
		return ProviderSessionResult{}, err
	}
	fingerprint := providerSessionRequestFingerprint(request)
	if err := s.bindRequest(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint); err != nil {
		return ProviderSessionResult{}, err
	}
	// A completed replacement is returned exactly once for retries of the
	// public request id. Internal request ids are deterministic as well, so a
	// bridge retry never writes a second native frame.
	if cached, ok, err := s.cached(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint); err != nil {
		return ProviderSessionResult{}, err
	} else if ok {
		return cached.result, cached.err
	}
	if err := s.begin(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint); err != nil {
		return ProviderSessionResult{}, err
	}
	s.recordProviderSessionTurnKind(request.RequestID, "", request.TurnKind)
	s.recordProviderSessionTurnLineage(request, "")
	defer s.end(request.RequestID, ProviderSessionModeInterruptThenReplace)

	s.emit(emit, ProviderSessionModeInterruptThenReplace, "requested", request.RequestID, s.currentTurn())
	// When the session is idle (no active provider turn), there is nothing to
	// interrupt. Claude Code completes turns and clears the active turn; a later
	// operator message must SendTurn on the same Query, not call interrupt with a
	// stale finished turn id ("Claude active turn identity mismatch").
	if activeTurn := strings.TrimSpace(s.currentTurn()); activeTurn != "" {
		settlementSession, settlementTurn, baseline := s.settlementTarget(request)
		interruptResult, err := s.native(ctx, ProviderSessionModeInterruptThenReplace, request, s.methods.interrupt, request.RequestID+":interrupt")
		if err != nil {
			s.storeFailure(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint, err)
			s.emit(emit, ProviderSessionModeInterruptThenReplace, "failed", request.RequestID, s.currentTurn())
			return ProviderSessionResult{}, err
		}
		s.emit(emit, ProviderSessionModeInterruptThenReplace, "acknowledged", request.RequestID, interruptResult.ProviderTurnID)
		if settlementSession == "" {
			settlementSession = interruptResult.SessionID
		}
		if settlementTurn == "" {
			settlementTurn = interruptResult.ProviderTurnID
		}
		if err := s.waitForSettlement(ctx, ProviderSessionModeInterruptThenReplace, settlementSession, settlementTurn, baseline); err != nil {
			s.storeFailure(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint, err)
			s.emit(emit, ProviderSessionModeInterruptThenReplace, "failed", request.RequestID, settlementTurn)
			return ProviderSessionResult{}, err
		}
		s.emit(emit, ProviderSessionModeInterruptThenReplace, "settled", request.RequestID, settlementTurn)
	} else {
		s.emit(emit, ProviderSessionModeInterruptThenReplace, "settled", request.RequestID, "")
	}

	replacement, err := s.native(ctx, ProviderSessionModeInterruptThenReplace, request, s.methods.send, request.RequestID+":replace")
	if err != nil {
		s.storeFailure(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint, err)
		s.emit(emit, ProviderSessionModeInterruptThenReplace, "failed", request.RequestID, s.currentTurn())
		return ProviderSessionResult{}, err
	}
	s.recordProviderSessionTurnKind(request.RequestID, replacement.ProviderTurnID, request.TurnKind)
	s.recordProviderSessionTurnLineage(request, replacement.ProviderTurnID)
	s.emit(emit, ProviderSessionModeInterruptThenReplace, "started", request.RequestID, replacement.ProviderTurnID)
	replacement.Mode = ProviderSessionModeInterruptThenReplace
	replacement.Outcome = "started"
	s.store(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint, replacement, nil)
	return replacement, nil
}

func (s *providerSessionAdapter) SteerInTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.run(ctx, ProviderSessionModeInTurnSteer, ProviderSessionCapabilityInTurnSteer, request, emit, s.methods.steer)
}

func (s *providerSessionAdapter) RespondPermission(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.run(ctx, ProviderSessionModePermissionResponse, ProviderSessionCapabilityPermissionResponse, request, emit, s.methods.permission)
}

func (s *providerSessionAdapter) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.active {
		s.mu.Unlock()
		return ErrProviderSessionControlConflict
	}
	s.closed = true
	transport := s.transport
	s.mu.Unlock()
	if transport == nil {
		return nil
	}
	return transport.Close(ctx)
}

// ControlBusy reports whether a native control operation is in flight.
func (s *providerSessionAdapter) ControlBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// SessionClosed reports whether Close has completed on this handle.
func (s *providerSessionAdapter) SessionClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// SessionOffline reports confirmed offline health from the current process or
// protocol terminal signal, never from stored session identity or elapsed time.
func (s *providerSessionAdapter) SessionOffline() bool {
	s.mu.Lock()
	closed := s.closed
	transport := s.transport
	s.mu.Unlock()
	if closed {
		return true
	}
	if source, ok := transport.(interface{ Terminated() <-chan struct{} }); ok {
		select {
		case <-source.Terminated():
			return true
		default:
		}
	}
	if source, ok := transport.(interface{ Closed() <-chan struct{} }); ok {
		select {
		case <-source.Closed():
			return true
		default:
		}
	}
	return false
}

// SessionUnexpectedOffline is true only when the process/protocol ended without
// an explicit Close. Operator Stop must not be treated as unexpected exit.
func (s *providerSessionAdapter) SessionUnexpectedOffline() bool {
	s.mu.Lock()
	closed := s.closed
	transport := s.transport
	s.mu.Unlock()
	if closed {
		return false
	}
	if source, ok := transport.(interface{ Terminated() <-chan struct{} }); ok {
		select {
		case <-source.Terminated():
			return true
		default:
		}
	}
	return false
}

func (s *providerSessionAdapter) run(ctx context.Context, mode ProviderSessionMode, capability ProviderSessionCapability, request ProviderSessionRequest, emit ProviderSessionEmit, method string) (ProviderSessionResult, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.TurnKind = normalizeRuntimeTurnKind(request.TurnKind)
	if request.RequestID == "" {
		return ProviderSessionResult{}, ErrInvalidProviderSessionRequest
	}
	if err := s.require(capability); err != nil {
		s.emit(emit, mode, "unsupported", request.RequestID, s.currentTurn())
		return ProviderSessionResult{}, err
	}
	fingerprint := providerSessionRequestFingerprint(request)
	if err := s.bindRequest(request.RequestID, mode, fingerprint); err != nil {
		return ProviderSessionResult{}, err
	}
	if cached, ok, err := s.cached(request.RequestID, mode, fingerprint); err != nil {
		return ProviderSessionResult{}, err
	} else if ok {
		return cached.result, cached.err
	}
	if err := s.begin(request.RequestID, mode, fingerprint); err != nil {
		return ProviderSessionResult{}, err
	}
	s.recordProviderSessionTurnKind(request.RequestID, "", request.TurnKind)
	s.recordProviderSessionTurnLineage(request, "")
	defer s.end(request.RequestID, mode)
	s.emit(emit, mode, "requested", request.RequestID, s.currentTurn())
	settlementSession, settlementTurn, baseline := s.settlementTarget(request)
	result, err := s.native(ctx, mode, request, method, request.RequestID)
	if err != nil {
		s.storeFailure(request.RequestID, mode, fingerprint, err)
		s.emit(emit, mode, "failed", request.RequestID, s.currentTurn())
		return ProviderSessionResult{}, err
	}
	s.recordProviderSessionTurnKind(request.RequestID, result.ProviderTurnID, request.TurnKind)
	s.recordProviderSessionTurnLineage(request, result.ProviderTurnID)
	outcome := "acknowledged"
	if mode == ProviderSessionModeSendTurn {
		outcome = "started"
		s.emit(emit, mode, "started", request.RequestID, result.ProviderTurnID)
	} else if mode == ProviderSessionModeInterruptTurn {
		s.emit(emit, mode, "acknowledged", request.RequestID, result.ProviderTurnID)
		if settlementSession == "" {
			settlementSession = result.SessionID
		}
		if settlementTurn == "" {
			settlementTurn = result.ProviderTurnID
		}
		if err := s.waitForSettlement(ctx, mode, settlementSession, settlementTurn, baseline); err != nil {
			s.storeFailure(request.RequestID, mode, fingerprint, err)
			s.emit(emit, mode, "failed", request.RequestID, settlementTurn)
			return ProviderSessionResult{}, err
		}
		outcome = "settled"
		s.emit(emit, mode, "settled", request.RequestID, result.ProviderTurnID)
	} else {
		s.emit(emit, mode, "acknowledged", request.RequestID, result.ProviderTurnID)
	}
	result.Mode, result.Outcome = mode, outcome
	s.store(request.RequestID, mode, fingerprint, result, nil)
	return result, nil
}

func (s *providerSessionAdapter) native(ctx context.Context, mode ProviderSessionMode, request ProviderSessionRequest, method, wireID string) (ProviderSessionResult, error) {
	if strings.TrimSpace(method) == "" {
		return ProviderSessionResult{}, &UnsupportedProviderSessionCapabilityError{Capability: ProviderSessionCapabilityInTurnSteer}
	}
	s.mu.Lock()
	transport, sessionID, activeTurnID := s.transport, s.sessionID, s.activeTurnID
	s.mu.Unlock()
	if transport == nil {
		return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: errors.New("provider session transport is required")}
	}
	// Selection setup runs only for the primary send path (SendTurn and the
	// replacement half of InterruptThenReplace), never for interrupt itself.
	if method == s.methods.send && s.methods.prepareSend != nil {
		if err := s.methods.prepareSend(ctx, transport, wireID, sessionID, activeTurnID, request); err != nil {
			return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: err}
		}
	}
	params := s.methods.params(sessionID, activeTurnID, request)
	if mode == ProviderSessionModeInterruptTurn || mode == ProviderSessionModeInterruptThenReplace && strings.HasSuffix(wireID, ":interrupt") {
		if request.ProviderTurnID != "" {
			params["turnId"] = request.ProviderTurnID
			params["turn_id"] = request.ProviderTurnID
		}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: err}
	}
	response, err := transport.Send(ctx, SandboxBridgeRequest{ID: wireID, Method: method, Params: encoded})
	if err != nil {
		return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: err}
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: &SandboxBridgeRPCError{RequestID: wireID}}
	}
	metadata := map[string]any{}
	if len(response.Result) > 0 {
		_ = json.Unmarshal(response.Result, &metadata)
	}
	turnID := s.methods.turnID(metadata)
	if turnID == "" {
		turnID = strings.TrimSpace(request.ProviderTurnID)
	}
	if turnID == "" {
		turnID = activeTurnID
	}
	newSessionID := s.methods.sessionID(metadata)
	if newSessionID == "" {
		newSessionID = sessionID
	}
	s.mu.Lock()
	s.sessionID, s.activeTurnID = newSessionID, turnID
	s.mu.Unlock()
	return ProviderSessionResult{RequestID: request.RequestID, SessionID: newSessionID, ProviderTurnID: turnID, Mode: mode, Outcome: "acknowledged"}, nil
}

func providerTurnSettled(metadata map[string]any) bool {
	status := strings.ToLower(providerJSONValue(metadata, "status", "turn_status", "turnStatus"))
	if status == "" {
		if turn, ok := metadata["turn"].(map[string]any); ok {
			status = strings.ToLower(providerJSONValue(turn, "status", "turn_status", "turnStatus"))
		}
	}
	switch status {
	case "aborted", "cancelled", "canceled", "completed", "failed", "interrupted", "rejected", "stopped":
		return true
	default:
		return false
	}
}

func (s *providerSessionAdapter) settlementTarget(request ProviderSessionRequest) (sessionID, turnID string, baseline uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID, turnID = s.sessionID, strings.TrimSpace(request.ProviderTurnID)
	if turnID == "" {
		turnID = s.activeTurnID
	}
	baseline = s.settlementSeq
	return
}

func providerSettlementKey(sessionID, turnID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(turnID)
}

func (s *providerSessionAdapter) waitForSettlement(ctx context.Context, mode ProviderSessionMode, sessionID, turnID string, baseline uint64) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(turnID) == "" {
		return &ProviderSessionOperationError{Mode: mode, Cause: errors.New("provider settlement correlation is incomplete")}
	}
	key := providerSettlementKey(sessionID, turnID)
	for {
		s.mu.Lock()
		if settlement, ok := s.settlements[key]; ok && settlement.seq > baseline {
			delete(s.settlements, key)
			s.mu.Unlock()
			return nil
		}
		changed := s.settlementChanged
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return &ProviderSessionOperationError{Mode: mode, Cause: ctx.Err()}
		case <-changed:
		}
	}
}

func (s *providerSessionAdapter) require(capability ProviderSessionCapability) error {
	s.mu.Lock()
	closed := s.closed
	capabilities := s.capabilities
	s.mu.Unlock()
	if closed {
		return ErrProviderSessionClosed
	}
	if !hasProviderSessionCapability(capabilities, capability) {
		return &UnsupportedProviderSessionCapabilityError{Capability: capability}
	}
	return nil
}

func (s *providerSessionAdapter) begin(requestID string, mode ProviderSessionMode, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrProviderSessionClosed
	}
	if s.active {
		if s.activeRequestID == requestID && s.activeMode == mode && s.activeFingerprint != fingerprint {
			return &ProviderSessionRequestConflictError{RequestID: requestID}
		}
		return ErrProviderSessionControlConflict
	}
	s.active = true
	s.activeRequestID = requestID
	s.activeMode = mode
	s.activeFingerprint = fingerprint
	return nil
}

func (s *providerSessionAdapter) bindRequest(requestID string, mode ProviderSessionMode, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.requests[requestID]; ok {
		if prior.mode != mode || prior.fingerprint != fingerprint {
			return &ProviderSessionRequestConflictError{RequestID: requestID}
		}
		return nil
	}
	s.requests[requestID] = providerSessionRequestIdentity{mode: mode, fingerprint: fingerprint}
	return nil
}

func (s *providerSessionAdapter) end(_ string, _ ProviderSessionMode) {
	s.mu.Lock()
	s.active = false
	s.activeRequestID = ""
	s.activeMode = ""
	s.activeFingerprint = ""
	s.mu.Unlock()
}

func (s *providerSessionAdapter) cached(requestID string, mode ProviderSessionMode, fingerprint string) (providerSessionCallResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.calls[requestID+"\x00"+string(mode)]
	if ok && result.fingerprint != fingerprint {
		return providerSessionCallResult{}, false, &ProviderSessionRequestConflictError{RequestID: requestID}
	}
	return result, ok, nil
}

func (s *providerSessionAdapter) store(requestID string, mode ProviderSessionMode, fingerprint string, result ProviderSessionResult, err error) {
	s.mu.Lock()
	s.calls[requestID+"\x00"+string(mode)] = providerSessionCallResult{fingerprint: fingerprint, result: result, err: err}
	s.mu.Unlock()
}

func (s *providerSessionAdapter) storeFailure(requestID string, mode ProviderSessionMode, fingerprint string, err error) {
	if providerSessionContextError(err) {
		return
	}
	s.store(requestID, mode, fingerprint, ProviderSessionResult{}, err)
}

func providerSessionRequestFingerprint(request ProviderSessionRequest) string {
	encoded, _ := json.Marshal(struct {
		Message                  string `json:"message"`
		ProviderTurnID           string `json:"provider_turn_id"`
		PermissionRequestID      string `json:"permission_request_id"`
		PermissionDecision       string `json:"permission_decision"`
		ModelProviderID          string `json:"model_provider_id"`
		Model                    string `json:"model"`
		RequestedReasoningEffort string `json:"requested_reasoning_effort"`
		TurnKind                 string `json:"turn_kind"`
	}{
		Message:                  request.Message,
		ProviderTurnID:           request.ProviderTurnID,
		PermissionRequestID:      request.PermissionRequestID,
		PermissionDecision:       request.PermissionDecision,
		ModelProviderID:          request.ModelProviderID,
		Model:                    request.Model,
		RequestedReasoningEffort: request.RequestedReasoningEffort,
		TurnKind:                 string(normalizeRuntimeTurnKind(request.TurnKind)),
	})
	return string(encoded)
}

func (s *providerSessionAdapter) currentTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeTurnID
}

func (s *providerSessionAdapter) emit(emit ProviderSessionEmit, mode ProviderSessionMode, outcome, requestID, turnID string) {
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	kind := task.EventKindLifecycle
	if mode == ProviderSessionModeInterruptTurn || mode == ProviderSessionModeInterruptThenReplace || mode == ProviderSessionModeInTurnSteer {
		kind = task.EventKindSteering
	}
	if strings.TrimSpace(turnID) == "" {
		turnID = s.currentTurn()
	}
	if emit != nil {
		emit(kind, task.EventPayload{"provider": s.provider, "request_id": requestID, "session_id": s.SessionID(), "provider_turn_id": turnID, "mode": string(mode), "outcome": outcome})
	}
}

// HandleEvent maps provider notifications to the normalized lifecycle channel.
// Providers use different event names, but all first-release adapters expose
// the same started/completed/interrupted/permission state vocabulary.
func (s *providerSessionAdapter) HandleEvent(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	if strings.TrimSpace(event.Method) == "" {
		return
	}
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	params := map[string]any{}
	if len(event.Params) > 0 && json.Unmarshal(event.Params, &params) != nil {
		return
	}
	method := strings.ToLower(event.Method)
	if method == "claude/runtime_output" {
		text := providerJSONValue(params, "text")
		if text == "" {
			return
		}
		sessionID := providerJSONValue(params, "session_id", "sessionId")
		if sessionID == "" {
			sessionID = s.SessionID()
		}
		turnID := providerJSONValue(params, "turn_id", "turnId")
		if turnID == "" {
			turnID = s.currentTurn()
		}
		if emit != nil {
			emit(task.EventKindRuntimeOutput, task.EventPayload{
				"provider": s.provider, "provider_event": event.Method,
				"session_id": sessionID, "provider_turn_id": turnID,
				"stream": providerJSONValue(params, "stream"), "text": text,
			})
		}
		return
	}
	mode := ProviderSessionModeSendTurn
	outcome := ""
	switch {
	case strings.Contains(method, "permission") || strings.Contains(method, "extension_ui"):
		mode, outcome = ProviderSessionModePermissionResponse, "requested"
	case strings.Contains(method, "interrupt") || strings.Contains(method, "abort") || strings.Contains(method, "cancel"):
		mode, outcome = ProviderSessionModeInterruptTurn, "settled"
	case strings.Contains(method, "agent_end") || strings.Contains(method, "turn_end") || strings.HasSuffix(method, "/end"):
		mode, outcome = ProviderSessionModeSendTurn, "completed"
	case strings.Contains(method, "completed") || strings.Contains(method, "complete"):
		mode, outcome = ProviderSessionModeSendTurn, "completed"
	case strings.Contains(method, "started") || strings.Contains(method, "start"):
		mode, outcome = ProviderSessionModeSendTurn, "started"
	case strings.Contains(method, "failed") || strings.Contains(method, "error") || strings.Contains(method, "rejected"):
		mode, outcome = ProviderSessionModeSendTurn, "failed"
	default:
		return
	}
	turnID := s.methods.turnID(params)
	sessionID := s.methods.sessionID(params)
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	if turnID == "" {
		turnID = s.currentTurn()
	}
	terminal := outcome == "settled" || outcome == "completed" || outcome == "failed" || providerTurnSettled(params)
	s.mu.Lock()
	currentSession := s.sessionID
	currentTurn := s.activeTurnID
	if currentSession == "" || currentSession == sessionID {
		s.sessionID = sessionID
	}
	// Capture settlement against the pre-clear active turn so interrupt waits
	// still release when the terminal notification arrives.
	interruptActive := s.active && (s.activeMode == ProviderSessionModeInterruptTurn || s.activeMode == ProviderSessionModeInterruptThenReplace)
	matchingSession := currentSession == "" || currentSession == sessionID
	matchingTurn := currentTurn == "" || currentTurn == turnID
	if terminal && interruptActive && matchingSession && matchingTurn && sessionID != "" && turnID != "" {
		s.settlementSeq++
		s.settlements[providerSettlementKey(sessionID, turnID)] = providerSettlement{seq: s.settlementSeq}
		close(s.settlementChanged)
		s.settlementChanged = make(chan struct{})
	}
	if currentSession == "" || currentSession == sessionID {
		if terminal {
			// A finished turn is no longer active. Keeping the completed id made
			// idle interrupt_then_replace call interrupt with a stale turn and
			// fail Claude ("active turn identity mismatch").
			if currentTurn == "" || currentTurn == turnID {
				s.activeTurnID = ""
			}
		} else if strings.TrimSpace(turnID) != "" {
			s.activeTurnID = turnID
		}
	}
	s.mu.Unlock()
	requestID := providerJSONValue(params, "request_id", "requestId", "control_id", "controlId")
	kind := task.EventKindLifecycle
	if mode == ProviderSessionModeInterruptTurn {
		kind = task.EventKindSteering
	}
	payload := task.EventPayload{
		"provider": s.provider, "provider_event": event.Method, "request_id": requestID,
		"session_id": sessionID, "provider_turn_id": turnID, "mode": string(mode), "outcome": outcome,
	}
	if mode == ProviderSessionModePermissionResponse {
		payload["permission_request_id"] = providerJSONValue(params, "permission_request_id", "permissionRequestId", "permission_id", "permissionId")
		payload["phase"] = "provider_permission_requested"
	}
	if emit != nil {
		emit(kind, payload)
	}
}

func defaultProviderCapabilities() runtimeplugin.Capabilities {
	return runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true}
}

func providerCapabilities(value runtimeplugin.Capabilities) runtimeplugin.Capabilities {
	if value.PersistentSession || value.SendTurn || value.InterruptTurn || value.InterruptThenReplace || value.InTurnSteer || value.PermissionResponse || value.ResumeSession {
		return value
	}
	return defaultProviderCapabilities()
}

func providerJSONValue(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nestedTurnID(record map[string]any) string {
	if turn, ok := record["turn"].(map[string]any); ok {
		return providerJSONValue(turn, "id", "turnId", "turn_id")
	}
	return providerJSONValue(record, "turnId", "turn_id", "id")
}

func identitySession(record map[string]any) string {
	return providerJSONValue(record, "sessionId", "session_id", "threadId", "thread_id")
}

func providerParams(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
	params := map[string]any{"session_id": sessionID, "turn_id": turnID, "message": request.Message}
	if request.PermissionRequestID != "" {
		params["permission_request_id"] = request.PermissionRequestID
		params["permissionRequestId"] = request.PermissionRequestID
	}
	if request.PermissionDecision != "" {
		params["permission_decision"] = request.PermissionDecision
		params["decision"] = request.PermissionDecision
	}
	return params
}

// CodexProviderSessionConfig configures one Task-owned Codex App Server
// session. ThreadID is the provider's durable session identity.
type CodexProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ThreadID     string
	ActiveTurnID string
	Capabilities runtimeplugin.Capabilities
}

type CodexProviderSession struct {
	*providerSessionAdapter
	assisted *codexAssistedSession
}

func NewCodexProviderSession(config CodexProviderSessionConfig) *CodexProviderSession {
	threadID := strings.TrimSpace(config.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(config.SessionID)
	}
	methods := providerWireMethods{
		send: "turn/start", interrupt: "turn/interrupt", permission: "item/permission/respond",
		params: func(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
			params := map[string]any{
				"threadId": sessionID,
				"turnId":   turnID,
				"input": []any{map[string]any{
					"type": "text",
					"text": request.Message,
				}},
			}
			if model := strings.TrimSpace(request.Model); model != "" {
				params["model"] = model
			}
			// Codex App Server accepts effort on turn/start. CyberPenda always
			// sends the resolved Requested Reasoning Effort explicitly.
			if effort := strings.TrimSpace(request.RequestedReasoningEffort); effort != "" {
				params["effort"] = effort
			}
			if request.PermissionRequestID != "" {
				params["permissionRequestId"] = request.PermissionRequestID
			}
			if request.PermissionDecision != "" {
				params["decision"] = request.PermissionDecision
			}
			// Persistent App Server does not inherit the exec-only
			// --dangerously-bypass-approvals-and-sandbox flag.
			params["approvalPolicy"] = "never"
			params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
			return params
		},
		turnID: nestedTurnID, sessionID: func(record map[string]any) string {
			return providerJSONValue(record, "threadId", "thread_id", "sessionId", "session_id")
		},
	}
	capabilities := providerCapabilities(config.Capabilities)
	if !capabilities.PersistentSession || !capabilities.SendTurn {
		capabilities.AssistedConclusion = false
	}
	return &CodexProviderSession{
		providerSessionAdapter: newProviderSessionAdapter("codex", config.Transport, threadID, config.ActiveTurnID, capabilities, methods),
		assisted:               newCodexAssistedSession(),
	}
}

// ClaudeCodeProviderSessionConfig configures one long-lived Claude Code SDK
// query exposed by the sandbox bridge.
type ClaudeCodeProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ActiveTurnID string
	Capabilities runtimeplugin.Capabilities
}

type ClaudeCodeProviderSession struct {
	*providerSessionAdapter
	assisted *claudeAssistedSession
}

func NewClaudeCodeProviderSession(config ClaudeCodeProviderSessionConfig) *ClaudeCodeProviderSession {
	methods := providerWireMethods{
		send: "claude/input", interrupt: "claude/interrupt", permission: "claude/permission/respond",
		params: claudeCodeParams, turnID: func(record map[string]any) string { return providerJSONValue(record, "turn_id", "turnId", "id") }, sessionID: identitySession,
	}
	capabilities := providerCapabilities(config.Capabilities)
	if !capabilities.PersistentSession || !capabilities.SendTurn {
		capabilities.AssistedConclusion = false
	}
	return &ClaudeCodeProviderSession{
		providerSessionAdapter: newProviderSessionAdapter("claude_code", config.Transport, config.SessionID, config.ActiveTurnID, capabilities, methods),
		assisted:               newClaudeAssistedSession(),
	}
}

// claudeCodeParams maps the complete Runtime Turn Selection onto claude/input.
// The long-lived Claude Query applies model and Requested Reasoning Effort
// before the turn; model_provider_id is delivered for wire completeness but a
// provider change still restarts through Config Projection.
func claudeCodeParams(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
	params := providerParams(sessionID, turnID, request)
	params["turn_kind"] = string(normalizeRuntimeTurnKind(request.TurnKind))
	if providerID := strings.TrimSpace(request.ModelProviderID); providerID != "" {
		params["model_provider_id"] = providerID
	}
	if model := strings.TrimSpace(request.Model); model != "" {
		params["model"] = model
	}
	if effort := strings.TrimSpace(request.RequestedReasoningEffort); effort != "" {
		params["requested_reasoning_effort"] = effort
	}
	return params
}

// PiProviderSessionConfig configures one long-lived Pi RPC child.
type PiProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ActiveTurnID string
	Capabilities runtimeplugin.Capabilities
}

type PiProviderSession struct{ *providerSessionAdapter }

// HermesProviderSessionConfig configures one Task-owned Hermes ACP process.
type HermesProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ActiveTurnID string
	// HermesHome is the projected HERMES_HOME. Per-turn Requested Reasoning
	// Effort is written here so ACP can apply it before session/prompt.
	HermesHome   string
	Capabilities runtimeplugin.Capabilities
}

type HermesProviderSession struct{ *providerSessionAdapter }

func NewHermesProviderSession(config HermesProviderSessionConfig) *HermesProviderSession {
	methods := providerWireMethods{
		send:      "session/prompt",
		interrupt: "session/cancel",
		params:    hermesACPParams,
		prepareSend: func(ctx context.Context, transport ProviderSessionTransport, wireBaseID, sessionID, turnID string, request ProviderSessionRequest) error {
			return hermesPrepareSendSelection(ctx, transport, wireBaseID, sessionID, turnID, config.HermesHome, request)
		},
		turnID:    func(record map[string]any) string { return providerJSONValue(record, "turn_id", "turnId", "id") },
		sessionID: identitySession,
	}
	return &HermesProviderSession{newProviderSessionAdapter("hermes", config.Transport, config.SessionID, config.ActiveTurnID, providerCapabilities(config.Capabilities), methods)}
}

func hermesACPParams(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
	return map[string]any{
		"sessionId":  sessionID,
		"session_id": sessionID,
		"turn_id":    turnID,
		"prompt":     []map[string]any{{"type": "text", "text": request.Message}},
	}
}

// hermesPrepareSendSelection applies Runtime Turn Selection through ACP
// session/set_model. session/prompt has no model field; extra keys are ignored.
// Named Model Providers use custom:<id>:<model> so Hermes does not auto-switch
// to a built-in MiniMax/OpenRouter route.
func hermesPrepareSendSelection(ctx context.Context, transport ProviderSessionTransport, wireBaseID, sessionID, turnID, hermesHome string, request ProviderSessionRequest) error {
	if transport == nil {
		return errors.New("provider session transport is required")
	}
	if err := writeHermesRequestedReasoningEffort(hermesHome, request.RequestedReasoningEffort); err != nil {
		return err
	}
	modelID := hermesACPModelID(request.ModelProviderID, request.Model)
	if modelID == "" {
		return nil
	}
	params := map[string]any{
		"sessionId":  sessionID,
		"session_id": sessionID,
		"modelId":    modelID,
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	response, err := transport.Send(ctx, SandboxBridgeRequest{
		ID: wireBaseID + ":set_model", Method: "session/set_model", Params: encoded,
	})
	if err != nil {
		return err
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return &SandboxBridgeRPCError{RequestID: wireBaseID + ":set_model"}
	}
	return nil
}

func writeHermesRequestedReasoningEffort(hermesHome, effort string) error {
	home := strings.TrimSpace(hermesHome)
	effort = strings.TrimSpace(effort)
	if home == "" || effort == "" {
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "cyberpenda-requested-reasoning-effort"), []byte(effort+"\n"), 0o600)
}

func hermesACPModelID(providerID, model string) string {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if providerID == "" || strings.HasPrefix(model, "custom:") {
		return model
	}
	if strings.HasPrefix(providerID, "custom:") {
		return providerID + ":" + model
	}
	return "custom:" + providerID + ":" + model
}

func NewPiProviderSession(config PiProviderSessionConfig) *PiProviderSession {
	methods := providerWireMethods{
		send: "pi/prompt", interrupt: "pi/abort", steer: "pi/steer", permission: "pi/permission/respond",
		params: providerParams,
		// Pi applies Runtime Turn Selection through discrete RPC commands.
		// Order is mandatory: model → thinking level → prompt, because
		// set_model can reset thinking level on the provider side.
		prepareSend: piPrepareSendSelection,
		turnID:      func(record map[string]any) string { return providerJSONValue(record, "turn_id", "turnId", "id") },
		sessionID:   identitySession,
	}
	return &PiProviderSession{newProviderSessionAdapter("pi", config.Transport, config.SessionID, config.ActiveTurnID, providerCapabilities(config.Capabilities), methods)}
}

// piPrepareSendSelection issues set_model then set_thinking_level before prompt
// when Model / ModelProviderID / RequestedReasoningEffort are present. Effort
// vocabulary is passed through 1:1; unsupported values surface as provider errors.
func piPrepareSendSelection(ctx context.Context, transport ProviderSessionTransport, wireBaseID, sessionID, turnID string, request ProviderSessionRequest) error {
	if transport == nil {
		return errors.New("provider session transport is required")
	}
	providerID := strings.TrimSpace(request.ModelProviderID)
	model := strings.TrimSpace(request.Model)
	effort := strings.TrimSpace(request.RequestedReasoningEffort)

	if providerID != "" || model != "" {
		params := map[string]any{"session_id": sessionID, "turn_id": turnID}
		if providerID != "" {
			params["provider"] = providerID
		}
		if model != "" {
			params["modelId"] = model
			params["model"] = model
		}
		encoded, err := json.Marshal(params)
		if err != nil {
			return err
		}
		response, err := transport.Send(ctx, SandboxBridgeRequest{
			ID: wireBaseID + ":set_model", Method: "pi/set_model", Params: encoded,
		})
		if err != nil {
			return err
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return &SandboxBridgeRPCError{RequestID: wireBaseID + ":set_model"}
		}
	}
	if effort != "" {
		// Do not rewrite or downgrade client vocabulary; Pi rejects unsupported levels.
		params := map[string]any{
			"session_id": sessionID,
			"turn_id":    turnID,
			"level":      effort,
		}
		encoded, err := json.Marshal(params)
		if err != nil {
			return err
		}
		response, err := transport.Send(ctx, SandboxBridgeRequest{
			ID: wireBaseID + ":set_thinking_level", Method: "pi/set_thinking_level", Params: encoded,
		})
		if err != nil {
			return err
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return &SandboxBridgeRPCError{RequestID: wireBaseID + ":set_thinking_level"}
		}
	}
	return nil
}

var (
	_ ProviderSession = (*CodexProviderSession)(nil)
	_ ProviderSession = (*ClaudeCodeProviderSession)(nil)
	_ ProviderSession = (*PiProviderSession)(nil)
)
