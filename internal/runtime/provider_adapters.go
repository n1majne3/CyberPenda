package runtime

import (
	"context"
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

// ProviderSessionResponseWaiter waits for one already-written provider request.
// The request remains owned by the transport when a caller stops waiting.
type ProviderSessionResponseWaiter interface {
	Wait(context.Context) (SandboxBridgeResponse, error)
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
	// steerParams is separate because provider-native same-turn steering can
	// have a narrower schema than starting a new turn. When nil, params is used.
	steerParams               func(string, string, ProviderSessionRequest) map[string]any
	steerRequiresExpectedTurn bool
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
	mu                        sync.Mutex
	transport                 ProviderSessionTransport
	provider                  string
	methods                   providerWireMethods
	capabilities              runtimeplugin.Capabilities
	sessionID                 string
	activeTurnID              string
	closed                    bool
	active                    bool
	activeRequestID           string
	activeMode                ProviderSessionMode
	activeFingerprint         string
	calls                     map[string]providerSessionCallResult
	requests                  map[string]providerSessionRequestIdentity
	eventSink                 ProviderSessionEmit
	observationSink           ProviderSessionObserve
	requestTurnKind           map[string]RuntimeTurnKind
	providerTurnKind          map[string]RuntimeTurnKind
	requestLineage            map[string]ProviderSessionTurnLineage
	providerLineage           map[string]ProviderSessionTurnLineage
	settlements               map[string]providerSettlement
	startGeneration           uint64
	pendingStartGeneration    uint64
	pendingStartTerminalTurns map[string]struct{}
	settlementSeq             uint64
	settlementChanged         chan struct{}
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

func (s *providerSessionAdapter) Capabilities() runtimeplugin.Capabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capabilities
}

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
func (s *providerSessionAdapter) forwardEvent(emit ProviderSessionEmit, kind task.EventKind, payload task.EventPayload) {
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	if emit != nil {
		emit(kind, payload)
	}
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
	s.activeTurnID = ""
	s.pendingStartGeneration = 0
	s.pendingStartTerminalTurns = nil
	transport := s.transport
	s.mu.Unlock()
	if transport == nil {
		return nil
	}
	return transport.Close(ctx)
}

func (s *providerSessionAdapter) ControlBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *providerSessionAdapter) TurnBusy() bool {
	return s.TurnState().TurnBusy()
}

// TurnState returns one atomic provider session Turn snapshot.
func (s *providerSessionAdapter) TurnState() ProviderSessionTurnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ProviderSessionTurnState{
		SessionID: s.sessionID, ActiveTurnID: s.activeTurnID,
		ActiveTurnKind: s.providerTurnKind[s.activeTurnID], ControlBusy: s.active,
	}
}

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

var (
	_ ProviderSession = (*CodexProviderSession)(nil)
	_ ProviderSession = (*ClaudeCodeProviderSession)(nil)
	_ ProviderSession = (*PiProviderSession)(nil)
)
