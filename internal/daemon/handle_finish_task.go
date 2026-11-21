package daemon

import (
	"context"
	"errors"
	"net/http"
	"time"

	"pentest/internal/finishreadiness"

	"pentest/internal/owner"
	"pentest/internal/task"
)

// handleFinishTask is operator-controlled Task completion. It requires current
// Runtime Activity live+idle, closes Runtime resources before harness exit so
// shutdown happens-before Continuation reconciliation, verifies the durable
// reconciliation marker, then marks the Task completed. Busy Runtimes use Stop.
// There is no MCP/Runtime path that invokes this handler.
func (server *Server) handleFinishTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}

	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	blackboardDisabled := found.RunControls.BlackboardMode == task.BlackboardModeDisabled
	if found.BlackboardProtocol == "fgs" && found.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		if err := server.acquireTaskControlAfterWorkingGraphSettlement(request.Context(), found, false, false); err != nil {
			if errors.Is(err, errSemanticConclusionActionRequired) {
				writeError(response, http.StatusConflict, "semantic_conclusion_action_required")
				return
			}
			if errors.Is(err, errTaskControlOperationActive) {
				writeError(response, http.StatusConflict, "task control operation already active")
				return
			}
			writeError(response, http.StatusConflict, err.Error())
			return
		}
	} else if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)

	// Re-read under the control lock so concurrent Stop/steer cannot race the gate.
	found, err = server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	readiness, err := server.finishReadiness.Require(request.Context(), projectID, taskID)
	if err != nil {
		var notReady *finishreadiness.NotReadyError
		if errors.As(err, &notReady) {
			writeFinishReadinessConflict(response, readiness)
			return
		}
		writeError(response, http.StatusInternalServerError, "evaluate Finish Readiness")
		return
	}
	activity := server.computeRuntimeActivity(found)
	if activity.Liveness != runtimeLivenessLive || activity.TurnActivity != runtimeTurnIdle {
		writeError(response, http.StatusConflict, finishRejectedMessage(activity))
		return
	}

	deadline := time.Now().Add(server.runtimeStopTimeout)
	stopContext, cancelStop := context.WithDeadline(context.Background(), deadline)
	defer cancelStop()

	// 1) Finish intent: Launch will exit without writing terminal status.
	// handleFinishTask is the sole owner of Continuation/Task completed.
	if server.harness != nil {
		server.harness.MarkFinishRequested(taskID)
	}
	if err := server.closeProviderSessionForStop(stopContext, taskID); err != nil {
		if server.harness != nil {
			server.harness.ClearFinishIntent(taskID)
		}
		server.finishDiagnostic(taskID, "provider_session_close", "provider session did not close")
		writeError(response, http.StatusConflict, "finish failed at provider_session_close: provider session did not close")
		return
	}
	// 3) Wait for harness exit (cancel residual wait is safe: finish path does
	// not write terminal status). On timeout, clear intent then force-stop so a
	// late exit uses stop/fail rather than hang with residual intent.
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	if server.harness != nil {
		if ok := server.harness.StopAndWait(taskID, remaining); !ok {
			server.harness.ClearFinishIntent(taskID)
			_ = server.harness.StopAndWait(taskID, 0)
			server.finishDiagnostic(taskID, "runtime_shutdown", "runtime did not stop in time")
			writeError(response, http.StatusConflict, "finish failed at runtime_shutdown: runtime did not stop in time")
			return
		}
	}

	// 4) Sole owner: mark Continuation completed. Blackboard-enabled owners
	// trigger reconciliation and verify its durable marker. Disabled owners have
	// no Blackboard reconciliation obligation. Then mark the Task completed.
	// After runtime is already closed, any failure here must settle Task to a
	// recoverable terminal (failed) so it does not remain durable running.
	cont, contErr := server.tasks.LatestContinuation(taskID)
	if contErr != nil {
		server.finishFailClosed(response, taskID, "continuation_lookup", contErr.Error(), http.StatusInternalServerError)
		return
	}
	if cont == nil {
		server.finishFailClosed(response, taskID, "continuation_missing", "no Continuation for Task", http.StatusConflict)
		return
	}
	// Complete the Continuation. For Blackboard-enabled owners, the same
	// idempotent call retries an incomplete terminal reconciliation marker.
	if cont.Status != task.StatusCompleted || (!blackboardDisabled && cont.BlackboardReconciliationStatus != task.ReconciliationCompleted) {
		if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusCompleted); err != nil {
			// Classify from durable re-read, never string-matching.
			refreshed, refErr := server.tasks.Continuation(cont.ID)
			if !blackboardDisabled && refErr == nil && refreshed.Status == task.StatusCompleted && refreshed.BlackboardReconciliationStatus != task.ReconciliationCompleted {
				server.finishFailClosed(response, taskID, "continuation_reconciliation", "marker="+string(refreshed.BlackboardReconciliationStatus), http.StatusConflict)
				return
			}
			if errors.Is(err, task.ErrContinuationStatusConflict) {
				server.finishFailClosed(response, taskID, "continuation_complete", "continuation status conflict", http.StatusConflict)
				return
			}
			server.finishFailClosed(response, taskID, "continuation_complete", err.Error(), http.StatusInternalServerError)
			return
		}
	}
	refreshed, refErr := server.tasks.Continuation(cont.ID)
	if refErr != nil {
		server.finishFailClosed(response, taskID, "continuation_reread", refErr.Error(), http.StatusInternalServerError)
		return
	}
	if refreshed.Status != task.StatusCompleted {
		server.finishFailClosed(response, taskID, "continuation_status", "status="+string(refreshed.Status), http.StatusConflict)
		return
	}
	if !blackboardDisabled && refreshed.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		server.finishFailClosed(response, taskID, "continuation_reconciliation", "marker="+string(refreshed.BlackboardReconciliationStatus), http.StatusConflict)
		return
	}

	// 5) Blackboard-enabled owners require a completed reconciliation marker.
	// Disabled owners retain the ordinary lifecycle gate only.
	if _, err := server.tasks.UpdateStatus(taskID, task.StatusCompleted); err != nil {
		server.finishFailClosed(response, taskID, "task_complete", err.Error(), http.StatusInternalServerError)
		return
	}
	// Lifecycle only — never a Runtime Activity audit/history record.
	_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
		"phase": "completed", "reason": "operator_finish",
	})
	server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerFinished, "Task finished with queued accepted steering")

	finished, err := server.taskDetail(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, finished)
}

