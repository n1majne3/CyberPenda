package runtime

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/runtimeoutput"
	"pentest/internal/task"
)

const (
	maxHermesAttemptCandidateBytes = 64 << 10
	maxHermesDeliveredTurnIDs      = 4096
)

type hermesAttemptCandidate struct {
	raw     []byte
	invalid bool
}

type hermesAssistedState struct {
	mu                    sync.Mutex
	resultSink            ProviderSessionAttemptResultSink
	validationFailureSink ProviderSessionAttemptResultValidationFailureSink
	candidates            map[string]hermesAttemptCandidate
	delivered             map[string]bool
	deliveredOrder        []string
	toolNames             map[string]string
	activeThoughtKey      string
	thoughtSegment        int
}

var hermesAssistedStates sync.Map

func (s *HermesProviderSession) assistedState() *hermesAssistedState {
	if found, ok := hermesAssistedStates.Load(s); ok {
		return found.(*hermesAssistedState)
	}
	created := &hermesAssistedState{
		candidates: map[string]hermesAttemptCandidate{},
		delivered:  map[string]bool{},
		toolNames:  map[string]string{},
	}
	found, _ := hermesAssistedStates.LoadOrStore(s, created)
	return found.(*hermesAssistedState)
}

func (s *HermesProviderSession) SetAttemptResultSink(sink ProviderSessionAttemptResultSink) {
	state := s.assistedState()
	state.mu.Lock()
	state.resultSink = sink
	state.mu.Unlock()
}

func (s *HermesProviderSession) SetAttemptResultValidationFailureSink(sink ProviderSessionAttemptResultValidationFailureSink) {
	state := s.assistedState()
	state.mu.Lock()
	state.validationFailureSink = sink
	state.mu.Unlock()
}

// HandleEvent maps Hermes ACP session/update notifications onto the bounded
// assisted observation and closed Attempt result seams. Agent thought chunks
// stream as batched runtime output so operator reasoning stays visible; raw
// tool payloads never become observations.
func (s *HermesProviderSession) HandleEvent(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	method := strings.ToLower(strings.TrimSpace(event.Method))
	params := map[string]any{}
	if len(event.Params) > 0 && json.Unmarshal(event.Params, &params) != nil {
		return
	}
	update := hermesACPUpdate(params)
	kind := hermesACPUpdateKind(method, update)
	if method == "session/update" && kind != "turn_ended" {
		sessionID := providerJSONValue(params, "sessionId", "session_id")
		if sessionID == "" {
			sessionID = s.SessionID()
		}
		turnID := providerJSONValue(params, "turn_id", "turnId")
		if turnID == "" {
			turnID = s.currentTurn()
		}
		s.recordProviderTurnEvent(sessionID, turnID, kind == "turn_ended")
	}
	switch kind {
	case "agent_message_chunk":
		s.captureHermesAttemptCandidate(params, update)
		s.completeHermesThought(emit, params, event)
		s.emitHermesRuntimeOutput(event, params, emit)
		return
	case "agent_thought_chunk":
		s.acceptHermesThoughtChunk(event, params, update, emit)
		return
	case "tool_call":
		s.completeHermesThought(emit, params, event)
		s.emitHermesToolObservation(params, update, ProviderSessionObservationToolUse)
		s.emitHermesRuntimeOutput(event, params, emit)
		return
	case "tool_call_update":
		s.completeHermesThought(emit, params, event)
		s.emitHermesToolObservation(params, update, ProviderSessionObservationToolResult)
		s.emitHermesRuntimeOutput(event, params, emit)
		return
	case "turn_ended":
		sessionID := providerJSONValue(params, "sessionId", "session_id")
		if sessionID == "" {
			sessionID = s.SessionID()
		}
		turnID := providerJSONValue(params, "turn_id", "turnId")
		if turnID == "" {
			turnID = s.currentTurn()
		}
		s.completeHermesThought(emit, params, event)
		s.projectHermesTerminalLifecycle(event, params, hermesACPStopStatus(update), emit)
		s.finishHermesAssistedTurn(params, update)
		s.recordProviderTurnEvent(sessionID, turnID, true)
		return
	default:
		s.completeHermesThought(emit, params, event)
		s.providerSessionAdapter.HandleEvent(event, emit)
	}
}

