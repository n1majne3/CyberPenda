package runtime

import (
	"encoding/json"
	"strings"
	"sync"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/runtimeoutput"
)

const maxClaudeAssistedDedupKeys = 4096

type claudeAssistedSession struct {
	mu                      sync.Mutex
	result                  ProviderSessionAttemptResultSink
	invalid                 ProviderSessionAttemptResultValidationFailureSink
	observationsDelivered   map[string]struct{}
	observationOrder        []string
	attemptResultsDelivered map[string]struct{}
	attemptResultOrder      []string
}

func newClaudeAssistedSession() *claudeAssistedSession {
	return &claudeAssistedSession{
		observationsDelivered:   map[string]struct{}{},
		attemptResultsDelivered: map[string]struct{}{},
	}
}

func (s *ClaudeCodeProviderSession) SetAttemptResultSink(sink ProviderSessionAttemptResultSink) {
	s.assisted.mu.Lock()
	s.assisted.result = sink
	s.assisted.mu.Unlock()
}

func (s *ClaudeCodeProviderSession) SetAttemptResultValidationFailureSink(sink ProviderSessionAttemptResultValidationFailureSink) {
	s.assisted.mu.Lock()
	s.assisted.invalid = sink
	s.assisted.mu.Unlock()
}

// HandleEvent consumes only the closed Claude assisted protocol before
// delegating ordinary runtime-output and lifecycle projection. Thinking
// stream deltas batch into cumulative runtime output; every other event first
// flushes the pending batch so durable order follows wire order. Provider wire
// payloads never become observations or validated Attempt results directly.
func (s *ClaudeCodeProviderSession) HandleEvent(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	method := strings.ToLower(strings.TrimSpace(event.Method))
	params := map[string]any{}
	if len(event.Params) > 0 && json.Unmarshal(event.Params, &params) != nil {
		return
	}
	if method == "claude/runtime_output" && s.tryBatchClaudeReasoningOutput(event, params, emit) {
		return
	}
	completedReasoningRecord := method == "claude/runtime_output" && claudeRuntimeOutputHasReasoning(params)
	if method == "claude/turn/completed" {
		s.completeClaudeReasoning(emit, params)
	} else {
		s.reasoning.Barrier()
	}
	switch method {
	case "claude/tool/used", "claude/tool/result", "claude/turn/completed":
		s.handleClaudeAssistedObservation(method, params)
	case "claude/attempt_result", "claude/attempt_result_invalid":
		s.handleClaudeAttemptResult(method, params)
		return
	}
	s.providerSessionAdapter.HandleEvent(event, emit)
	if completedReasoningRecord {
		// The full assistant record has the same stable thinking-block id and
		// replaces the streamed delta row. Its projection closes the segment.
		s.reasoning.Reset()
	}
}

// tryBatchClaudeReasoningOutput accumulates one thinking stream delta into the
// session batcher. Flushed batches carry the cumulative text keyed by the
// bridge-synthesized thinking item id so transcript projections grow one
// stable reasoning entry in place. It reports whether the event was consumed.
func (s *ClaudeCodeProviderSession) tryBatchClaudeReasoningOutput(event SandboxBridgeEvent, params map[string]any, emit ProviderSessionEmit) bool {
	text := providerJSONValue(params, "text")
	if text == "" {
		return false
	}
	var record map[string]any
	if json.Unmarshal([]byte(text), &record) != nil {
		return false
	}
	if providerJSONValue(record, "type") != "content_block_delta" {
		return false
	}
	delta, ok := record["delta"].(map[string]any)
	if !ok {
		return false
	}
	// Thinking text must stay untrimmed: a trailing space often separates words.
	thought := claudeThinkingDeltaText(delta)
	itemID := providerJSONValue(record, "item_id", "itemId")
	if thought == "" || itemID == "" {
		return false
	}
	turnID := providerJSONValue(params, "turn_id", "turnId")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	sessionID := providerJSONValue(params, "session_id", "sessionId")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	stream := providerJSONValue(params, "stream")
	providerEvent := event.Method
	s.reasoning.Add(itemID, func(cumulative string) {
		s.emitBatchedClaudeThinking(emit, sessionID, turnID, itemID, stream, providerEvent, cumulative)
	}, thought)
	return true
}

