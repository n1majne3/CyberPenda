package runtime

import (
	"encoding/json"
	"strings"

	"pentest/internal/task"
)

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

type providerSessionBeginSnapshot struct {
	activeTurnID  string
	sessionID     string
	targetTurnID  string
	settlementSeq uint64
}

func (s *providerSessionAdapter) begin(request ProviderSessionRequest, mode ProviderSessionMode, fingerprint string) (providerSessionBeginSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return providerSessionBeginSnapshot{}, ErrProviderSessionClosed
	}
	if s.active {
		if s.activeRequestID == request.RequestID && s.activeMode == mode && s.activeFingerprint != fingerprint {
			return providerSessionBeginSnapshot{}, &ProviderSessionRequestConflictError{RequestID: request.RequestID}
		}
		return providerSessionBeginSnapshot{}, ErrProviderSessionControlConflict
	}
	snapshot := providerSessionBeginSnapshot{
		activeTurnID:  s.activeTurnID,
		sessionID:     s.sessionID,
		targetTurnID:  strings.TrimSpace(request.ProviderTurnID),
		settlementSeq: s.settlementSeq,
	}
	if snapshot.targetTurnID == "" {
		snapshot.targetTurnID = snapshot.activeTurnID
	}
	s.active = true
	s.activeRequestID = request.RequestID
	s.activeMode = mode
	s.activeFingerprint = fingerprint
	return snapshot, nil
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
	kind := task.EventKindLifecycle
	if mode == ProviderSessionModeInterruptTurn || mode == ProviderSessionModeInterruptThenReplace || mode == ProviderSessionModeInTurnSteer {
		kind = task.EventKindSteering
	}
	if strings.TrimSpace(turnID) == "" {
		turnID = s.currentTurn()
	}
	s.forwardEvent(emit, kind, task.EventPayload{"provider": s.provider, "request_id": requestID, "session_id": s.SessionID(), "provider_turn_id": turnID, "mode": string(mode), "outcome": outcome})
}
