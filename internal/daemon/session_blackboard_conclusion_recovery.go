package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"pentest/internal/session"
)

// recoverSessionBlackboardConclusionReceipts applies the same proven-live
// Runtime ownership and replay policy as Project Tasks. Session persistence is
// isolated, while restart coordination remains owner-neutral (ADR 0021).
func (server *Server) recoverSessionBlackboardConclusionReceipts(ctx context.Context) ProviderSessionRecoveryReport {
	obligations, err := server.sessions.BlackboardConclusionRecoveryCandidates()
	if err != nil {
		server.logger.Printf("assisted conclusion: list Session restart candidates: %v", err)
		return ProviderSessionRecoveryReport{}
	}
	requests := server.sessionBlackboardConclusionOwnershipRequests(obligations)
	report := server.recoverProviderSessionOwnership(ctx, requests)
	live := make(map[string]bool, len(report.LiveOwnerIDs))
	selectedObligation := make(map[string]string, len(requests))
	for _, request := range requests {
		selectedObligation[request.Owner.ID] = request.ReceiptID
	}
	for _, ownerID := range report.LiveOwnerIDs {
		live[ownerID] = true
	}
	for _, obligation := range obligations {
		if !obligation.SourceRequestCorrelationExact {
			// A legacy source correlation cannot be proven; no ownership probe
			// may run for it, so the obligation fails closed with the specific
			// operator-visible reason.
			server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryLegacyCorrelationUnproven, nil)
			continue
		}
		if !live[obligation.SessionID] || selectedObligation[obligation.SessionID] != obligation.ID {
			server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryRuntimeOwnershipNotProven, nil)
			continue
		}
		server.recoverLiveSessionBlackboardConclusionObligation(ctx, obligation)
	}
	return report
}

func (server *Server) sessionBlackboardConclusionOwnershipRequests(obligations []session.BlackboardConclusionReceipt) []ProviderSessionRecoveryRequest {
	bySession := make(map[string]ProviderSessionRecoveryRequest)
	order := make([]string, 0)
	for _, obligation := range obligations {
		if !obligation.SourceRequestCorrelationExact {
			continue
		}
		found, err := server.sessions.Get(obligation.SessionID)
		if err != nil {
			server.logger.Printf("assisted conclusion: load recovery Session %s: %v", obligation.SessionID, err)
			continue
		}
		active, err := server.sessions.ActiveContinuation(obligation.SessionID)
		if err != nil || active == nil {
			server.logger.Printf("assisted conclusion: load recovery Session Continuation %s: %v", obligation.SessionID, err)
			continue
		}
		if _, seen := bySession[obligation.SessionID]; !seen {
			order = append(order, obligation.SessionID)
		}
		bySession[obligation.SessionID] = ProviderSessionRecoveryRequest{
			Owner: found.OwnerContract(), Continuation: ownerContinuationFromSession(*active),
			ReceiptID: obligation.ID, SourceSessionID: active.NativeSessionID,
			SourceRequestID: obligation.SourceRequestID, DispatchRequestID: obligation.DispatchRequestID,
			ContainerID: active.ContainerID, NativeSessionID: active.NativeSessionID,
			NativeSessionPath: active.NativeSessionPath,
		}
	}
	requests := make([]ProviderSessionRecoveryRequest, 0, len(order))
	for _, sessionID := range order {
		requests = append(requests, bySession[sessionID])
	}
	return requests
}

func (server *Server) recoverLiveSessionBlackboardConclusionObligation(ctx context.Context, obligation session.BlackboardConclusionReceipt) {
	active, err := server.sessions.ActiveContinuation(obligation.SessionID)
	if err != nil || active == nil {
		server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryWritableReplacementUnavailable, err)
		return
	}
	provider, live := server.sessionProviderSessions.get(obligation.SessionID)
	if !live || provider == nil {
		server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryRuntimeOwnershipNotProven, nil)
		return
	}
	boundToCurrent := obligation.ContinuationID == active.ID && obligation.SourceSessionID == provider.SessionID()
	if !boundToCurrent {
		recovered, won, err := server.sessions.CreateRecoveryConclusionDispatch(obligation.ID, active.ID, provider.SessionID(), time.Now().UTC())
		if err != nil || !won {
			server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryWritableReplacementUnavailable, err)
			return
		}
		server.scheduleRecoveredSessionConclusionDispatch(recovered)
		return
	}
	server.resumeLiveSessionBlackboardConclusionObligation(ctx, obligation)
}