// acceptHermesThoughtChunk accumulates one agent thought chunk into the
// session batcher. Flushed batches carry the cumulative text keyed by turn so
// transcript projections grow one stable reasoning entry in place.
func (s *HermesProviderSession) acceptHermesThoughtChunk(event SandboxBridgeEvent, params, update map[string]any, emit ProviderSessionEmit) {
	// Thought text must stay untrimmed: a trailing space often separates words.
	text := hermesThoughtText(update)
	if text == "" {
		return
	}
	turnID := providerJSONValue(params, "turn_id", "turnId")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	sessionID := providerJSONValue(params, "sessionId", "session_id")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	key := s.hermesThoughtSegmentKey(turnID)
	s.reasoning.Add(key, func(cumulative string) {
		s.emitBatchedHermesThought(emit, sessionID, turnID, key, cumulative, event)
	}, text)
}

func (s *HermesProviderSession) hermesThoughtSegmentKey(turnID string) string {
	state := s.assistedState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.activeThoughtKey == "" {
		state.thoughtSegment++
		state.activeThoughtKey = "hermes-thought-" + turnID + "-" + strconv.Itoa(state.thoughtSegment)
	}
	return state.activeThoughtKey
}

func (s *HermesProviderSession) completeHermesThought(emit ProviderSessionEmit, params map[string]any, event SandboxBridgeEvent) {
	key, text, ok := s.reasoning.Complete()
	state := s.assistedState()
	state.mu.Lock()
	state.activeThoughtKey = ""
	state.mu.Unlock()
	if !ok {
		return
	}
	turnID := providerJSONValue(params, "turn_id", "turnId")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	sessionID := providerJSONValue(params, "sessionId", "session_id")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	s.emitHermesThoughtRecord(emit, sessionID, turnID, key, text, runtimeoutput.ReasoningPhaseCompleted, event)
}

func (s *HermesProviderSession) emitBatchedHermesThought(emit ProviderSessionEmit, sessionID, turnID, key, cumulative string, event SandboxBridgeEvent) {
	s.emitHermesThoughtRecord(emit, sessionID, turnID, key, cumulative, runtimeoutput.ReasoningPhaseStreaming, event)
}

func (s *HermesProviderSession) emitHermesThoughtRecord(emit ProviderSessionEmit, sessionID, turnID, key, cumulative, phase string, event SandboxBridgeEvent) {
	if cumulative == "" {
		return
	}
	encoded, err := json.Marshal(map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"text":          cumulative,
		"item_id":       key,
		"phase":         phase,
	})
	if err != nil {
		return
	}
	s.emitReasoningRuntimeOutput(emit, reasoningRuntimeOutput{
		ProviderEvent: event.Method,
		SessionID:     sessionID,
		TurnID:        turnID,
		ItemID:        key,
		Stream:        "hermes_acp",
		Phase:         phase,
		Text:          string(encoded),
	})
}

func (s *HermesProviderSession) emitHermesRuntimeOutput(event SandboxBridgeEvent, params map[string]any, emit ProviderSessionEmit) {
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	if emit == nil || len(event.Params) == 0 {
		return
	}
	sessionID := providerJSONValue(params, "sessionId", "session_id")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	turnID := providerJSONValue(params, "turn_id", "turnId")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	emit(task.EventKindRuntimeOutput, task.EventPayload{
		"provider": s.provider, "provider_event": event.Method,
		"session_id": sessionID, "provider_turn_id": turnID,
		"stream": "hermes_acp", "text": string(event.Params),
	})
}

