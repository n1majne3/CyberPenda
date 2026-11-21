package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/task"
	"pentest/internal/timeline"
)

func writeTaskAcceptedSteering(response http.ResponseWriter, record *owner.SteeringRecord, fallbackSessionID string) {
	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackSessionID)
	}
	writeJSON(response, http.StatusAccepted, struct {
		RequestID string                      `json:"request_id"`
		SessionID string                      `json:"session_id"`
		Mode      runtime.ProviderSessionMode `json:"mode"`
		Outcome   string                      `json:"outcome"`
	}{RequestID: record.RequestID, SessionID: sessionID, Mode: runtime.ProviderSessionMode(record.Mode), Outcome: steeringOutcomeFromRecord(record)})
}

// nativeSteerOperationFunc is one provider session steer operation selected by
// the session mode (in_turn_steer or interrupt_then_replace).
type nativeSteerOperationFunc func(context.Context, runtime.ProviderSessionRequest, runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error)

// taskConclusionSettlement waits for durable Working Graph intents to settle
// before an accepted native steer may take the provider control boundary. The
// second (post-acquisition) pass is non-blocking so a conclusion that becomes
// pending can yield control to its own coordinator.
func (server *Server) taskConclusionSettlement(found task.Task) providerControlSettlement {
	return func(ctx context.Context, wait bool) (bool, error) {
		if found.BlackboardProtocol == "fgs" && found.RunControls.BlackboardMode != task.BlackboardModeDisabled {
			return server.settleTaskWorkingGraph(ctx, found, true)
		}
		return true, nil
	}
}

// executeNativeSteerOperation sends one accepted native steer Turn under Task
// control and projects applied/failed/action_required outcome events on the
// correct Runtime Continuation. The caller owns Task control for the whole
// operation. The returned execution is the durable terminal outcome.
func (server *Server) executeNativeSteerOperation(ctx context.Context, found task.Task, session runtime.ProviderSession, mode runtime.ProviderSessionMode, operation nativeSteerOperationFunc, providerRequest runtime.ProviderSessionRequest, conversation task.Event, continuationID string, freshContinuation bool) steeringExecution {
	return runNativeSteerTurn(ctx, steerExecutionSpec{
		operation:             operation,
		request:               providerRequest,
		mode:                  mode,
		providerSessionID:     session.SessionID(),
		initialContinuationID: continuationID,
		advanceOnSettled:      !freshContinuation,
		vocabulary:            taskSteerEventVocabulary,
		persistEvent: func(current string, kind task.EventKind, payload task.EventPayload) {
			payload["conversation_event_id"] = conversation.ID
			if current != "" {
				_, _ = server.tasks.AppendContinuationEvent(found.ID, current, kind, payload)
			}
		},
		persistOwnerEvent: func(kind task.EventKind, payload task.EventPayload) {
			payload["conversation_event_id"] = conversation.ID
			_, _ = server.tasks.AppendEvent(found.ID, kind, payload)
		},
		advance: func(current string) (string, error) {
			next := current
			if err := server.advanceNativeSteerContinuation(current, session, &next); err != nil {
				return "", err
			}
			return next, nil
		},
		onAdvanceSettled: func(current, _ string, advanceErr error) {
			if advanceErr == nil {
				return
			}
			_, _ = server.tasks.AppendContinuationEvent(found.ID, current, task.EventKindSteering, task.EventPayload{
				"request_id": providerRequest.RequestID, "session_id": session.SessionID(),
				"mode": string(mode), "outcome": "failed", "phase": "replacement_continuation_failed",
				"error_code": "continuation_transition_failed",
			})
		},
		failClosed: func(string) {
			_ = server.closeProviderSession(found.ID)
			if current, _ := server.tasks.ActiveContinuation(found.ID); current != nil {
				_, _ = server.tasks.UpdateContinuationStatus(current.ID, task.StatusFailed)
			}
			_, _ = server.tasks.UpdateStatus(found.ID, task.StatusFailed)
		},
		failOwnerExecution: true,
	})
}

type providerPermissionResponseRequest struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
}

// handleProviderPermissionResponse answers one provider permission request on
// the same Task-owned session. It is authenticated by ServeHTTP's daemon
// middleware and never exposes provider wire payloads.
func (server *Server) handleProviderPermissionResponse(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	session, bound := server.providerSessions.get(found.ID)
	if !bound || session == nil {
		writeError(response, http.StatusConflict, "provider session is unavailable")
		return
	}
	input, permissionID, valid := readPermissionResponse(response, request, json.NewDecoder(request.Body).Decode)
	if !valid {
		return
	}
	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	if !permissionResponsePending(response, input, permissionID, session.SessionID(), eventsToTimelineEvents(events)) {
		return
	}
	if found.Status != task.StatusRunning && found.Status != task.StatusPaused {
		writeError(response, http.StatusConflict, "provider permission requires an active Task")
		return
	}
	if !session.Capabilities().PermissionResponse {
		writeError(response, http.StatusConflict, "provider session does not support permission responses")
		return
	}
	if !server.acquireProviderTaskControl(found.ID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	active, err := server.tasks.ActiveContinuation(found.ID)
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}
	continuationID := ""
	if active != nil {
		continuationID = active.ID
	}
	requestedPayload := permissionResponseRequestedPayload(input, permissionID, session.SessionID())
	if continuationID != "" {
		_, err = server.tasks.AppendContinuationEvent(found.ID, continuationID, task.EventKindLifecycle, requestedPayload)
	} else {
		_, err = server.tasks.AppendEvent(found.ID, task.EventKindLifecycle, requestedPayload)
	}
	if err != nil {
		server.releaseProviderTaskControl(found.ID)
		writeTaskError(response, err)
		return
	}
	taskCtx := server.providerTaskContext(found.ID)
	persistLadderEvent := func(kind task.EventKind, payload task.EventPayload) {
		if continuationID != "" {
			_, _ = server.tasks.AppendContinuationEvent(found.ID, continuationID, kind, payload)
		} else {
			_, _ = server.tasks.AppendEvent(found.ID, kind, payload)
		}
	}
	emit := newPermissionResponseEmit(input, permissionID, persistLadderEvent)
	go func() {
		defer server.releaseProviderTaskControl(found.ID)
		ctx, cancel := context.WithTimeout(taskCtx, 30*time.Second)
		defer cancel()
		deliverPermissionResponse(ctx, session, input, permissionID, emit, persistLadderEvent)
	}()
	writePermissionResponseAccepted(response, input, permissionID, session.SessionID(), "accepted")
}

