package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
	"pentest/internal/runtime"
	"pentest/internal/session"
)

func (server *Server) sessionBlackboardConclusionSettlement(sessionID string, allowActionRequired bool) providerControlSettlement {
	return func(ctx context.Context, wait bool) (bool, error) {
		for {
			found, err := server.sessions.Get(sessionID)
			if err != nil {
				return false, err
			}
			receipt, err := server.sessions.LatestBlackboardConclusion(sessionID)
			if err != nil {
				return false, err
			}
			if receipt == nil {
				return true, nil
			}
			switch receipt.View(found.RunControls.BlackboardConclusionMode).State {
			case session.BlackboardConclusionStateClean:
				return true, nil
			case session.BlackboardConclusionStateActionRequired:
				if allowActionRequired {
					return true, nil
				}
				return false, errSemanticConclusionActionRequired
			}
			if !wait {
				return false, nil
			}
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func (server *Server) waitForSessionAssistedConclusionSettlement(ctx context.Context, sessionID string, allowActionRequired bool) error {
	_, err := server.sessionBlackboardConclusionSettlement(sessionID, allowActionRequired)(ctx, true)
	return err
}

func (server *Server) observeSessionProviderSession(sessionID, continuationID, providerID string, lineage runtime.ProviderSessionTurnLineage, observation runtime.ProviderSessionObservation) {
	found, err := server.sessions.Get(sessionID)
	if err != nil || found.RunControls.BlackboardConclusionMode != session.BlackboardConclusionModeAssisted {
		return
	}
	if strings.TrimSpace(continuationID) == "" {
		return
	}
	if lineage.Kind == runtime.RuntimeTurnKindControl {
		switch observation.Kind {
		case runtime.ProviderSessionObservationToolUse, runtime.ProviderSessionObservationToolResult:
			server.acceptSessionBlackboardConclusionControlFailure(sessionID, providerID, lineage)
		case runtime.ProviderSessionObservationTurnCompleted:
			server.acceptSessionBlackboardConclusionControlTerminal(sessionID, providerID, lineage, observation.Status)
		}
		return
	}
	if lineage.Kind != runtime.RuntimeTurnKindWork {
		return
	}
	turnID := strings.TrimSpace(observation.ProviderTurnID)
	if turnID == "" {
		return
	}
	key := runtime.AssistedConclusionTurnKey{OwnerID: sessionID, ContinuationID: continuationID, ProviderSessionID: strings.TrimSpace(providerID), TurnID: turnID}
	switch observation.Kind {
	case runtime.ProviderSessionObservationToolResult:
		toolCallID := strings.TrimSpace(observation.ToolCallID)
		toolSemantics, trusted := classifyTrustedBlackboardTool(observation.ToolName)
		server.sessionBlackboardConclusions.RecordToolResult(key, toolCallID, !trusted,
			trusted && observation.Status == "succeeded" && toolSemantics == blackboardToolSemanticPersistence)
		return
	case runtime.ProviderSessionObservationTurnCompleted:
		// Completion is handled below after taking a stable snapshot.
	default:
		return
	}
	state, ok := server.sessionBlackboardConclusions.SnapshotTurn(key)
	if !ok {
		return
	}

	if len(state.CompletedToolCalls) == 0 {
		server.sessionBlackboardConclusions.DeleteTurn(key)
		return
	}
	receipt, inserted, err := server.sessions.RecordBlackboardConclusionCheckpoint(
		sessionID, continuationID, lineage.RequestID, key.ProviderSessionID, turnID,
		session.RuntimeTurnSelection{ModelProviderID: lineage.ModelProviderID, Model: lineage.Model, ReasoningEffort: lineage.RequestedReasoningEffort},
		session.SemanticDebtWatermarks{SourceWork: state.SourceWorkWatermark, SemanticPersistence: state.SemanticPersistenceWatermark},
	)
	if err != nil {
		server.logger.Printf("assisted conclusion: record pending Session %s Turn %s (retained for retry): %v", sessionID, turnID, err)
		return
	}
	server.sessionBlackboardConclusions.DeleteTurn(key)
	if inserted && receipt.InternalState == session.BlackboardConclusionReceiptPending && observation.Status != "completed" {
		if _, _, err := server.sessions.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(receipt.ID, time.Now().UTC(), blackboardConclusionRetryCooldown); err != nil {
			server.logger.Printf("assisted conclusion: mark failed Work attention Session %s Turn %s: %v", sessionID, turnID, err)
		}
		return
	}
	if inserted && receipt.InternalState == session.BlackboardConclusionReceiptPending {
		server.scheduleSessionBlackboardConclusionDispatch(receipt)
	}
}

func (server *Server) scheduleSessionBlackboardConclusionDispatch(receipt session.BlackboardConclusionReceipt) {
	queued := server.enqueueProviderTaskControl(receipt.SessionID, func(ctx context.Context) {
		if err := server.dispatchSessionBlackboardConclusion(ctx, receipt); err != nil {
			server.requireSessionBlackboardConclusionRecovery(receipt, err)
			server.logger.Printf("assisted conclusion: dispatch Session %s receipt %s: %v", receipt.SessionID, receipt.ID, err)
		}
	})
	if !queued {
		server.requireSessionBlackboardConclusionRecovery(receipt, fmt.Errorf("provider control queue is closed"))
	}
}

func (server *Server) dispatchSessionBlackboardConclusion(ctx context.Context, pending session.BlackboardConclusionReceipt) error {
	provider, ok := server.sessionProviderSessions.get(pending.SessionID)
	if !ok || provider.SessionID() != pending.SourceSessionID {
		return fmt.Errorf("source Session provider session is not live")
	}
	snapshot, err := server.blackboardV2.SessionRuntimeSnapshot(ctx, pending.SessionID)
	if err != nil {
		return fmt.Errorf("read Session Blackboard revision: %w", err)
	}
	receipt, won, err := server.sessions.ClaimBlackboardConclusionDispatch(pending.ID, snapshot.Revision)
	if err != nil {
		return err
	}
	if !won {
		return nil
	}
	return server.sendSessionBlackboardConclusionTurn(ctx, receipt, concludeSessionBlackboardDirective(snapshot.Revision))
}

func concludeSessionBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`Stop the Session's exploratory work and perform only the Harness conclusion below.
Return exactly one JSON object (no markdown fences, no prose) with this shape and base_revision %d:
{"schema":"runtime-attempt-result/v1","base_revision":%d,"attempt":{"key":"attempt:example","create":true,"summary":"One sentence outcome of the completed Session work.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:example","create_objective":{"objective":"What was tested."}}],"produced_targets":[]}
Replace example keys and summaries with this Turn's real semantic targets.
Rules: outcome must be one of succeeded, failed, blocked, or inconclusive. Describe one Attempt and at least one tested target. Do not call tools, continue testing, include raw tool output or reasoning, finish the Session, or write the Session Blackboard directly.`, baseRevision, baseRevision)
}

func repairSessionBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`Your previous Session Blackboard conclusion result was invalid.
Stop exploratory work and correct only that semantic result.
Return exactly one JSON object (no markdown fences, no prose) with schema runtime-attempt-result/v1 and base_revision %d.
Use outcome inconclusive, failed, or blocked with produced_targets [] when no existing produced target is available.
Do not call tools, continue testing, include raw tool output or reasoning, finish the Session, or write the Session Blackboard directly.`, baseRevision)
}

func regenerateSessionBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`The Session Blackboard changed after your previous semantic result was produced.
Reread the current Session Blackboard context and regenerate the semantic result against base_revision %d.
Return exactly one JSON object with schema runtime-attempt-result/v1.
Do not call tools, continue testing, include raw tool output or reasoning, finish the Session, or write the Session Blackboard directly.`, baseRevision)
}

func (server *Server) sessionBlackboardConclusionCoordinator(sessionID string) runtime.AssistedConclusionCoordinator {
	return runtime.AssistedConclusionCoordinator{
		OwnerID: sessionID, Tracker: server.sessionBlackboardConclusions,
		LoadReceipt: func(requestID string) (runtime.AssistedConclusionReceiptView, error) {
			receipt, err := server.sessions.BlackboardConclusionByDispatchRequestID(requestID)
			if err != nil {
				return runtime.AssistedConclusionReceiptView{}, err
			}
			return runtime.AssistedConclusionReceiptView{
				OwnerID: receipt.SessionID, SourceSessionID: receipt.SourceSessionID,
				DispatchRequestID: receipt.DispatchRequestID, ControlTurnID: receipt.ControlTurnID,
				State: receipt.InternalState,
			}, nil
		},
		IsCanonical: func(requestID, providerSessionID, providerTurnID string) bool {
			return server.isCanonicalSessionBlackboardConclusionCallback(sessionID, requestID, providerSessionID, providerTurnID)
		},
		Enqueue: func(run func(context.Context)) bool {
			return server.enqueueProviderTaskControl(sessionID, run)
		},
		EnqueueExisting: func(run func(context.Context)) bool {
			return server.enqueueExistingProviderTaskControl(sessionID, run)
		},
		OnFailure: func(ctx context.Context, requestID, code string) error {
			return server.handleSessionBlackboardConclusionFailure(sessionID, requestID, session.BlackboardConclusionErrorCode(code))
		},
		OnResult: func(ctx context.Context, result runtime.ProviderSessionAttemptResult) error {
			return server.applySessionBlackboardConclusionResult(ctx, sessionID, result)
		},
		OnError: func(err error) {
			server.logger.Printf("assisted conclusion: Session %s callback coordination: %v", sessionID, err)
		},
	}
}