func (s *HermesProviderSession) projectHermesTerminalLifecycle(event SandboxBridgeEvent, params map[string]any, outcome string, emit ProviderSessionEmit) {
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	lineage, sessionID, turnID, ok := s.hermesEventLineage(params)
	if !ok || emit == nil {
		return
	}
	emit(task.EventKindLifecycle, task.EventPayload{
		"provider": "hermes", "provider_event": event.Method,
		"request_id": lineage.RequestID, "session_id": sessionID,
		"provider_turn_id": turnID, "mode": string(ProviderSessionModeSendTurn),
		"outcome": outcome,
	})
}

func (s *HermesProviderSession) captureHermesAttemptCandidate(params, update map[string]any) {
	lineage, _, _, ok := s.hermesEventLineage(params)
	if !ok || lineage.Kind != RuntimeTurnKindControl {
		return
	}
	text := hermesACPAssistantText(update)
	candidate := hermesAttemptCandidate{invalid: text == "" || len(text) > maxHermesAttemptCandidateBytes}
	if text != "" && !candidate.invalid {
		candidate.raw = append(candidate.raw, text...)
	}
	state := s.assistedState()
	state.mu.Lock()
	if !state.delivered[lineage.RequestID] {
		prior := state.candidates[lineage.RequestID]
		if !candidate.invalid && len(prior.raw) > 0 && !prior.invalid {
			combined := append(append([]byte{}, prior.raw...), candidate.raw...)
			if len(combined) > maxHermesAttemptCandidateBytes {
				prior.invalid = true
				prior.raw = nil
			} else {
				prior.raw = combined
			}
			state.candidates[lineage.RequestID] = prior
		} else {
			state.candidates[lineage.RequestID] = candidate
		}
	}
	state.mu.Unlock()
}

func (s *HermesProviderSession) emitHermesToolObservation(params, update map[string]any, kind ProviderSessionObservationKind) {
	lineage, sessionID, turnID, ok := s.hermesEventLineage(params)
	if !ok {
		return
	}
	toolCallID := providerJSONValue(update, "toolCallId", "tool_call_id")
	toolName := providerJSONValue(update, "title", "toolName", "tool_name", "name")
	state := s.assistedState()
	state.mu.Lock()
	if toolCallID != "" && toolName != "" {
		state.toolNames[toolCallID] = toolName
	} else if toolCallID != "" && toolName == "" {
		toolName = state.toolNames[toolCallID]
	}
	state.mu.Unlock()
	observation := ProviderSessionObservation{
		Kind: kind, RequestID: lineage.RequestID, SessionID: sessionID,
		ProviderTurnID: turnID, ToolCallID: toolCallID, ToolName: toolName,
	}
	observation.BlackboardOperation, _ = ClassifyTrustedBlackboardTool(toolName)
	if kind == ProviderSessionObservationToolResult {
		observation.Status = hermesACPToolResultStatus(update)
	}
	s.emitObservation(observation)
}

func (s *HermesProviderSession) finishHermesAssistedTurn(params, update map[string]any) {
	lineage, sessionID, turnID, ok := s.hermesEventLineage(params)
	if !ok {
		return
	}
	state := s.assistedState()
	state.mu.Lock()
	if state.delivered[lineage.RequestID] {
		state.mu.Unlock()
		return
	}
	state.delivered[lineage.RequestID] = true
	state.deliveredOrder = append(state.deliveredOrder, lineage.RequestID)
	if len(state.deliveredOrder) > maxHermesDeliveredTurnIDs {
		oldest := state.deliveredOrder[0]
		state.deliveredOrder = state.deliveredOrder[1:]
		delete(state.delivered, oldest)
	}
	candidate, found := state.candidates[lineage.RequestID]
	delete(state.candidates, lineage.RequestID)
	resultSink := state.resultSink
	failureSink := state.validationFailureSink
	state.mu.Unlock()

	s.emitObservation(ProviderSessionObservation{
		Kind: ProviderSessionObservationTurnCompleted, RequestID: lineage.RequestID,
		SessionID: sessionID, ProviderTurnID: turnID, Status: hermesACPStopStatus(update),
	})
	if lineage.Kind != RuntimeTurnKindControl {
		return
	}

	validated, err := blackboardconclusion.Decode(candidate.raw)
	if !found || candidate.invalid || err != nil {
		if failureSink != nil {
			reason := blackboardconclusion.ValidationReasonInvalidResult
			if found && candidate.invalid {
				reason = blackboardconclusion.ValidationReasonResultTooLarge
			}
			failureSink(attemptResultValidationFailure(lineage.RequestID, sessionID, turnID, err, reason))
		}
		return
	}
	if resultSink != nil {
		resultSink(ProviderSessionAttemptResult{
			RequestID: lineage.RequestID, SessionID: sessionID,
			ProviderTurnID: turnID, Validated: validated,
		})
	}
}

