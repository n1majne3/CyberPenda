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
	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

const blackboardConclusionRetryCooldown time.Duration = 0

func (server *Server) observeProviderSession(taskID, continuationID, sessionID string, lineage runtime.ProviderSessionTurnLineage, observation runtime.ProviderSessionObservation) {
	found, err := server.tasks.Get(taskID)
	if err != nil || found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted {
		return
	}
	observeAssistedConclusion(server.blackboardConclusions, taskID, continuationID, sessionID, lineage, observation, assistedConclusionObservationHooks{
		ControlFailure: func() { server.acceptBlackboardConclusionControlFailure(taskID, sessionID, lineage) },
		ControlTerminal: func(status string) {
			server.acceptBlackboardConclusionControlTerminal(taskID, sessionID, lineage, status)
		},
		OnLaterSourceWork: func(continuation string, watermarks runtime.AssistedConclusionObservedTurn) {
			server.invalidateTaskFinishIntentOnLaterSourceWork(found.ProjectID, continuation, taskID)
		},
		WorkCompleted: func(state runtime.AssistedConclusionObservedTurn, key runtime.AssistedConclusionTurnKey, status string) (bool, error) {
			receipt, inserted, err := server.tasks.RecordBlackboardConclusionCheckpoint(
				taskID, continuationID, lineage.RequestID, key.ProviderSessionID, key.TurnID,
				task.TurnSelection{ModelProviderID: lineage.ModelProviderID, Model: lineage.Model, ReasoningEffort: lineage.RequestedReasoningEffort},
				task.SemanticDebtWatermarks{SourceWork: state.SourceWorkWatermark, SemanticPersistence: state.SemanticPersistenceWatermark},
			)
			if err != nil {
				return false, err
			}
			// The Blackboard write protocol closes only when the Work Runtime Turn
			// has settled AND its Pending Blackboard Conclusion obligations are
			// terminal. A debt-free Turn is born clean at the checkpoint, so a
			// valid Finish Intent can settle immediately. A Turn with uncovered
			// semantic debt is born Pending and settles only after the conclusion
			// dispatch applies a terminal result (ADR 0022, criterion 3).
			if receipt.InternalState == task.BlackboardConclusionReceiptClean {
				server.settleTaskFinishIntentAfterApply(context.Background(), found.ProjectID, continuationID)
			}
			if inserted && receipt.InternalState == task.BlackboardConclusionReceiptPending && status != "completed" {
				_, _, err = server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(receipt.ID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), blackboardConclusionRetryCooldown)
				return true, err
			}
			if inserted && receipt.InternalState == task.BlackboardConclusionReceiptPending {
				server.scheduleBlackboardConclusionDispatch(receipt)
			}
			return true, nil
		},
		OnError: func(err error) {
			server.logger.Printf("assisted conclusion: record pending Task %s Turn %s (retained for retry): %v", taskID, observation.ProviderTurnID, err)
		},
	})
}

func (server *Server) scheduleBlackboardConclusionDispatch(receipt task.BlackboardConclusionReceipt) {
	queued := server.enqueueProviderTaskControl(receipt.TaskID, func(ctx context.Context) {
		if err := server.dispatchBlackboardConclusion(ctx, receipt); err != nil {
			server.requireBlackboardConclusionRecovery(receipt, task.ConclusionRecoveryDispatchFailed, err)
			server.logger.Printf("assisted conclusion: dispatch Task %s receipt %s: %v", receipt.TaskID, receipt.ID, err)
		}
	})
	if !queued {
		server.requireBlackboardConclusionRecovery(receipt, task.ConclusionRecoveryDispatchFailed, fmt.Errorf("provider control queue is closed"))
	}
}

func (server *Server) dispatchBlackboardConclusion(ctx context.Context, pending task.BlackboardConclusionReceipt) error {
	session, ok := server.providerSessions.get(pending.TaskID)
	if !ok || session.SessionID() != pending.SourceSessionID {
		return fmt.Errorf("source provider session is not live")
	}
	found, err := server.tasks.Get(pending.TaskID)
	if err != nil {
		return err
	}
	snapshot, err := server.blackboardV2.RuntimeSnapshot(ctx, found.ProjectID)
	if err != nil {
		return fmt.Errorf("read base Blackboard revision: %w", err)
	}
	receipt, won, err := server.tasks.ClaimBlackboardConclusionDispatch(pending.ID, snapshot.Revision)
	if err != nil {
		return err
	}
	if !won {
		return nil
	}
	return server.sendBlackboardConclusionTurn(ctx, receipt, concludeBlackboardDirective(snapshot.Revision))
}

func concludeBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`Stop security testing and perform only the Harness conclusion below.
Return exactly one JSON object (no markdown fences, no prose) with this shape and base_revision %d:
{"schema":"runtime-attempt-result/v1","base_revision":%d,"attempt":{"key":"attempt/example","create":true,"summary":"One sentence outcome of the completed work.","outcome":"inconclusive"},"tested_targets":[{"key":"objective/example","create_objective":{"objective":"What was tested."}}],"produced_targets":[]}
Conclude only the current source Work Turn. Do not restate an older terminal Attempt.
Use only existing Blackboard Keys and versions already present in the conversation from the completed source Work Turn. Copy them exactly; never change punctuation or switch between ':' and '/'. If an exact existing key and version are not already known, do not guess or look them up. Create a new descriptive slash-style Attempt or Objective key and use an inconclusive, failed, or blocked outcome without produced targets. A new key must not be a punctuation alias of a current or historical key.
Replace example keys and summaries with this Turn's real semantic targets.
Rules: outcome must be one of succeeded, failed, blocked, or inconclusive. Use inconclusive/failed/blocked when the Turn did not create durable produced graph targets. succeeded requires at least one produced_targets entry that references an already-existing Blackboard key with expected_version; do not invent produced_targets on an empty board.
Describe one Attempt and at least one tested target. Do not read files. Do not call tools, continue testing, include raw tool output or reasoning, finish the Task, or write the Blackboard directly.`, baseRevision, baseRevision)
}

func repairBlackboardDirective(baseRevision int, detail owner.ConclusionValidationDetail) string {
	directive := fmt.Sprintf(`Your previous Blackboard conclusion result was invalid.
Stop security testing and correct only that semantic result.
Return exactly one JSON object (no markdown fences, no prose) with schema runtime-attempt-result/v1 and base_revision %d.
If the board has no existing produced targets, use outcome "inconclusive" (or failed/blocked) with produced_targets [].
Example:
{"schema":"runtime-attempt-result/v1","base_revision":%d,"attempt":{"key":"attempt/example","create":true,"summary":"One sentence outcome of the completed work.","outcome":"inconclusive"},"tested_targets":[{"key":"objective/example","create_objective":{"objective":"What was tested."}}],"produced_targets":[]}
Conclude only the current source Work Turn. Use only existing Blackboard Keys and versions already present in the conversation. Copy them exactly; never change punctuation or switch between ':' and '/'. If an exact existing key and version are not already known, do not guess or look them up. Create a new descriptive slash-style Attempt or Objective key and use an inconclusive, failed, or blocked outcome without produced targets. Do not restate an older terminal Attempt.
Do not read files. Do not call tools, continue testing, include raw tool output or reasoning, finish the Task, or write the Blackboard directly.`, baseRevision, baseRevision)
	return conclusionValidationRepairLine(detail) + "\n" + directive
}

// conclusionValidationRepairLine renders the bounded public reason for one
// rejected closed result into a repair directive. The tokens are closed
// vocabulary and static expected forms; raw provider output never appears.
func conclusionValidationRepairLine(detail owner.ConclusionValidationDetail) string {
	if !detail.Valid() {
		return ""
	}
	line := "Validation: " + detail.Reason
	if detail.FieldPath != "" {
		line += " at " + detail.FieldPath
	}
	if detail.Expected != "" {
		line += ". Expected: " + detail.Expected
	}
	return line + "."
}

func regenerateBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`The Project Blackboard changed after your previous semantic result was produced.
Regenerate the semantic result against base_revision %d. Use only exact Blackboard Keys and versions already present in the conversation. If a required current version is not already known, create new descriptive slash-style Attempt and Objective keys and use an inconclusive, failed, or blocked outcome with no produced targets. Do not guess or look up current state.
Return exactly one JSON object with schema runtime-attempt-result/v1.
Do not read files. Do not call tools, continue testing, include raw tool output or reasoning, finish the Task, or write the Blackboard directly.`, baseRevision)
}

