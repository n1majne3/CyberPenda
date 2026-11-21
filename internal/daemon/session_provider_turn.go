package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/task"
)

type sessionProviderTurnPayload struct {
	Message   string
	Selection runtime.ProviderSessionRequest
}

type sessionProviderTurnReservation struct {
	ready chan sessionProviderTurnPayload
	once  sync.Once
}

func (reservation *sessionProviderTurnReservation) commit(message string, selection runtime.ProviderSessionRequest) {
	reservation.once.Do(func() {
		reservation.ready <- sessionProviderTurnPayload{Message: message, Selection: selection}
		close(reservation.ready)
	})
}

func (reservation *sessionProviderTurnReservation) cancel() {
	reservation.once.Do(func() { close(reservation.ready) })
}

func (server *Server) reserveSessionProviderTurn(sessionID string, provider runtime.ProviderSession, continuationID, requestID string, mode runtime.ProviderSessionMode) (*sessionProviderTurnReservation, bool) {
	reservation := &sessionProviderTurnReservation{ready: make(chan sessionProviderTurnPayload, 1)}
	if !server.enqueueSessionProviderTurn(sessionID, provider, continuationID, requestID, mode, reservation.ready) {
		return nil, false
	}
	return reservation, true
}

func (server *Server) enqueueSessionProviderTurn(sessionID string, provider runtime.ProviderSession, continuationID, requestID string, mode runtime.ProviderSessionMode, turnReady <-chan sessionProviderTurnPayload) bool {
	operation := provider.SendTurn
	if mode != runtime.ProviderSessionModeSendTurn {
		operation = nativeSteerOperation(provider, mode)
	}
	if operation == nil {
		return false
	}
	return server.enqueueProviderTaskControlAfterSettlement(sessionID, server.sessionWorkingGraphSettlement(sessionID, false), func(ctx context.Context) {
		var turn sessionProviderTurnPayload
		select {
		case ready, ok := <-turnReady:
			if !ok {
				return
			}
			turn = ready
		case <-ctx.Done():
			return
		}
		selection, message := turn.Selection, turn.Message
		selection.RequestID, selection.Message, selection.TurnKind = requestID, message, runtime.RuntimeTurnKindWork
		if mode != runtime.ProviderSessionModeSendTurn {
			selection.TurnKind = runtime.RuntimeTurnKindControl
		}
		spec := server.sessionSteerEmitSpec(ctx, sessionID, provider)
		protocol := newSteerEmitProtocol(continuationID, spec)
		result, err := operation(ctx, selection, protocol.emit)
		if err != nil || protocol.transitionErr() != nil {
			server.persistSessionProviderEventForContinuation(sessionID, protocol.currentID(), task.EventKindLifecycle, task.EventPayload{
				"phase": "provider_turn_failed", "request_id": requestID, "mode": string(mode), "outcome": "failed",
			})
			if transitionErr := protocol.transitionErr(); transitionErr != nil {
				_ = server.closeSessionProviderSession(sessionID)
				if current := protocol.currentID(); current != "" {
					_, _ = server.sessions.UpdateContinuationStatus(current, session.RuntimeStatusFailed)
				}
			}
			return
		}
		payload := result.Payload()
		payload["phase"] = "provider_turn_applied"
		payload["outcome"] = "applied"
		// The provider result is a structured Runtime turn result, not a
		// transcript message. Conversation contains only explicit user/runtime
		// messages; provider control/result data stays on the Session Timeline.
		server.persistSessionProviderEventForContinuation(sessionID, protocol.currentID(), task.EventKindConversation, payload)
	})
}

// sessionSteerEmitSpec is the Session owner's adapter into the shared steer
// emit protocol: provider events persist bound to the current Session
// Continuation and an interrupt_then_replace settlement advances it with the
// Session's boundary lifecycle events.
func (server *Server) sessionSteerEmitSpec(ctx context.Context, sessionID string, provider runtime.ProviderSession) steerEmitProtocolSpec {
	return steerEmitProtocolSpec{
		advanceOnSettled: true,
		persistEvent: func(current string, kind task.EventKind, payload task.EventPayload) {
			server.persistSessionProviderEventForContinuation(sessionID, current, kind, payload)
		},
		advance: func(current string) (string, error) {
			next := current
			if err := server.advanceSessionRuntimeContinuation(ctx, sessionID, provider, current, &next); err != nil {
				return "", err
			}
			return next, nil
		},
		onAdvanceSettled: func(current, replacement string, advanceErr error) {
			if advanceErr == nil {
				server.persistSessionProviderEventForContinuation(sessionID, replacement, task.EventKindLifecycle, task.EventPayload{
					"phase": "continuation_replaced", "previous_continuation_id": current,
				})
				return
			}
			server.persistSessionProviderEventForContinuation(sessionID, current, task.EventKindLifecycle, task.EventPayload{
				"phase": "continuation_replace_failed", "outcome": "failed",
			})
		},
	}
}