func (server *Server) acceptSessionBlackboardConclusionValidationFailure(sessionID string, failure runtime.ProviderSessionAttemptResultValidationFailure) {
	server.sessionBlackboardConclusionCoordinator(sessionID).AcceptValidationFailure(failure)
}

func (server *Server) acceptSessionBlackboardConclusionControlFailure(sessionID, providerID string, lineage runtime.ProviderSessionTurnLineage) {
	server.sessionBlackboardConclusionCoordinator(sessionID).AcceptControlFailure(providerID, lineage)
}

func (server *Server) acceptSessionBlackboardConclusionControlTerminal(sessionID, providerID string, lineage runtime.ProviderSessionTurnLineage, status string) {
	server.sessionBlackboardConclusionCoordinator(sessionID).AcceptControlTerminal(providerID, lineage, status)
}

func (server *Server) drainSessionBlackboardConclusionCallbacks(ctx context.Context, sessionID, requestID string) {
	_ = server.sessionBlackboardConclusionCoordinator(sessionID).Drain(ctx, requestID)
}

func (server *Server) handleSessionBlackboardConclusionFailure(sessionID, requestID string, code session.BlackboardConclusionErrorCode) error {
	receipt, dispatchRepair, err := server.sessions.HandleBlackboardConclusionFailure(requestID, code, time.Now().UTC(), blackboardConclusionRetryCooldown)
	if err != nil {
		return err
	}
	if dispatchRepair && receipt.InternalState == session.BlackboardConclusionReceiptRepairDispatchRequested {
		queued := server.enqueueProviderTaskControl(sessionID, func(ctx context.Context) {
			if err := server.sendSessionBlackboardConclusionTurn(ctx, receipt, repairSessionBlackboardDirective(pointerValue(receipt.BaseRevision))); err != nil {
				server.requireSessionBlackboardConclusionRecovery(receipt, err)
			}
		})
		if !queued {
			server.requireSessionBlackboardConclusionRecovery(receipt, fmt.Errorf("provider control queue is closed"))
		}
	}
	return nil
}

func pointerValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (server *Server) acceptSessionBlackboardConclusionResult(sessionID string, result runtime.ProviderSessionAttemptResult) {
	server.sessionBlackboardConclusionCoordinator(sessionID).AcceptResult(result)
}

func (server *Server) isCanonicalSessionBlackboardConclusionCallback(sessionID, requestID, providerID, providerTurnID string) bool {
	provider, ok := server.sessionProviderSessions.get(sessionID)
	if !ok || provider.SessionID() != strings.TrimSpace(providerID) {
		return false
	}
	resolver, ok := provider.(runtime.ProviderSessionCompleteTurnLineageResolver)
	if !ok {
		return false
	}
	lineage, resolved := resolver.ResolveProviderSessionTurnLineage(requestID, providerTurnID)
	return resolved && lineage.Kind == runtime.RuntimeTurnKindControl && lineage.RequestID == strings.TrimSpace(requestID) && lineage.ProviderTurnID == strings.TrimSpace(providerTurnID)
}

