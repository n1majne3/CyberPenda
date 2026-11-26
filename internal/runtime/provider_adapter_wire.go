package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

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
	snapshot, err := s.begin(request, mode, fingerprint)
	if err != nil {
		return ProviderSessionResult{}, err
	}
	s.recordProviderSessionTurnKind(request.RequestID, "", request.TurnKind)
	s.recordProviderSessionTurnLineage(request, "")
	defer s.end(request.RequestID, mode)
	requestedTurn := s.currentTurn()
	if mode == ProviderSessionModeInterruptTurn {
		requestedTurn = snapshot.activeTurnID
	}
	s.emit(emit, mode, "requested", request.RequestID, requestedTurn)
	settlementSession, settlementTurn, baseline := snapshot.sessionID, snapshot.targetTurnID, snapshot.settlementSeq
	result, err := s.native(ctx, mode, request, method, request.RequestID)
	if err != nil {
		s.storeFailure(request.RequestID, mode, fingerprint, err)
		s.emit(emit, mode, "failed", request.RequestID, s.currentTurn())
		return ProviderSessionResult{}, err
	}
	// Same-turn steering correlates a control request with an existing provider
	// Work Turn. It must not reclassify or replace that Turn's original lineage.
	if mode != ProviderSessionModeInTurnSteer {
		s.recordProviderSessionTurnKind(request.RequestID, result.ProviderTurnID, request.TurnKind)
		s.recordProviderSessionTurnLineage(request, result.ProviderTurnID)
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
	// Selection setup runs only for the primary send path (SendTurn and the
	// replacement half of InterruptThenReplace), never for interrupt itself.
	if method == s.methods.send && s.methods.prepareSend != nil {
		if err := s.methods.prepareSend(ctx, transport, wireID, sessionID, activeTurnID, request); err != nil {
			return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: err}
		}
	}
	paramsBuilder := s.methods.params
	if mode == ProviderSessionModeInTurnSteer && s.methods.steerParams != nil {
		paramsBuilder = s.methods.steerParams
	}
	params := paramsBuilder(sessionID, activeTurnID, request)
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
	startGeneration := uint64(0)
	if method == s.methods.send && mode != ProviderSessionModeInTurnSteer {
		s.mu.Lock()
		s.startGeneration++
		startGeneration = s.startGeneration
		s.pendingStartGeneration = startGeneration
		s.pendingStartTerminalTurns = make(map[string]struct{})
		s.mu.Unlock()
	}
	clearPendingStart := func() {
		if startGeneration == 0 {
			return
		}
		s.mu.Lock()
		if s.pendingStartGeneration == startGeneration {
			s.pendingStartGeneration = 0
			s.pendingStartTerminalTurns = nil
		}
		s.mu.Unlock()
	}
	wireRequest := SandboxBridgeRequest{ID: wireID, Method: method, Params: encoded}

	response, err := transport.Send(ctx, wireRequest)
	if err != nil {
		clearPendingStart()
		return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: err}
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		clearPendingStart()
		rpcErr := sandboxBridgeRPCError(wireID, response.Error)
		if mode == ProviderSessionModeInTurnSteer && errors.Is(rpcErr, ErrProviderMethodUnavailable) {
			s.mu.Lock()
			s.capabilities.InTurnSteer = false
			s.mu.Unlock()
		}
		return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: rpcErr}
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
	if mode == ProviderSessionModeInTurnSteer {
		expectedTurnID := strings.TrimSpace(request.ProviderTurnID)
		if expectedTurnID != "" && turnID != expectedTurnID {
			return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: mode, Cause: &ProviderTurnChangedError{
				ExpectedTurnID: expectedTurnID,
				ActiveTurnID:   turnID,
			}}
		}
	}
	newSessionID := s.methods.sessionID(metadata)
	if newSessionID == "" {
		newSessionID = sessionID
	}
	s.mu.Lock()
	s.sessionID = newSessionID
	if mode != ProviderSessionModeInTurnSteer {
		_, terminal := s.pendingStartTerminalTurns[providerSettlementKey(newSessionID, turnID)]
		if s.pendingStartGeneration == startGeneration {
			s.pendingStartGeneration = 0
			s.pendingStartTerminalTurns = nil
		}
		if terminal {
			s.activeTurnID = ""
		} else {
			s.activeTurnID = turnID
		}
	}
	s.mu.Unlock()
	return ProviderSessionResult{RequestID: request.RequestID, SessionID: newSessionID, ProviderTurnID: turnID, Mode: mode, Outcome: "acknowledged"}, nil
}

func sandboxBridgeRPCError(requestID string, raw json.RawMessage) *SandboxBridgeRPCError {
	kind := SandboxBridgeRPCErrorUnknown
	var envelope struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(raw, &envelope)
	lower := strings.ToLower(string(raw))
	switch {
	case envelope.Code == -32601 || strings.Contains(lower, "method not found"):
		kind = SandboxBridgeRPCErrorMethodUnavailable
	case strings.Contains(lower, "activeturnnotsteerable") || strings.Contains(lower, "active_turn_not_steerable"):
		kind = SandboxBridgeRPCErrorTurnNotSteerable
	case strings.Contains(lower, "no active turn"):
		kind = SandboxBridgeRPCErrorTargetTurnCompleted
	case strings.Contains(lower, "expectedturnid") || strings.Contains(lower, "expected_turn_id") ||
		strings.Contains(lower, "active turn mismatch"):
		kind = SandboxBridgeRPCErrorTargetTurnChanged
	}
	return &SandboxBridgeRPCError{RequestID: requestID, Kind: kind}
}
