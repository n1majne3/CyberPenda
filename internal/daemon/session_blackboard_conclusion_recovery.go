package daemon

import (
	"context"
	"fmt"

	"pentest/internal/session"
)

// recoverSessionBlackboardConclusionReceipts applies the same proven-live
// Runtime ownership and replay policy as Project Tasks. Session persistence is
// isolated, while restart coordination remains owner-neutral.
func (server *Server) recoverSessionBlackboardConclusionReceipts(ctx context.Context) ProviderSessionRecoveryReport {
	receipts, err := server.sessions.BlackboardConclusionRecoveryCandidates()
	if err != nil {
		server.logger.Printf("assisted conclusion: list Session restart candidates: %v", err)
		return ProviderSessionRecoveryReport{}
	}
	requests := server.sessionBlackboardConclusionOwnershipRequests(receipts)
	report := server.recoverProviderSessionOwnership(ctx, requests)
	live := make(map[string]bool, len(report.LiveOwnerIDs))
	selectedReceipt := make(map[string]string, len(requests))
	for _, request := range requests {
		selectedReceipt[request.Owner.ID] = request.ReceiptID
	}
	for _, ownerID := range report.LiveOwnerIDs {
		live[ownerID] = true
	}
	for _, receipt := range receipts {
		if !live[receipt.SessionID] || selectedReceipt[receipt.SessionID] != receipt.ID {
			server.requireSessionBlackboardConclusionRecovery(receipt, nil)
			continue
		}
		server.recoverLiveSessionBlackboardConclusionReceipt(ctx, receipt)
	}
	return report
}

func (server *Server) sessionBlackboardConclusionOwnershipRequests(receipts []session.BlackboardConclusionReceipt) []ProviderSessionRecoveryRequest {
	bySession := make(map[string]ProviderSessionRecoveryRequest)
	order := make([]string, 0)
	for _, receipt := range receipts {
		if !receipt.SourceRequestCorrelationExact {
			continue
		}
		found, err := server.sessions.Get(receipt.SessionID)
		if err != nil {
			server.logger.Printf("assisted conclusion: load recovery Session %s: %v", receipt.SessionID, err)
			continue
		}
		continuation, err := server.sessions.Continuation(receipt.ContinuationID)
		if err != nil {
			server.logger.Printf("assisted conclusion: load recovery Session Continuation %s: %v", receipt.ContinuationID, err)
			continue
		}
		if _, seen := bySession[receipt.SessionID]; !seen {
			order = append(order, receipt.SessionID)
		}
		bySession[receipt.SessionID] = ProviderSessionRecoveryRequest{
			Owner: found.OwnerContract(), Continuation: ownerContinuationFromSession(continuation),
			ReceiptID: receipt.ID, SourceSessionID: receipt.SourceSessionID,
			SourceRequestID: receipt.SourceRequestID, DispatchRequestID: receipt.DispatchRequestID,
			ContainerID: continuation.ContainerID, NativeSessionID: continuation.NativeSessionID,
			NativeSessionPath: continuation.NativeSessionPath,
		}
	}
	requests := make([]ProviderSessionRecoveryRequest, 0, len(order))
	for _, sessionID := range order {
		requests = append(requests, bySession[sessionID])
	}
	return requests
}

func (server *Server) recoverLiveSessionBlackboardConclusionReceipt(ctx context.Context, receipt session.BlackboardConclusionReceipt) {
	recoverLiveAssistedConclusion(assistedConclusionRecoveryReceipt{
		State: string(receipt.InternalState), SendAttemptCount: receipt.SendAttemptCount,
		SendStarted: receipt.SendStartedAt != nil, BaseRevision: receipt.BaseRevision,
		ExplicitRetryCount: receipt.ExplicitRetryCount,
	}, assistedConclusionRecoveryHooks{
		Pending: func() {
			server.enqueueRecoveredSessionBlackboardConclusion(receipt, func(controlCtx context.Context) error {
				return server.dispatchSessionBlackboardConclusion(controlCtx, receipt)
			})
		},
		Dispatch: func(state string, baseRevision int, explicitRetryCount int) {
			server.enqueueRecoveredSessionBlackboardConclusion(receipt, func(controlCtx context.Context) error {
				directive := concludeSessionBlackboardDirective(baseRevision)
				switch session.BlackboardConclusionReceiptState(state) {
				case session.BlackboardConclusionReceiptRepairDispatchRequested:
					directive = repairSessionBlackboardDirective(baseRevision, conclusionDetailFromSessionReceipt(receipt))
				case session.BlackboardConclusionReceiptVersionRegenerationDispatchRequested:
					directive = regenerateSessionBlackboardDirective(baseRevision)
				default:
					if explicitRetryCount > 0 {
						directive = repairSessionBlackboardDirective(baseRevision, conclusionDetailFromSessionReceipt(receipt))
					}
				}
				return server.sendSessionBlackboardConclusionTurn(controlCtx, receipt, directive)
			})
		},
		VersionSync: func() {
			if err := server.regenerateSessionBlackboardConclusionAfterVersionConflict(ctx, receipt); err != nil {
				server.requireSessionBlackboardConclusionRecovery(receipt, err)
			}
		},
		Require: func() { server.requireSessionBlackboardConclusionRecovery(receipt, nil) },
	})
}

func (server *Server) enqueueRecoveredSessionBlackboardConclusion(receipt session.BlackboardConclusionReceipt, operation func(context.Context) error) {
	queued := server.enqueueProviderTaskControl(receipt.SessionID, func(ctx context.Context) {
		if err := operation(ctx); err != nil {
			server.requireSessionBlackboardConclusionRecovery(receipt, err)
		}
	})
	if !queued {
		server.requireSessionBlackboardConclusionRecovery(receipt, fmt.Errorf("provider control queue is closed"))
	}
}