func (server *Server) blackboardConclusionCoordinator(taskID string) runtime.AssistedConclusionCoordinator {
	return runtime.AssistedConclusionCoordinator{
		OwnerID: taskID, Tracker: server.blackboardConclusions,
		LoadReceipt: func(requestID string) (runtime.AssistedConclusionReceiptView, error) {
			receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(requestID)
			if err != nil {
				return runtime.AssistedConclusionReceiptView{}, err
			}
			return runtime.AssistedConclusionReceiptView{
				OwnerID: receipt.TaskID, SourceSessionID: receipt.SourceSessionID,
				DispatchRequestID: receipt.DispatchRequestID, ControlTurnID: receipt.ControlTurnID,
				State: receipt.InternalState,
			}, nil
		},
		IsCanonical: func(requestID, providerSessionID, providerTurnID string) bool {
			return server.isCanonicalBlackboardConclusionCallback(taskID, requestID, providerSessionID, providerTurnID)
		},
		Enqueue: func(run func(context.Context)) bool {
			return server.enqueueProviderTaskControl(taskID, run)
		},
		EnqueueExisting: func(run func(context.Context)) bool {
			return server.enqueueExistingProviderTaskControl(taskID, run)
		},
		OnFailure: func(ctx context.Context, requestID string, failure runtime.AssistedConclusionQueuedFailure) error {
			return server.handleBlackboardConclusionFailure(taskID, requestID, task.BlackboardConclusionErrorCode(failure.Code), failure.Detail)
		},
		OnResult: func(ctx context.Context, result runtime.ProviderSessionAttemptResult) error {
			return server.applyBlackboardConclusionResult(ctx, taskID, result)
		},
		OnError: func(err error) {
			server.logger.Printf("assisted conclusion: Task %s callback coordination: %v", taskID, err)
		},
	}
}

func (server *Server) acceptBlackboardConclusionValidationFailure(taskID string, failure runtime.ProviderSessionAttemptResultValidationFailure) {
	server.blackboardConclusionCoordinator(taskID).AcceptValidationFailure(failure)
}

func (server *Server) acceptBlackboardConclusionControlFailure(taskID, sessionID string, lineage runtime.ProviderSessionTurnLineage) {
	server.blackboardConclusionCoordinator(taskID).AcceptControlFailure(sessionID, lineage)
}

func (server *Server) acceptBlackboardConclusionControlTerminal(taskID, sessionID string, lineage runtime.ProviderSessionTurnLineage, status string) {
	server.blackboardConclusionCoordinator(taskID).AcceptControlTerminal(sessionID, lineage, status)
}

func (server *Server) drainBlackboardConclusionCallbacks(ctx context.Context, taskID, requestID string) {
	_ = server.blackboardConclusionCoordinator(taskID).Drain(ctx, requestID)
}

