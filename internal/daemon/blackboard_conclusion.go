package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

type blackboardConclusionTurnKey struct {
	taskID         string
	continuationID string
	sessionID      string
	turnID         string
}

type blackboardConclusionObservedTurn struct {
	sourceWorkWatermark          int
	semanticPersistenceWatermark int
	completedToolCalls           map[string]struct{}
}

// blackboardConclusionTracker retains only in-flight, bounded observation
// watermarks. The durable receipt created at the completed Turn boundary is
// the source of truth exposed by Task APIs.
type blackboardConclusionTracker struct {
	mu       sync.Mutex
	turns    map[blackboardConclusionTurnKey]blackboardConclusionObservedTurn
	results  map[string]queuedBlackboardConclusionResult
	failures map[string]queuedBlackboardConclusionFailure
	terminal map[string]queuedBlackboardConclusionTerminal
}

type queuedBlackboardConclusionResult struct {
	taskID string
	result runtime.ProviderSessionAttemptResult
}

type queuedBlackboardConclusionFailure struct {
	taskID         string
	sessionID      string
	providerTurnID string
	code           task.BlackboardConclusionErrorCode
}

type queuedBlackboardConclusionTerminal struct {
	taskID         string
	sessionID      string
	providerTurnID string
}

const blackboardConclusionRetryCooldown time.Duration = 0

func newBlackboardConclusionTracker() *blackboardConclusionTracker {
	return &blackboardConclusionTracker{
		turns:    make(map[blackboardConclusionTurnKey]blackboardConclusionObservedTurn),
		results:  make(map[string]queuedBlackboardConclusionResult),
		failures: make(map[string]queuedBlackboardConclusionFailure),
		terminal: make(map[string]queuedBlackboardConclusionTerminal),
	}
}

func (tracker *blackboardConclusionTracker) deleteTask(taskID string) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for key := range tracker.turns {
		if key.taskID == taskID {
			delete(tracker.turns, key)
		}
	}
	for requestID, queued := range tracker.results {
		if queued.taskID == taskID {
			delete(tracker.results, requestID)
		}
	}
	for requestID, queued := range tracker.failures {
		if queued.taskID == taskID {
			delete(tracker.failures, requestID)
		}
	}
	for requestID, queued := range tracker.terminal {
		if queued.taskID == taskID {
			delete(tracker.terminal, requestID)
		}
	}
}

func (tracker *blackboardConclusionTracker) queueResult(taskID string, result runtime.ProviderSessionAttemptResult) {
	tracker.mu.Lock()
	if _, exists := tracker.results[result.RequestID]; !exists {
		tracker.results[result.RequestID] = queuedBlackboardConclusionResult{taskID: taskID, result: result}
	}
	tracker.mu.Unlock()
}

func (tracker *blackboardConclusionTracker) takeActionableResult(taskID, requestID string) (runtime.ProviderSessionAttemptResult, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	queued, ok := tracker.results[requestID]
	if !ok || queued.taskID != taskID {
		return runtime.ProviderSessionAttemptResult{}, false
	}
	terminal := tracker.terminal[requestID]
	if terminal.taskID != taskID || terminal.sessionID != queued.result.SessionID || terminal.providerTurnID != queued.result.ProviderTurnID {
		return runtime.ProviderSessionAttemptResult{}, false
	}
	delete(tracker.results, requestID)
	return queued.result, true
}

func (tracker *blackboardConclusionTracker) queueFailure(requestID string, failure queuedBlackboardConclusionFailure) {
	tracker.mu.Lock()
	prior, exists := tracker.failures[requestID]
	if !exists || prior == failure || blackboardConclusionFailurePriority(failure.code) > blackboardConclusionFailurePriority(prior.code) {
		tracker.failures[requestID] = failure
	}
	tracker.mu.Unlock()
}

func blackboardConclusionFailurePriority(code task.BlackboardConclusionErrorCode) int {
	if code == task.BlackboardConclusionErrorToolUseForbidden {
		return 3
	}
	if code == task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		return 2
	}
	return 1
}

