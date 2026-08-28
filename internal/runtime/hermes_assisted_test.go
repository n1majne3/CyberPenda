package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

const validHermesAttemptResult = `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:hermes-search","create":true,"summary":"Tested search without proving exploitability.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:hermes-search","create_objective":{"objective":"Test Hermes search."}}],"produced_targets":[]}`

func newAssistedHermesSession(t *testing.T, kind RuntimeTurnKind) (*HermesProviderSession, string) {
	t.Helper()
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"session/prompt": {Result: json.RawMessage(`{"sessionId":"hermes-session","turn_id":"hermes-turn"}`)},
	}}
	session := NewHermesProviderSession(HermesProviderSessionConfig{
		Transport: transport, SessionID: "hermes-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	requestID := "conclude-hermes"
	if kind == RuntimeTurnKindWork {
		requestID = "work-hermes"
	}
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: requestID, Message: "return one closed result", TurnKind: kind,
		ModelProviderID: "provider-a", Model: "model-a", RequestedReasoningEffort: "high",
	}, nil); err != nil {
		t.Fatalf("send Hermes turn: %v", err)
	}
	return session, requestID
}

func TestHermesControlTurnExtractsAttemptResultAtCorrelatedTerminalBoundary(t *testing.T) {
	session, requestID := newAssistedHermesSession(t, RuntimeTurnKindControl)
	var got ProviderSessionAttemptResult
	session.SetAttemptResultSink(func(result ProviderSessionAttemptResult) { got = result })

	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + mustRawJSONString(t, validHermesAttemptResult) + `}}}`),
	}, nil)
	if got.RequestID != "" {
		t.Fatalf("Attempt result emitted before terminal boundary: %#v", got)
	}

	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"turn_ended","stopReason":"end_turn"}}`),
	}, nil)
	if got.RequestID != requestID || got.SessionID != "hermes-session" || got.ProviderTurnID != "hermes-turn" {
		t.Fatalf("Attempt result correlation = %#v", got)
	}
	if string(got.Validated.CanonicalJSON) != validHermesAttemptResult {
		t.Fatalf("canonical result = %s", got.Validated.CanonicalJSON)
	}
}

func TestHermesWorkTurnNeverProducesAttemptResult(t *testing.T) {
	session, _ := newAssistedHermesSession(t, RuntimeTurnKindWork)
	callbacks := 0
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { callbacks++ })
	session.SetAttemptResultValidationFailureSink(func(ProviderSessionAttemptResultValidationFailure) { callbacks++ })
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + mustRawJSONString(t, validHermesAttemptResult) + `}}}`),
	}, nil)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"turn_ended","stopReason":"end_turn"}}`),
	}, nil)
	if callbacks != 0 {
		t.Fatalf("work Turn produced %d Attempt result callbacks", callbacks)
	}
}

func TestHermesRejectsEventsFromAnotherSession(t *testing.T) {
	session, _ := newAssistedHermesSession(t, RuntimeTurnKindControl)
	var observations, results, failures int
	session.SetObservationSink(func(ProviderSessionObservation) { observations++ })
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { results++ })
	session.SetAttemptResultValidationFailureSink(func(ProviderSessionAttemptResultValidationFailure) { failures++ })
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-spoofed","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + mustRawJSONString(t, validHermesAttemptResult) + `}}}`),
	}, nil)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-spoofed","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"bash"}}`),
	}, nil)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-spoofed","update":{"sessionUpdate":"turn_ended","stopReason":"end_turn"}}`),
	}, nil)
	if observations != 0 || results != 0 || failures != 0 {
		t.Fatalf("wrong-session callbacks: observations=%d results=%d failures=%d", observations, results, failures)
	}
}