func (server *Server) handleBlackboardConclusionFailure(taskID, requestID string, code task.BlackboardConclusionErrorCode, detail task.ConclusionValidationDetail) error {
	receipt, dispatchRepair, err := server.tasks.HandleBlackboardConclusionFailure(
		requestID, code, detail, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	if err != nil {
		return err
	}
	if dispatchRepair && receipt.InternalState == task.BlackboardConclusionReceiptRepairDispatchRequested {
		queued := server.enqueueProviderTaskControl(taskID, func(ctx context.Context) {
			if err := server.dispatchBlackboardConclusionRepair(ctx, receipt); err != nil {
				server.recoverBlackboardConclusionDispatchFailure(receipt, err)
			}
		})
		if !queued {
			server.recoverBlackboardConclusionDispatchFailure(receipt, fmt.Errorf("provider control queue is closed"))
		}
	}
	return nil
}

func (server *Server) dispatchBlackboardConclusionRepair(ctx context.Context, receipt task.BlackboardConclusionReceipt) error {
	if receipt.BaseRevision == nil {
		return fmt.Errorf("Blackboard conclusion repair has no base revision")
	}
	return server.sendBlackboardConclusionTurn(ctx, receipt, repairBlackboardDirective(*receipt.BaseRevision, conclusionDetailFromTaskReceipt(receipt)))
}

func conclusionDetailFromTaskReceipt(receipt task.BlackboardConclusionReceipt) owner.ConclusionValidationDetail {
	return owner.ConclusionValidationDetail{
		Reason: receipt.ValidationReason, FieldPath: receipt.ValidationFieldPath, Expected: receipt.ValidationExpected,
	}
}

func waitForBlackboardConclusionEligibility(ctx context.Context, eligibleAt *time.Time, now func() time.Time) error {
	if eligibleAt == nil {
		return nil
	}
	delay := eligibleAt.Sub(now().UTC())
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (server *Server) handleRetryBlackboardConclusion(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	found, err := server.tasks.Get(taskID)
	if err != nil || found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(response, http.StatusBadRequest, "Idempotency-Key is required")
		return
	}
	latest, err := server.tasks.LatestBlackboardConclusion(taskID)
	if err != nil || latest == nil {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if latest.RecoveryReason == string(task.ConclusionRecoveryAcceptanceAmbiguous) {
		// An acceptance-ambiguous provider delivery is never resent: a generic
		// Retry could duplicate a request the provider already accepted.
		writeError(response, http.StatusConflict, "Blackboard conclusion cannot be retried after an acceptance-ambiguous delivery")
		return
	}
	// A dispatch_failed obligation can outlive the Runtime Continuation that
	// owned its last immutable dispatch. When this daemon currently owns a live
	// provider session and storage confirms the active Continuation carries that
	// exact native session, bind the NEW retry dispatch to the live replacement.
	// Other recovery reasons keep their reason-specific fail-closed policy.
	replacementContinuationID, replacementSessionID := "", ""
	replacementBaseRevision := -1
	if latest.RecoveryReason == string(task.ConclusionRecoveryDispatchFailed) {
		session, live := server.providerSessions.get(taskID)
		active, activeErr := server.tasks.ActiveContinuation(taskID)
		if !live || session == nil || activeErr != nil || active == nil ||
			strings.TrimSpace(active.NativeSessionID) != session.SessionID() {
			writeError(response, http.StatusConflict, "Blackboard conclusion retry requires a proven live Runtime")
			return
		}
		if latest.DispatchRequestID == "" || latest.ContinuationID != active.ID || latest.SourceSessionID != session.SessionID() {
			snapshot, snapshotErr := server.blackboardV2.RuntimeSnapshot(request.Context(), found.ProjectID)
			if snapshotErr != nil {
				writeError(response, http.StatusConflict, "Blackboard conclusion retry cannot resolve the current Blackboard revision")
				return
			}
			replacementContinuationID, replacementSessionID = active.ID, session.SessionID()
			replacementBaseRevision = snapshot.Revision
		}
	}
	retried, won, err := server.tasks.RetryLatestBlackboardConclusionForRuntime(
		taskID, idempotencyKey, replacementContinuationID, replacementSessionID, replacementBaseRevision, time.Now().UTC(),
	)
	if err != nil {
		if errors.Is(err, task.ErrBlackboardConclusionRetryCooldown) {
			writeError(response, http.StatusConflict, "Blackboard conclusion retry is not yet available")
			return
		}
		writeError(response, http.StatusConflict, "Blackboard conclusion cannot be retried")
		return
	}
	if won {
		if retried.InternalState == task.BlackboardConclusionReceiptPending {
			server.scheduleBlackboardConclusionDispatch(retried)
		} else {
			queued := server.enqueueProviderTaskControl(taskID, func(ctx context.Context) {
				if err := server.dispatchBlackboardConclusionRepair(ctx, retried); err != nil {
					server.recoverBlackboardConclusionDispatchFailure(retried, err)
				}
			})
			if !queued {
				server.recoverBlackboardConclusionDispatchFailure(retried, fmt.Errorf("provider control queue is closed"))
			}
		}
	}
	detailed, err := server.taskDetail(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	status := http.StatusOK
	if won {
		status = http.StatusAccepted
	}
	writeJSON(response, status, detailed)
}

func (server *Server) recoverBlackboardConclusionDispatchFailure(receipt task.BlackboardConclusionReceipt, cause error) {
	if errors.Is(cause, context.Canceled) && !server.hasLiveProviderTaskContext(receipt.TaskID) {
		return
	}
	_, _, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequired(
		receipt.DispatchRequestID, task.ConclusionRecoveryDispatchFailed, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	if err != nil {
		server.logger.Printf("assisted conclusion: recover failed dispatch Task %s receipt %s: %v (dispatch error: %v)", receipt.TaskID, receipt.ID, err, cause)
		return
	}
	server.logger.Printf("assisted conclusion: dispatch requires operator action Task %s receipt %s: %v", receipt.TaskID, receipt.ID, cause)
}

// recoverConclusionObligationsForReplacedContinuation creates a NEW Conclusion
// Dispatch bound to the replacement Continuation + session for every in-flight
// assisted-conclusion obligation of a Task whose active dispatch is bound to
// the old (now terminal) Continuation, after an interrupt_then_replace native
// steer or a writable-continuation recovery (#197). Historical dispatches are
// superseded, never rewritten: each recovery dispatch keeps its own immutable
// binding and deterministic request lineage (ADR 0021). Errors are logged, not
// fatal: a recovery failure must not block the steer.
func (server *Server) recoverConclusionObligationsForReplacedContinuation(taskID, oldContinuationID, replacementContinuationID, replacementSessionID string) {
	dispatches, err := server.tasks.CreateRecoveryConclusionDispatches(taskID, oldContinuationID, replacementContinuationID, replacementSessionID)
	if err != nil {
		server.logger.Printf("assisted conclusion: recover in-flight obligations Task %s old=%s replacement=%s: %v", taskID, oldContinuationID, replacementContinuationID, err)
		return
	}
	for _, view := range dispatches {
		server.scheduleRecoveredConclusionDispatch(view)
	}
}

// scheduleRecoveredConclusionDispatch enqueues a pre-send recovery Conclusion
// Dispatch for its owner so the control turn is delivered on the replacement
// runtime. A recovery dispatch is always pre-send (send fence cleared), so it
// is safe to send; acceptance-ambiguous post-fence dispatches are never
// replayed automatically.
func (server *Server) scheduleRecoveredConclusionDispatch(view task.BlackboardConclusionReceipt) {
	queued := server.enqueueProviderTaskControl(view.TaskID, func(ctx context.Context) {
		if err := server.dispatchRecoveredConclusionDispatch(ctx, view); err != nil {
			server.recoverBlackboardConclusionDispatchFailure(view, err)
		}
	})
	if !queued {
		server.recoverBlackboardConclusionDispatchFailure(view, fmt.Errorf("provider control queue is closed"))
	}
}

func (server *Server) dispatchRecoveredConclusionDispatch(ctx context.Context, view task.BlackboardConclusionReceipt) error {
	directive := concludeBlackboardDirective(pointerValue(view.BaseRevision))
	switch view.InternalState {
	case task.BlackboardConclusionReceiptRepairDispatchRequested:
		directive = repairBlackboardDirective(pointerValue(view.BaseRevision), conclusionDetailFromTaskReceipt(view))
	case task.BlackboardConclusionReceiptVersionRegenerationDispatchRequested:
		directive = regenerateBlackboardDirective(pointerValue(view.BaseRevision))
	default:
		if view.ExplicitRetryCount > 0 {
			directive = repairBlackboardDirective(pointerValue(view.BaseRevision), conclusionDetailFromTaskReceipt(view))
		}
	}
	return server.sendBlackboardConclusionTurn(ctx, view, directive)
}

func (server *Server) reconcileValidatedBlackboardConclusionApplies() {
	receipts, err := server.tasks.ValidatedBlackboardConclusions()
	if err != nil {
		server.logger.Printf("assisted conclusion: list validated apply intents: %v", err)
		return
	}
	for _, receipt := range receipts {
		if err := server.reconcileValidatedBlackboardConclusionApply(context.Background(), receipt); err != nil {
			server.logger.Printf("assisted conclusion: reconcile validated apply Task %s receipt %s: %v", receipt.TaskID, receipt.ID, err)
		}
	}
}

func (server *Server) reconcileValidatedBlackboardConclusionApply(ctx context.Context, receipt task.BlackboardConclusionReceipt) error {
	if receipt.BaseRevision == nil || len(receipt.CanonicalResultJSON) == 0 || strings.TrimSpace(receipt.ApplyIdempotencyKey) == "" {
		return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorInvalidResult)
	}
	validated, err := blackboardconclusion.Decode(receipt.CanonicalResultJSON)
	if err != nil || validated.Result.BaseRevision != *receipt.BaseRevision ||
		validated.SHA256 != receipt.CanonicalResultSHA256 {
		return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorInvalidResult)
	}
	batch, err := blackboardconclusion.Compile(validated.Result, receipt.ApplyIdempotencyKey)
	if err != nil {
		return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorInvalidResult)
	}
	found, err := server.tasks.Get(receipt.TaskID)
	if err != nil || found.ProjectID == "" {
		return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorInvalidResult)
	}
	if err := server.rejectBlackboardConclusionKeyAliases(ctx, found.ProjectID, validated.Result); err != nil {
		return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorInvalidResult)
	}
	applied, err := server.blackboardV2.ApplyForContinuationAtRevision(
		ctx, found.ProjectID, receipt.ContinuationID, *receipt.BaseRevision, batch,
	)
	if err == nil {
		_, _, markErr := server.tasks.MarkBlackboardConclusionApplied(receipt.DispatchRequestID, applied.Revision)
		if markErr == nil {
			server.blackboardConclusions.ClearRequest(receipt.TaskID, receipt.DispatchRequestID)
			server.settleTaskFinishIntentAfterApply(ctx, found.ProjectID, receipt.ContinuationID)
		}
		return markErr
	}
	if isBlackboardConclusionBaseRevisionConflict(err) {
		if session, live := server.providerSessions.get(receipt.TaskID); live && session.SessionID() == receipt.SourceSessionID {
			return server.regenerateBlackboardConclusionAfterVersionConflict(ctx, found.ProjectID, receipt)
		}
		return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorVersionConflict)
	}
	var semanticErr *blackboardv2.Error
	if errors.As(err, &semanticErr) && semanticErr.Code == "version_conflict" {
		return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorVersionConflict)
	}
	return server.failValidatedBlackboardConclusionApply(receipt, task.BlackboardConclusionErrorInvalidResult)
}

