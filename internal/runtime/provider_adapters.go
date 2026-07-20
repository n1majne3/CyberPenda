package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
	turnID     func(map[string]any) string
	sessionID  func(map[string]any) string
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

func (s *providerSessionAdapter) SendTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.run(ctx, ProviderSessionModeSendTurn, ProviderSessionCapabilitySendTurn, request, emit, s.methods.send)
}

func (s *providerSessionAdapter) InterruptTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	return s.run(ctx, ProviderSessionModeInterruptTurn, ProviderSessionCapabilityInterruptTurn, request, emit, s.methods.interrupt)
}

func (s *providerSessionAdapter) InterruptThenReplace(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
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
	defer s.end(request.RequestID, ProviderSessionModeInterruptThenReplace)

	s.emit(emit, ProviderSessionModeInterruptThenReplace, "requested", request.RequestID, s.currentTurn())
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

	replacement, err := s.native(ctx, ProviderSessionModeInterruptThenReplace, request, s.methods.send, request.RequestID+":replace")
	if err != nil {
		s.storeFailure(request.RequestID, ProviderSessionModeInterruptThenReplace, fingerprint, err)
		s.emit(emit, ProviderSessionModeInterruptThenReplace, "failed", request.RequestID, s.currentTurn())
		return ProviderSessionResult{}, err
	}
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

func (s *providerSessionAdapter) run(ctx context.Context, mode ProviderSessionMode, capability ProviderSessionCapability, request ProviderSessionRequest, emit ProviderSessionEmit, method string) (ProviderSessionResult, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
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
	defer s.end(request.RequestID, mode)
	s.emit(emit, mode, "requested", request.RequestID, s.currentTurn())
	settlementSession, settlementTurn, baseline := s.settlementTarget(request)
	result, err := s.native(ctx, mode, request, method, request.RequestID)
	if err != nil {
		s.storeFailure(request.RequestID, mode, fingerprint, err)
		s.emit(emit, mode, "failed", request.RequestID, s.currentTurn())
		return ProviderSessionResult{}, err
	}
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
	}{
		Message:                  request.Message,
		ProviderTurnID:           request.ProviderTurnID,
		PermissionRequestID:      request.PermissionRequestID,
		PermissionDecision:       request.PermissionDecision,
		ModelProviderID:          request.ModelProviderID,
		Model:                    request.Model,
		RequestedReasoningEffort: request.RequestedReasoningEffort,
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
		if !terminal || currentTurn == "" || currentTurn == turnID {
			s.activeTurnID = turnID
		}
	}
	interruptActive := s.active && (s.activeMode == ProviderSessionModeInterruptTurn || s.activeMode == ProviderSessionModeInterruptThenReplace)
	matchingSession := currentSession == "" || currentSession == sessionID
	matchingTurn := currentTurn == "" || currentTurn == turnID
	if terminal && interruptActive && matchingSession && matchingTurn && sessionID != "" && turnID != "" {
		s.settlementSeq++
		s.settlements[providerSettlementKey(sessionID, turnID)] = providerSettlement{seq: s.settlementSeq}
		close(s.settlementChanged)
		s.settlementChanged = make(chan struct{})
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

type CodexProviderSession struct{ *providerSessionAdapter }

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
			return params
		},
		turnID: nestedTurnID, sessionID: func(record map[string]any) string {
			return providerJSONValue(record, "threadId", "thread_id", "sessionId", "session_id")
		},
	}
	return &CodexProviderSession{newProviderSessionAdapter("codex", config.Transport, threadID, config.ActiveTurnID, providerCapabilities(config.Capabilities), methods)}
}

// ClaudeCodeProviderSessionConfig configures one long-lived Claude Code SDK
// query exposed by the sandbox bridge.
type ClaudeCodeProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ActiveTurnID string
	Capabilities runtimeplugin.Capabilities
}

type ClaudeCodeProviderSession struct{ *providerSessionAdapter }

func NewClaudeCodeProviderSession(config ClaudeCodeProviderSessionConfig) *ClaudeCodeProviderSession {
	methods := providerWireMethods{
		send: "claude/input", interrupt: "claude/interrupt", permission: "claude/permission/respond",
		params: claudeCodeParams, turnID: func(record map[string]any) string { return providerJSONValue(record, "turn_id", "turnId", "id") }, sessionID: identitySession,
	}
	return &ClaudeCodeProviderSession{newProviderSessionAdapter("claude_code", config.Transport, config.SessionID, config.ActiveTurnID, providerCapabilities(config.Capabilities), methods)}
}

// claudeCodeParams maps the complete Runtime Turn Selection onto claude/input.
// The long-lived Claude Query applies model and Requested Reasoning Effort
// before the turn; model_provider_id is delivered for wire completeness but a
// provider change still restarts through Config Projection.
func claudeCodeParams(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
	params := providerParams(sessionID, turnID, request)
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

func NewPiProviderSession(config PiProviderSessionConfig) *PiProviderSession {
	methods := providerWireMethods{
		send: "pi/prompt", interrupt: "pi/abort", steer: "pi/steer", permission: "pi/permission/respond",
		params: providerParams, turnID: func(record map[string]any) string { return providerJSONValue(record, "turn_id", "turnId", "id") }, sessionID: identitySession,
	}
	return &PiProviderSession{newProviderSessionAdapter("pi", config.Transport, config.SessionID, config.ActiveTurnID, providerCapabilities(config.Capabilities), methods)}
}

var (
	_ ProviderSession = (*CodexProviderSession)(nil)
	_ ProviderSession = (*ClaudeCodeProviderSession)(nil)
	_ ProviderSession = (*PiProviderSession)(nil)
)