func TestHermesEventsProjectBoundedCorrelatedObservationsWithoutRawPayloads(t *testing.T) {
	session, requestID := newAssistedHermesSession(t, RuntimeTurnKindWork)
	var got []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { got = append(got, observation) })
	var events []task.EventPayload
	emit := func(kind task.EventKind, payload task.EventPayload) {
		if kind == task.EventKindRuntimeOutput {
			return
		}
		events = append(events, payload)
	}

	for _, event := range []SandboxBridgeEvent{
		{Method: "session/update", Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"bash","rawInput":{"token":"secret"}}}`)},
		{Method: "session/update", Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-1","title":"bash","status":"completed","content":"secret output"}}`)},
		{Method: "session/update", Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"turn_ended","stopReason":"end_turn","output":"secret"}}`)},
	} {
		session.HandleEvent(event, emit)
	}

	want := []ProviderSessionObservation{
		{Kind: ProviderSessionObservationToolUse, RequestID: requestID, SessionID: "hermes-session", ProviderTurnID: "hermes-turn", ToolCallID: "call-1", ToolName: "bash"},
		{Kind: ProviderSessionObservationToolResult, RequestID: requestID, SessionID: "hermes-session", ProviderTurnID: "hermes-turn", ToolCallID: "call-1", ToolName: "bash", Status: "succeeded"},
		{Kind: ProviderSessionObservationTurnCompleted, RequestID: requestID, SessionID: "hermes-session", ProviderTurnID: "hermes-turn", Status: "completed"},
	}
	if len(got) != len(want) {
		t.Fatalf("observations = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	encoded, err := json.Marshal(struct {
		Observations []ProviderSessionObservation
		Events       []task.EventPayload
	}{got, events})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret output", "token"} {
		if containsJSONText(encoded, secret) {
			t.Fatalf("normalized Hermes metadata leaked %q: %s", secret, encoded)
		}
	}
}

func TestHermesToolObservationsCarryCanonicalBlackboardOperationIdentity(t *testing.T) {
	session, _ := newAssistedHermesSession(t, RuntimeTurnKindWork)
	var got []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { got = append(got, observation) })
	for _, event := range []SandboxBridgeEvent{
		{Method: "session/update", Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"mcp__pentest__blackboard_change"}}`)},
		{Method: "session/update", Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-1","title":"mcp__pentest__blackboard_change","status":"completed"}}`)},
		{Method: "session/update", Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-2","title":"mcp__evil__blackboard_change","status":"completed"}}`)},
		{Method: "session/update", Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-3","title":"blackboard_change","status":"completed"}}`)},
	} {
		session.HandleEvent(event, nil)
	}
	want := []struct {
		operation BlackboardOperation
		toolName  string
	}{
		{operation: BlackboardOperationChange, toolName: "mcp__pentest__blackboard_change"},
		{operation: BlackboardOperationChange, toolName: "mcp__pentest__blackboard_change"},
		{operation: "", toolName: "mcp__evil__blackboard_change"},
		{operation: "", toolName: "blackboard_change"},
	}
	if len(got) != len(want) {
		t.Fatalf("observations = %#v, want %d", got, len(want))
	}
	for index, expected := range want {
		observation := got[index]
		if observation.BlackboardOperation != expected.operation || observation.ToolName != expected.toolName {
			t.Fatalf("observation %d = %#v, want operation %q tool %q", index, observation, expected.operation, expected.toolName)
		}
	}
}

func TestHermesSessionUpdateEmitsRuntimeOutputForTranscript(t *testing.T) {
	session, _ := newAssistedHermesSession(t, RuntimeTurnKindWork)
	var outputs []task.EventPayload
	emit := func(kind task.EventKind, payload task.EventPayload) {
		if kind == task.EventKindRuntimeOutput {
			outputs = append(outputs, payload)
		}
	}
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Inspecting the app."}}}`),
	}, emit)
	if len(outputs) != 1 {
		t.Fatalf("runtime_output events = %#v", outputs)
	}
	if outputs[0]["provider"] != "hermes" || outputs[0]["stream"] != "hermes_acp" {
		t.Fatalf("payload = %#v", outputs[0])
	}
	text, _ := outputs[0]["text"].(string)
	if !strings.Contains(text, "Inspecting the app.") || !strings.Contains(text, "agent_message_chunk") {
		t.Fatalf("text = %q", text)
	}
}

