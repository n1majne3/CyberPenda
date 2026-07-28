package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/task"
)

const (
	maxPiAttemptCandidateBytes = 64 << 10
	maxPiDeliveredTurnIDs      = 4096
)

type piAttemptCandidate struct {
	raw     []byte
	invalid bool
}

type piAssistedState struct {
	mu                    sync.Mutex
	resultSink            ProviderSessionAttemptResultSink
	validationFailureSink ProviderSessionAttemptResultValidationFailureSink
	candidates            map[string]piAttemptCandidate
	delivered             map[string]bool
	deliveredOrder        []string
}

var piAssistedStates sync.Map

func (s *PiProviderSession) assistedState() *piAssistedState {
	if found, ok := piAssistedStates.Load(s); ok {
		return found.(*piAssistedState)
	}
	created := &piAssistedState{
		candidates: map[string]piAttemptCandidate{},
		delivered:  map[string]bool{},
	}
	found, _ := piAssistedStates.LoadOrStore(s, created)
	return found.(*piAssistedState)
}

func (s *PiProviderSession) SetAttemptResultSink(sink ProviderSessionAttemptResultSink) {
	state := s.assistedState()
	state.mu.Lock()
	state.resultSink = sink
	state.mu.Unlock()
}

func (s *PiProviderSession) SetAttemptResultValidationFailureSink(sink ProviderSessionAttemptResultValidationFailureSink) {
	state := s.assistedState()
	state.mu.Lock()
	state.validationFailureSink = sink
	state.mu.Unlock()
}

// HandleEvent extends Pi's ordinary lifecycle projection with the bounded
// observations and closed Attempt result required by assisted conclusions.
// PiWire supplies session/Turn correlation; raw tool payloads and reasoning
// are neither represented in observations nor forwarded to Task events.
func (s *PiProviderSession) HandleEvent(event SandboxBridgeEvent, emit ProviderSessionEmit) {
	method := strings.ToLower(strings.TrimSpace(event.Method))
	params := map[string]any{}
	if method == "" || len(event.Params) > 0 && json.Unmarshal(event.Params, &params) != nil {
		return
	}

	switch {
	case piStartedBoundary(method):
		s.emitPiLifecycle(event.Method, params, "started", emit)
		return
	case piMessageBoundary(method):
		s.capturePiAttemptCandidate(params)
		return
	case piToolUseBoundary(method):
		s.emitPiToolObservation(params, ProviderSessionObservationToolUse)
		return
	case piToolResultBoundary(method):
		s.emitPiToolObservation(params, ProviderSessionObservationToolResult)
		return
	case piTerminalBoundary(method, params):
		// Pi status is carried in params, so preserve completed/failed/
		// interrupted rather than classifying every agent_end as completed.
		s.projectPiTerminalLifecycle(event, params, piTerminalStatus(method, params), emit)
		s.finishPiAssistedTurn(method, params)
		return
	default:
		s.providerSessionAdapter.HandleEvent(event, emit)
	}
}

// projectPiTerminalLifecycle keeps the public Pi status vocabulary while
// passing the terminal through the common adapter. The latter owns settlement
// bookkeeping required by interrupt-then-replace, including sessions resumed
// with an already-active Turn that has no newly-recorded assisted lineage.
func (s *PiProviderSession) projectPiTerminalLifecycle(event SandboxBridgeEvent, params map[string]any, outcome string, emit ProviderSessionEmit) {
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	projected := false
	s.providerSessionAdapter.HandleEvent(event, func(kind task.EventKind, payload task.EventPayload) {
		projected = true
		method := strings.ToLower(event.Method)
		// Keep the existing interrupt lifecycle contract ("settled") for
		// explicit abort/cancel/interrupt notifications. The assisted
		// observation emitted below still uses the normalized "interrupted"
		// terminal status.
		if !strings.Contains(method, "abort") && !strings.Contains(method, "cancel") && !strings.Contains(method, "interrupt") {
			payload["outcome"] = outcome
		}
		if lineage, _, _, ok := s.piEventLineage(params); ok {
			payload["request_id"] = lineage.RequestID
		}
		if emit != nil {
			emit(kind, payload)
		}
	})
	// Some Pi terminal aliases (for example agent_settled) are intentionally
	// provider-specific and are not classified by the generic lifecycle mapper.
	if !projected {
		s.emitPiLifecycle(event.Method, params, outcome, emit)
	}
}