var errSemanticConclusionActionRequired = errors.New("semantic_conclusion_action_required")
var errTaskControlOperationActive = errors.New("task control operation already active")

// acquireTaskControlAfterWorkingGraphSettlement closes the settle/acquire race:
// after taking Task control it rechecks the durable receipt. If a conclusion
// became pending between the optimistic drain and acquisition, it releases
// control so the Harness coordinator can run, drains again, and retries.
func (server *Server) acquireTaskControlAfterWorkingGraphSettlement(ctx context.Context, found task.Task, allowActionRequired, providerControl bool) error {
	if found.BlackboardProtocol == "fgs" && found.RunControls.BlackboardMode != task.BlackboardModeDisabled {
		acquired := server.acquireTaskControl(found.ID)
		if providerControl {
			acquired = server.acquireProviderTaskControl(found.ID)
		}
		if !acquired {
			return errTaskControlOperationActive
		}
		if _, err := server.settleTaskWorkingGraph(ctx, found, allowActionRequired); err != nil {
			if providerControl {
				server.releaseProviderTaskControl(found.ID)
			} else {
				server.releaseTaskControl(found.ID)
			}
			return err
		}
		return nil
	}
	if providerControl {
		if !server.acquireProviderTaskControl(found.ID) {
			return errTaskControlOperationActive
		}
		return nil
	}
	if !server.acquireTaskControl(found.ID) {
		return errTaskControlOperationActive
	}
	return nil
}

// finishFailClosed settles a post-runtime Finish failure: Task must not stay
// durable running. Prefer failed (resumable). Public HTTP uses stable stage
// messages; raw detail stays in logs/events only.
func (server *Server) finishFailClosed(response http.ResponseWriter, taskID, stage, detail string, status int) {
	server.finishDiagnostic(taskID, stage, detail)
	server.settleTaskFailedAfterFinishAbort(taskID)
	server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerStateChanged, "Task finish aborted before the accepted steering dispatched")
	writeError(response, status, "finish failed at "+stage)
}

