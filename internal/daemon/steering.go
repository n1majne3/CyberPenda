package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
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
	mode    runtime.ProviderSessionMode
	// continuationID is the writable Continuation after provider settlement.
	// Applied Timeline projection is deferred until the durable record commits.
	continuationID string
	// failOwner marks the Task owner failed when a required Continuation
	// transition fails after the provider accepted the steer.
	failOwner bool
}

type steeringControlProjection struct {
	requestID   string
	state       string
	errorCode   string
	error       string
	nonTerminal bool
}

func (server *Server) latestSteeringControl(kind owner.Kind, ownerID string) (steeringControlProjection, error) {
	record, err := server.steering.Latest(kind, ownerID)
	if err != nil || record == nil {
		return steeringControlProjection{}, err
	}
	return steeringControlProjection{
		requestID:   record.RequestID,
		state:       steeringOutcomeFromRecord(record),
		errorCode:   string(record.ErrorCode),
		error:       record.ErrorMessage,
		nonTerminal: !record.State.Terminal(),
	}, nil
}

// steeringAdapter binds one Runtime Owner to the owner-neutral Accepted
// Steering dispatcher. Owner-specific persistence and execution stay behind
// these callbacks; the state machine and fencing decisions are shared.
type steeringAdapter struct {
	kind       owner.Kind
	id         string
	settlement providerControlSettlement
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
	// projectApplied runs only after the durable record reached applied.
	projectApplied func(record owner.SteeringRecord, continuationID string, result map[string]any)
}

func (adapter *steeringAdapter) providerRequest(record *owner.SteeringRecord) runtime.ProviderSessionRequest {
	return runtime.ProviderSessionRequest{
		RequestID:                record.RequestID,
		Message:                  record.Message,
		ModelProviderID:          record.ModelProviderID,
		Model:                    record.Model,
		RequestedReasoningEffort: record.RequestedReasoningEffort,
		ProviderTurnID:           record.ExpectedProviderTurnID,
	}
}