func TestHermesInvalidControlResultReportsOnlyBoundedFailure(t *testing.T) {
	session, requestID := newAssistedHermesSession(t, RuntimeTurnKindControl)
	var got ProviderSessionAttemptResultValidationFailure
	session.SetAttemptResultValidationFailureSink(func(failure ProviderSessionAttemptResultValidationFailure) { got = failure })
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"not JSON and secret"}}}`),
	}, nil)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","update":{"sessionUpdate":"turn_ended","stopReason":"end_turn"}}`),
	}, nil)
	if got.RequestID != requestID || got.SessionID != "hermes-session" || got.ProviderTurnID != "hermes-turn" || got.ValidationErrorCode != ProviderSessionAttemptResultInvalid {
		t.Fatalf("validation failure = %#v", got)
	}
}

func TestHermesProviderSessionStreamsThoughtChunks(t *testing.T) {
	var events []task.EventPayload
	session := NewHermesProviderSession(HermesProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "hermes-session", ActiveTurnID: "hermes-turn",
	})
	emit := func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) }

	// Two thought chunks arrive before any other event: batching keeps both
	// out of the event store until the window or a barrier flushes.
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","turn_id":"hermes-turn","update":{"sessionUpdate":"agent_thought_chunk","text":"checking "}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","turn_id":"hermes-turn","update":{"sessionUpdate":"agent_thought_chunk","text":"the auth flow"}}`),
	}, emit)
	if len(events) != 0 {
		t.Fatalf("thought chunks must batch before a flush: %#v", events)
	}

	// The next tool call event barriers the pending reasoning batch so the
	// durable order matches the wire order.
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","turn_id":"hermes-turn","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"bash","rawInput":{"command":"ls"}}}`),
	}, emit)

	if len(events) != 2 {
		t.Fatalf("events after barrier = %#v", events)
	}
	batched := events[0]
	if batched["provider_event"] != "session/update" || batched["stream"] != "hermes_acp" {
		t.Fatalf("batched thought payload = %#v", batched)
	}
	if batched["provider_item_id"] != "hermes-thought-hermes-turn-1" {
		t.Fatalf("batched thought identity = %#v", batched)
	}
	text, _ := batched["text"].(string)
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		t.Fatalf("batched thought text is not JSON: %q", text)
	}
	if record["sessionUpdate"] != "agent_thought_chunk" || record["text"] != "checking the auth flow" {
		t.Fatalf("batched thought record = %#v", record)
	}
	if record["item_id"] != "hermes-thought-hermes-turn-1" {
		t.Fatalf("batched thought item id = %#v", record)
	}
	if events[1]["provider_item_id"] != nil {
		t.Fatalf("tool call event must not carry reasoning identity: %#v", events[1])
	}

	// A turn boundary flushes any trailing reasoning tail.
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","turn_id":"hermes-turn","update":{"sessionUpdate":"agent_thought_chunk","text":" tail"}}`),
	}, emit)
	if len(events) != 2 {
		t.Fatalf("trailing chunk must stay batched: %#v", events)
	}
	session.HandleEvent(SandboxBridgeEvent{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"hermes-session","turn_id":"hermes-turn","update":{"sessionUpdate":"turn_ended","stopReason":"end_turn"}}`),
	}, emit)
	if len(events) != 3 {
		t.Fatalf("turn end must complete the trailing reasoning segment: %#v", events)
	}
	// The first segment was completed before the tool call. The post-tool tail
	// is a new reasoning segment with a different stable identity.
	if events[0]["phase"] != "completed" || events[0]["provider_item_id"] != "hermes-thought-hermes-turn-1" {
		t.Fatalf("first completed thought segment = %#v", events[0])
	}
	tailText, _ := events[2]["text"].(string)
	if events[2]["phase"] != "completed" || events[2]["provider_item_id"] != "hermes-thought-hermes-turn-2" || !strings.Contains(tailText, " tail") {
		t.Fatalf("second completed thought segment = %#v", events[2])
	}
}