func (server *Server) resumeLiveSessionBlackboardConclusionObligation(ctx context.Context, obligation session.BlackboardConclusionReceipt) {
	recoverLiveAssistedConclusion(assistedConclusionRecoveryReceipt{
		State: string(obligation.InternalState), SendAttemptCount: obligation.SendAttemptCount,
		SendStarted: obligation.SendStartedAt != nil, BaseRevision: obligation.BaseRevision,
		ExplicitRetryCount: obligation.ExplicitRetryCount,
	}, assistedConclusionRecoveryHooks{
		Pending: func() {
			server.enqueueRecoveredSessionBlackboardConclusion(obligation, func(controlCtx context.Context) error {
				return server.dispatchSessionBlackboardConclusion(controlCtx, obligation)
			})
		},
		Dispatch: func(state string, baseRevision int, explicitRetryCount int) {
			server.enqueueRecoveredSessionBlackboardConclusion(obligation, func(controlCtx context.Context) error {
				directive := concludeDirective(sessionConclusionDirectiveProfile, baseRevision)
				switch session.BlackboardConclusionReceiptState(state) {
				case session.BlackboardConclusionReceiptRepairDispatchRequested:
					directive = repairDirective(sessionConclusionDirectiveProfile, baseRevision, conclusionDetailFromSessionReceipt(obligation))
				case session.BlackboardConclusionReceiptVersionRegenerationDispatchRequested:
					directive = regenerateDirective(sessionConclusionDirectiveProfile, baseRevision)
				default:
					if explicitRetryCount > 0 {
						directive = repairDirective(sessionConclusionDirectiveProfile, baseRevision, conclusionDetailFromSessionReceipt(obligation))
					}
				}
				return server.sendSessionBlackboardConclusionTurn(controlCtx, obligation, directive)
			})
		},
		VersionSync: func() {
			if err := server.regenerateSessionBlackboardConclusionAfterVersionConflict(ctx, obligation); err != nil {
				server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryDispatchFailed, err)
			}
		},
		Require: func() {
			server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryAcceptanceAmbiguous, nil)
		},
	})
}

func (server *Server) enqueueRecoveredSessionBlackboardConclusion(obligation session.BlackboardConclusionReceipt, operation func(context.Context) error) {
	queued := server.enqueueProviderTaskControl(obligation.SessionID, func(ctx context.Context) {
		if err := operation(ctx); err != nil {
			server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryDispatchFailed, err)
		}
	})
	if !queued {
		server.requireSessionBlackboardConclusionRecovery(obligation, session.ConclusionRecoveryDispatchFailed, fmt.Errorf("provider control queue is closed"))
	}
}

func (server *Server) requireSessionBlackboardConclusionRecovery(obligation session.BlackboardConclusionReceipt, reason session.ConclusionRecoveryReason, cause error) {
	if errors.Is(cause, context.Canceled) && !server.hasLiveProviderSessionContext(obligation.SessionID) {
		return
	}
	if _, _, err := server.sessions.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(obligation.ID, reason, time.Now().UTC(), blackboardConclusionRetryCooldown); err != nil {
		server.logger.Printf("assisted conclusion: Session recovery failed %s obligation %s: %v (dispatch error: %v)", obligation.SessionID, obligation.ID, err, cause)
	}
}

// scheduleRecoveredSessionConclusionDispatch enqueues a pre-send recovery
// Conclusion Dispatch for its Session owner so the control turn is delivered on
// the replacement runtime.
func (server *Server) scheduleRecoveredSessionConclusionDispatch(view session.BlackboardConclusionReceipt) {
	queued := server.enqueueProviderTaskControl(view.SessionID, func(ctx context.Context) {
		if err := server.dispatchRecoveredSessionConclusionDispatch(ctx, view); err != nil {
			server.requireSessionBlackboardConclusionRecovery(view, session.ConclusionRecoveryDispatchFailed, err)
		}
	})
	if !queued {
		server.requireSessionBlackboardConclusionRecovery(view, session.ConclusionRecoveryDispatchFailed, fmt.Errorf("provider control queue is closed"))
	}
}

func (server *Server) dispatchRecoveredSessionConclusionDispatch(ctx context.Context, view session.BlackboardConclusionReceipt) error {
	directive := concludeDirective(sessionConclusionDirectiveProfile, pointerValue(view.BaseRevision))
	switch view.InternalState {
	case session.BlackboardConclusionReceiptRepairDispatchRequested:
		directive = repairDirective(sessionConclusionDirectiveProfile, pointerValue(view.BaseRevision), conclusionDetailFromSessionReceipt(view))
	case session.BlackboardConclusionReceiptVersionRegenerationDispatchRequested:
		directive = regenerateDirective(sessionConclusionDirectiveProfile, pointerValue(view.BaseRevision))
	default:
		if view.ExplicitRetryCount > 0 {
			directive = repairDirective(sessionConclusionDirectiveProfile, pointerValue(view.BaseRevision), conclusionDetailFromSessionReceipt(view))
		}
	}
	return server.sendSessionBlackboardConclusionTurn(ctx, view, directive)
}
