package runtime

import (
	"encoding/json"
	"strings"

	"pentest/internal/task"
)

// HandleEvent maps provider notifications to the normalized lifecycle channel.
// Providers use different event names, but all first-release adapters expose
// the same started/completed/interrupted/permission state vocabulary.
func (s *providerSessionAdapter) HandleEvent(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	if strings.TrimSpace(event.Method) == "" {
		return
	}
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	params := map[string]any{}
	if len(event.Params) > 0 && json.Unmarshal(event.Params, &params) != nil {
		return
	}
	method := strings.ToLower(event.Method)
	if method == "claude/runtime_output" {
		text := providerJSONValue(params, "text")
		if text == "" {
			return
		}
		sessionID := providerJSONValue(params, "session_id", "sessionId")
		if sessionID == "" {
			sessionID = s.SessionID()
		}
		turnID := providerJSONValue(params, "turn_id", "turnId")
		if turnID == "" {
			turnID = s.currentTurn()
		}
		if emit != nil {
			emit(task.EventKindRuntimeOutput, task.EventPayload{
				"provider": s.provider, "provider_event": event.Method,
				"session_id": sessionID, "provider_turn_id": turnID,
				"stream": providerJSONValue(params, "stream"), "text": text,
			})
		}
		return
	}
	mode := ProviderSessionModeSendTurn
	outcome := ""
	switch {
	case strings.Contains(method, "permission") || strings.Contains(method, "extension_ui"):
		mode, outcome = ProviderSessionModePermissionResponse, "requested"
	case strings.Contains(method, "interrupt") || strings.Contains(method, "abort") || strings.Contains(method, "cancel"):
		mode, outcome = ProviderSessionModeInterruptTurn, "settled"
	case strings.Contains(method, "agent_end") || strings.Contains(method, "turn_end") || strings.HasSuffix(method, "/end"):
		mode, outcome = ProviderSessionModeSendTurn, "completed"
	case strings.Contains(method, "completed") || strings.Contains(method, "complete"):
		mode, outcome = ProviderSessionModeSendTurn, "completed"
	case strings.Contains(method, "started") || strings.Contains(method, "start"):
		mode, outcome = ProviderSessionModeSendTurn, "started"
	case strings.Contains(method, "failed") || strings.Contains(method, "error") || strings.Contains(method, "rejected"):
		mode, outcome = ProviderSessionModeSendTurn, "failed"
	default:
		return
	}
	turnID := s.methods.turnID(params)
	sessionID := s.methods.sessionID(params)
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	if turnID == "" {
		turnID = s.currentTurn()
	}
	terminal := outcome == "settled" || outcome == "completed" || outcome == "failed" || providerTurnSettled(params)
	s.recordProviderTurnEvent(sessionID, turnID, terminal)
	requestID := providerJSONValue(params, "request_id", "requestId", "control_id", "controlId")
	kind := task.EventKindLifecycle
	if mode == ProviderSessionModeInterruptTurn {
		kind = task.EventKindSteering
	}
	payload := task.EventPayload{
		"provider": s.provider, "provider_event": event.Method, "request_id": requestID,
		"session_id": sessionID, "provider_turn_id": turnID, "mode": string(mode), "outcome": outcome,
	}
	if mode == ProviderSessionModePermissionResponse {
		payload["permission_request_id"] = providerJSONValue(params, "permission_request_id", "permissionRequestId", "permission_id", "permissionId")
		payload["phase"] = "provider_permission_requested"
	}
	if emit != nil {
		emit(kind, payload)
	}
}

func (s *providerSessionAdapter) recordProviderTurnEvent(sessionID, turnID string, terminal bool) {
	s.mu.Lock()
	currentSession := s.sessionID
	currentTurn := s.activeTurnID
	if currentSession == "" || currentSession == sessionID {
		s.sessionID = sessionID
	}
	// Capture settlement against the pre-clear active turn so interrupt waits
	// still release when the terminal notification arrives.
	interruptActive := s.active && (s.activeMode == ProviderSessionModeInterruptTurn || s.activeMode == ProviderSessionModeInterruptThenReplace)
	matchingSession := currentSession == "" || currentSession == sessionID
	matchingTurn := currentTurn == "" || currentTurn == turnID

	if terminal && interruptActive && matchingSession && matchingTurn && sessionID != "" && turnID != "" {
		s.settlementSeq++
		s.settlements[providerSettlementKey(sessionID, turnID)] = providerSettlement{seq: s.settlementSeq}
		close(s.settlementChanged)
		s.settlementChanged = make(chan struct{})
	}
	if currentSession == "" || currentSession == sessionID {
		if terminal {
			// A terminal notification can race the matching turn/start response.
			// Keep only operation-local terminal identities for the one pending
			// start generation; arbitrary historical Turn ids never accumulate.
			if s.pendingStartGeneration != 0 && sessionID != "" && turnID != "" && len(s.pendingStartTerminalTurns) < 8 {
				s.pendingStartTerminalTurns[providerSettlementKey(sessionID, turnID)] = struct{}{}
			}
			if currentTurn == "" || currentTurn == turnID {
				s.activeTurnID = ""
			}
		} else if strings.TrimSpace(turnID) != "" {
			s.activeTurnID = turnID
		}
	}
	s.mu.Unlock()
}
