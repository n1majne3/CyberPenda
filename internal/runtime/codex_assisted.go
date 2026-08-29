package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/runtimeoutput"
	"pentest/internal/task"
)

const (
	maxCodexPendingAssistedEvents = 512
	maxCodexAttemptResultBytes    = 64 << 10
	maxCodexTerminalTurnIDs       = 4096
)

type codexAssistedEventKind string

const (
	codexAssistedToolUse    codexAssistedEventKind = "tool_use"
	codexAssistedToolResult codexAssistedEventKind = "tool_result"
	codexAssistedMessage    codexAssistedEventKind = "agent_message"
	codexAssistedTerminal   codexAssistedEventKind = "terminal"
)

// codexAssistedEvent is the complete in-memory assisted-observation boundary.
// It contains only bounded correlation metadata and, for a possible control
// result, at most one decoder-sized assistant message. Tool arguments/results
// and reasoning never enter this semantic observation type; the separate
// runtime-output projection retains transcript-visible reasoning.
type codexAssistedEvent struct {
	kind      codexAssistedEventKind
	sessionID string
	turnID    string
	callID    string
	toolName  string
	status    string
	message   []byte
	oversize  bool
}

type codexAssistedSession struct {
	mu          sync.Mutex
	observe     ProviderSessionObserve
	result      ProviderSessionAttemptResultSink
	invalid     ProviderSessionAttemptResultValidationFailureSink
	pending     map[string][]codexAssistedEvent
	candidates  map[string]codexAssistedEvent
	terminals   map[string]struct{}
	terminalIDs []string
	pendingSize int
}

func newCodexAssistedSession() *codexAssistedSession {
	return &codexAssistedSession{
		pending:    map[string][]codexAssistedEvent{},
		candidates: map[string]codexAssistedEvent{},
		terminals:  map[string]struct{}{},
	}
}

func (s *CodexProviderSession) SetObservationSink(sink ProviderSessionObserve) {
	s.assisted.mu.Lock()
	s.assisted.observe = sink
	s.assisted.mu.Unlock()
}

func (s *CodexProviderSession) SetAttemptResultSink(sink ProviderSessionAttemptResultSink) {
	s.assisted.mu.Lock()
	s.assisted.result = sink
	s.assisted.mu.Unlock()
}

func (s *CodexProviderSession) SetAttemptResultValidationFailureSink(sink ProviderSessionAttemptResultValidationFailureSink) {
	s.assisted.mu.Lock()
	s.assisted.invalid = sink
	s.assisted.mu.Unlock()
}

// SendTurn flushes notifications that raced ahead of the turn/start response.
// The generic adapter records immutable Harness lineage before this method
// makes any pending App Server notification observable.
func (s *CodexProviderSession) SendTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	result, err := s.providerSessionAdapter.SendTurn(ctx, request, emit)
	if err == nil {
		s.flushCodexAssisted(result.ProviderTurnID)
	}
	return result, err
}

func (s *CodexProviderSession) InterruptThenReplace(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	result, err := s.providerSessionAdapter.InterruptThenReplace(ctx, request, emit)
	if err == nil {
		s.flushCodexAssisted(result.ProviderTurnID)
	}
	return result, err
}

// HandleEvent keeps the existing interactive lifecycle channel while adding a
// separate closed semantic-observation channel. Bounded item projections enter
// runtime_output Task Events, while only turn/permission notifications enter
// the lifecycle normalizer. Reasoning deltas stream as batched runtime output;
// any other event first flushes the pending reasoning batch so durable order
// follows wire order.
func (s *CodexProviderSession) HandleEvent(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	method := strings.ToLower(strings.TrimSpace(event.Method))
	if method == "item/reasoning/summarytextdelta" {
		s.acceptCodexReasoningDelta(event, emit)
		return
	}
	if method == "turn/completed" {
		s.completeCodexReasoning(emit, event.Params)
	} else {
		s.reasoning.Barrier()
	}
	if strings.HasPrefix(method, "item/") {
		s.emitCodexRuntimeOutput(event, emit)
	} else {
		actualEmit := emit
		if actualEmit == nil {
			s.providerSessionAdapter.mu.Lock()
			actualEmit = s.providerSessionAdapter.eventSink
			s.providerSessionAdapter.mu.Unlock()
		}
		s.providerSessionAdapter.HandleEvent(event, func(kind task.EventKind, payload task.EventPayload) {
			if method == "turn/completed" {
				if status := codexTurnStatus(event.Params); status != "" {
					payload["outcome"] = status
				}
			}
			if turnID := codexEventTurnID(event.Params); turnID != "" {
				if lineage, ok := s.ResolveProviderSessionTurnLineage("", turnID); ok && lineage.ProviderTurnID == turnID {
					payload["request_id"] = lineage.RequestID
				}
			}
			if actualEmit != nil {
				actualEmit(kind, payload)
			}
		})
	}
	if assistedEvent, ok := parseCodexAssistedEvent(method, event.Params); ok {
		s.acceptCodexAssisted(assistedEvent)
	}
}