func (server *Server) failValidatedBlackboardConclusionApply(receipt task.BlackboardConclusionReceipt, code task.BlackboardConclusionErrorCode) error {
	_, _, err := server.tasks.MarkBlackboardConclusionApplyActionRequired(
		receipt.DispatchRequestID, code, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	return err
}

func (server *Server) acceptBlackboardConclusionResult(taskID string, result runtime.ProviderSessionAttemptResult) {
	server.blackboardConclusionCoordinator(taskID).AcceptResult(result)
}

func (server *Server) isCanonicalBlackboardConclusionCallback(taskID, requestID, sessionID, providerTurnID string) bool {
	session, ok := server.providerSessions.get(taskID)
	if !ok || session.SessionID() != strings.TrimSpace(sessionID) {
		return false
	}
	resolver, ok := session.(runtime.ProviderSessionCompleteTurnLineageResolver)
	if !ok {
		return false
	}
	lineage, resolved := resolver.ResolveProviderSessionTurnLineage(requestID, providerTurnID)
	return resolved && lineage.Kind == runtime.RuntimeTurnKindControl &&
		lineage.RequestID == strings.TrimSpace(requestID) && lineage.ProviderTurnID == strings.TrimSpace(providerTurnID)
}

func (server *Server) applyBlackboardConclusionResult(ctx context.Context, taskID string, result runtime.ProviderSessionAttemptResult) error {
	receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(result.RequestID)
	if err != nil {
		return err
	}
	if receipt.TaskID != taskID || receipt.SourceSessionID != result.SessionID || receipt.ControlTurnID != result.ProviderTurnID || receipt.BaseRevision == nil {
		return fmt.Errorf("Conclude Turn result correlation mismatch")
	}
	if result.Validated.Result.BaseRevision != *receipt.BaseRevision {
		server.blackboardConclusions.QueueFailure(result.RequestID, runtime.AssistedConclusionQueuedFailure{
			OwnerID: taskID, ProviderSessionID: result.SessionID, ProviderTurnID: result.ProviderTurnID,
			Code: string(task.BlackboardConclusionErrorInvalidResult),
			Detail: owner.ConclusionValidationDetail{
				Reason: string(blackboardconclusion.ValidationReasonBaseRevisionMismatch), FieldPath: "base_revision",
				Expected: fmt.Sprintf("base_revision must equal the receipt's claimed revision %d", *receipt.BaseRevision),
			},
		})
		server.drainBlackboardConclusionCallbacks(ctx, taskID, result.RequestID)
		return nil
	}
	receipt, _, err = server.tasks.MarkBlackboardConclusionValidated(result.RequestID, result.Validated.CanonicalJSON)
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
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return err
	}
	if err := server.rejectBlackboardConclusionKeyAliases(ctx, found.ProjectID, validated.Result); err != nil {
		_, _, actionErr := server.tasks.MarkBlackboardConclusionApplyActionRequired(
			receipt.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, time.Now().UTC(), blackboardConclusionRetryCooldown,
		)
		if actionErr == nil {
			server.blackboardConclusions.ClearRequest(receipt.TaskID, receipt.DispatchRequestID)
		}
		return actionErr
	}
	applied, err := server.blackboardV2.ApplyForContinuationAtRevision(ctx, found.ProjectID, receipt.ContinuationID, *receipt.BaseRevision, batch)
	if err != nil {
		if isBlackboardConclusionBaseRevisionConflict(err) {
			return server.regenerateBlackboardConclusionAfterVersionConflict(ctx, found.ProjectID, receipt)
		}
		var semanticErr *blackboardv2.Error
		if errors.As(err, &semanticErr) {
			code := task.BlackboardConclusionErrorInvalidResult
			if semanticErr.Code == "version_conflict" {
				code = task.BlackboardConclusionErrorVersionConflict
			}
			_, _, actionErr := server.tasks.MarkBlackboardConclusionApplyActionRequired(
				receipt.DispatchRequestID, code, time.Now().UTC(), blackboardConclusionRetryCooldown,
			)
			if actionErr == nil {
				server.blackboardConclusions.ClearRequest(receipt.TaskID, receipt.DispatchRequestID)
			}
			return actionErr
		}
		// A non-semantic error may be publication loss after the Blackboard
		// transaction committed. Replay the durable validated intent exactly
		// once so service idempotency can recover that acknowledgement window.
		return server.reconcileValidatedBlackboardConclusionApply(ctx, receipt)
	}
	_, _, err = server.tasks.MarkBlackboardConclusionApplied(result.RequestID, applied.Revision)
	if err == nil {
		server.blackboardConclusions.ClearRequest(taskID, result.RequestID)
		// The obligation reached terminal-clean. If a valid Blackboard Finish
		// Intent exists and the Work Turn settled, close the Blackboard write
		// protocol now. This path also covers daemon restart recovery, where the
		// observer does not run (ADR 0022).
		server.settleTaskFinishIntentAfterApply(ctx, found.ProjectID, receipt.ContinuationID)
	}
	return err
}