func (tracker *blackboardConclusionTracker) takeActionableFailure(taskID, requestID string) (queuedBlackboardConclusionFailure, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	queued, ok := tracker.failures[requestID]
	if !ok || queued.taskID != taskID {
		return queuedBlackboardConclusionFailure{}, false
	}
	terminal := tracker.terminal[requestID]
	terminalMatches := terminal.taskID == queued.taskID && terminal.sessionID == queued.sessionID && terminal.providerTurnID == queued.providerTurnID
	if queued.code != task.BlackboardConclusionErrorToolUseForbidden && !terminalMatches {
		return queuedBlackboardConclusionFailure{}, false
	}
	return queued, true
}

func (tracker *blackboardConclusionTracker) markTerminal(requestID string, terminal queuedBlackboardConclusionTerminal) {
	tracker.mu.Lock()
	tracker.terminal[requestID] = terminal
	tracker.mu.Unlock()
}

func (tracker *blackboardConclusionTracker) clearRequest(requestID string) {
	tracker.mu.Lock()
	delete(tracker.results, requestID)
	delete(tracker.failures, requestID)
	delete(tracker.terminal, requestID)
	tracker.mu.Unlock()
}

func (server *Server) observeProviderSession(taskID, continuationID, sessionID string, lineage runtime.ProviderSessionTurnLineage, observation runtime.ProviderSessionObservation) {
	found, err := server.tasks.Get(taskID)
	if err != nil || found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted {
		return
	}
	if strings.TrimSpace(continuationID) == "" {
		return
	}
	if lineage.Kind == runtime.RuntimeTurnKindControl {
		switch observation.Kind {
		case runtime.ProviderSessionObservationToolUse, runtime.ProviderSessionObservationToolResult:
			server.acceptBlackboardConclusionControlFailure(taskID, sessionID, lineage)
		case runtime.ProviderSessionObservationTurnCompleted:
			server.acceptBlackboardConclusionControlTerminal(taskID, sessionID, lineage, observation.Status)
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
	key := blackboardConclusionTurnKey{
		taskID: taskID, continuationID: continuationID,
		sessionID: strings.TrimSpace(sessionID), turnID: turnID,
	}

	server.blackboardConclusions.mu.Lock()
	if observation.Kind == runtime.ProviderSessionObservationToolResult {
		for observedKey := range server.blackboardConclusions.turns {
			if observedKey.taskID == key.taskID && observedKey.continuationID == key.continuationID &&
				observedKey.sessionID == key.sessionID && observedKey.turnID != key.turnID {
				delete(server.blackboardConclusions.turns, observedKey)
			}
		}
	}
	state := server.blackboardConclusions.turns[key]
	switch observation.Kind {
	case runtime.ProviderSessionObservationToolResult:
		if state.completedToolCalls == nil {
			state.completedToolCalls = make(map[string]struct{})
		}
		toolCallID := strings.TrimSpace(observation.ToolCallID)
		if _, duplicate := state.completedToolCalls[toolCallID]; !duplicate {
			state.completedToolCalls[toolCallID] = struct{}{}
			toolSemantics, trusted := classifyTrustedBlackboardTool(observation.ToolName)
			switch {
			case !trusted:
				// Failed work still changes what the runtime learned and must be
				// represented by a later semantic conclusion.
				state.sourceWorkWatermark++
			case observation.Status == "succeeded" && toolSemantics == blackboardToolSemanticPersistence:
				state.semanticPersistenceWatermark = state.sourceWorkWatermark
			}
			server.blackboardConclusions.turns[key] = state
		}
		server.blackboardConclusions.mu.Unlock()
		return
	case runtime.ProviderSessionObservationTurnCompleted:
	default:
		server.blackboardConclusions.mu.Unlock()
		return
	}
	server.blackboardConclusions.mu.Unlock()

	if len(state.completedToolCalls) == 0 {
		server.blackboardConclusions.mu.Lock()
		delete(server.blackboardConclusions.turns, key)
		server.blackboardConclusions.mu.Unlock()
		return
	}
	receipt, inserted, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		taskID, continuationID, lineage.RequestID, key.sessionID, turnID, task.TurnSelection{
			ModelProviderID: lineage.ModelProviderID,
			Model:           lineage.Model,
			ReasoningEffort: lineage.RequestedReasoningEffort,
		}, task.SemanticDebtWatermarks{
			SourceWork: state.sourceWorkWatermark, SemanticPersistence: state.semanticPersistenceWatermark,
		},
	)
	if err != nil {
		// Retain the bounded watermark so duplicate provider completion delivery
		// can retry the idempotent durable receipt after a transient Store error.
		server.logger.Printf("assisted conclusion: record pending Task %s Turn %s (retained for retry): %v", taskID, turnID, err)
		return
	}
	server.blackboardConclusions.mu.Lock()
	delete(server.blackboardConclusions.turns, key)
	server.blackboardConclusions.mu.Unlock()
	if inserted && receipt.InternalState == task.BlackboardConclusionReceiptPending && observation.Status != "completed" {
		if _, _, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
			receipt.ID, time.Now().UTC(), blackboardConclusionRetryCooldown,
		); err != nil {
			server.logger.Printf("assisted conclusion: mark failed Work attention Task %s Turn %s: %v", taskID, turnID, err)
		}
		return
	}
	if inserted && receipt.InternalState == task.BlackboardConclusionReceiptPending {
		server.scheduleBlackboardConclusionDispatch(receipt)
	}
}