// providerPermissionStatus scans either owner's event stream for the state of
// one provider permission request. It runs over the neutral event shape shared
// with timeline.Build so task and session permission handling cannot drift.
func providerPermissionStatus(events []timeline.Event, permissionID, requestID string) (pending bool, outcome, decision string) {
	for _, event := range events {
		if event.Payload["permission_request_id"] != permissionID {
			continue
		}
		rid, _ := event.Payload["request_id"].(string)
		if rid == requestID {
			if value, ok := event.Payload["permission_decision"].(string); ok && value != "" {
				decision = normalizePermissionDecision(value)
			}
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			pending = true
		case "provider_permission_response_requested", "provider_permission_response_acknowledged":
			if rid == requestID {
				outcome = "pending"
			}
		case "provider_permission_response_applied":
			pending = false
			if rid == requestID {
				outcome = "applied"
			}
		case "provider_permission_response_failed":
			pending = true
			if rid == requestID {
				outcome = "failed"
			}
		}
	}
	return pending, outcome, decision
}

func decodeOptionalJSON(request *http.Request, target any) error {
	if request.Body == nil {
		return nil
	}
	raw, err := readBoundedJSONBody(request.Body, maxJSONRequestBodyBytes)
	if err != nil {
		return err
	}
	err = json.NewDecoder(bytes.NewReader(raw)).Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// advanceNativeSteerContinuation performs the old-turn settlement boundary
// while the provider control operation is still serialized. The adapter emits
// settled before sending the replacement turn, so the next provider event is
// guaranteed to land on the fresh Continuation.
func (server *Server) advanceNativeSteerContinuation(currentID string, session runtime.ProviderSession, continuationID *string) error {
	old, err := server.tasks.Continuation(currentID)
	if err != nil {
		return fmt.Errorf("load old continuation: %w", err)
	}
	found, err := server.tasks.Get(old.TaskID)
	if err != nil {
		return fmt.Errorf("load Task for replacement continuation: %w", err)
	}
	if _, err := server.settleTaskWorkingGraph(context.Background(), found, true); err != nil {
		return fmt.Errorf("settle old Working Graph: %w", err)
	}
	disabled := found.RunControls.BlackboardMode == task.BlackboardModeDisabled
	var next task.TaskContinuation
	if disabled {
		next, err = server.tasks.CreateReplacementContinuationWithoutBlackboard(old)
	} else {
		next, err = server.tasks.CreateReplacementContinuation(old)
	}
	if err != nil {
		return fmt.Errorf("create replacement continuation: %w", err)
	}
	if binder, ok := session.(runtime.ProviderSessionContinuationBinder); ok {
		if err := binder.BindContinuation(next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("bind provider continuation: %w", err)
		}
	}
	if _, err := server.tasks.UpdateContinuationStatus(next.ID, task.StatusRunning); err != nil {
		_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
		return fmt.Errorf("start replacement continuation: %w", err)
	}
	if server.harness.IsActive(old.TaskID) {
		if err := server.harness.RebindContinuation(old.TaskID, next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("rebind runtime continuation: %w", err)
		}
	}
	// Carry the Blackboard grant and Working Snapshot ownership to the
	// replacement before the old Continuation is completed. The in-container MCP
	// token is immutable, so this rebind keeps the still-running agent's
	// Blackboard writes alive on the replacement instead of resolving to the
	// completed old Continuation (closed_continuation).
	if !disabled && server.blackboardV2Continuity != nil {
		if err := server.blackboardV2Continuity.RebindContinuationForNativeSteer(context.Background(), old.ID, next.ID); err != nil {
			_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
			return fmt.Errorf("rebind Blackboard continuation grant: %w", err)
		}
	}
	if _, err := server.tasks.UpdateContinuationStatus(old.ID, task.StatusCompleted); err != nil {
		_, _ = server.tasks.UpdateContinuationStatus(next.ID, task.StatusFailed)
		return fmt.Errorf("settle old continuation: %w", err)
	}
	*continuationID = next.ID
	return nil
}

var errNoContinuationToContinue = errors.New("no prior continuation to continue after terminal Blackboard state")