func (server *Server) rejectBlackboardConclusionKeyAliases(ctx context.Context, projectID string, result blackboardconclusion.RuntimeAttemptResult) error {
	createdKeys := make([]string, 0, 1+len(result.TestedTargets))
	if result.Attempt.Create {
		createdKeys = append(createdKeys, result.Attempt.Key)
	}
	for _, target := range result.TestedTargets {
		if target.CreateObjective != nil {
			createdKeys = append(createdKeys, target.Key)
		}
	}
	for _, key := range createdKeys {
		alias := blackboardKeySeparatorAlias(key)
		if alias == "" {
			continue
		}
		exists, err := server.blackboardV2.HasSemanticKey(ctx, projectID, alias)
		if err != nil {
			return err
		}
		if exists {
			return &blackboardv2.Error{Code: "key_conflict", Path: "key", Message: "a separator alias of the proposed Blackboard Key already exists"}
		}
	}
	return nil
}

func blackboardKeySeparatorAlias(key string) string {
	for _, prefix := range []string{"attempt", "objective"} {
		if strings.HasPrefix(key, prefix+":") {
			return prefix + "/" + strings.TrimPrefix(key, prefix+":")
		}
		if strings.HasPrefix(key, prefix+"/") {
			return prefix + ":" + strings.TrimPrefix(key, prefix+"/")
		}
	}
	return ""
}

