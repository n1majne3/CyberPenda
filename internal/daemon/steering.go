package daemon

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/steering"
	"pentest/internal/task"
)

// Accepted Steering is the durable Runtime Harness responsibility for both Task
// and Session owners. The conversation event is a projection; the
// accepted_steering record is the dispatch source of truth. Requests dispatch
// first-in, first-out per owner with a durable send-start fence: a pre-fence
// request may be sent after recovery, while a post-fence request with no
// durable outcome becomes action_required and is never replayed.

// steeringExecution is the terminal outcome of one accepted steer dispatch.
type steeringExecution struct {
	state   owner.SteeringState
	reason  owner.SteeringFailureReason
	message string
	result  map[string]any
	// failOwner marks the Task owner failed when a required Continuation
	// transition fails after the provider accepted the steer.
	failOwner bool
}

// steeringAdapter binds one Runtime Owner to the owner-neutral Accepted
// Steering dispatcher. Owner-specific persistence and execution stay behind
// these callbacks; the state machine and fencing decisions are shared.
type steeringAdapter struct {
	kind       owner.Kind
	id         string
	settlement providerControlSettlement
	session    func() (runtime.ProviderSession, bool)
	// resolveContinuation revalidates the durable owner and returns the
	// writable Runtime Continuation for delivery. fresh reports a Continuation
	// created by this resolution so the executor does not advance it again.
	resolveContinuation func(context.Context) (string, bool, error)
	// execute delivers the accepted request against the resolved Continuation
	// and returns the terminal outcome. fresh reports that the Continuation was
	// created during this dispatch.
	execute func(context.Context, *owner.SteeringRecord, string, bool) steeringExecution
	// project appends the owner-local Timeline projection of one terminal
	// settlement (used by settlement and recovery paths that did not dispatch).
	project func(record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string)
}

func (adapter *steeringAdapter) providerRequest(record *owner.SteeringRecord) runtime.ProviderSessionRequest {
	return runtime.ProviderSessionRequest{
		RequestID:                record.RequestID,
		Message:                  record.Message,
		ModelProviderID:          record.ModelProviderID,
		Model:                    record.Model,
		RequestedReasoningEffort: record.RequestedReasoningEffort,
	}
}

func taskSteeringAdapter(server *Server, taskID string, settlement providerControlSettlement) *steeringAdapter {
	adapter := &steeringAdapter{
		kind:       owner.KindTask,
		id:         taskID,
		settlement: settlement,
		session: func() (runtime.ProviderSession, bool) {
			return server.providerSessions.get(taskID)
		},
		project: func(record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
			projectTaskSteeringSettlement(server, record, state, reason, message)
		},
	}
	adapter.resolveContinuation = func(ctx context.Context) (string, bool, error) {
		found, err := server.tasks.Get(taskID)
		if err != nil {
			return "", false, err
		}
		if found.Status != task.StatusRunning && found.Status != task.StatusPaused {
			return "", false, errSteeringOwnerNotActive
		}
		boundSession, bound := server.providerSessions.get(taskID)
		if !bound || boundSession == nil {
			return "", false, errSteeringSessionUnavailable
		}
		active, err := server.tasks.ActiveContinuation(taskID)
		if err != nil {
			return "", false, err
		}
		if active != nil {
			return active.ID, false, nil
		}
		fresh, err := server.createWritableContinuationForLiveSession(found, boundSession)
		if err != nil {
			// A Task that never had a Runtime Continuation keeps task-level
			// events in the legacy degenerate state; the steer still sends.
			if errors.Is(err, errNoContinuationToContinue) {
				return "", false, nil
			}
			return "", false, err
		}
		return fresh.ID, true, nil
	}
	adapter.execute = func(ctx context.Context, record *owner.SteeringRecord, continuationID string, fresh bool) steeringExecution {
		found, err := server.tasks.Get(taskID)
		if err != nil {
			return steeringExecution{state: owner.SteeringFailed, reason: owner.SteeringReasonOwnerStateChanged, message: err.Error()}
		}
		boundSession, bound := server.providerSessions.get(taskID)
		if !bound || boundSession == nil {
			return steeringExecution{state: owner.SteeringFailed, reason: owner.SteeringReasonSessionClosed, message: "provider session is unavailable"}
		}
		mode := runtime.ProviderSessionMode(record.Mode)
		operation := nativeSteerOperation(boundSession, mode)
		if operation == nil {
			return steeringExecution{state: owner.SteeringFailed, reason: owner.SteeringReasonProviderControlUnavailable, message: "provider session does not support the accepted steer mode"}
		}
		conversation := task.Event{ID: record.ConversationEventID}
		return server.executeNativeSteerOperation(ctx, found, boundSession, mode, operation,
			adapter.providerRequest(record), conversation, continuationID, fresh)
	}
	return adapter
}