func taskSteeringAdapter(server *Server, taskID string, settlement providerControlSettlement) *steeringAdapter {
	adapter := &steeringAdapter{
		kind:       owner.KindTask,
		id:         taskID,
		settlement: settlement,
		project: func(record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
			projectTaskSteeringSettlement(server, record, state, reason, message)
		},
		projectApplied: func(record owner.SteeringRecord, continuationID string, result map[string]any) {
			projectTaskSteeringApplied(server, record, continuationID, result)
		},
	}
	adapter.resolveContinuation = func(ctx context.Context) (string, bool, error) {
		found, err := server.tasks.Get(taskID)
		if err != nil {
			return "", false, err
		}
		if found.Status == task.StatusStopped || found.Status == task.StatusCompleted || found.Status == task.StatusFailed {
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
		project: func(record owner.SteeringRecord, state owner.SteeringState, reason owner.SteeringFailureReason, message string) {
			projectSessionSteeringSettlement(server, record, state, reason, message)
		},
		projectApplied: func(record owner.SteeringRecord, continuationID string, result map[string]any) {
			projectSessionSteeringApplied(server, record, continuationID, result)
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

// acceptedSteeringDispatchTimeout bounds one provider delivery. A timed-out
// dispatch is delivery-ambiguous (post-fence, no durable outcome) and settles
// action_required instead of being replayed.
const acceptedSteeringDispatchTimeout = 30 * time.Second

// acceptAcceptedSteering durably records one native steer request together with
// its conversation projection, then enqueues its owner-neutral dispatch. The
// record is committed before this returns, so a 202 Accepted is returned only
// after the request and its settlement responsibility are durable.
func (server *Server) acceptSteeringDurably(ctx context.Context, adapter *steeringAdapter, input steering.AcceptRequest, project func(tx *sql.Tx) (string, error)) (*owner.SteeringRecord, error) {
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
		// Each provider delivery is bounded so a hung steer operation cannot
		// leave the owner queue blocked or the record permanently dispatch_started.
		runCtx, cancel := context.WithTimeout(ctx, acceptedSteeringDispatchTimeout)
		execution := adapter.execute(runCtx, fenced, continuationID, freshContinuation)
		cancel()
		switch execution.state {
		case owner.SteeringApplied:
			effectiveMode := owner.SteeringMode(execution.mode)
			if effectiveMode == "" {
				effectiveMode = fenced.Mode
			}
			updated, err := server.steering.MarkApplied(ctx, fenced.ID, effectiveMode, execution.result)
			if err != nil && !errors.Is(err, steering.ErrNotFound) {
				server.logger.Printf("Accepted Steering applied: %v", err)
				return
			}
			if err == nil && updated != nil && adapter.projectApplied != nil {
				adapter.projectApplied(*updated, execution.continuationID, execution.result)
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
	var updated *owner.SteeringRecord
	var err error
	if state == owner.SteeringActionRequired {
		updated, err = server.steering.MarkActionRequired(ctx, record.ID, reason, message)
	} else {
		updated, err = server.steering.MarkFailed(ctx, record.ID, reason, message)
	}
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
		server.settleOwnerSteeringRecord(ctx, record, owner.SteeringFailed, owner.SteeringReasonOwnerStateChanged, "Task is no longer available for accepted steering")
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
		server.settleOwnerSteeringRecord(ctx, record, owner.SteeringFailed, owner.SteeringReasonOwnerStateChanged, "Session is no longer available for accepted steering")
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

// steeringReplayIdentity contains only client-controlled idempotency fields.
// Delivery mode and provider Turn fences are server-selected and may change
// while the same durable request is being dispatched or safely falling back.
type steeringReplayIdentity struct {
	requestID                string
	operatorMessage          string
	clientSelectionIdentity  string
	modelProviderID          string
	model                    string
	requestedReasoningEffort string
}

type steeringClientSelectionIdentity struct {
	ModelProviderID          string `json:"model_provider_id"`
	Model                    string `json:"model"`
	RequestedReasoningEffort string `json:"requested_reasoning_effort"`
}

// canonicalSteeringClientSelectionIdentity preserves omitted selection fields
// as empty values. This is different from the resolved Runtime Turn Selection:
// idempotency compares exactly what the client controlled, while provider
// defaults and fallback mode remain server-owned.
func canonicalSteeringClientSelectionIdentity(modelProviderID, model, requestedReasoningEffort string) string {
	effort := strings.TrimSpace(requestedReasoningEffort)
	if effort != "" {
		if normalized, err := runtimeprofile.NormalizeReasoningEffort(effort); err == nil {
			effort = string(normalized)
		}
	}
	encoded, _ := json.Marshal(steeringClientSelectionIdentity{
		ModelProviderID:          strings.TrimSpace(modelProviderID),
		Model:                    strings.TrimSpace(model),
		RequestedReasoningEffort: effort,
	})
	return string(encoded)
}

func steeringReplayIdentityFromAccept(input steering.AcceptRequest) steeringReplayIdentity {
	operatorMessage := input.OperatorMessage
	if strings.TrimSpace(operatorMessage) == "" {
		operatorMessage = input.Message
	}
	return steeringReplayIdentity{
		requestID:                input.RequestID,
		operatorMessage:          operatorMessage,
		clientSelectionIdentity:  input.ClientSelectionIdentity,
		modelProviderID:          input.ModelProviderID,
		model:                    input.Model,
		requestedReasoningEffort: input.RequestedReasoningEffort,
	}
}

// steeringConflictMessage compares a repeated request against the durable
// client-controlled identity. New records compare the canonical raw selection,
// including empty fields. Legacy records have no raw identity and retain the
// former non-empty fallback comparison for upgrade compatibility.
func steeringConflictMessage(record *owner.SteeringRecord, identity steeringReplayIdentity) string {
	recordedMessage := record.OperatorMessage
	if strings.TrimSpace(recordedMessage) == "" {
		recordedMessage = record.Message
	}
	if strings.TrimSpace(recordedMessage) != strings.TrimSpace(identity.operatorMessage) {
		return "steer request id already belongs to a different message"
	}
	if record.ClientSelectionIdentity != "" {
		if record.ClientSelectionIdentity != identity.clientSelectionIdentity {
			return "steer request id already belongs to a different turn selection"
		}
		return ""
	}
	if value := strings.TrimSpace(identity.modelProviderID); value != "" && value != strings.TrimSpace(record.ModelProviderID) {
		return "steer request id already belongs to a different turn selection"
	}
	if value := strings.TrimSpace(identity.model); value != "" && value != strings.TrimSpace(record.Model) {
		return "steer request id already belongs to a different turn selection"
	}
	if value := strings.TrimSpace(identity.requestedReasoningEffort); value != "" && value != strings.TrimSpace(record.RequestedReasoningEffort) {
		return "steer request id already belongs to a different turn selection"
	}
	return ""
}

type steeringRequestConflictError struct{ message string }

func (err *steeringRequestConflictError) Error() string { return err.message }

func (server *Server) findAcceptedSteeringReplayByIdentity(adapter *steeringAdapter, identity steeringReplayIdentity) (*owner.SteeringRecord, bool, error) {
	record, err := server.steering.ByRequestID(adapter.kind, adapter.id, identity.requestID)
	if errors.Is(err, steering.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if conflict := steeringConflictMessage(record, identity); conflict != "" {
		return nil, false, &steeringRequestConflictError{message: conflict}
	}
	return record, true, nil
}

func (server *Server) findAcceptedSteeringReplay(adapter *steeringAdapter, input steering.AcceptRequest) (*owner.SteeringRecord, bool, error) {
	return server.findAcceptedSteeringReplayByIdentity(adapter, steeringReplayIdentityFromAccept(input))
}

// acceptSteeringOrReplay is the shared Task/Session acceptance kernel. It owns
// both the normal idempotency check and the concurrent unique-key race replay;
// owner handlers only format their public response and projection callback.
func (server *Server) acceptSteeringOrReplay(ctx context.Context, adapter *steeringAdapter, input steering.AcceptRequest, project func(*sql.Tx) (string, error)) (*owner.SteeringRecord, bool, error) {
	if record, replayed, err := server.findAcceptedSteeringReplay(adapter, input); err != nil || replayed {
		return record, replayed, err
	}
	record, err := server.acceptSteeringDurably(ctx, adapter, input, project)
	if !errors.Is(err, steering.ErrDuplicateRequest) {
		return record, false, err
	}
	replayed, found, replayErr := server.findAcceptedSteeringReplay(adapter, input)
	if replayErr != nil {
		return nil, false, replayErr
	}
	if !found {
		return nil, false, steering.ErrDuplicateRequest
	}
	return replayed, true, nil
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

func appliedSteeringPayload(record owner.SteeringRecord, result map[string]any, phase string) task.EventPayload {
	payload := make(task.EventPayload, len(result)+8)
	for key, value := range result {
		payload[key] = value
	}
	payload["request_id"] = record.RequestID
	payload["session_id"] = record.SessionID
	payload["mode"] = string(record.Mode)
	payload["outcome"] = "applied"
	payload["phase"] = phase
	payload["model_provider_id"] = record.ModelProviderID
	payload["model"] = record.Model
	payload["requested_reasoning_effort"] = record.RequestedReasoningEffort
	return payload
}

func projectTaskSteeringApplied(server *Server, record owner.SteeringRecord, continuationID string, result map[string]any) {
	payload := appliedSteeringPayload(record, result, taskSteerEventVocabulary.appliedPhase)
	payload["conversation_event_id"] = record.ConversationEventID
	if continuationID != "" {
		_, _ = server.tasks.AppendContinuationEvent(record.OwnerID, continuationID, taskSteerEventVocabulary.appliedKind, payload)
		return
	}
	_, _ = server.tasks.AppendEvent(record.OwnerID, taskSteerEventVocabulary.appliedKind, payload)
}

func projectSessionSteeringApplied(server *Server, record owner.SteeringRecord, continuationID string, result map[string]any) {
	payload := appliedSteeringPayload(record, result, sessionSteerEventVocabulary.appliedPhase)
	server.persistSessionProviderEventForContinuation(record.OwnerID, continuationID, sessionSteerEventVocabulary.appliedKind, payload)
}
