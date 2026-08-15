package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

// The provider permission-response ladder is one owner-neutral protocol
// (ADR 0020): both Task and Session owners derive the response identity the
// same way, scan the same idempotency status, persist the same bounded event
// shapes, and classify delivery failures through one code table. Owners keep
// only their gating rules, persistence sinks, and provider-control discipline.

// normalizePermissionDecision folds common decision spellings onto the closed
// allow/deny vocabulary.
func normalizePermissionDecision(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "allow", "approve", "approved", "yes":
		return "allow"
	case "deny", "reject", "rejected", "no":
		return "deny"
	default:
		return ""
	}
}

// derivePermissionResponseRequestID fills the idempotent response identity:
// the body's request_id, else the Idempotency-Key header, else a stable
// permission-scoped default.
func derivePermissionResponseRequestID(request *http.Request, input *providerPermissionResponseRequest) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		input.RequestID = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if input.RequestID == "" {
		input.RequestID = "permission-" + request.PathValue("permission_id") + "-" + input.Decision
	}
}

// permissionResponseRequestedPayload builds the pending intent event both
// owners persist before dispatching the response.
func permissionResponseRequestedPayload(input providerPermissionResponseRequest, permissionID, providerSessionID string) task.EventPayload {
	return task.EventPayload{
		"phase": "provider_permission_response_requested", "mode": string(runtime.ProviderSessionModePermissionResponse),
		"outcome": "pending", "request_id": input.RequestID, "permission_request_id": permissionID,
		"permission_decision": input.Decision, "session_id": providerSessionID,
	}
}

// newPermissionResponseEmit builds the redacting sink for provider callbacks
// during one permission response. Fixed correlation allowlist only: raw
// protocol payload never reaches the owner Timeline.
func newPermissionResponseEmit(input providerPermissionResponseRequest, permissionID string, persist func(task.EventKind, task.EventPayload)) func(task.EventKind, task.EventPayload) {
	return func(kind task.EventKind, payload task.EventPayload) {
		redacted := task.EventPayload{}
		for _, key := range []string{"provider", "request_id", "session_id", "provider_turn_id", "mode", "outcome", "permission_request_id", "permission_decision", "error_code", "phase"} {
			if value, ok := payload[key]; ok {
				redacted[key] = value
			}
		}
		if redacted["request_id"] == nil {
			redacted["request_id"] = input.RequestID
		}
		redacted["permission_request_id"] = permissionID
		if redacted["mode"] == nil {
			redacted["mode"] = string(runtime.ProviderSessionModePermissionResponse)
		}
		switch redacted["outcome"] {
		case "requested":
			redacted["phase"] = "provider_permission_response_requested"
		case "acknowledged":
			redacted["phase"] = "provider_permission_response_acknowledged"
		case "failed":
			redacted["phase"] = "provider_permission_response_failed"
		}
		persist(kind, redacted)
	}
}

// deliverPermissionResponse executes the bounded provider RespondPermission
// call and projects its terminal outcome: a classified failure event through
// the redacting sink, or the applied result event through persist.
func deliverPermissionResponse(ctx context.Context, provider runtime.ProviderSession, input providerPermissionResponseRequest, permissionID string, emit, persist func(task.EventKind, task.EventPayload)) {
	result, operationErr := provider.RespondPermission(ctx, runtime.ProviderSessionRequest{
		RequestID: input.RequestID, PermissionRequestID: permissionID, PermissionDecision: input.Decision,
	}, emit)
	if operationErr != nil {
		emit(task.EventKindLifecycle, task.EventPayload{
			"outcome": "failed", "phase": "provider_permission_response_failed",
			"error_code": permissionResponseErrorCode(operationErr),
		})
		return
	}
	payload := result.Payload()
	payload["phase"] = "provider_permission_response_applied"
	payload["outcome"] = "applied"
	payload["permission_request_id"] = permissionID
	persist(task.EventKindLifecycle, payload)
}

// writePermissionResponseAccepted answers the operator with the idempotent
// acceptance (or prior-outcome replay) envelope.
func writePermissionResponseAccepted(response http.ResponseWriter, input providerPermissionResponseRequest, permissionID, providerSessionID, outcome string) {
	writeJSON(response, http.StatusAccepted, map[string]any{
		"request_id": input.RequestID, "permission_request_id": permissionID,
		"session_id": providerSessionID, "decision": input.Decision, "outcome": outcome,
	})
}

// permissionResponseErrorCode maps a provider permission-response failure to
// the stable error_code both owner kinds persist, so task and session
// permission handling share one classification.
func permissionResponseErrorCode(operationErr error) string {
	switch {
	case errors.Is(operationErr, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(operationErr, context.Canceled):
		return "server_closing"
	case errors.Is(operationErr, runtime.ErrProviderSessionClosed):
		return "session_closed"
	case errors.Is(operationErr, runtime.ErrProviderSessionControlConflict):
		return "control_conflict"
	default:
		return "provider_rejected"
	}
}