// executeSessionNativeSteerOperation sends one accepted native steer Turn on a
// Session-owned provider session and projects applied/failed/action_required
// outcome events. The caller runs under the Session provider control queue.
// The returned execution is the durable terminal outcome.
func (server *Server) executeSessionNativeSteerOperation(ctx context.Context, sessionID string, provider runtime.ProviderSession, mode runtime.ProviderSessionMode, operation nativeSteerOperationFunc, providerRequest runtime.ProviderSessionRequest, continuationID string) steeringExecution {
	spec := server.sessionSteerEmitSpec(ctx, sessionID, provider)
	return runNativeSteerTurn(ctx, steerExecutionSpec{
		operation:             operation,
		request:               providerRequest,
		mode:                  mode,
		providerSessionID:     provider.SessionID(),
		initialContinuationID: continuationID,
		advanceOnSettled:      spec.advanceOnSettled,
		vocabulary:            sessionSteerEventVocabulary,
		persistEvent:          spec.persistEvent,
		advance:               spec.advance,
		onAdvanceSettled:      spec.onAdvanceSettled,
		failClosed: func(current string) {
			_ = server.closeSessionProviderSession(sessionID)
			if current != "" {
				_, _ = server.sessions.UpdateContinuationStatus(current, session.RuntimeStatusFailed)
			}
		},
	})
}

func payloadOutcome(payload task.EventPayload) string {
	value, _ := payload["outcome"].(string)
	return strings.TrimSpace(value)
}

func (server *Server) advanceSessionRuntimeContinuation(ctx context.Context, sessionID string, provider runtime.ProviderSession, currentID string, continuationID *string) error {
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		return fmt.Errorf("load Session for replacement continuation: %w", err)
	}
	if _, err := server.settleSessionWorkingGraph(ctx, found, true); err != nil {
		return fmt.Errorf("settle old Session Working Graph: %w", err)
	}
	disabled := found.RunControls.BlackboardMode == session.BlackboardModeDisabled
	old, err := server.sessions.Continuation(currentID)
	if err != nil {
		return fmt.Errorf("load Session continuation: %w", err)
	}
	configVersions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil {
		return fmt.Errorf("load Session Runtime config: %w", err)
	}
	var config map[string]any
	if len(configVersions) > 0 {
		config = configVersions[len(configVersions)-1].Config
	}
	next, err := server.sessions.CreateReplacementContinuation(old, config)
	if err != nil {
		return fmt.Errorf("create Session replacement continuation: %w", err)
	}
	if binder, ok := provider.(runtime.ProviderSessionContinuationBinder); ok {
		if err := binder.BindContinuation(next.ID); err != nil {
			_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
			return fmt.Errorf("bind Session provider continuation: %w", err)
		}
	}
	if _, err := server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusRunning); err != nil {
		_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
		return fmt.Errorf("start Session replacement continuation: %w", err)
	}
	if !disabled && server.blackboardV2 != nil {
		if err := server.blackboardV2.RebindSessionContinuation(ctx, sessionID, old.ID, next.ID); err != nil {
			_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
			return fmt.Errorf("rebind Session Blackboard continuation: %w", err)
		}
	}
	if server.sessionHarness != nil && server.sessionHarness.IsActive(sessionID) {
		if err := server.sessionHarness.RebindContinuation(sessionID, next.ID); err != nil {
			_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
			return fmt.Errorf("rebind Session Runtime continuation: %w", err)
		}
	}
	if _, err := server.sessions.UpdateContinuationStatus(old.ID, session.RuntimeStatusCompleted); err != nil {
		_, _ = server.sessions.UpdateContinuationStatus(next.ID, session.RuntimeStatusFailed)
		return fmt.Errorf("settle Session continuation: %w", err)
	}
	*continuationID = next.ID
	return nil
}