func (s *HermesProviderSession) hermesEventLineage(params map[string]any) (ProviderSessionTurnLineage, string, string, bool) {
	sessionID := providerJSONValue(params, "sessionId", "session_id")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	turnID := providerJSONValue(params, "turn_id", "turnId")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	requestID := providerJSONValue(params, "request_id", "requestId")
	lineage, ok := s.ResolveProviderSessionTurnLineage(requestID, turnID)
	if !ok && requestID == "" {
		lineage, ok = s.ResolveProviderSessionTurnLineage(turnID, turnID)
	}
	if !ok || strings.TrimSpace(sessionID) == "" || sessionID != s.SessionID() || strings.TrimSpace(turnID) == "" {
		return ProviderSessionTurnLineage{}, "", "", false
	}
	return lineage, sessionID, turnID, true
}

func hermesACPUpdate(params map[string]any) map[string]any {
	if update, ok := params["update"].(map[string]any); ok {
		return update
	}
	return params
}

func hermesACPUpdateKind(method string, update map[string]any) string {
	kind := strings.ToLower(strings.TrimSpace(providerJSONValue(update, "sessionUpdate", "session_update")))
	if kind != "" {
		return kind
	}
	switch {
	case strings.Contains(method, "tool_call_update"), strings.HasSuffix(method, "tool/result"):
		return "tool_call_update"
	case strings.Contains(method, "tool_call"), strings.HasSuffix(method, "tool/used"):
		return "tool_call"
	case strings.Contains(method, "agent_message"), strings.Contains(method, "message_chunk"):
		return "agent_message_chunk"
	case strings.Contains(method, "turn_ended"), strings.Contains(method, "turn/completed"):
		return "turn_ended"
	default:
		return kind
	}
}

func hermesACPAssistantText(update map[string]any) string {
	if text := providerJSONValue(update, "text"); text != "" {
		return text
	}
	content, ok := update["content"].(map[string]any)
	if !ok {
		return ""
	}
	return providerJSONValue(content, "text")
}

// hermesThoughtText extracts an agent thought chunk without trimming so
// word boundaries survive delta batching.
func hermesThoughtText(update map[string]any) string {
	if text, ok := update["text"].(string); ok && text != "" {
		return text
	}
	if content, ok := update["content"].(map[string]any); ok {
		if text, ok := content["text"].(string); ok {
			return text
		}
	}
	return ""
}

func hermesACPToolResultStatus(update map[string]any) string {
	status := strings.ToLower(strings.TrimSpace(providerJSONValue(update, "status")))
	switch status {
	case "failed", "error", "cancelled", "canceled":
		return "failed"
	default:
		return "succeeded"
	}
}

func hermesACPStopStatus(update map[string]any) string {
	reason := strings.ToLower(strings.TrimSpace(providerJSONValue(update, "stopReason", "stop_reason", "status")))
	switch reason {
	case "cancelled", "canceled", "interrupted":
		return "interrupted"
	case "refusal", "failed", "error":
		return "failed"
	default:
		return "completed"
	}
}