func (server *Server) applySessionBlackboardConclusionResult(ctx context.Context, sessionID string, result runtime.ProviderSessionAttemptResult) error {
	receipt, err := server.sessions.BlackboardConclusionByDispatchRequestID(result.RequestID)
	if err != nil {
		return err
	}
	if receipt.SessionID != sessionID || receipt.SourceSessionID != result.SessionID || receipt.ControlTurnID != result.ProviderTurnID || receipt.BaseRevision == nil {
		return fmt.Errorf("Session Conclude Turn result correlation mismatch")
	}
	if result.Validated.Result.BaseRevision != *receipt.BaseRevision {
		server.sessionBlackboardConclusions.QueueFailure(result.RequestID, runtime.AssistedConclusionQueuedFailure{OwnerID: sessionID, ProviderSessionID: result.SessionID, ProviderTurnID: result.ProviderTurnID, Code: string(session.BlackboardConclusionErrorInvalidResult)})
		server.drainSessionBlackboardConclusionCallbacks(ctx, sessionID, result.RequestID)
		return nil
	}
	receipt, _, err = server.sessions.MarkBlackboardConclusionValidated(result.RequestID, result.Validated.CanonicalJSON)
	if err != nil {
		return err
	}
	validated, err := blackboardconclusion.Decode(receipt.CanonicalResultJSON)
	if err != nil {
		return err
	}
	batch, err := blackboardconclusion.Compile(validated.Result, receipt.ApplyIdempotencyKey)
	if err != nil {
		return err
	}
	applied, err := server.blackboardV2.ApplyForSessionContinuationAtRevision(ctx, sessionID, receipt.ContinuationID, *receipt.BaseRevision, batch)
	if err != nil {
		if isBlackboardConclusionBaseRevisionConflict(err) {
			return server.regenerateSessionBlackboardConclusionAfterVersionConflict(ctx, receipt)
		}
		var semanticErr *blackboardv2.Error
		if errors.As(err, &semanticErr) {
			code := session.BlackboardConclusionErrorInvalidResult
			if semanticErr.Code == "version_conflict" {
				code = session.BlackboardConclusionErrorVersionConflict
			}
			_, _, actionErr := server.sessions.MarkBlackboardConclusionApplyActionRequired(receipt.DispatchRequestID, code, time.Now().UTC(), blackboardConclusionRetryCooldown)
			if actionErr == nil {
				server.sessionBlackboardConclusions.ClearRequest(receipt.SessionID, receipt.DispatchRequestID)
			}
			return actionErr
		}
		return server.reconcileValidatedSessionBlackboardConclusionApply(ctx, receipt)
	}
	_, _, err = server.sessions.MarkBlackboardConclusionApplied(result.RequestID, applied.Revision)
	if err == nil {
		server.sessionBlackboardConclusions.ClearRequest(sessionID, result.RequestID)
	}
	return err
}

func (server *Server) regenerateSessionBlackboardConclusionAfterVersionConflict(ctx context.Context, receipt session.BlackboardConclusionReceipt) error {
	syncIntent, won, err := server.sessions.ClaimBlackboardConclusionVersionSync(receipt.DispatchRequestID)
	if err != nil {
		return err
	}
	if !won && syncIntent.InternalState != session.BlackboardConclusionReceiptVersionSyncRequested {
		return nil
	}
	if syncIntent.BaseRevision == nil {
		return fmt.Errorf("Session Blackboard conclusion has no base revision for synchronization")
	}
	attachment, err := server.blackboardV2.SynchronizeSessionContinuation(ctx, syncIntent.SessionID, syncIntent.ContinuationID, *syncIntent.BaseRevision)
	if err != nil {
		_, _, actionErr := server.sessions.MarkBlackboardConclusionApplyActionRequired(syncIntent.DispatchRequestID, session.BlackboardConclusionErrorInvalidResult, time.Now().UTC(), blackboardConclusionRetryCooldown)
		server.sessionBlackboardConclusions.ClearRequest(syncIntent.SessionID, syncIntent.DispatchRequestID)
		if actionErr != nil {
			return fmt.Errorf("synchronize Session Working Snapshot: %v; require operator action: %w", err, actionErr)
		}
		return err
	}
	regeneration, won, err := server.sessions.HandleBlackboardConclusionVersionConflict(syncIntent.DispatchRequestID, attachment.Revision, time.Now().UTC(), blackboardConclusionRetryCooldown)
	if err != nil {
		return err
	}
	server.sessionBlackboardConclusions.ClearRequest(syncIntent.SessionID, syncIntent.DispatchRequestID)
	if !won || regeneration.InternalState != session.BlackboardConclusionReceiptVersionRegenerationDispatchRequested {
		return nil
	}
	queued := server.enqueueProviderTaskControl(regeneration.SessionID, func(dispatchCtx context.Context) {
		if dispatchErr := server.sendSessionBlackboardConclusionTurn(dispatchCtx, regeneration, regenerateSessionBlackboardDirective(pointerValue(regeneration.BaseRevision))); dispatchErr != nil {
			server.requireSessionBlackboardConclusionRecovery(regeneration, dispatchErr)
		}
	})
	if !queued {
		server.requireSessionBlackboardConclusionRecovery(regeneration, fmt.Errorf("provider control queue is closed"))
	}
	return nil
}

