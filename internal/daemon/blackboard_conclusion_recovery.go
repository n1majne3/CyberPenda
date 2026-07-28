package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"pentest/internal/task"
)

// recoverBlackboardConclusionReceipts resolves every durable pre-apply receipt
// without opening or launching a Runtime. Only an exact provider session
// adopted by the ownership recovery seam may receive a deterministic replay.
func (server *Server) recoverBlackboardConclusionReceipts(ctx context.Context) ProviderSessionRecoveryReport {
	receipts, err := server.tasks.BlackboardConclusionRecoveryCandidates()
	if err != nil {
		server.logger.Printf("assisted conclusion: list restart candidates: %v", err)
		return ProviderSessionRecoveryReport{}
	}
	requests := server.blackboardConclusionOwnershipRequests(receipts)
	report := server.recoverProviderSessionOwnership(ctx, requests)
	live := make(map[string]bool, len(report.LiveTaskIDs))
	selectedReceipt := make(map[string]string, len(requests))
	for _, request := range requests {
		selectedReceipt[request.Task.ID] = request.ReceiptID
	}
	for _, taskID := range report.LiveTaskIDs {
		live[taskID] = true
	}
	for _, receipt := range receipts {
		if !live[receipt.TaskID] || selectedReceipt[receipt.TaskID] != receipt.ID {
			server.requireBlackboardConclusionRecovery(receipt, nil)
			continue
		}
		server.recoverLiveBlackboardConclusionReceipt(ctx, receipt)
	}
	return report
}

// One ownership probe per Task is sufficient. The newest unresolved receipt is
// the exact control lineage that a recovered Task-owned session must accept.
func (server *Server) blackboardConclusionOwnershipRequests(receipts []task.BlackboardConclusionReceipt) []ProviderSessionRecoveryRequest {
	byTask := make(map[string]ProviderSessionRecoveryRequest)
	order := make([]string, 0)
	for _, receipt := range receipts {
		if !receipt.SourceRequestCorrelationExact {
			continue
		}
		found, err := server.tasks.Get(receipt.TaskID)
		if err != nil {
			server.logger.Printf("assisted conclusion: load recovery Task %s: %v", receipt.TaskID, err)
			continue
		}
		continuation, err := server.tasks.Continuation(receipt.ContinuationID)
		if err != nil {
			server.logger.Printf("assisted conclusion: load recovery Continuation %s: %v", receipt.ContinuationID, err)
			continue
		}
		if _, seen := byTask[receipt.TaskID]; !seen {
			order = append(order, receipt.TaskID)
		}
		byTask[receipt.TaskID] = ProviderSessionRecoveryRequest{
			Task: found, Continuation: continuation, ReceiptID: receipt.ID,
			SourceSessionID: receipt.SourceSessionID, SourceRequestID: receipt.SourceRequestID,
			DispatchRequestID: receipt.DispatchRequestID, ContainerID: continuation.ContainerID,
			NativeSessionID: continuation.NativeSessionID, NativeSessionPath: continuation.NativeSessionPath,
		}
	}
	requests := make([]ProviderSessionRecoveryRequest, 0, len(order))
	for _, taskID := range order {
		requests = append(requests, byTask[taskID])
	}
	return requests
}

func (server *Server) recoverLiveBlackboardConclusionReceipt(ctx context.Context, receipt task.BlackboardConclusionReceipt) {
	switch receipt.InternalState {
	case task.BlackboardConclusionReceiptPending:
		server.enqueueRecoveredBlackboardConclusion(receipt, func(controlCtx context.Context) error {
			return server.dispatchBlackboardConclusion(controlCtx, receipt)
		})
	case task.BlackboardConclusionReceiptDispatchRequested,
		task.BlackboardConclusionReceiptRepairDispatchRequested,
		task.BlackboardConclusionReceiptVersionRegenerationDispatchRequested:
		if receipt.SendAttemptCount != 0 || receipt.SendStartedAt != nil {
			server.requireBlackboardConclusionRecovery(receipt, nil)
			return
		}
		server.enqueueRecoveredBlackboardConclusion(receipt, func(controlCtx context.Context) error {
			switch receipt.InternalState {
			case task.BlackboardConclusionReceiptRepairDispatchRequested:
				return server.dispatchBlackboardConclusionRepair(controlCtx, receipt)
			case task.BlackboardConclusionReceiptVersionRegenerationDispatchRequested:
				return server.dispatchBlackboardConclusionVersionRegeneration(controlCtx, receipt)
			default:
				if receipt.BaseRevision == nil {
					return fmt.Errorf("Blackboard conclusion recovery has no base revision")
				}
				directive := concludeBlackboardDirective(*receipt.BaseRevision)
				if receipt.ExplicitRetryCount > 0 {
					directive = repairBlackboardDirective(*receipt.BaseRevision)
				}
				return server.sendBlackboardConclusionTurn(controlCtx, receipt, directive)
			}
		})
	case task.BlackboardConclusionReceiptVersionSyncRequested:
		found, err := server.tasks.Get(receipt.TaskID)
		if err != nil {
			server.requireBlackboardConclusionRecovery(receipt, err)
			return
		}
		if err := server.regenerateBlackboardConclusionAfterVersionConflict(ctx, found.ProjectID, receipt); err != nil {
			server.requireBlackboardConclusionRecovery(receipt, err)
		}
	case task.BlackboardConclusionReceiptAwaitingResult:
		server.requireBlackboardConclusionRecovery(receipt, nil)
	}
}

func (server *Server) enqueueRecoveredBlackboardConclusion(receipt task.BlackboardConclusionReceipt, operation func(context.Context) error) {
	queued := server.enqueueProviderTaskControl(receipt.TaskID, func(ctx context.Context) {
		if err := operation(ctx); err != nil {
			server.requireBlackboardConclusionRecovery(receipt, err)
		}
	})
	if !queued {
		server.requireBlackboardConclusionRecovery(receipt, fmt.Errorf("provider control queue is closed"))
	}
}

func (server *Server) requireBlackboardConclusionRecovery(receipt task.BlackboardConclusionReceipt, cause error) {
	if errors.Is(cause, context.Canceled) && !server.hasLiveProviderTaskContext(receipt.TaskID) {
		return
	}
	_, _, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		receipt.ID, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	if err != nil {
		server.logger.Printf("assisted conclusion: mark restart recovery Task %s receipt %s: %v", receipt.TaskID, receipt.ID, err)
		return
	}
	if cause != nil {
		server.logger.Printf("assisted conclusion: restart recovery requires operator action Task %s receipt %s: %v", receipt.TaskID, receipt.ID, cause)
	}
}
