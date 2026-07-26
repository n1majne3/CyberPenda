package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"pentest/internal/blackboardconclusion"
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
	terminalToolResults int
}

// blackboardConclusionTracker retains only in-flight, bounded observation
// watermarks. The durable receipt created at the completed Turn boundary is
// the source of truth exposed by Task APIs.
type blackboardConclusionTracker struct {
	mu      sync.Mutex
	turns   map[blackboardConclusionTurnKey]blackboardConclusionObservedTurn
	results map[string]queuedBlackboardConclusionResult
}

type queuedBlackboardConclusionResult struct {
	taskID string
	result runtime.ProviderSessionAttemptResult
}

func newBlackboardConclusionTracker() *blackboardConclusionTracker {
	return &blackboardConclusionTracker{
		turns:   make(map[blackboardConclusionTurnKey]blackboardConclusionObservedTurn),
		results: make(map[string]queuedBlackboardConclusionResult),
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
}

func (tracker *blackboardConclusionTracker) queueResult(taskID string, result runtime.ProviderSessionAttemptResult) {
	tracker.mu.Lock()
	tracker.results[result.RequestID] = queuedBlackboardConclusionResult{taskID: taskID, result: result}
	tracker.mu.Unlock()
}

func (tracker *blackboardConclusionTracker) takeResult(taskID, requestID string) (runtime.ProviderSessionAttemptResult, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	queued, ok := tracker.results[requestID]
	if !ok || queued.taskID != taskID {
		return runtime.ProviderSessionAttemptResult{}, false
	}
	delete(tracker.results, requestID)
	return queued.result, true
}

func (server *Server) observeProviderSession(taskID, continuationID, sessionID string, lineage runtime.ProviderSessionTurnLineage, observation runtime.ProviderSessionObservation) {
	found, err := server.tasks.Get(taskID)
	if err != nil || found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted {
		return
	}
	if strings.TrimSpace(continuationID) == "" || lineage.Kind != runtime.RuntimeTurnKindWork {
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
		if !isTrustedBlackboardTool(observation.ToolName) {
			state.terminalToolResults++
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

	if observation.Status != "completed" || state.terminalToolResults == 0 {
		server.blackboardConclusions.mu.Lock()
		delete(server.blackboardConclusions.turns, key)
		server.blackboardConclusions.mu.Unlock()
		return
	}
	receipt, inserted, err := server.tasks.RecordPendingBlackboardConclusion(
		taskID, continuationID, key.sessionID, turnID, task.TurnSelection{
			ModelProviderID: lineage.ModelProviderID,
			Model:           lineage.Model,
			ReasoningEffort: lineage.RequestedReasoningEffort,
		}, state.terminalToolResults,
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
	if inserted {
		server.scheduleBlackboardConclusionDispatch(receipt)
	}
}

func (server *Server) scheduleBlackboardConclusionDispatch(receipt task.BlackboardConclusionReceipt) {
	server.enqueueProviderTaskControl(receipt.TaskID, func(ctx context.Context) {
		if err := server.dispatchBlackboardConclusion(ctx, receipt); err != nil {
			server.logger.Printf("assisted conclusion: dispatch Task %s receipt %s: %v", receipt.TaskID, receipt.ID, err)
		}
	})
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
	result, err := session.SendTurn(ctx, runtime.ProviderSessionRequest{
		RequestID:                receipt.DispatchRequestID,
		Message:                  concludeBlackboardDirective(snapshot.Revision),
		ModelProviderID:          receipt.SourceSelection.ModelProviderID,
		Model:                    receipt.SourceSelection.Model,
		RequestedReasoningEffort: receipt.SourceSelection.ReasoningEffort,
		TurnKind:                 runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		return err
	}
	if result.RequestID != receipt.DispatchRequestID || result.SessionID != receipt.SourceSessionID || strings.TrimSpace(result.ProviderTurnID) == "" {
		return fmt.Errorf("provider returned mismatched Conclude Turn correlation")
	}
	_, _, err = server.tasks.MarkBlackboardConclusionAwaiting(receipt.DispatchRequestID, result.ProviderTurnID)
	if err != nil {
		return err
	}
	if queued, ok := server.blackboardConclusions.takeResult(receipt.TaskID, receipt.DispatchRequestID); ok {
		return server.applyBlackboardConclusionResult(ctx, receipt.TaskID, queued)
	}
	return nil
}

func concludeBlackboardDirective(baseRevision int) string {
	return fmt.Sprintf(`Stop security testing and perform only the Harness conclusion below.
Return exactly one JSON object with schema runtime-attempt-result/v1 and base_revision %d.
Describe one Attempt, at least one tested target, and only reusable produced targets already justified by the completed work.
Do not call tools, continue testing, include raw tool output or reasoning, finish the Task, or write the Blackboard directly.`, baseRevision)
}

func (server *Server) acceptBlackboardConclusionResult(taskID string, result runtime.ProviderSessionAttemptResult) {
	receipt, err := server.tasks.BlackboardConclusionByDispatchRequestID(result.RequestID)
	if err != nil || receipt.TaskID != taskID || receipt.SourceSessionID != result.SessionID {
		return
	}
	server.blackboardConclusions.queueResult(taskID, result)
	server.enqueueProviderTaskControl(taskID, func(ctx context.Context) {
		queued, ok := server.blackboardConclusions.takeResult(taskID, result.RequestID)
		if !ok {
			return
		}
		if err := server.applyBlackboardConclusionResult(ctx, taskID, queued); err != nil {
			server.logger.Printf("assisted conclusion: apply Task %s request %s: %v", taskID, queued.RequestID, err)
		}
	})
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
		return fmt.Errorf("Conclude Turn result base revision mismatch")
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
		return err
	}
	_, _, err = server.tasks.MarkBlackboardConclusionApplied(result.RequestID, applied.Revision)
	return err
}

func isTrustedBlackboardTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "blackboard_change", "blackboard_read", "blackboard_history", "blackboard_retain_evidence", "blackboard_checkpoint_attempt", "blackboard_finish":
		return true
	default:
		return false
	}
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
