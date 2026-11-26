package runtime

import (
	"strings"

	"pentest/internal/runtimeplugin"
)

// CodexProviderSessionConfig configures one Task-owned Codex App Server
// session. ThreadID is the provider's durable session identity.
type CodexProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ThreadID     string
	ActiveTurnID string
	Capabilities runtimeplugin.Capabilities
}

type CodexProviderSession struct {
	*providerSessionAdapter
	assisted  *codexAssistedSession
	reasoning *reasoningDeltaBatcher
}

func NewCodexProviderSession(config CodexProviderSessionConfig) *CodexProviderSession {
	threadID := strings.TrimSpace(config.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(config.SessionID)
	}
	methods := providerWireMethods{
		send: "turn/start", interrupt: "turn/interrupt", steer: "turn/steer", permission: "item/permission/respond",
		params: func(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
			params := map[string]any{
				"threadId": sessionID,
				"turnId":   turnID,
				"input": []any{map[string]any{
					"type": "text",
					"text": request.Message,
				}},
			}
			if model := strings.TrimSpace(request.Model); model != "" {
				params["model"] = model
			}
			// Codex App Server accepts effort on turn/start. CyberPenda always
			// sends the resolved Requested Reasoning Effort explicitly.
			if effort := strings.TrimSpace(request.RequestedReasoningEffort); effort != "" {
				params["effort"] = effort
			}
			if request.PermissionRequestID != "" {
				params["permissionRequestId"] = request.PermissionRequestID
			}
			if request.PermissionDecision != "" {
				params["decision"] = request.PermissionDecision
			}
			// Persistent App Server does not inherit the exec-only
			// --dangerously-bypass-approvals-and-sandbox flag.
			params["approvalPolicy"] = "never"
			params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
			return params
		},
		steerParams: func(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
			expectedTurnID := strings.TrimSpace(request.ProviderTurnID)
			if expectedTurnID == "" {
				expectedTurnID = strings.TrimSpace(turnID)
			}
			return map[string]any{
				"threadId":       sessionID,
				"expectedTurnId": expectedTurnID,
				"input": []any{map[string]any{
					"type": "text",
					"text": request.Message,
				}},
				"clientUserMessageId": request.RequestID,
			}
		},
		steerRequiresExpectedTurn: true,
		turnID:                    nestedTurnID, sessionID: func(record map[string]any) string {
			return providerJSONValue(record, "threadId", "thread_id", "sessionId", "session_id")
		},
	}
	capabilities := providerCapabilities(config.Capabilities)
	return &CodexProviderSession{
		providerSessionAdapter: newProviderSessionAdapter("codex", config.Transport, threadID, config.ActiveTurnID, capabilities, methods),
		assisted:               newCodexAssistedSession(),
		reasoning:              newReasoningDeltaBatcher(reasoningBatchWindow),
	}
}