func (s *CodexProviderSession) emitCodexRuntimeOutput(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	method := strings.ToLower(strings.TrimSpace(event.Method))
	if (method != "item/started" && method != "item/completed") || len(event.Params) == 0 {
		return
	}
	params := map[string]any{}
	if json.Unmarshal(event.Params, &params) != nil {
		return
	}
	item, ok := params["item"].(map[string]any)
	if !ok {
		return
	}
	itemType := strings.ToLower(providerJSONValue(item, "type"))
	text := string(event.Params)
	switch itemType {
	case "agentmessage":
		if method != "item/completed" || providerJSONValue(item, "text") == "" {
			return
		}
	case "commandexecution", "mcptoolcall":
	case "subagentactivity", "collabagenttoolcall":
		// Subagent Activity: project child-thread lifecycle onto the timeline.
		// The Harness only observes these; it never spawns or schedules.
	case "filechange", "websearch":
		if method != "item/completed" {
			return
		}
	case "reasoning":
		if method != "item/completed" {
			return
		}
		text = codexReasoningRuntimeOutput(params, item)
		if text == "" {
			itemID, cumulative, ok := s.reasoning.Complete()
			if !ok {
				return
			}
			text = codexCompletedReasoningRuntimeOutput(params, itemID, cumulative)
			if text == "" {
				return
			}
		} else {
			// The completed item provides the final cumulative text and closes the
			// streaming segment. Clear the batcher base after the prior Barrier.
			s.reasoning.Reset()
		}
	default:
		return
	}
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	if emit == nil {
		return
	}
	sessionID := providerJSONValue(params, "threadId", "thread_id")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	turnID := providerJSONValue(params, "turnId", "turn_id")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	emit(task.EventKindRuntimeOutput, task.EventPayload{
		"provider": s.provider, "provider_event": event.Method,
		"session_id": sessionID, "provider_turn_id": turnID,
		"stream": "codex_app_server", "text": text,
	})
}

func codexCompletedReasoningRuntimeOutput(params map[string]any, itemID, content string) string {
	if itemID == "" || content == "" {
		return ""
	}
	return codexReasoningRuntimeOutput(params, map[string]any{
		"type":    "reasoning",
		"id":      itemID,
		"content": []any{content},
	})
}

