package runtime

import (
	"strings"

	"pentest/internal/runtimeplugin"
)

func defaultProviderCapabilities() runtimeplugin.Capabilities {
	return runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true}
}

func providerCapabilities(value runtimeplugin.Capabilities) runtimeplugin.Capabilities {
	if value.PersistentSession || value.SendTurn || value.InterruptTurn || value.InterruptThenReplace || value.InTurnSteer || value.PermissionResponse || value.ResumeSession {
		return value
	}
	return defaultProviderCapabilities()
}

func providerJSONValue(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nestedTurnID(record map[string]any) string {
	if turn, ok := record["turn"].(map[string]any); ok {
		return providerJSONValue(turn, "id", "turnId", "turn_id")
	}
	return providerJSONValue(record, "turnId", "turn_id", "id")
}

func identitySession(record map[string]any) string {
	return providerJSONValue(record, "sessionId", "session_id", "threadId", "thread_id")
}

func providerParams(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
	params := map[string]any{"session_id": sessionID, "turn_id": turnID, "message": request.Message}
	if request.PermissionRequestID != "" {
		params["permission_request_id"] = request.PermissionRequestID
		params["permissionRequestId"] = request.PermissionRequestID
	}
	if request.PermissionDecision != "" {
		params["permission_decision"] = request.PermissionDecision
		params["decision"] = request.PermissionDecision
	}
	return params
}