// settleTaskFailedAfterFinishAbort marks Task failed when still active after
// runtime close. Non-terminal Continuations become failed; existing different
// terminal Continuation statuses are left unchanged.
func (server *Server) settleTaskFailedAfterFinishAbort(taskID string) {
	if cont, err := server.tasks.LatestContinuation(taskID); err == nil && cont != nil {
		switch cont.Status {
		case task.StatusRunning, task.StatusPaused, task.StatusPending:
			if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusFailed); err != nil {
				if refreshed, refErr := server.tasks.Continuation(cont.ID); refErr == nil {
					if refreshed.Status == task.StatusRunning || refreshed.Status == task.StatusPaused || refreshed.Status == task.StatusPending {
						server.finishDiagnostic(taskID, "settle_continuation", err.Error())
					}
				}
			}
		}
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return
	}
	if found.Status == task.StatusRunning || found.Status == task.StatusPaused || found.Status == task.StatusPending {
		if _, err := server.tasks.UpdateStatus(taskID, task.StatusFailed); err != nil {
			server.finishDiagnostic(taskID, "settle_task", err.Error())
		}
	}
}

// finishDiagnostic records a fail-closed Finish diagnostic without inventing
// Runtime Activity audit history. detail may include raw errors for operators.
func (server *Server) finishDiagnostic(taskID, stage, detail string) {
	if found, err := server.tasks.Get(taskID); err == nil {
		server.logTask(found, "finish_failed", stage+": "+detail)
	} else if server.logger != nil {
		server.logger.Printf("task finish_failed id=%s detail=%q", taskID, stage+": "+detail)
	}
	_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
		"phase": "finish_failed", "stage": stage, "detail": detail,
	})
}

func finishRejectedMessage(activity task.RuntimeActivity) string {
	switch activity.Liveness {
	case runtimeLivenessLive:
		if activity.TurnActivity == runtimeTurnBusy {
			return "finish requires a live idle Runtime; Stop interrupts a busy Runtime"
		}
		return "finish requires a live idle Runtime"
	case runtimeLivenessOffline:
		return "finish requires a live idle Runtime; runtime is offline"
	case runtimeLivenessOrphaned:
		return "finish requires a live idle Runtime; runtime ownership is orphaned"
	case runtimeLivenessUnknown:
		return "finish requires a live idle Runtime; runtime health is unknown"
	default:
		return "finish requires a live idle Runtime"
	}
}

func (server *Server) acquireTaskControl(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	if server.closing || server.activeControls[taskID] {
		return false
	}
	server.activeControls[taskID] = true
	return true
}

func (server *Server) acquireProviderTaskControl(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	if server.closing || server.activeControls[taskID] {
		return false
	}
	server.activeControls[taskID] = true
	server.activeProviderControls[taskID] = true
	server.providerTaskContextLocked(taskID)
	server.providerControlWG.Add(1)
	return true
}

func (server *Server) providerTaskContextLocked(taskID string) context.Context {
	if existing := server.providerTaskContexts[taskID]; existing != nil && existing.Err() == nil {
		return existing
	}
	ctx, cancel := context.WithCancel(server.providerControlCtx)
	server.providerTaskContexts[taskID] = ctx
	server.providerTaskCancels[taskID] = cancel
	return ctx
}

func (server *Server) providerTaskContext(taskID string) context.Context {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	return server.providerTaskContextLocked(taskID)
}

func (server *Server) cancelProviderTaskControls(taskID string) bool {
	server.controlMu.Lock()
	if !server.activeProviderControls[taskID] && server.queuedProviderControls[taskID] == 0 {
		server.controlMu.Unlock()
		return false
	}
	cancel := server.providerTaskCancels[taskID]
	delete(server.providerTaskContexts, taskID)
	delete(server.providerTaskCancels, taskID)
	server.controlMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (server *Server) decrementQueuedProviderControlLocked(taskID string) {
	queued := server.queuedProviderControls[taskID]
	if queued <= 1 {
		delete(server.queuedProviderControls, taskID)
		return
	}
	server.queuedProviderControls[taskID] = queued - 1
}

func (server *Server) waitAcquireTaskControl(ctx context.Context, taskID string) bool {
	for {
		if server.acquireTaskControl(taskID) {
			return true
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