func codexReasoningRuntimeOutput(params, item map[string]any) string {
	content, hasContent := item["content"]
	summary, hasSummary := item["summary"]
	// Raw reasoning content is durable transcript content. Third-party model
	// summaries are not the real thinking, so content is kept and preferred;
	// the summary stays as the fallback. Shape-based redaction still applies
	// when the event is stored.
	hasText := (hasContent && strings.TrimSpace(providerReasoningText(content)) != "") ||
		(hasSummary && strings.TrimSpace(providerReasoningText(summary)) != "")
	if !hasText {
		return ""
	}
	minimalItem := map[string]any{
		"type": "reasoning",
		"id":   providerJSONValue(item, "id"),
	}
	if hasContent {
		minimalItem["content"] = content
	}
	if hasSummary {
		minimalItem["summary"] = summary
	}
	minimal := map[string]any{"item": minimalItem}
	if threadID := providerJSONValue(params, "threadId", "thread_id"); threadID != "" {
		minimal["threadId"] = threadID
	}
	if turnID := providerJSONValue(params, "turnId", "turn_id"); turnID != "" {
		minimal["turnId"] = turnID
	}
	encoded, err := json.Marshal(minimal)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// providerReasoningText joins string parts and text-bearing object parts of a
// reasoning content or summary list.
func providerReasoningText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, part := range typed {
			if text, ok := part.(string); ok {
				if strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
				continue
			}
			if partMap, ok := part.(map[string]any); ok {
				if text := providerJSONValue(partMap, "text", "content"); strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// acceptCodexReasoningDelta accumulates one reasoning summary text delta into
// the session batcher. Flushed batches carry the cumulative text so transcript
// projections grow one stable reasoning entry in place.
func (s *CodexProviderSession) acceptCodexReasoningDelta(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	params := map[string]any{}
	if len(event.Params) == 0 || json.Unmarshal(event.Params, &params) != nil {
		return
	}
	itemID := providerJSONValue(params, "itemId", "item_id")
	// Delta text must stay untrimmed: a trailing space often separates words.
	delta, _ := params["delta"].(string)
	if itemID == "" || delta == "" {
		return
	}
	turnID := providerJSONValue(params, "turnId", "turn_id")
	if turnID == "" {
		turnID = s.currentTurn()
	}
	sessionID := providerJSONValue(params, "threadId", "thread_id")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	s.reasoning.Add(itemID, func(cumulative string) {
		s.emitBatchedCodexReasoning(emit, sessionID, turnID, itemID, cumulative)
	}, delta)
}

func (s *CodexProviderSession) completeCodexReasoning(emit ProviderSessionEmit, raw json.RawMessage) {
	itemID, content, ok := s.reasoning.Complete()
	if !ok {
		return
	}
	params := map[string]any{}
	_ = json.Unmarshal(raw, &params)
	sessionID := providerJSONValue(params, "threadId", "thread_id")
	if sessionID == "" {
		sessionID = s.SessionID()
	}
	turnID := providerJSONValue(params, "turnId", "turn_id")
	if turn, ok := params["turn"].(map[string]any); ok && turnID == "" {
		turnID = providerJSONValue(turn, "id", "turnId", "turn_id")
	}
	if turnID == "" {
		turnID = s.currentTurn()
	}
	text := codexCompletedReasoningRuntimeOutput(params, itemID, content)
	if text == "" {
		return
	}
	s.emitCodexReasoningRecord(emit, sessionID, turnID, itemID, "item/completed", text, runtimeoutput.ReasoningPhaseCompleted)
}

func (s *CodexProviderSession) emitBatchedCodexReasoning(emit ProviderSessionEmit, sessionID, turnID, itemID, cumulative string) {
	encoded, err := json.Marshal(map[string]any{
		"item": map[string]any{"type": "reasoning", "id": itemID, "summary": []string{cumulative}},
	})
	if err != nil {
		return
	}
	s.emitCodexReasoningRecord(emit, sessionID, turnID, itemID, "item/reasoning/summaryTextDelta", string(encoded), runtimeoutput.ReasoningPhaseStreaming)
}

func (s *CodexProviderSession) emitCodexReasoningRecord(emit ProviderSessionEmit, sessionID, turnID, itemID, providerEvent, text, phase string) {
	s.emitReasoningRuntimeOutput(emit, reasoningRuntimeOutput{
		ProviderEvent: providerEvent,
		SessionID:     sessionID,
		TurnID:        turnID,
		ItemID:        itemID,
		Stream:        "codex_app_server",
		Phase:         phase,
		Text:          text,
	})
}

func (s *CodexProviderSession) acceptCodexAssisted(event codexAssistedEvent) {
	if event.sessionID != s.SessionID() {
		return
	}
	lineage, ok := s.ResolveProviderSessionTurnLineage("", event.turnID)
	if !ok || lineage.ProviderTurnID != event.turnID {
		s.assisted.mu.Lock()
		if s.assisted.pendingSize < maxCodexPendingAssistedEvents {
			s.assisted.pending[event.turnID] = append(s.assisted.pending[event.turnID], event)
			s.assisted.pendingSize++
		}
		s.assisted.mu.Unlock()
		return
	}
	s.deliverCodexAssisted(lineage, event)
}

func (s *CodexProviderSession) flushCodexAssisted(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	lineage, ok := s.ResolveProviderSessionTurnLineage("", turnID)
	if !ok || lineage.ProviderTurnID != turnID {
		return
	}
	s.assisted.mu.Lock()
	pending := append([]codexAssistedEvent(nil), s.assisted.pending[turnID]...)
	delete(s.assisted.pending, turnID)
	s.assisted.pendingSize -= len(pending)
	s.assisted.mu.Unlock()
	for _, event := range pending {
		s.deliverCodexAssisted(lineage, event)
	}
}

func (s *CodexProviderSession) deliverCodexAssisted(lineage ProviderSessionTurnLineage, event codexAssistedEvent) {
	s.assisted.mu.Lock()
	if _, delivered := s.assisted.terminals[event.turnID]; delivered {
		s.assisted.mu.Unlock()
		return
	}
	if event.kind == codexAssistedMessage {
		if lineage.Kind == RuntimeTurnKindControl {
			s.assisted.candidates[event.turnID] = event
		}
		s.assisted.mu.Unlock()
		return
	}
	observe, resultSink, invalidSink := s.assisted.observe, s.assisted.result, s.assisted.invalid
	var candidate codexAssistedEvent
	if event.kind == codexAssistedTerminal {
		s.assisted.terminals[event.turnID] = struct{}{}
		s.assisted.terminalIDs = append(s.assisted.terminalIDs, event.turnID)
		if len(s.assisted.terminalIDs) > maxCodexTerminalTurnIDs {
			oldest := s.assisted.terminalIDs[0]
			s.assisted.terminalIDs = s.assisted.terminalIDs[1:]
			delete(s.assisted.terminals, oldest)
		}
		candidate = s.assisted.candidates[event.turnID]
		delete(s.assisted.candidates, event.turnID)
	}
	s.assisted.mu.Unlock()

	if event.kind == codexAssistedTerminal && lineage.Kind == RuntimeTurnKindControl {
		validated, err := blackboardconclusion.Decode(candidate.message)
		if err == nil && !candidate.oversize {
			if resultSink != nil {
				resultSink(ProviderSessionAttemptResult{
					RequestID: lineage.RequestID, SessionID: event.sessionID,
					ProviderTurnID: event.turnID, Validated: validated,
				})
			}
		} else if invalidSink != nil {
			reason := blackboardconclusion.ValidationReasonInvalidResult
			if candidate.oversize {
				reason = blackboardconclusion.ValidationReasonResultTooLarge
			}
			invalidSink(attemptResultValidationFailure(lineage.RequestID, event.sessionID, event.turnID, err, reason))
		}
	}
	observation, ok := codexObservation(lineage, event)
	if ok && observation.Validate() == nil && observe != nil {
		observe(observation)
	}
}

func codexObservation(lineage ProviderSessionTurnLineage, event codexAssistedEvent) (ProviderSessionObservation, bool) {
	observation := ProviderSessionObservation{
		RequestID: lineage.RequestID, SessionID: event.sessionID, ProviderTurnID: event.turnID,
		ToolCallID: event.callID, ToolName: event.toolName, Status: event.status,
	}
	observation.BlackboardOperation, _ = ClassifyTrustedBlackboardTool(observation.ToolName)
	switch event.kind {
	case codexAssistedToolUse:
		observation.Kind = ProviderSessionObservationToolUse
	case codexAssistedToolResult:
		observation.Kind = ProviderSessionObservationToolResult
	case codexAssistedTerminal:
		observation.Kind = ProviderSessionObservationTurnCompleted
	default:
		return ProviderSessionObservation{}, false
	}
	return observation, true
}

func parseCodexAssistedEvent(method string, raw json.RawMessage) (codexAssistedEvent, bool) {
	params := map[string]any{}
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil {
		return codexAssistedEvent{}, false
	}
	event := codexAssistedEvent{
		sessionID: providerJSONValue(params, "threadId", "thread_id"),
		turnID:    providerJSONValue(params, "turnId", "turn_id"),
	}
	if turn, ok := params["turn"].(map[string]any); ok {
		if event.turnID == "" {
			event.turnID = providerJSONValue(turn, "id", "turnId", "turn_id")
		}
	}
	if event.sessionID == "" || event.turnID == "" {
		return codexAssistedEvent{}, false
	}
	switch method {
	case "turn/completed":
		event.kind = codexAssistedTerminal
		event.status = codexTurnStatus(raw)
		return event, event.status != ""
	case "item/started", "item/completed":
	default:
		return codexAssistedEvent{}, false
	}
	item, ok := params["item"].(map[string]any)
	if !ok {
		return codexAssistedEvent{}, false
	}
	itemType := strings.ToLower(providerJSONValue(item, "type"))
	if itemType == "agentmessage" {
		if method != "item/completed" {
			return codexAssistedEvent{}, false
		}
		if phase := strings.ToLower(providerJSONValue(item, "phase")); phase != "" && phase != "final_answer" {
			return codexAssistedEvent{}, false
		}
		text := providerJSONValue(item, "text")
		if text == "" {
			return codexAssistedEvent{}, false
		}
		event.kind = codexAssistedMessage
		if len(text) > maxCodexAttemptResultBytes {
			event.oversize = true
			text = text[:maxCodexAttemptResultBytes]
		}
		event.message = []byte(text)
		return event, true
	}
	event.callID = providerJSONValue(item, "id", "callId", "call_id")
	event.toolName = codexToolName(itemType, item)
	if event.callID == "" || event.toolName == "" {
		return codexAssistedEvent{}, false
	}
	if method == "item/started" {
		event.kind = codexAssistedToolUse
		return event, true
	}
	event.kind = codexAssistedToolResult
	event.status = codexToolStatus(providerJSONValue(item, "status"))
	return event, event.status != ""
}

func codexToolName(itemType string, item map[string]any) string {
	if name := providerJSONValue(item, "tool", "toolName", "tool_name", "name"); name != "" {
		if itemType == "mcptoolcall" && !strings.HasPrefix(name, "mcp__") {
			if server := providerJSONValue(item, "server", "serverName", "server_name"); server != "" {
				return "mcp__" + server + "__" + name
			}
		}
		return name
	}
	switch itemType {
	case "commandexecution":
		return "command_execution"
	case "filechange":
		return "file_change"
	case "websearch":
		return "web_search"
	case "imagegeneration":
		return "image_generation"
	case "imageview":
		return "image_view"
	case "sleep":
		return "sleep"
	default:
		return ""
	}
}

func codexToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "success":
		return "succeeded"
	case "failed", "declined", "rejected", "cancelled", "canceled", "interrupted":
		return "failed"
	default:
		return ""
	}
}

func codexTurnStatus(raw json.RawMessage) string {
	params := map[string]any{}
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil {
		return ""
	}
	status := providerJSONValue(params, "status", "turnStatus", "turn_status")
	if turn, ok := params["turn"].(map[string]any); ok {
		if nested := providerJSONValue(turn, "status", "turnStatus", "turn_status"); nested != "" {
			status = nested
		}
	}
	switch strings.ToLower(status) {
	case "completed":
		return "completed"
	case "failed", "rejected":
		return "failed"
	case "interrupted", "cancelled", "canceled", "aborted", "stopped":
		return "interrupted"
	default:
		return ""
	}
}

func codexEventTurnID(raw json.RawMessage) string {
	params := map[string]any{}
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil {
		return ""
	}
	if turn, ok := params["turn"].(map[string]any); ok {
		if turnID := providerJSONValue(turn, "id", "turnId", "turn_id"); turnID != "" {
			return turnID
		}
	}
	return providerJSONValue(params, "turnId", "turn_id")
}

var (
	_ ProviderSessionObservationSink             = (*CodexProviderSession)(nil)
	_ ProviderSessionCompleteTurnLineageResolver = (*CodexProviderSession)(nil)
	_ ProviderSessionAttemptResultSource         = (*CodexProviderSession)(nil)
)