func sessionSteeringAdapter(server *Server, sessionID string, settlement providerControlSettlement) *steeringAdapter {
	adapter := &steeringAdapter{
		kind:       owner.KindSession,
		id:         sessionID,
		settlement: settlement,
		session: func() (runtime.ProviderSession, bool) {
			return server.sessionProviderSessions.get(sessionID)
		},
		project: func(record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
			projectSessionSteeringSettlement(server, record, state, reason, message)
		},
	}
	adapter.resolveContinuation = func(ctx context.Context) (string, bool, error) {
		found, err := server.sessions.Get(sessionID)
		if err != nil {
			return "", false, err
		}
		if found.Lifecycle != session.LifecycleOpen {
			return "", false, errSteeringOwnerNotActive
		}
		boundSession, bound := server.sessionProviderSessions.get(sessionID)
		if !bound || boundSession == nil {
			return "", false, errSteeringSessionUnavailable
		}
		active, err := server.sessions.ActiveContinuation(sessionID)
		if err != nil {
			return "", false, err
		}
		if active == nil {
			return "", false, errSteeringContinuationUnavailable
		}
		return active.ID, false, nil
	}
	adapter.execute = func(ctx context.Context, record *owner.SteeringRecord, continuationID string, fresh bool) steeringExecution {
		boundSession, bound := server.sessionProviderSessions.get(sessionID)
		if !bound || boundSession == nil {
			return steeringExecution{state: owner.SteeringFailed, reason: owner.SteeringReasonSessionClosed, message: "provider session is unavailable"}
		}
		mode := runtime.ProviderSessionMode(record.Mode)
		operation := nativeSteerOperation(boundSession, mode)
		if operation == nil {
			return steeringExecution{state: owner.SteeringFailed, reason: owner.SteeringReasonProviderControlUnavailable, message: "provider session does not support the accepted steer mode"}
		}
		return server.executeSessionNativeSteerOperation(ctx, sessionID, boundSession, mode, operation,
			adapter.providerRequest(record), continuationID)
	}
	return adapter
}

var (
	errSteeringOwnerNotActive          = errors.New("accepted steering owner is no longer active")
	errSteeringSessionUnavailable      = errors.New("accepted steering provider session is unavailable")
	errSteeringContinuationUnavailable = errors.New("accepted steering continuation is unavailable")
)

// acceptAcceptedSteering durably records one native steer request together with
// its conversation projection, then enqueues its owner-neutral dispatch. The
// record is committed before this returns, so a 202 Accepted is returned only
// after the request and its settlement responsibility are durable.
func (server *Server) acceptAcceptedSteering(ctx context.Context, adapter *steeringAdapter, input steering.AcceptRequest, project func(tx *sql.Tx) (string, error)) (*owner.SteeringRecord, error) {
	record, err := server.steering.Accept(ctx, adapter.kind, adapter.id, input, project)
	if err != nil {
		return nil, err
	}
	server.enqueueAcceptedSteeringDispatch(adapter)
	return record, nil
}

// enqueueAcceptedSteeringDispatch triggers the owner-neutral FIFO dispatcher
// for one owner. The dispatch loop drains every pending record in durable
// queue order, so repeated triggers are safe.
func (server *Server) enqueueAcceptedSteeringDispatch(adapter *steeringAdapter) {
	server.enqueueProviderTaskControlWithSettlement(adapter.id, false, adapter.settlement, func(ctx context.Context) {
		server.dispatchAcceptedSteering(ctx, adapter)
	})
}

