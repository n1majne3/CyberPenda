package runtime

import (
	"strings"

	"pentest/internal/runtimeplugin"
)

// ClaudeCodeProviderSessionConfig configures one long-lived Claude Code SDK
// query exposed by the sandbox bridge.
type ClaudeCodeProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ActiveTurnID string
	Capabilities runtimeplugin.Capabilities
}

type ClaudeCodeProviderSession struct {
	*providerSessionAdapter
	assisted  *claudeAssistedSession
	reasoning *reasoningDeltaBatcher
}

func NewClaudeCodeProviderSession(config ClaudeCodeProviderSessionConfig) *ClaudeCodeProviderSession {
	methods := providerWireMethods{
		send: "claude/input", interrupt: "claude/interrupt", permission: "claude/permission/respond",
		params: claudeCodeParams, turnID: func(record map[string]any) string { return providerJSONValue(record, "turn_id", "turnId", "id") }, sessionID: identitySession,
	}
	capabilities := providerCapabilities(config.Capabilities)
	return &ClaudeCodeProviderSession{
		providerSessionAdapter: newProviderSessionAdapter("claude_code", config.Transport, config.SessionID, config.ActiveTurnID, capabilities, methods),
		assisted:               newClaudeAssistedSession(),
		reasoning:              newReasoningDeltaBatcher(reasoningBatchWindow),
	}
}

// claudeCodeParams maps the complete Runtime Turn Selection onto claude/input.
// The long-lived Claude Query applies model and Requested Reasoning Effort
// before the turn; model_provider_id is delivered for wire completeness but a
// provider change still restarts through Config Projection.
func claudeCodeParams(sessionID, turnID string, request ProviderSessionRequest) map[string]any {
	params := providerParams(sessionID, turnID, request)
	params["turn_kind"] = string(normalizeRuntimeTurnKind(request.TurnKind))
	if providerID := strings.TrimSpace(request.ModelProviderID); providerID != "" {
		params["model_provider_id"] = providerID
	}
	if model := strings.TrimSpace(request.Model); model != "" {
		params["model"] = model
	}
	if effort := strings.TrimSpace(request.RequestedReasoningEffort); effort != "" {
		params["requested_reasoning_effort"] = effort
	}
	return params
}
