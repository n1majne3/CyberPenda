package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"pentest/internal/task"
)

// recoverBlackboardConclusionReceipts resolves every durable pre-apply
// obligation without opening or launching a Runtime. Only an exact provider
// session adopted by the ownership recovery seam may receive a deterministic
// replay; a stuck obligation whose active dispatch is bound to a dead
// continuation gets a NEW Conclusion Dispatch only when live ownership of the
// current Task-scoped Runtime is proven (ADR 0021). Otherwise the obligation
// becomes action_required with an operator-visible recovery reason.
func (server *Server) recoverBlackboardConclusionReceipts(ctx context.Context) ProviderSessionRecoveryReport {
	obligations, err := server.tasks.BlackboardConclusionRecoveryCandidates()
	if err != nil {
		server.logger.Printf("assisted conclusion: list restart candidates: %v", err)
		return ProviderSessionRecoveryReport{}
	}
	requests := server.blackboardConclusionOwnershipRequests(obligations)
	report := server.recoverProviderSessionOwnership(ctx, requests)
	live := make(map[string]bool, len(report.LiveOwnerIDs))
	selectedObligation := make(map[string]string, len(requests))
	for _, request := range requests {
		selectedObligation[request.Owner.ID] = request.ReceiptID
	}
	for _, taskID := range report.LiveOwnerIDs {
		live[taskID] = true
	}
	for _, obligation := range obligations {
		if !obligation.SourceRequestCorrelationExact {
			// A legacy source correlation cannot be proven; no ownership probe
			// may run for it, so the obligation fails closed with the specific
			// operator-visible reason.
			server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryLegacyCorrelationUnproven, nil)
			continue
		}
		if !live[obligation.TaskID] || selectedObligation[obligation.TaskID] != obligation.ID {
			server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryRuntimeOwnershipNotProven, nil)
			continue
		}
		server.recoverLiveBlackboardConclusionObligation(ctx, obligation)
	}
	return report
}

// blackboardConclusionOwnershipRequests builds one ownership probe per Task
// from the obligation whose ACTIVE dispatch owns the exact control lineage.
// The probe continuation is the CURRENT ACTIVE continuation of the owner, so a
// stuck dispatch bound to a dead continuation can still prove ownership of the
// live Task-scoped Runtime before a new dispatch is created.
func (server *Server) blackboardConclusionOwnershipRequests(obligations []task.BlackboardConclusionReceipt) []ProviderSessionRecoveryRequest {
	byTask := make(map[string]ProviderSessionRecoveryRequest)
	order := make([]string, 0)
	for _, obligation := range obligations {
		if !obligation.SourceRequestCorrelationExact {
			continue
		}
		found, err := server.tasks.Get(obligation.TaskID)
		if err != nil {
			server.logger.Printf("assisted conclusion: load recovery Task %s: %v", obligation.TaskID, err)
			continue
		}
		active, err := server.tasks.ActiveContinuation(obligation.TaskID)
		if err != nil || active == nil {
			server.logger.Printf("assisted conclusion: load recovery Continuation for Task %s: %v", obligation.TaskID, err)
			continue
		}
		if _, seen := byTask[obligation.TaskID]; !seen {
			order = append(order, obligation.TaskID)
		}
		byTask[obligation.TaskID] = ProviderSessionRecoveryRequest{
			Owner: found.OwnerContract(""), Continuation: ownerContinuationFromTask(*active), ReceiptID: obligation.ID,
			// The probe targets the CURRENT active continuation of the owner, so
			// the session identity comes from that continuation's native session
			// (the exact runtime the factory may adopt), never from a stale
			// dispatch binding.
			SourceSessionID: active.NativeSessionID, SourceRequestID: obligation.SourceRequestID,
			DispatchRequestID: obligation.DispatchRequestID, ContainerID: active.ContainerID,
			NativeSessionID: active.NativeSessionID, NativeSessionPath: active.NativeSessionPath,
		}
	}
	requests := make([]ProviderSessionRecoveryRequest, 0, len(order))
	for _, taskID := range order {
		requests = append(requests, byTask[taskID])
	}
	return requests
}