// dispatchAcceptedSteering sends every pending Accepted Steering record for one
// owner in durable FIFO order. Each request is fenced (dispatch_started +
// send_started_at) before the irreversible provider step, and the next record
// is dispatched only after the previous one reached a terminal outcome.
func (server *Server) dispatchAcceptedSteering(ctx context.Context, adapter *steeringAdapter) {
	for {
		record, err := server.steering.OldestPending(adapter.kind, adapter.id)
		if err != nil {
			server.logger.Printf("Accepted Steering dispatch: %v", err)
			return
		}
		if record == nil {
			return
		}
		continuationID, freshContinuation, resolveErr := adapter.resolveContinuation(ctx)
		if resolveErr != nil {
			reason := owner.SteeringReasonContinuationUnavailable
			if errors.Is(resolveErr, errSteeringOwnerNotActive) {
				reason = owner.SteeringReasonOwnerStateChanged
			} else if errors.Is(resolveErr, errSteeringSessionUnavailable) {
				reason = owner.SteeringReasonSessionClosed
			}
			server.settleSteeringRecord(ctx, adapter, *record, owner.SteeringFailed, reason, resolveErr.Error())
			continue
		}
		fenced, err := server.steering.MarkDispatchStarted(ctx, record.ID, continuationID, time.Now().UTC())
		if err != nil {
			if !errors.Is(err, steering.ErrNotFound) {
				server.logger.Printf("Accepted Steering fence: %v", err)
			}
			return
		}
		if fenced.State != owner.SteeringDispatchStarted {
			// A concurrent dispatcher already advanced this record; re-read the queue.
			continue
		}
		execution := adapter.execute(ctx, fenced, continuationID, freshContinuation)
		switch execution.state {
		case owner.SteeringApplied:
			if _, err := server.steering.MarkApplied(ctx, fenced.ID, execution.result); err != nil && !errors.Is(err, steering.ErrNotFound) {
				server.logger.Printf("Accepted Steering applied: %v", err)
			}
		case owner.SteeringFailed:
			server.settleSteeringRecord(ctx, adapter, *fenced, owner.SteeringFailed, execution.reason, execution.message)
			if execution.failOwner {
				server.failSteeringOwner(adapter)
			}
		case owner.SteeringActionRequired:
			server.settleSteeringRecord(ctx, adapter, *fenced, owner.SteeringActionRequired, execution.reason, execution.message)
		default:
			// The dispatch was canceled without a provider outcome. The record
			// stays fenced; Stop/Finish settlement or restart recovery resolves
			// it as delivery-ambiguous, never as a replayed request.
			return
		}
	}
}

// settleSteeringRecord marks one record terminal and projects the owner-local
// Timeline event. The record state is the source of truth; the event is the
// projection.
func (server *Server) settleSteeringRecord(ctx context.Context, adapter *steeringAdapter, record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
	mark := func() (*owner.SteeringRecord, error) {
		switch state {
		case owner.SteeringApplied:
			return server.steering.MarkApplied(ctx, record.ID, record.Result)
		case owner.SteeringActionRequired:
			return server.steering.MarkActionRequired(ctx, record.ID, reason, message)
		default:
			return server.steering.MarkFailed(ctx, record.ID, reason, message)
		}
	}
	updated, err := mark()
	if err != nil {
		if !errors.Is(err, steering.ErrNotFound) {
			server.logger.Printf("Accepted Steering settle %s: %v", state, err)
		}
		return
	}
	adapter.project(*updated, state, reason, message)
}

// failSteeringOwner marks the Task failed after an accepted steer's required
// Continuation transition failed (the provider already holds the steer).
func (server *Server) failSteeringOwner(adapter *steeringAdapter) {
	if adapter.kind != owner.KindTask {
		return
	}
	_ = server.closeProviderSession(adapter.id)
	if current, err := server.tasks.ActiveContinuation(adapter.id); err == nil && current != nil {
		_, _ = server.tasks.UpdateContinuationStatus(current.ID, task.StatusFailed)
	}
	_, _ = server.tasks.UpdateStatus(adapter.id, task.StatusFailed)
}

// settleOwnerAcceptedSteering settles every queued Accepted Steering record for
// one owner with truthful terminal outcomes. Pending requests become failed
// with the given reason; post-fence requests become action_required because
// delivery is ambiguous. Stop, Task Finish, Session Stop, and permanent Runtime
// loss all call this instead of discarding queued work.
func (server *Server) settleOwnerAcceptedSteering(adapter *steeringAdapter, reason owner.SteeringFailureReason, message string) {
	ctx := context.Background()
	records, err := server.steering.SettleOwner(ctx, adapter.kind, adapter.id, reason, message)
	if err != nil {
		server.logger.Printf("Accepted Steering settle owner %s %s: %v", adapter.kind, adapter.id, err)
		return
	}
	for _, record := range records {
		adapter.project(record, record.State, record.ErrorCode, record.ErrorMessage)
	}
}

