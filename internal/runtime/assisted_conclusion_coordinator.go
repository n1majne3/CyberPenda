package runtime

import (
	"context"
	"strings"

	"pentest/internal/owner"
)

// AssistedConclusionReceiptView is the small owner-neutral receipt contract
// needed to arbitrate provider callbacks. Durable Task and Session receipts
// remain separate; the coordinator never imports either aggregate.
type AssistedConclusionReceiptView struct {
	OwnerID           string
	SourceSessionID   string
	DispatchRequestID string
	ControlTurnID     string
	State             owner.BlackboardConclusionReceiptState
}

func (view AssistedConclusionReceiptView) AcceptsCallback() bool {
	switch view.State {
	case owner.BlackboardConclusionReceiptDispatchRequested,
		owner.BlackboardConclusionReceiptRepairDispatchRequested,
		owner.BlackboardConclusionReceiptVersionRegenerationDispatchRequested,
		owner.BlackboardConclusionReceiptAwaitingResult:
		return true
	default:
		return false
	}
}

// AssistedConclusionCoordinator owns the shared callback/terminal protocol.
// Owner adapters provide receipt lookup and durable transitions; the sequence
// of canonical callback checks, terminal arbitration, bounded draining, and
// owner-scoped cleanup is identical for every owner kind.
type AssistedConclusionCoordinator struct {
	OwnerID         string
	Tracker         *AssistedConclusionTracker
	LoadReceipt     func(string) (AssistedConclusionReceiptView, error)
	IsCanonical     func(requestID, providerSessionID, providerTurnID string) bool
	Enqueue         func(func(context.Context)) bool
	EnqueueExisting func(func(context.Context)) bool
	OnFailure       func(context.Context, string, AssistedConclusionQueuedFailure) error
	OnResult        func(context.Context, ProviderSessionAttemptResult) error
	OnError         func(error)
}

func (coordinator AssistedConclusionCoordinator) AcceptValidationFailure(failure ProviderSessionAttemptResultValidationFailure) {
	if coordinator.Tracker == nil || coordinator.LoadReceipt == nil || coordinator.IsCanonical == nil ||
		strings.TrimSpace(failure.RequestID) == "" || failure.ValidationErrorCode != ProviderSessionAttemptResultInvalid ||
		!coordinator.IsCanonical(failure.RequestID, failure.SessionID, failure.ProviderTurnID) {
		return
	}
	receipt, err := coordinator.LoadReceipt(failure.RequestID)
	if err != nil || receipt.OwnerID != coordinator.OwnerID || receipt.SourceSessionID != strings.TrimSpace(failure.SessionID) || !receipt.AcceptsCallback() {
		return
	}
	coordinator.Tracker.QueueFailure(failure.RequestID, AssistedConclusionQueuedFailure{
		OwnerID: coordinator.OwnerID, ProviderSessionID: failure.SessionID, ProviderTurnID: failure.ProviderTurnID,
		Code: string(owner.BlackboardConclusionErrorInvalidResult),
		Detail: owner.ConclusionValidationDetail{
			Reason: string(failure.Reason), FieldPath: failure.FieldPath, Expected: failure.Expected,
		},
	})
	coordinator.enqueueDrain(failure.RequestID, false)
}

func (coordinator AssistedConclusionCoordinator) AcceptControlFailure(sessionID string, lineage ProviderSessionTurnLineage) {
	if coordinator.Tracker == nil || coordinator.LoadReceipt == nil {
		return
	}
	receipt, err := coordinator.LoadReceipt(lineage.RequestID)
	if err != nil || receipt.OwnerID != coordinator.OwnerID || receipt.SourceSessionID != strings.TrimSpace(sessionID) || !receipt.AcceptsCallback() {
		return
	}
	coordinator.Tracker.QueueFailure(lineage.RequestID, AssistedConclusionQueuedFailure{
		OwnerID: coordinator.OwnerID, ProviderSessionID: sessionID, ProviderTurnID: lineage.ProviderTurnID,
		Code: string(owner.BlackboardConclusionErrorToolUseForbidden),
	})
	coordinator.enqueueDrain(lineage.RequestID, false)
}

