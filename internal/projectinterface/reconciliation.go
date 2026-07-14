package projectinterface

import (
	"context"
	"fmt"
	"strings"

	"pentest/internal/blackboard"
	"pentest/internal/task"
)

// ReconciliationFailurePoint names the durable boundary after graph Apply and
// before the Continuation marker update.
type ReconciliationFailurePoint string

const ReconciliationFailureAfterApply ReconciliationFailurePoint = "apply_commit"

// ReconciliationFailureInjector is the stable crash injection point for the
// graph-committed/marker-missing recovery window.
type ReconciliationFailureInjector interface {
	FailAfter(ReconciliationFailurePoint) error
}

// ReconcileContinuationResult reports the durable normal-audit or unexpected
// recovery outcome for one terminal Runtime Continuation.
type ReconcileContinuationResult struct {
	Continuation task.TaskContinuation
	MutationID   string
	Attempts     []blackboard.ContinuationAttemptRef
}

// ReconcileTerminalContinuation implements task.ContinuationReconciler for the
// production terminal-status path.
func (s *Service) ReconcileTerminalContinuation(ctx context.Context, continuationID, reason string) error {
	_, err := s.ReconcileContinuation(ctx, continuationID, reason)
	return err
}

// ReconcileContinuation applies the canonical sequencing contract after a
// Runtime Continuation has become terminal. Completed Continuations are
// audited without guessing an Attempt outcome; every other terminal status is
// recovered as an unexpected end.
func (s *Service) ReconcileContinuation(ctx context.Context, continuationID, reason string) (ReconcileContinuationResult, error) {
	if s.tasks == nil || s.graph == nil {
		return ReconcileContinuationResult{}, fmt.Errorf("Continuation reconciliation services are unavailable")
	}
	continuation, err := s.tasks.Continuation(continuationID)
	if err != nil {
		return ReconcileContinuationResult{}, err
	}
	if continuation.Status != task.StatusCompleted && continuation.Status != task.StatusFailed &&
		continuation.Status != task.StatusStopped && continuation.Status != task.StatusInterrupted {
		return ReconcileContinuationResult{}, fmt.Errorf("Continuation %s is not terminal", continuationID)
	}
	owner, err := s.tasks.Get(continuation.TaskID)
	if err != nil {
		return ReconcileContinuationResult{}, err
	}

	if continuation.Status == task.StatusCompleted {
		if strings.TrimSpace(continuation.BlackboardFinishSummaryVersionID) != "" {
			if err := s.tasks.VerifyContinuationFinishMarker(ctx, continuation.ID); err != nil {
				return ReconcileContinuationResult{}, err
			}
		}
		attempts, err := s.graph.OpenAttemptsForContinuation(ctx, owner.ProjectID, owner.ID, continuation.ID)
		if err != nil {
			return ReconcileContinuationResult{}, err
		}
		marker := task.ReconciliationCompleted
		event := task.EventPayload(nil)
		if len(attempts) > 0 {
			marker = task.ReconciliationFailed
			attemptIDs := make([]string, len(attempts))
			for index := range attempts {
				attemptIDs[index] = attempts[index].ID
			}
			event = task.EventPayload{"phase": "reconciliation_failed", "attempt_node_ids": attemptIDs}
		} else if continuation.BlackboardReconciliationStatus == task.ReconciliationCompleted {
			return ReconcileContinuationResult{Continuation: continuation}, nil
		} else if strings.TrimSpace(continuation.BlackboardFinishSummaryVersionID) == "" {
			event = task.EventPayload{"phase": "reconciliation_completed", "finish_omitted": true}
		}
		marked, err := s.tasks.MarkContinuationReconciliationWithEvent(ctx, continuation.ID, marker, "", s.clock.Now(), event)
		if err != nil {
			return ReconcileContinuationResult{}, err
		}
		return ReconcileContinuationResult{Continuation: marked, Attempts: attempts}, nil
	}

	if strings.TrimSpace(reason) == "" {
		reason = string(continuation.Status)
	}
	if continuation.BlackboardReconciliationStatus == task.ReconciliationCompleted {
		attempts, err := s.graph.OpenAttemptsForContinuation(ctx, owner.ProjectID, owner.ID, continuation.ID)
		if err != nil {
			return ReconcileContinuationResult{}, err
		}
		if len(attempts) == 0 {
			return ReconcileContinuationResult{Continuation: continuation, MutationID: continuation.BlackboardReconciliationMutationID}, nil
		}
	}
	reconciled, err := s.graph.ReconcileUnexpectedContinuation(ctx, owner.ProjectID, owner.ID, continuation.ID, reason)
	if err != nil {
		return ReconcileContinuationResult{}, err
	}
	if len(reconciled.Attempts) > 0 && s.reconciliationFailures != nil {
		if err := s.reconciliationFailures.FailAfter(ReconciliationFailureAfterApply); err != nil {
			return ReconcileContinuationResult{}, err
		}
	}
	marked, err := s.tasks.MarkContinuationReconciliation(ctx, continuation.ID, task.ReconciliationCompleted, reconciled.MutationID, s.clock.Now())
	if err != nil {
		return ReconcileContinuationResult{}, err
	}
	return ReconcileContinuationResult{
		Continuation: marked, MutationID: reconciled.MutationID, Attempts: reconciled.Attempts,
	}, nil
}