// settleTaskAcceptedSteering settles every queued Accepted Steering request of
// one Task with a truthful terminal outcome (Stop, Task Finish, permanent
// Runtime loss).
func (server *Server) settleTaskAcceptedSteering(taskID string, reason owner.SteeringFailureReason, message string) {
	server.settleOwnerAcceptedSteering(taskSteeringAdapter(server, taskID, nil), reason, message)
}

// settleSessionAcceptedSteering settles every queued Accepted Steering request
// of one Session with a truthful terminal outcome (Stop, archive).
func (server *Server) settleSessionAcceptedSteering(sessionID string, reason owner.SteeringFailureReason, message string) {
	server.settleOwnerAcceptedSteering(sessionSteeringAdapter(server, sessionID, nil), reason, message)
}

// triggerAcceptedSteeringDispatch resumes pending Accepted Steering dispatch
// for an owner whose provider session was just bound (launch, resume, or
// startup recovery). Pre-fence requests are safe to send on the live session.
func (server *Server) triggerAcceptedSteeringDispatch(adapter *steeringAdapter) {
	pending, err := server.steering.OldestPending(adapter.kind, adapter.id)
	if err != nil || pending == nil {
		return
	}
	server.enqueueAcceptedSteeringDispatch(adapter)
}

// recoverAcceptedSteering is the restart reconciliation for Accepted Steering.
// A post-fence record with no durable outcome becomes action_required and is
// never replayed. A pre-fence pending record resumes dispatch when the owner
// Runtime is live; for interrupted (resumable) owners it stays pending and
// dispatch is triggered when a provider session is bound again. Owners that
// reached a terminal durable state at recovery settle their queued requests
// with truthful reasons.
func (server *Server) recoverAcceptedSteering(ctx context.Context) {
	records, err := server.steering.NonTerminal()
	if err != nil {
		server.logger.Printf("Accepted Steering recovery: %v", err)
		return
	}
	for _, record := range records {
		if record.State == owner.SteeringDispatchStarted {
			server.settleOwnerSteeringRecord(ctx, record, owner.SteeringActionRequired, owner.SteeringReasonDeliveryAmbiguous, "provider delivery outcome is unknown after daemon restart")
			continue
		}
		if record.OwnerKind == owner.KindTask {
			server.recoverTaskPendingSteering(ctx, record)
			continue
		}
		server.recoverSessionPendingSteering(ctx, record)
	}
}

func (server *Server) recoverTaskPendingSteering(ctx context.Context, record owner.SteeringRecord) {
	found, err := server.tasks.Get(record.OwnerID)
	if err != nil {
		server.logger.Printf("Accepted Steering recovery Task %s: %v", record.OwnerID, err)
		return
	}
	switch {
	case found.Status == task.StatusRunning || found.Status == task.StatusPaused:
		if _, bound := server.providerSessions.get(record.OwnerID); bound {
			server.enqueueAcceptedSteeringDispatch(taskSteeringAdapter(server, record.OwnerID, server.taskConclusionSettlementForID(record.OwnerID)))
		}
		// Without a bound session the owner is not yet resumable; the pending
		// request dispatches when a provider session binds again.
	case found.Status == task.StatusStopped:
		server.settleOwnerSteeringRecord(ctx, record, owner.SteeringFailed, owner.SteeringReasonOwnerStopped, "Task was stopped before the accepted steering dispatched")
	case found.Status == task.StatusFailed:
		server.settleOwnerSteeringRecord(ctx, record, owner.SteeringFailed, owner.SteeringReasonOwnerRuntimeLost, "Task Runtime was lost before the accepted steering dispatched")
	case found.Status == task.StatusCompleted:
		server.settleOwnerSteeringRecord(ctx, record, owner.SteeringFailed, owner.SteeringReasonOwnerFinished, "Task finished before the accepted steering dispatched")
	default:
		// Interrupted or pending: resumable owner. The pending request stays
		// queued and dispatches when the Runtime is live again.
	}
}

func (server *Server) recoverSessionPendingSteering(ctx context.Context, record owner.SteeringRecord) {
	found, err := server.sessions.Get(record.OwnerID)
	if err != nil {
		server.logger.Printf("Accepted Steering recovery Session %s: %v", record.OwnerID, err)
		return
	}
	switch {
	case found.Lifecycle == session.LifecycleOpen:
		if _, bound := server.sessionProviderSessions.get(record.OwnerID); bound {
			server.enqueueAcceptedSteeringDispatch(sessionSteeringAdapter(server, record.OwnerID, server.sessionConclusionSettlementForID(record.OwnerID)))
		}
		// Open but not bound: dispatch triggers when the Session Runtime binds.
	default:
		server.settleOwnerSteeringRecord(ctx, record, owner.SteeringFailed, owner.SteeringReasonOwnerArchived, "Session was archived before the accepted steering dispatched")
	}
}

