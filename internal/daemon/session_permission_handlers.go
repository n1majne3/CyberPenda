package daemon

import (
	"context"
	"net/http"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/timeline"
)

// handleSessionProviderPermissionResponse answers a provider approval request
// on the Session-owned persistent provider session. It intentionally mirrors
// the provider control contract without routing through Project/Task lookup.
func (server *Server) handleSessionProviderPermissionResponse(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := server.sessions.Get(id); err != nil {
		writeSessionError(response, err)
		return
	}
	if err := server.waitForSessionWorkingGraphSettlement(request.Context(), id, true); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	provider, bound := server.sessionProviderSessions.get(id)
	if !bound || provider == nil {
		writeError(response, http.StatusConflict, "Session Runtime provider session is unavailable")
		return
	}
	input, permissionID, valid := readPermissionResponse(response, request, func(value any) error { return decodeOptionalJSON(request, value) })
	if !valid {
		return
	}
	events, err := server.sessions.Events(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if !permissionResponsePending(response, input, permissionID, provider.SessionID(), sessionEventsAsTimeline(events)) {
		return
	}
	if !provider.Capabilities().PermissionResponse {
		writeError(response, http.StatusConflict, "provider session does not support permission responses")
		return
	}
	active, err := server.sessions.ActiveContinuation(id)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if active == nil {
		writeError(response, http.StatusConflict, "provider permission requires an active Session Runtime")
		return
	}
	server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle,
		permissionResponseRequestedPayload(input, permissionID, provider.SessionID()))
	persistLadderEvent := func(kind task.EventKind, payload task.EventPayload) {
		server.persistSessionProviderEventForContinuation(id, active.ID, kind, payload)
	}
	emit := newPermissionResponseEmit(input, permissionID, persistLadderEvent)
	queued := server.enqueueProviderTaskControlAfterSettlement(id, server.sessionWorkingGraphSettlement(id, true), func(ctx context.Context) {
		operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		deliverPermissionResponse(operationCtx, provider, input, permissionID, emit, persistLadderEvent)
	})
	if !queued {
		server.persistSessionProviderEventForContinuation(id, active.ID, task.EventKindLifecycle, task.EventPayload{
			"phase": "provider_permission_response_failed", "outcome": "failed", "request_id": input.RequestID,
			"permission_request_id": permissionID, "mode": string(runtime.ProviderSessionModePermissionResponse), "error_code": "control_unavailable",
		})
		writeError(response, http.StatusConflict, "Session Runtime control operation is unavailable")
		return
	}
	writePermissionResponseAccepted(response, input, permissionID, provider.SessionID(), "accepted")
}

// sessionEventsAsTimeline projects session events into the neutral event shape
// shared with timeline.Build and the owner-neutral permission status scanner.
func sessionEventsAsTimeline(events []session.Event) []timeline.Event {
	converted := make([]timeline.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, timeline.Event{
			ID:        event.ID,
			Seq:       event.Seq,
			Kind:      string(event.Kind),
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}
	return converted
}

func (server *Server) handleSessionStop(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := server.sessions.Get(id); err != nil {
		writeSessionError(response, err)
		return
	}
	if err := server.stopSessionRuntime(id); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	server.writeDecoratedSession(response, http.StatusOK, id)
}