// claudeThinkingDeltaText extracts a thinking stream delta without trimming
// so word boundaries survive delta batching.
func claudeThinkingDeltaText(delta map[string]any) string {
	for _, key := range []string{"thinking", "reasoning", "reasoning_content"} {
		if text, ok := delta[key].(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func claudeRuntimeOutputHasReasoning(params map[string]any) bool {
	text := providerJSONValue(params, "text")
	if text == "" {
		return false
	}
	var record map[string]any
	if json.Unmarshal([]byte(text), &record) != nil {
		return false
	}
	message, ok := record["message"].(map[string]any)
	if !ok {
		return false
	}
	content, ok := message["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		if typed, ok := block.(map[string]any); ok {
			kind := strings.ToLower(providerJSONValue(typed, "type"))
			if kind == "thinking" || kind == "reasoning" {
				return true
			}
		}
	}
	return false
}

func (s *ClaudeCodeProviderSession) completeClaudeReasoning(emit ProviderSessionEmit, params map[string]any) {
	itemID, text, ok := s.reasoning.Complete()
	if !ok {
		return
	}
	turnID := providerJSONValue(params, "turn_id", "turnId")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	sessionID := providerJSONValue(params, "session_id", "sessionId")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	s.emitClaudeReasoningRecord(emit, sessionID, turnID, itemID, "stream_event", "claude/runtime_output", text, runtimeoutput.ReasoningPhaseCompleted)
}

func (s *ClaudeCodeProviderSession) emitBatchedClaudeThinking(emit ProviderSessionEmit, sessionID, turnID, itemID, stream, providerEvent, cumulative string) {
	s.emitClaudeReasoningRecord(emit, sessionID, turnID, itemID, stream, providerEvent, cumulative, runtimeoutput.ReasoningPhaseStreaming)
}

func (s *ClaudeCodeProviderSession) emitClaudeReasoningRecord(emit ProviderSessionEmit, sessionID, turnID, itemID, stream, providerEvent, cumulative, phase string) {
	if cumulative == "" {
		return
	}
	encoded, err := json.Marshal(map[string]any{
		"type":    "content_block_delta",
		"index":   0,
		"item_id": itemID,
		"phase":   phase,
		"delta":   map[string]any{"type": "thinking_delta", "thinking": cumulative},
	})
	if err != nil {
		return
	}
	s.emitReasoningRuntimeOutput(emit, reasoningRuntimeOutput{
		ProviderEvent: providerEvent,
		SessionID:     sessionID,
		TurnID:        turnID,
		ItemID:        itemID,
		Stream:        stream,
		Phase:         phase,
		Text:          string(encoded),
	})
}

func (s *ClaudeCodeProviderSession) correlatedClaudeLineage(params map[string]any) (ProviderSessionTurnLineage, string, string, string, bool) {
	requestID := providerJSONValue(params, "request_id", "requestId")
	sessionID := providerJSONValue(params, "session_id", "sessionId")
	providerTurnID := providerJSONValue(params, "turn_id", "turnId")
	if requestID == "" || sessionID != s.SessionID() || providerTurnID == "" {
		return ProviderSessionTurnLineage{}, "", "", "", false
	}
	lineage, ok := s.ResolveProviderSessionTurnLineage(requestID, providerTurnID)
	if !ok || lineage.RequestID != requestID || (lineage.ProviderTurnID != "" && lineage.ProviderTurnID != providerTurnID) {
		return ProviderSessionTurnLineage{}, "", "", "", false
	}
	return lineage, requestID, sessionID, providerTurnID, true
}

func (s *ClaudeCodeProviderSession) handleClaudeAssistedObservation(method string, params map[string]any) {
	_, requestID, sessionID, providerTurnID, ok := s.correlatedClaudeLineage(params)
	if !ok {
		return
	}
	observation := ProviderSessionObservation{
		RequestID: requestID, SessionID: sessionID, ProviderTurnID: providerTurnID,
	}
	switch method {
	case "claude/tool/used":
		observation.Kind = ProviderSessionObservationToolUse
		observation.ToolCallID = providerJSONValue(params, "tool_call_id", "toolCallId")
		observation.ToolName = providerJSONValue(params, "tool_name", "toolName")
	case "claude/tool/result":
		observation.Kind = ProviderSessionObservationToolResult
		observation.ToolCallID = providerJSONValue(params, "tool_call_id", "toolCallId")
		observation.ToolName = providerJSONValue(params, "tool_name", "toolName")
		observation.Status = providerJSONValue(params, "status")
	case "claude/turn/completed":
		observation.Kind = ProviderSessionObservationTurnCompleted
		observation.Status = providerJSONValue(params, "status")
	}
	observation.BlackboardOperation, _ = ClassifyTrustedBlackboardTool(observation.ToolName)
	if observation.Validate() != nil {
		return
	}
	key := string(observation.Kind) + "\x00" + observation.RequestID + "\x00" + observation.ToolCallID
	s.assisted.mu.Lock()
	if _, delivered := s.assisted.observationsDelivered[key]; delivered {
		s.assisted.mu.Unlock()
		return
	}
	s.assisted.observationsDelivered[key] = struct{}{}
	s.assisted.observationOrder = append(s.assisted.observationOrder, key)
	if len(s.assisted.observationOrder) > maxClaudeAssistedDedupKeys {
		oldest := s.assisted.observationOrder[0]
		s.assisted.observationOrder = s.assisted.observationOrder[1:]
		delete(s.assisted.observationsDelivered, oldest)
	}
	s.assisted.mu.Unlock()
	s.emitObservation(observation)
}

func (s *ClaudeCodeProviderSession) handleClaudeAttemptResult(method string, params map[string]any) {
	lineage, requestID, sessionID, providerTurnID, ok := s.correlatedClaudeLineage(params)
	if !ok || lineage.Kind != RuntimeTurnKindControl {
		return
	}
	s.assisted.mu.Lock()
	if _, delivered := s.assisted.attemptResultsDelivered[requestID]; delivered {
		s.assisted.mu.Unlock()
		return
	}
	s.assisted.attemptResultsDelivered[requestID] = struct{}{}
	s.assisted.attemptResultOrder = append(s.assisted.attemptResultOrder, requestID)
	if len(s.assisted.attemptResultOrder) > maxClaudeAssistedDedupKeys {
		oldest := s.assisted.attemptResultOrder[0]
		s.assisted.attemptResultOrder = s.assisted.attemptResultOrder[1:]
		delete(s.assisted.attemptResultsDelivered, oldest)
	}
	resultSink, invalidSink := s.assisted.result, s.assisted.invalid
	s.assisted.mu.Unlock()
	if method == "claude/attempt_result_invalid" {
		if invalidSink != nil {
			invalidSink(attemptResultValidationFailure(requestID, sessionID, providerTurnID, nil, blackboardconclusion.ValidationReasonInvalidResult))
		}
		return
	}
	validated, err := blackboardconclusion.Decode([]byte(providerJSONValue(params, "result")))
	if err != nil {
		if invalidSink != nil {
			invalidSink(attemptResultValidationFailure(requestID, sessionID, providerTurnID, err, blackboardconclusion.ValidationReasonInvalidResult))
		}
		return
	}
	if resultSink != nil {
		resultSink(ProviderSessionAttemptResult{
			RequestID: requestID, SessionID: sessionID, ProviderTurnID: providerTurnID, Validated: validated,
		})
	}
}
