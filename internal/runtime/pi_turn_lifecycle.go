package runtime

import (
	"maps"
	"strings"
)

type piTurnPhase string

const (
	piTurnRunning  piTurnPhase = "running"
	piTurnRetrying piTurnPhase = "retrying"
	piTurnSettling piTurnPhase = "settling"
	piTurnSettled  piTurnPhase = "settled"
)

type piTurnLifecycle struct {
	phase   piTurnPhase
	outcome string
	err     string
}

func (s *PiProviderSession) handlePiTurnLifecycle(event SandboxBridgeEvent, params map[string]any, emit ProviderSessionEmit) {
	if s.SessionClosed() {
		return
	}
	sessionID := providerJSONValue(params, "session_id", "sessionId")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	turnID := providerJSONValue(params, "turn_id", "turnId")
	active := s.currentTurn()
	if turnID == "" {
		turnID = active
	}
	if sessionID != s.SessionID() || turnID == "" || active != "" && active != turnID {
		return
	}
	if _, _, _, known := s.piEventLineage(params); !known && active != turnID {
		return
	}
	state := s.assistedState()
	state.mu.Lock()
	if state.turns == nil {
		state.turns = make(map[string]piTurnLifecycle)
	}
	current, exists := state.turns[turnID]
	next, projection := transitionPiTurn(current, strings.ToLower(event.Method), params)
	if !exists {
		state.turnOrder = append(state.turnOrder, turnID)
		if len(state.turnOrder) > maxPiDeliveredTurnIDs {
			delete(state.turns, state.turnOrder[0])
			state.turnOrder = state.turnOrder[1:]
		}
	}
	state.turns[turnID] = next
	if next.phase == piTurnSettled {
		state.turns[turnID] = piTurnLifecycle{phase: piTurnSettled}
	}
	state.mu.Unlock()
	if projection == "" {
		return
	}
	resolved := maps.Clone(params)
	resolved["session_id"], resolved["turn_id"] = sessionID, turnID
	if next.err != "" {
		resolved["errorMessage"] = next.err
	}
	if next.phase != piTurnSettled {
		s.emitPiLifecycle(event.Method, resolved, projection, emit)
		return
	}
	s.projectPiTerminalLifecycle(event, resolved, next.outcome, emit)
	s.finishPiAssistedTurn(resolved, next.outcome)
}

func piLoopBoundary(method string) bool {
	switch method {
	case "pi/agent_start", "pi/turn_start", "pi/turn_end", "pi/agent_end", "pi/agent_settled", "pi/auto_retry_start", "pi/auto_retry_end":
		return true
	default:
		return false
	}
}

func transitionPiTurn(current piTurnLifecycle, method string, params map[string]any) (piTurnLifecycle, string) {
	if current.phase == piTurnSettled {
		return current, ""
	}
	next := current
	switch method {
	case "pi/agent_start", "pi/turn_start":
		next = piTurnLifecycle{phase: piTurnRunning}
		return next, "started"
	case "pi/turn_end":
		if current.phase == piTurnSettling {
			return current, ""
		}
		next.phase = piTurnRunning
		next.outcome = piTerminalStatus(method, params)
		next.err, _ = piTerminalError(params)
		return next, "step_" + next.outcome
	case "pi/agent_end":
		if current.phase == piTurnSettling {
			return current, ""
		}
		next.outcome = piTerminalStatus(method, params)
		next.err, _ = piTerminalError(params)
		if retry, modern := params["willRetry"].(bool); modern {
			next.phase = piTurnSettling
			if retry {
				next.phase = piTurnRetrying
			}
			return next, string(next.phase)
		}
	case "pi/auto_retry_start":
		if current.phase == piTurnSettling {
			return current, ""
		}
		next.phase, next.outcome = piTurnRetrying, "failed"
		next.err, _ = piTerminalError(params)
		return next, "retrying"
	case "pi/auto_retry_end":
		next.phase = piTurnSettling
		if success, known := params["success"].(bool); known {
			next.outcome, next.err = "completed", ""
			if !success {
				next.outcome, next.err = "failed", providerJSONValue(params, "finalError", "errorMessage")
			}
		}
		return next, "settling"
	case "pi/agent_settled":
		if piTurnAborted(params) {
			next.outcome, next.err = "interrupted", ""
		} else if next.outcome == "" {
			next.outcome, next.err = "failed", "Pi settled without a terminal result"
		}
	default:
		next.outcome = piTerminalStatus(method, params)
		next.err, _ = piTerminalError(params)
	}
	next.phase = piTurnSettled
	return next, next.outcome
}

func piTurnAborted(params map[string]any) bool {
	if aborted, _ := params["aborted"].(bool); aborted {
		return true
	}
	abortedMessage := func(message map[string]any) bool {
		reason := strings.ToLower(providerJSONValue(message, "stopReason", "stop_reason"))
		return reason == "aborted" || reason == "interrupted" || reason == "canceled" || reason == "cancelled"
	}
	if abortedMessage(params) {
		return true
	}
	if message, ok := params["message"].(map[string]any); ok && abortedMessage(message) {
		return true
	}
	if messages, ok := params["messages"].([]any); ok {
		for index := len(messages) - 1; index >= 0; index-- {
			if message, ok := messages[index].(map[string]any); ok && providerJSONValue(message, "role") == "assistant" {
				return abortedMessage(message)
			}
		}
	}
	return false
}