// recoverLiveBlackboardConclusionObligation resumes or re-dispatches one
// obligation whose Task-scoped Runtime ownership was proven live. A pre-send
// dispatch bound to the current active continuation resumes with the SAME
// deterministic request id. A dispatch bound to a dead continuation is
// superseded and a NEW recovery dispatch is created on the live continuation +
// session. Post-fence and awaiting-result dispatches fail closed: an
// acceptance-ambiguous provider delivery is never replayed automatically.
func (server *Server) recoverLiveBlackboardConclusionObligation(ctx context.Context, obligation task.BlackboardConclusionReceipt) {
	active, err := server.tasks.ActiveContinuation(obligation.TaskID)
	if err != nil || active == nil {
		server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryWritableReplacementUnavailable, err)
		return
	}
	session, live := server.providerSessions.get(obligation.TaskID)
	if !live || session == nil {
		server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryRuntimeOwnershipNotProven, nil)
		return
	}
	boundToCurrent := obligation.ContinuationID == active.ID && obligation.SourceSessionID == session.SessionID()
	if !boundToCurrent {
		// The active dispatch is bound to a dead or foreign continuation (for
		// example a migrated legacy receipt). Recovery creates a NEW dispatch on
		// the proven-live continuation + session; the old dispatch is superseded
		// and its identity is never rewritten.
		recovered, won, err := server.tasks.CreateRecoveryConclusionDispatch(obligation.ID, active.ID, session.SessionID(), time.Now().UTC())
		if err != nil || !won {
			server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryWritableReplacementUnavailable, err)
			return
		}
		server.scheduleRecoveredConclusionDispatch(recovered)
		return
	}
	server.resumeLiveBlackboardConclusionObligation(ctx, obligation)
}

// resumeLiveBlackboardConclusionObligation runs the owner-neutral pre-apply
// state machine for a live-bound obligation.
func (server *Server) resumeLiveBlackboardConclusionObligation(ctx context.Context, obligation task.BlackboardConclusionReceipt) {
	recoverLiveAssistedConclusion(assistedConclusionRecoveryReceipt{
		State: string(obligation.InternalState), SendAttemptCount: obligation.SendAttemptCount,
		SendStarted: obligation.SendStartedAt != nil, BaseRevision: obligation.BaseRevision,
		ExplicitRetryCount: obligation.ExplicitRetryCount,
	}, assistedConclusionRecoveryHooks{
		Pending: func() {
			server.enqueueRecoveredBlackboardConclusion(obligation, func(controlCtx context.Context) error {
				return server.dispatchBlackboardConclusion(controlCtx, obligation)
			})
		},
		Dispatch: func(_ string, _ int, _ int) {
			server.enqueueRecoveredBlackboardConclusion(obligation, func(controlCtx context.Context) error {
				return server.dispatchRecoveredConclusionDispatch(controlCtx, obligation)
			})
		},
		VersionSync: func() {
			found, err := server.tasks.Get(obligation.TaskID)
			if err == nil {
				err = server.regenerateBlackboardConclusionAfterVersionConflict(ctx, found.ProjectID, obligation)
			}
			if err != nil {
				server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryDispatchFailed, err)
			}
		},
		Require: func() {
			server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryAcceptanceAmbiguous, nil)
		},
	})
}

func (server *Server) enqueueRecoveredBlackboardConclusion(obligation task.BlackboardConclusionReceipt, operation func(context.Context) error) {
	queued := server.enqueueProviderTaskControl(obligation.TaskID, func(ctx context.Context) {
		if err := operation(ctx); err != nil {
			server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryDispatchFailed, err)
		}
	})
	if !queued {
		server.requireBlackboardConclusionRecovery(obligation, task.ConclusionRecoveryDispatchFailed, fmt.Errorf("provider control queue is closed"))
	}
}

func (server *Server) requireBlackboardConclusionRecovery(obligation task.BlackboardConclusionReceipt, reason task.ConclusionRecoveryReason, cause error) {
	if errors.Is(cause, context.Canceled) && !server.hasLiveProviderTaskContext(obligation.TaskID) {
		return
	}
	_, _, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		obligation.ID, reason, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	if err != nil {
		server.logger.Printf("assisted conclusion: mark restart recovery Task %s obligation %s: %v", obligation.TaskID, obligation.ID, err)
		return
	}
	if cause != nil {
		server.logger.Printf("assisted conclusion: restart recovery requires operator action Task %s obligation %s: %v", obligation.TaskID, obligation.ID, cause)
	}
}