func (server *Server) scheduleBlackboardConclusionDispatch(receipt task.BlackboardConclusionReceipt) {
	queued := server.enqueueProviderTaskControl(receipt.TaskID, func(ctx context.Context) {
		if err := server.dispatchBlackboardConclusion(ctx, receipt); err != nil {
			server.requireBlackboardConclusionRecovery(receipt, err)
			server.logger.Printf("assisted conclusion: dispatch Task %s receipt %s: %v", receipt.TaskID, receipt.ID, err)
		}
	})
	if !queued {
		server.requireBlackboardConclusionRecovery(receipt, fmt.Errorf("provider control queue is closed"))
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
{"schema":"runtime-attempt-result/v1","base_revision":%d,"attempt":{"key":"attempt:example","create":true,"summary":"One sentence outcome of the completed work.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:example","create_objective":{"objective":"What was tested."}}],"produced_targets":[]}
Replace example keys/summaries with this Turn's real semantic targets.
Rules: outcome must be one of succeeded, failed, blocked, or inconclusive. Use inconclusive/failed/blocked when the Turn did not create durable produced graph targets. succeeded requires at least one produced_targets entry that references an already-existing Blackboard key with expected_version; do not invent produced_targets on an empty board.
Describe one Attempt and at least one tested target. Do not call tools, continue testing, include raw tool output or reasoning, finish the Task, or write the Blackboard directly.`, baseRevision, baseRevision)
}

func repairBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`Your previous Blackboard conclusion result was invalid.
Stop security testing and correct only that semantic result.
Return exactly one JSON object (no markdown fences, no prose) with schema runtime-attempt-result/v1 and base_revision %d.
If the board has no existing produced targets, use outcome "inconclusive" (or failed/blocked) with produced_targets [].
Example:
{"schema":"runtime-attempt-result/v1","base_revision":%d,"attempt":{"key":"attempt:example","create":true,"summary":"One sentence outcome of the completed work.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:example","create_objective":{"objective":"What was tested."}}],"produced_targets":[]}
Do not call tools, continue testing, include raw tool output or reasoning, finish the Task, or write the Blackboard directly.`, baseRevision, baseRevision)
}

func regenerateBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`The Project Blackboard changed after your previous semantic result was produced.
Reread the current .pentest/blackboard.json and regenerate the semantic result against base_revision %d.
Return exactly one JSON object with schema runtime-attempt-result/v1.
Do not call tools, continue testing, include raw tool output or reasoning, finish the Task, or write the Blackboard directly.`, baseRevision)
}

func (server *Server) acceptBlackboardConclusionValidationFailure(taskID string, failure runtime.ProviderSessionAttemptResultValidationFailure) {
	if !server.isCanonicalBlackboardConclusionCallback(taskID, failure.RequestID, failure.SessionID, failure.ProviderTurnID) {
		return
	}
	receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(failure.RequestID)
	if err != nil || receipt.TaskID != taskID || receipt.SourceSessionID != failure.SessionID ||
		failure.ValidationErrorCode != runtime.ProviderSessionAttemptResultInvalid || !blackboardConclusionReceiptAcceptsCallback(receipt) {
		return
	}
	server.blackboardConclusions.queueFailure(failure.RequestID, queuedBlackboardConclusionFailure{
		taskID: taskID, sessionID: failure.SessionID, providerTurnID: failure.ProviderTurnID,
		code: task.BlackboardConclusionErrorInvalidResult,
	})
	server.enqueueProviderTaskControl(taskID, func(ctx context.Context) {
		server.drainBlackboardConclusionCallbacks(ctx, taskID, failure.RequestID)
	})
}

func (server *Server) acceptBlackboardConclusionControlFailure(taskID, sessionID string, lineage runtime.ProviderSessionTurnLineage) {
	receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(lineage.RequestID)
	if err != nil || receipt.TaskID != taskID || receipt.SourceSessionID != strings.TrimSpace(sessionID) ||
		!blackboardConclusionReceiptAcceptsCallback(receipt) {
		return
	}
	server.blackboardConclusions.queueFailure(lineage.RequestID, queuedBlackboardConclusionFailure{
		taskID: taskID, sessionID: sessionID, providerTurnID: lineage.ProviderTurnID,
		code: task.BlackboardConclusionErrorToolUseForbidden,
	})
	server.enqueueProviderTaskControl(taskID, func(ctx context.Context) {
		server.drainBlackboardConclusionCallbacks(ctx, taskID, lineage.RequestID)
	})
}

func (server *Server) acceptBlackboardConclusionControlTerminal(taskID, sessionID string, lineage runtime.ProviderSessionTurnLineage, status string) {
	receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(lineage.RequestID)
	if err != nil || receipt.TaskID != taskID || receipt.SourceSessionID != strings.TrimSpace(sessionID) {
		return
	}
	stateAllowsTerminal := receipt.InternalState == task.BlackboardConclusionReceiptDispatchRequested ||
		receipt.InternalState == task.BlackboardConclusionReceiptRepairDispatchRequested ||
		receipt.InternalState == task.BlackboardConclusionReceiptVersionRegenerationDispatchRequested ||
		receipt.InternalState == task.BlackboardConclusionReceiptAwaitingResult
	if !stateAllowsTerminal || (receipt.ControlTurnID != "" && receipt.ControlTurnID != lineage.ProviderTurnID) {
		return
	}
	server.blackboardConclusions.markTerminal(lineage.RequestID, queuedBlackboardConclusionTerminal{
		taskID: taskID, sessionID: sessionID, providerTurnID: lineage.ProviderTurnID,
	})
	if status != "completed" {
		server.blackboardConclusions.queueFailure(lineage.RequestID, queuedBlackboardConclusionFailure{
			taskID: taskID, sessionID: sessionID, providerTurnID: lineage.ProviderTurnID,
			code: task.BlackboardConclusionErrorRuntimeRecoveryRequired,
		})
	}
	enqueue := server.enqueueProviderTaskControl
	if status != "completed" {
		// A failed terminal belongs to the active provider operation. Requiring
		// its existing context keeps Stop-triggered interruption from creating a
		// new recovery coordinator after Stop canceled that operation.
		enqueue = server.enqueueExistingProviderTaskControl
	}
	if !enqueue(taskID, func(ctx context.Context) {
		server.drainBlackboardConclusionCallbacks(ctx, taskID, lineage.RequestID)
	}) && status != "completed" {
		server.blackboardConclusions.clearRequest(lineage.RequestID)
	}
}

func (server *Server) drainBlackboardConclusionCallbacks(ctx context.Context, taskID, requestID string) {
	if failure, ok := server.blackboardConclusions.takeActionableFailure(taskID, requestID); ok {
		receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(requestID)
		if err == nil && receipt.TaskID == taskID && receipt.SourceSessionID == failure.sessionID &&
			receipt.ControlTurnID == failure.providerTurnID {
			if handleErr := server.handleBlackboardConclusionFailure(taskID, requestID, failure.code); handleErr != nil {
				server.logger.Printf("assisted conclusion: record failure Task %s request %s: %v", taskID, requestID, handleErr)
			} else {
				server.blackboardConclusions.clearRequest(requestID)
			}
		}
		return
	}
	if result, ok := server.blackboardConclusions.takeActionableResult(taskID, requestID); ok {
		if err := server.applyBlackboardConclusionResult(ctx, taskID, result); err != nil {
			server.logger.Printf("assisted conclusion: apply Task %s request %s: %v", taskID, requestID, err)
		}
	}
}

func (server *Server) handleBlackboardConclusionFailure(taskID, requestID string, code task.BlackboardConclusionErrorCode) error {
	receipt, dispatchRepair, err := server.tasks.HandleBlackboardConclusionFailure(
		requestID, code, time.Now().UTC(), blackboardConclusionRetryCooldown,
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
	return server.sendBlackboardConclusionTurn(ctx, receipt, repairBlackboardDirective(*receipt.BaseRevision))
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
	retried, won, err := server.tasks.RetryLatestBlackboardConclusion(taskID, idempotencyKey, time.Now().UTC())
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
		receipt.DispatchRequestID, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	if err != nil {
		server.logger.Printf("assisted conclusion: recover failed dispatch Task %s receipt %s: %v (dispatch error: %v)", receipt.TaskID, receipt.ID, err, cause)
		return
	}
	server.logger.Printf("assisted conclusion: dispatch requires operator action Task %s receipt %s: %v", receipt.TaskID, receipt.ID, cause)
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
	applied, err := server.blackboardV2.ApplyForContinuationAtRevision(
		ctx, found.ProjectID, receipt.ContinuationID, *receipt.BaseRevision, batch,
	)
	if err == nil {
		_, _, markErr := server.tasks.MarkBlackboardConclusionApplied(receipt.DispatchRequestID, applied.Revision)
		if markErr == nil {
			server.blackboardConclusions.clearRequest(receipt.DispatchRequestID)
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
	if !server.isCanonicalBlackboardConclusionCallback(taskID, result.RequestID, result.SessionID, result.ProviderTurnID) {
		return
	}
	receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(result.RequestID)
	if err != nil || receipt.TaskID != taskID || receipt.SourceSessionID != result.SessionID ||
		!blackboardConclusionReceiptAcceptsCallback(receipt) {
		return
	}
	server.blackboardConclusions.queueResult(taskID, result)
	server.enqueueProviderTaskControl(taskID, func(ctx context.Context) {
		server.drainBlackboardConclusionCallbacks(ctx, taskID, result.RequestID)
	})
}

func blackboardConclusionReceiptAcceptsCallback(receipt task.BlackboardConclusionReceipt) bool {
	return receipt.InternalState == task.BlackboardConclusionReceiptDispatchRequested ||
		receipt.InternalState == task.BlackboardConclusionReceiptRepairDispatchRequested ||
		receipt.InternalState == task.BlackboardConclusionReceiptVersionRegenerationDispatchRequested ||
		receipt.InternalState == task.BlackboardConclusionReceiptAwaitingResult
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
		server.blackboardConclusions.queueFailure(result.RequestID, queuedBlackboardConclusionFailure{
			taskID: taskID, sessionID: result.SessionID, providerTurnID: result.ProviderTurnID,
			code: task.BlackboardConclusionErrorInvalidResult,
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
				server.blackboardConclusions.clearRequest(receipt.DispatchRequestID)
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
		server.blackboardConclusions.clearRequest(result.RequestID)
	}
	return err
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
		server.blackboardConclusions.clearRequest(syncIntent.DispatchRequestID)
		return fmt.Errorf("synchronize Working Blackboard Snapshot for conclusion regeneration: %w", err)
	}
	regeneration, won, err := server.tasks.HandleBlackboardConclusionVersionConflict(
		syncIntent.DispatchRequestID, attachment.Revision, time.Now().UTC(), blackboardConclusionRetryCooldown,
	)
	if err != nil {
		return err
	}
	server.blackboardConclusions.clearRequest(syncIntent.DispatchRequestID)
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

func classifyTrustedBlackboardTool(name string) (blackboardToolSemantics, bool) {
	semantics, trusted := trustedBlackboardToolSemantics[strings.TrimSpace(name)]
	return semantics, trusted
}

type blackboardToolSemantics uint8

const (
	blackboardToolReadOnly blackboardToolSemantics = iota
	blackboardToolEvidenceRetention
	blackboardToolSemanticPersistence
)

var trustedBlackboardToolSemantics = map[string]blackboardToolSemantics{
	"blackboard_change":             blackboardToolSemanticPersistence,
	"blackboard_read":               blackboardToolReadOnly,
	"blackboard_history":            blackboardToolReadOnly,
	"blackboard_retain_evidence":    blackboardToolEvidenceRetention,
	"blackboard_checkpoint_attempt": blackboardToolSemanticPersistence,
	"blackboard_finish":             blackboardToolSemanticPersistence,
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
	found.BlackboardConclusion = view
	return found, nil
}