func (s *PiProviderSession) emitPiLifecycle(providerEvent string, params map[string]any, outcome string, emit ProviderSessionEmit) {
	lineage, sessionID, turnID, ok := s.piEventLineage(params)
	if !ok {
		return
	}
	if emit == nil {
		s.mu.Lock()
		emit = s.eventSink
		s.mu.Unlock()
	}
	if emit != nil {
		emit(task.EventKindLifecycle, task.EventPayload{
			"provider": "pi", "provider_event": providerEvent,
			"request_id": lineage.RequestID, "session_id": sessionID,
			"provider_turn_id": turnID, "mode": string(ProviderSessionModeSendTurn),
			"outcome": outcome,
		})
	}
}

func (s *PiProviderSession) capturePiAttemptCandidate(params map[string]any) {
	lineage, _, _, ok := s.piEventLineage(params)
	if !ok || lineage.Kind != RuntimeTurnKindControl {
		return
	}
	text, found := piAssistantText(params)
	candidate := piAttemptCandidate{invalid: !found || len(text) > maxPiAttemptCandidateBytes}
	if found && !candidate.invalid {
		candidate.raw = []byte(text)
	}
	state := s.assistedState()
	state.mu.Lock()
	if !state.delivered[lineage.RequestID] {
		state.candidates[lineage.RequestID] = candidate
	}
	state.mu.Unlock()
}

func (s *PiProviderSession) emitPiToolObservation(params map[string]any, kind ProviderSessionObservationKind) {
	lineage, sessionID, turnID, ok := s.piEventLineage(params)
	if !ok {
		return
	}
	toolCallID, toolName := piToolIdentity(params)
	observation := ProviderSessionObservation{
		Kind: kind, RequestID: lineage.RequestID, SessionID: sessionID,
		ProviderTurnID: turnID, ToolCallID: toolCallID, ToolName: toolName,
	}
	if kind == ProviderSessionObservationToolResult {
		observation.Status = piToolResultStatus(params)
	}
	s.emitPiObservation(observation)
}

