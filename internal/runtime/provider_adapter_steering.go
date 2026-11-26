package runtime

import (
	"context"
	"errors"
	"strings"

	"pentest/internal/task"
)

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
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.TurnKind = normalizeRuntimeTurnKind(request.TurnKind)
	fingerprint := providerSessionRequestFingerprint(request)
	if request.RequestID != "" {
		if cached, ok, cacheErr := s.cached(request.RequestID, ProviderSessionModeInTurnSteer, fingerprint); cacheErr != nil {
			return ProviderSessionResult{}, cacheErr
		} else if ok {
			return cached.result, cached.err
		}
	}
	if s.methods.steerRequiresExpectedTurn {
		s.mu.Lock()
		closed, activeTurnID := s.closed, strings.TrimSpace(s.activeTurnID)
		s.mu.Unlock()
		if closed {
			return ProviderSessionResult{}, ErrProviderSessionClosed
		}
		if activeTurnID == "" {
			return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: ProviderSessionModeInTurnSteer, Cause: ErrProviderTurnUnavailable}
		}
		expectedTurnID := strings.TrimSpace(request.ProviderTurnID)
		if expectedTurnID == "" {
			return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: ProviderSessionModeInTurnSteer, Cause: ErrInvalidProviderSessionRequest}
		} else if expectedTurnID != activeTurnID {
			return ProviderSessionResult{}, &ProviderSessionOperationError{Mode: ProviderSessionModeInTurnSteer, Cause: &ProviderTurnChangedError{
				ExpectedTurnID: expectedTurnID,
				ActiveTurnID:   activeTurnID,
			}}
		}
	}
	var deferredFailureKind task.EventKind
	var deferredFailure task.EventPayload
	steerEmit := func(kind task.EventKind, payload task.EventPayload) {
		if payloadOutcomeValue(payload) == "failed" {
			deferredFailureKind, deferredFailure = kind, cloneProviderEventPayload(payload)
			return
		}
		s.forwardEvent(emit, kind, payload)
	}
	result, err := s.run(ctx, ProviderSessionModeInTurnSteer, ProviderSessionCapabilityInTurnSteer, request, steerEmit, s.methods.steer)
	if !errors.Is(err, ErrProviderMethodUnavailable) || !s.Capabilities().InterruptThenReplace {
		if deferredFailure != nil {
			s.forwardEvent(emit, deferredFailureKind, deferredFailure)
		}
		return result, err
	}

	// A JSON-RPC method-not-found response proves that turn/steer did not apply
	// the message. Fall back safely for this same Accepted Steering request. The
	// provider adapter uses a deterministic internal identity because the public
	// request id is already bound to the attempted in-turn operation; projected
	// Events keep the original public identity.
	fallbackRequest := request
	fallbackRequest.RequestID = request.RequestID + ":interrupt-replace-fallback"
	fallbackRequest.TurnKind = RuntimeTurnKindWork
	fallbackEmit := func(kind task.EventKind, payload task.EventPayload) {
		projected := cloneProviderEventPayload(payload)
		projected["request_id"] = request.RequestID
		s.forwardEvent(emit, kind, projected)
	}
	fallback, fallbackErr := s.InterruptThenReplace(ctx, fallbackRequest, fallbackEmit)
	if fallbackErr != nil {
		return ProviderSessionResult{}, fallbackErr
	}
	fallback.RequestID = request.RequestID
	publicFallbackRequest := request
	publicFallbackRequest.TurnKind = RuntimeTurnKindWork
	s.recordProviderSessionTurnKind(request.RequestID, fallback.ProviderTurnID, publicFallbackRequest.TurnKind)
	s.recordProviderSessionTurnLineage(publicFallbackRequest, fallback.ProviderTurnID)
	s.store(request.RequestID, ProviderSessionModeInTurnSteer, fingerprint, fallback, nil)
	return fallback, nil
}

func payloadOutcomeValue(payload task.EventPayload) string {
	value, _ := payload["outcome"].(string)
	return strings.TrimSpace(value)
}

func cloneProviderEventPayload(payload task.EventPayload) task.EventPayload {
	cloned := make(task.EventPayload, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}