func isBlackboardConclusionBaseRevisionConflict(err error) bool {
	var semanticErr *blackboardv2.Error
	return errors.As(err, &semanticErr) && semanticErr.Code == "version_conflict" && semanticErr.Path == "base_revision"
}

func (server *Server) regenerateBlackboardConclusionAfterVersionConflict(ctx context.Context, projectID string, receipt task.BlackboardConclusionReceipt) error {
	syncIntent, won, err := server.tasks.ClaimBlackboardConclusionVersionSync(receipt.DispatchRequestID)
	if err != nil {
		return err
	}
	if !won && syncIntent.InternalState != task.BlackboardConclusionReceiptVersionSyncRequested {
		return nil
	}
	attachment, err := server.blackboardV2.SynchronizeContinuation(
		ctx, projectID, syncIntent.TaskID, syncIntent.ContinuationID, *syncIntent.BaseRevision,
	)
	if err != nil {
		if _, _, actionErr := server.tasks.MarkBlackboardConclusionApplyActionRequired(
			syncIntent.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, time.Now().UTC(), blackboardConclusionRetryCooldown,
		); actionErr != nil {
			return fmt.Errorf("synchronize Working Blackboard Snapshot for conclusion regeneration: %v; require operator action: %w", err, actionErr)
		}
		server.blackboardConclusions.ClearRequest(syncIntent.TaskID, syncIntent.DispatchRequestID)
		return fmt.Errorf("synchronize Working Blackboard Snapshot for conclusion regeneration: %w", err)
	}
	regeneration, won, err := server.tasks.HandleBlackboardConclusionVersionConflict(
		syncIntent.DispatchRequestID, attachment.Revision, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	if err != nil {
		return err
	}
	server.blackboardConclusions.ClearRequest(syncIntent.TaskID, syncIntent.DispatchRequestID)
	if !won || regeneration.InternalState != task.BlackboardConclusionReceiptVersionRegenerationDispatchRequested {
		return nil
	}
	queued := server.enqueueProviderTaskControl(receipt.TaskID, func(dispatchCtx context.Context) {
		if dispatchErr := server.dispatchBlackboardConclusionVersionRegeneration(dispatchCtx, regeneration); dispatchErr != nil {
			server.recoverBlackboardConclusionDispatchFailure(regeneration, dispatchErr)
		}
	})
	if !queued {
		server.recoverBlackboardConclusionDispatchFailure(regeneration, fmt.Errorf("provider control queue is closed"))
	}
	return nil
}

func (server *Server) dispatchBlackboardConclusionVersionRegeneration(ctx context.Context, receipt task.BlackboardConclusionReceipt) error {
	if receipt.BaseRevision == nil {
		return fmt.Errorf("Blackboard conclusion regeneration has no base revision")
	}
	return server.sendBlackboardConclusionTurn(ctx, receipt, regenerateBlackboardDirective(*receipt.BaseRevision))
}

func (server *Server) sendBlackboardConclusionTurn(ctx context.Context, receipt task.BlackboardConclusionReceipt, directive string) error {
	session, ok := server.providerSessions.get(receipt.TaskID)
	if !ok || session.SessionID() != receipt.SourceSessionID {
		return fmt.Errorf("source provider session is not live")
	}
	if err := waitForBlackboardConclusionEligibility(ctx, receipt.NextEligibleAt, time.Now); err != nil {
		return err
	}
	var won bool
	var err error
	receipt, won, err = server.tasks.MarkBlackboardConclusionSendStarted(receipt.DispatchRequestID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !won {
		return fmt.Errorf("Blackboard conclusion provider send was already started")
	}
	result, err := session.SendTurn(ctx, runtime.ProviderSessionRequest{
		RequestID:                receipt.DispatchRequestID,
		Message:                  directive,
		ModelProviderID:          receipt.SourceSelection.ModelProviderID,
		Model:                    receipt.SourceSelection.Model,
		RequestedReasoningEffort: receipt.SourceSelection.ReasoningEffort,
		TurnKind:                 runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		if errors.Is(err, runtime.ErrProviderSessionControlConflict) {
			// A non-yielding work turn holds the single active call, so the
			// control turn cannot be delivered right now. Record the bounded
			// conflict here (staying operator-retryable until the budget is
			// exhausted, then becoming the non-retryable never-settled terminal)
			// and report success so the generic recovery callers do not re-mark
			// this as the recoverable runtime-recovery code.
			if _, _, markErr := server.tasks.MarkBlackboardConclusionWorkTurnConflict(receipt.DispatchRequestID, time.Now().UTC(), blackboardConclusionRetryCooldown); markErr != nil {
				return markErr
			}
			return nil
		}
		return err
	}
	if result.RequestID != receipt.DispatchRequestID || result.SessionID != receipt.SourceSessionID || strings.TrimSpace(result.ProviderTurnID) == "" {
		return fmt.Errorf("provider returned mismatched Blackboard conclusion Turn correlation")
	}
	_, _, err = server.tasks.MarkBlackboardConclusionAwaiting(receipt.DispatchRequestID, result.ProviderTurnID)
	if err == nil {
		server.drainBlackboardConclusionCallbacks(ctx, receipt.TaskID, receipt.DispatchRequestID)
	}
	return err
}

func (server *Server) attachBlackboardConclusion(found task.Task) (task.Task, error) {
	view := task.BlackboardConclusion{
		Mode:  found.RunControls.BlackboardConclusionMode,
		State: task.BlackboardConclusionStateClean,
	}
	receipt, err := server.tasks.LatestBlackboardConclusion(found.ID)
	if err != nil {
		return task.Task{}, err
	}
	if receipt != nil {
		view = receipt.View(found.RunControls.BlackboardConclusionMode)
	}
	// A recorded-but-unsettled Blackboard Finish Intent keeps the public
	// conclusion state non-clean: the Continuation has not actually closed while
	// the Work Runtime Turn can still produce work (ADR 0022, criterion 4).
	if found.RunControls.BlackboardConclusionMode == task.BlackboardConclusionModeAssisted && view.State == task.BlackboardConclusionStateClean {
		if server.hasUnsettledTaskFinishIntent(found.ID) {
			view.State = task.BlackboardConclusionStatePending
		}
	}
	found.BlackboardConclusion = view
	return found, nil
}