func (server *Server) sendSessionBlackboardConclusionTurn(ctx context.Context, receipt session.BlackboardConclusionReceipt, directive string) error {
	provider, ok := server.sessionProviderSessions.get(receipt.SessionID)
	if !ok || provider.SessionID() != receipt.SourceSessionID {
		return fmt.Errorf("source Session provider session is not live")
	}
	if err := waitForBlackboardConclusionEligibility(ctx, receipt.NextEligibleAt, time.Now); err != nil {
		return err
	}
	receipt, won, err := server.sessions.MarkBlackboardConclusionSendStarted(receipt.DispatchRequestID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !won {
		return fmt.Errorf("Session Blackboard conclusion provider send was already started")
	}
	result, err := provider.SendTurn(ctx, runtime.ProviderSessionRequest{
		RequestID: receipt.DispatchRequestID, Message: directive,
		ModelProviderID: receipt.SourceSelection.ModelProviderID, Model: receipt.SourceSelection.Model,
		RequestedReasoningEffort: receipt.SourceSelection.ReasoningEffort, TurnKind: runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		if errors.Is(err, runtime.ErrProviderSessionControlConflict) {
			if _, _, markErr := server.sessions.MarkBlackboardConclusionWorkTurnConflict(receipt.DispatchRequestID, time.Now().UTC(), blackboardConclusionRetryCooldown); markErr != nil {
				return markErr
			}
			return nil
		}
		return err
	}
	if result.RequestID != receipt.DispatchRequestID || result.SessionID != receipt.SourceSessionID || strings.TrimSpace(result.ProviderTurnID) == "" {
		return fmt.Errorf("provider returned mismatched Session Blackboard conclusion Turn correlation")
	}
	_, _, err = server.sessions.MarkBlackboardConclusionAwaiting(receipt.DispatchRequestID, result.ProviderTurnID)
	if err == nil {
		server.drainSessionBlackboardConclusionCallbacks(ctx, receipt.SessionID, receipt.DispatchRequestID)
	}
	return err
}

func (server *Server) requireSessionBlackboardConclusionRecovery(receipt session.BlackboardConclusionReceipt, cause error) {
	if errors.Is(cause, context.Canceled) && !server.hasLiveProviderSessionContext(receipt.SessionID) {
		return
	}
	if _, _, err := server.sessions.MarkBlackboardConclusionRecoveryActionRequired(receipt.DispatchRequestID, time.Now().UTC(), blackboardConclusionRetryCooldown); err != nil {
		server.logger.Printf("assisted conclusion: Session recovery failed %s receipt %s: %v (dispatch error: %v)", receipt.SessionID, receipt.ID, err, cause)
	}
}

func (server *Server) hasLiveProviderSessionContext(sessionID string) bool {
	provider, ok := server.sessionProviderSessions.get(sessionID)
	return ok && provider != nil
}

func (server *Server) reconcileValidatedSessionBlackboardConclusionApplies() {
	receipts, err := server.sessions.ValidatedBlackboardConclusions()
	if err != nil {
		server.logger.Printf("assisted conclusion: list validated Session apply intents: %v", err)
		return
	}
	for _, receipt := range receipts {
		if err := server.reconcileValidatedSessionBlackboardConclusionApply(context.Background(), receipt); err != nil {
			server.logger.Printf("assisted conclusion: reconcile validated apply Session %s receipt %s: %v", receipt.SessionID, receipt.ID, err)
		}
	}
}

func (server *Server) reconcileValidatedSessionBlackboardConclusionApply(ctx context.Context, receipt session.BlackboardConclusionReceipt) error {
	if receipt.BaseRevision == nil || len(receipt.CanonicalResultJSON) == 0 || strings.TrimSpace(receipt.ApplyIdempotencyKey) == "" {
		_, _, err := server.sessions.MarkBlackboardConclusionApplyActionRequired(receipt.DispatchRequestID, session.BlackboardConclusionErrorInvalidResult, time.Now().UTC(), blackboardConclusionRetryCooldown)
		return err
	}
	validated, err := blackboardconclusion.Decode(receipt.CanonicalResultJSON)
	if err != nil || validated.Result.BaseRevision != *receipt.BaseRevision || validated.SHA256 != receipt.CanonicalResultSHA256 {
		_, _, actionErr := server.sessions.MarkBlackboardConclusionApplyActionRequired(receipt.DispatchRequestID, session.BlackboardConclusionErrorInvalidResult, time.Now().UTC(), blackboardConclusionRetryCooldown)
		return actionErr
	}
	batch, err := blackboardconclusion.Compile(validated.Result, receipt.ApplyIdempotencyKey)
	if err != nil {
		_, _, actionErr := server.sessions.MarkBlackboardConclusionApplyActionRequired(receipt.DispatchRequestID, session.BlackboardConclusionErrorInvalidResult, time.Now().UTC(), blackboardConclusionRetryCooldown)
		return actionErr
	}
	applied, err := server.blackboardV2.ApplyForSessionContinuationAtRevision(ctx, receipt.SessionID, receipt.ContinuationID, *receipt.BaseRevision, batch)
	if err == nil {
		_, _, markErr := server.sessions.MarkBlackboardConclusionApplied(receipt.DispatchRequestID, applied.Revision)
		if markErr == nil {
			server.sessionBlackboardConclusions.ClearRequest(receipt.SessionID, receipt.DispatchRequestID)
		}
		return markErr
	}
	if isBlackboardConclusionBaseRevisionConflict(err) {
		if provider, live := server.sessionProviderSessions.get(receipt.SessionID); live && provider.SessionID() == receipt.SourceSessionID {
			return server.regenerateSessionBlackboardConclusionAfterVersionConflict(ctx, receipt)
		}
		_, _, actionErr := server.sessions.MarkBlackboardConclusionApplyActionRequired(receipt.DispatchRequestID, session.BlackboardConclusionErrorVersionConflict, time.Now().UTC(), blackboardConclusionRetryCooldown)
		return actionErr
	}
	var semanticErr *blackboardv2.Error
	if errors.As(err, &semanticErr) && semanticErr.Code == "version_conflict" {
		_, _, actionErr := server.sessions.MarkBlackboardConclusionApplyActionRequired(receipt.DispatchRequestID, session.BlackboardConclusionErrorVersionConflict, time.Now().UTC(), blackboardConclusionRetryCooldown)
		return actionErr
	}
	_, _, actionErr := server.sessions.MarkBlackboardConclusionApplyActionRequired(receipt.DispatchRequestID, session.BlackboardConclusionErrorInvalidResult, time.Now().UTC(), blackboardConclusionRetryCooldown)
	return actionErr
}

func (server *Server) handleRetrySessionBlackboardConclusion(response http.ResponseWriter, request *http.Request) {
	sessionID := request.PathValue("id")
	if _, err := server.sessions.Get(sessionID); err != nil {
		writeSessionError(response, err)
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(response, http.StatusBadRequest, "Idempotency-Key is required")
		return
	}
	retried, won, err := server.sessions.RetryLatestBlackboardConclusion(sessionID, idempotencyKey, time.Now().UTC())
	if err != nil {
		if errors.Is(err, session.ErrBlackboardConclusionRetryCooldown) {
			writeError(response, http.StatusConflict, "Session Blackboard conclusion retry is not yet available")
			return
		}
		writeError(response, http.StatusConflict, "Session Blackboard conclusion cannot be retried")
		return
	}
	if won {
		if retried.InternalState == session.BlackboardConclusionReceiptPending {
			server.scheduleSessionBlackboardConclusionDispatch(retried)
		} else {
			queued := server.enqueueProviderTaskControl(sessionID, func(ctx context.Context) {
				if err := server.sendSessionBlackboardConclusionTurn(ctx, retried, repairSessionBlackboardDirective(pointerValue(retried.BaseRevision))); err != nil {
					server.requireSessionBlackboardConclusionRecovery(retried, err)
				}
			})
			if !queued {
				server.requireSessionBlackboardConclusionRecovery(retried, fmt.Errorf("provider control queue is closed"))
			}
		}
	}
	found, err := server.sessions.Get(sessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	detailed, err := server.decorateSession(found)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	status := http.StatusOK
	if won {
		status = http.StatusAccepted
	}
	writeJSON(response, status, detailed)
}