func (coordinator AssistedConclusionCoordinator) AcceptControlTerminal(sessionID string, lineage ProviderSessionTurnLineage, status string) {
	if coordinator.Tracker == nil || coordinator.LoadReceipt == nil {
		return
	}
	receipt, err := coordinator.LoadReceipt(lineage.RequestID)
	if err != nil || receipt.OwnerID != coordinator.OwnerID || receipt.SourceSessionID != strings.TrimSpace(sessionID) || !receipt.AcceptsCallback() ||
		(receipt.ControlTurnID != "" && receipt.ControlTurnID != lineage.ProviderTurnID) {
		return
	}
	coordinator.Tracker.MarkTerminal(lineage.RequestID, AssistedConclusionQueuedTerminal{
		OwnerID: coordinator.OwnerID, ProviderSessionID: sessionID, ProviderTurnID: lineage.ProviderTurnID,
	})
	if status != "completed" {
		coordinator.Tracker.QueueFailure(lineage.RequestID, AssistedConclusionQueuedFailure{
			OwnerID: coordinator.OwnerID, ProviderSessionID: sessionID, ProviderTurnID: lineage.ProviderTurnID,
			Code: string(owner.BlackboardConclusionErrorRuntimeRecoveryRequired),
		})
	}
	if !coordinator.enqueueDrain(lineage.RequestID, status != "completed") && status != "completed" {
		coordinator.Tracker.ClearRequest(coordinator.OwnerID, lineage.RequestID)
	}
}

func (coordinator AssistedConclusionCoordinator) AcceptResult(result ProviderSessionAttemptResult) {
	if coordinator.Tracker == nil || coordinator.LoadReceipt == nil || coordinator.IsCanonical == nil ||
		!coordinator.IsCanonical(result.RequestID, result.SessionID, result.ProviderTurnID) {
		return
	}
	receipt, err := coordinator.LoadReceipt(result.RequestID)
	if err != nil || receipt.OwnerID != coordinator.OwnerID || receipt.SourceSessionID != result.SessionID || !receipt.AcceptsCallback() {
		return
	}
	coordinator.Tracker.QueueResult(coordinator.OwnerID, result)
	coordinator.enqueueDrain(result.RequestID, false)
}

func (coordinator AssistedConclusionCoordinator) Drain(ctx context.Context, requestID string) error {
	if coordinator.Tracker == nil || coordinator.LoadReceipt == nil {
		return nil
	}
	if failure, ok := coordinator.Tracker.TakeActionableFailure(coordinator.OwnerID, requestID); ok {
		receipt, err := coordinator.LoadReceipt(requestID)
		if err != nil || receipt.OwnerID != coordinator.OwnerID || receipt.SourceSessionID != failure.ProviderSessionID || receipt.ControlTurnID != failure.ProviderTurnID {
			return nil
		}
		if coordinator.OnFailure == nil {
			return nil
		}
		if err := coordinator.OnFailure(ctx, requestID, failure); err != nil {
			return err
		}
		coordinator.Tracker.ClearRequest(coordinator.OwnerID, requestID)
		return nil
	}
	if result, ok := coordinator.Tracker.TakeActionableResult(coordinator.OwnerID, requestID); ok && coordinator.OnResult != nil {
		return coordinator.OnResult(ctx, result)
	}
	return nil
}

func (coordinator AssistedConclusionCoordinator) enqueueDrain(requestID string, existing bool) bool {
	enqueue := coordinator.Enqueue
	if existing && coordinator.EnqueueExisting != nil {
		enqueue = coordinator.EnqueueExisting
	}
	if enqueue == nil {
		return false
	}
	queued := enqueue(func(ctx context.Context) {
		if err := coordinator.Drain(ctx, requestID); err != nil && coordinator.OnError != nil {
			coordinator.OnError(err)
		}
	})
	return queued
}