// settleOwnerSteeringRecord settles ONE record with the given terminal state.
// Recovery uses failed for pre-fence pending records of terminal owners and
// action_required for post-fence records whose delivery is ambiguous.
func (server *Server) settleOwnerSteeringRecord(ctx context.Context, record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
	adapter := server.steeringAdapterFor(record)
	server.settleSteeringRecord(ctx, adapter, record, state, reason, message)
}

// taskConclusionSettlementForID waits for the durable assisted conclusion of
// one Task to settle before an accepted steer takes the provider control
// boundary. Interactive owners have no conclusion gate.
func (server *Server) taskConclusionSettlementForID(taskID string) providerControlSettlement {
	return func(ctx context.Context, wait bool) (bool, error) {
		found, err := server.tasks.Get(taskID)
		if err != nil {
			return false, err
		}
		if found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted {
			return true, nil
		}
		return server.taskConclusionSettlement(found)(ctx, wait)
	}
}

// sessionConclusionSettlementForID mirrors the Task gate for Session owners.
func (server *Server) sessionConclusionSettlementForID(sessionID string) providerControlSettlement {
	return server.sessionBlackboardConclusionSettlement(sessionID, true)
}

func (server *Server) steeringAdapterFor(record owner.SteeringRecord) *steeringAdapter {
	if record.OwnerKind == owner.KindTask {
		return taskSteeringAdapter(server, record.OwnerID, server.taskConclusionSettlementForID(record.OwnerID))
	}
	return sessionSteeringAdapter(server, record.OwnerID, server.sessionConclusionSettlementForID(record.OwnerID))
}

// steeringConflictMessage compares a repeated request against the durable
// record and returns a conflict message, or "" when the repeat matches.
func steeringConflictMessage(record *owner.SteeringRecord, message string, selection runtime.ProviderSessionRequest) string {
	if strings.TrimSpace(record.Message) != strings.TrimSpace(message) {
		return "steer request id already belongs to a different message"
	}
	if strings.TrimSpace(record.ModelProviderID) != strings.TrimSpace(selection.ModelProviderID) ||
		strings.TrimSpace(record.Model) != strings.TrimSpace(selection.Model) ||
		strings.TrimSpace(record.RequestedReasoningEffort) != strings.TrimSpace(selection.RequestedReasoningEffort) {
		return "steer request id already belongs to a different turn selection"
	}
	return ""
}

// steeringOutcomeFromRecord projects the durable state as the API outcome
// string used by the steer response and Timeline events.
func steeringOutcomeFromRecord(record *owner.SteeringRecord) string {
	switch record.State {
	case owner.SteeringApplied:
		return "applied"
	case owner.SteeringFailed:
		return "failed"
	case owner.SteeringActionRequired:
		return "action_required"
	default:
		return "pending"
	}
}

func projectTaskSteeringSettlement(server *Server, record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
	payload := task.EventPayload{
		"request_id": record.RequestID, "session_id": record.SessionID, "mode": string(record.Mode),
		"outcome": steeringOutcomeFromRecord(&record), "error_code": string(reason), "error": message,
		"model_provider_id": record.ModelProviderID, "model": record.Model,
		"requested_reasoning_effort": record.RequestedReasoningEffort,
	}
	phase := "steering_failed"
	if state == owner.SteeringActionRequired {
		phase = "steering_action_required"
	}
	payload["phase"] = phase
	_, _ = server.tasks.AppendEvent(record.OwnerID, task.EventKindSteering, payload)
}

func projectSessionSteeringSettlement(server *Server, record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
	payload := session.EventPayload{
		"request_id": record.RequestID, "session_id": record.SessionID, "mode": string(record.Mode),
		"outcome": steeringOutcomeFromRecord(&record), "error_code": string(reason), "error": message,
		"model_provider_id": record.ModelProviderID, "model": record.Model,
		"requested_reasoning_effort": record.RequestedReasoningEffort,
	}
	phase := "steering_failed"
	if state == owner.SteeringActionRequired {
		phase = "steering_action_required"
	}
	payload["phase"] = phase
	_, _ = server.sessions.AppendEvent(record.OwnerID, session.EventKindSteering, payload)
}