func (s *PiProviderSession) finishPiAssistedTurn(method string, params map[string]any) {
	lineage, sessionID, turnID, ok := s.piEventLineage(params)
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
	if len(state.deliveredOrder) > maxPiDeliveredTurnIDs {
		oldest := state.deliveredOrder[0]
		state.deliveredOrder = state.deliveredOrder[1:]
		delete(state.delivered, oldest)
	}
	candidate, found := state.candidates[lineage.RequestID]
	delete(state.candidates, lineage.RequestID)
	resultSink := state.resultSink
	failureSink := state.validationFailureSink
	state.mu.Unlock()

	status := piTerminalStatus(method, params)
	s.emitPiObservation(ProviderSessionObservation{
		Kind: ProviderSessionObservationTurnCompleted, RequestID: lineage.RequestID,
		SessionID: sessionID, ProviderTurnID: turnID, Status: status,
	})
	if lineage.Kind != RuntimeTurnKindControl {
		return
	}

	validated, err := blackboardconclusion.Decode(candidate.raw)
	if !found || candidate.invalid || err != nil {
		if failureSink != nil {
			failureSink(ProviderSessionAttemptResultValidationFailure{
				RequestID: lineage.RequestID, SessionID: sessionID, ProviderTurnID: turnID,
				ValidationErrorCode: ProviderSessionAttemptResultInvalid,
			})
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

func (s *PiProviderSession) piEventLineage(params map[string]any) (ProviderSessionTurnLineage, string, string, bool) {
	sessionID := providerJSONValue(params, "session_id", "sessionId")
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
		// PiWire defines the provider Turn id from the daemon-owned prompt
		// request id, allowing early native events to resolve pre-response.
		lineage, ok = s.ResolveProviderSessionTurnLineage(turnID, turnID)
	}
	if !ok || strings.TrimSpace(sessionID) == "" || sessionID != s.SessionID() || strings.TrimSpace(turnID) == "" {
		return ProviderSessionTurnLineage{}, "", "", false
	}
	return lineage, sessionID, turnID, true
}

func (s *PiProviderSession) emitPiObservation(observation ProviderSessionObservation) {
	if observation.Validate() != nil {
		return
	}
	s.mu.Lock()
	sink := s.observationSink
	s.mu.Unlock()
	if sink != nil {
		sink(observation)
	}
}

func (s *PiProviderSession) Close(ctx context.Context) error {
	err := s.providerSessionAdapter.Close(ctx)
	if err == nil || err == ErrProviderSessionClosed {
		piAssistedStates.Delete(s)
	}
	return err
}

func piMessageBoundary(method string) bool {
	return method == "pi/message_end" || method == "pi/message_completed"
}

func piStartedBoundary(method string) bool {
	return method == "pi/agent_start" || method == "pi/turn_start"
}

func piToolUseBoundary(method string) bool {
	return method == "pi/tool_execution_start" || method == "pi/tool_start"
}

func piToolResultBoundary(method string) bool {
	return method == "pi/tool_execution_end" || method == "pi/tool_end"
}

func piTerminalBoundary(method string, params map[string]any) bool {
	if method == "pi/agent_end" || method == "pi/agent_settled" || method == "pi/turn_end" ||
		strings.Contains(method, "abort") || strings.Contains(method, "cancel") || strings.Contains(method, "interrupt") {
		return true
	}
	status := strings.ToLower(providerJSONValue(params, "status", "turn_status", "turnStatus"))
	return status == "completed" || status == "failed" || status == "interrupted"
}

func piTerminalStatus(method string, params map[string]any) string {
	status := strings.ToLower(providerJSONValue(params, "status", "turn_status", "turnStatus"))
	switch status {
	case "failed", "error", "rejected":
		return "failed"
	case "aborted", "cancelled", "canceled", "interrupted", "stopped":
		return "interrupted"
	case "completed", "complete", "succeeded", "success":
		return "completed"
	}
	if strings.Contains(method, "abort") || strings.Contains(method, "cancel") || strings.Contains(method, "interrupt") {
		return "interrupted"
	}
	if strings.Contains(method, "fail") || strings.Contains(method, "error") || strings.Contains(method, "reject") {
		return "failed"
	}
	return "completed"
}

func piToolIdentity(params map[string]any) (string, string) {
	callID := providerJSONValue(params, "toolCallId", "tool_call_id", "callId", "call_id", "id")
	name := providerJSONValue(params, "toolName", "tool_name", "name")
	if tool, ok := params["tool"].(map[string]any); ok {
		if callID == "" {
			callID = providerJSONValue(tool, "toolCallId", "tool_call_id", "callId", "call_id", "id")
		}
		if name == "" {
			name = providerJSONValue(tool, "toolName", "tool_name", "name")
		}
	}
	return callID, name
}

func piToolResultStatus(params map[string]any) string {
	if value, ok := params["isError"].(bool); ok && value {
		return "failed"
	}
	if value, ok := params["is_error"].(bool); ok && value {
		return "failed"
	}
	status := strings.ToLower(providerJSONValue(params, "status", "outcome"))
	if status == "failed" || status == "error" || status == "rejected" {
		return "failed"
	}
	return "succeeded"
}

func piAssistantText(params map[string]any) (string, bool) {
	message, ok := params["message"].(map[string]any)
	if !ok || strings.ToLower(providerJSONValue(message, "role")) != "assistant" {
		return "", false
	}
	if text, ok := message["content"].(string); ok {
		return text, strings.TrimSpace(text) != ""
	}
	blocks, ok := message["content"].([]any)
	if !ok {
		return "", false
	}
	texts := make([]string, 0, len(blocks))
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok || strings.ToLower(providerJSONValue(block, "type")) != "text" {
			continue
		}
		if text := providerJSONValue(block, "text"); text != "" {
			texts = append(texts, text)
		}
	}
	text := strings.Join(texts, "\n")
	return text, strings.TrimSpace(text) != ""
}

var (
	_ ProviderSessionObservationSink     = (*PiProviderSession)(nil)
	_ ProviderSessionAttemptResultSource = (*PiProviderSession)(nil)
	_ ProviderSessionEventHandler        = (*PiProviderSession)(nil)
	_ ProviderSession                    = (*PiProviderSession)(nil)
)
