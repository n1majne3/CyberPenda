package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

const validPiAttemptResult = `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:pi-search","create":true,"summary":"Tested search without proving exploitability.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:pi-search","create_objective":{"objective":"Test Pi search."}}],"produced_targets":[]}`

func newAssistedPiSession(t *testing.T, kind RuntimeTurnKind) (*PiProviderSession, string) {
	t.Helper()
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/prompt": {Result: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{
		Transport: transport, SessionID: "pi-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	requestID := "conclude-pi"
	if kind == RuntimeTurnKindWork {
		requestID = "work-pi"
	}
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: requestID, Message: "return one closed result", TurnKind: kind,
		ModelProviderID: "provider-a", Model: "model-a", RequestedReasoningEffort: "high",
	}, nil); err != nil {
		t.Fatalf("send Pi turn: %v", err)
	}
	return session, requestID
}

func TestPiControlTurnExtractsAttemptResultAtCorrelatedTerminalBoundary(t *testing.T) {
	session, requestID := newAssistedPiSession(t, RuntimeTurnKindControl)
	var got ProviderSessionAttemptResult
	session.SetAttemptResultSink(func(result ProviderSessionAttemptResult) { got = result })

	session.HandleEvent(SandboxBridgeEvent{
		Method: "pi/message_end",
		Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","message":{"role":"assistant","content":[{"type":"text","text":` + mustRawJSONString(t, validPiAttemptResult) + `}]},"reasoning":"must not escape"}`),
	}, nil)
	if got.RequestID != "" {
		t.Fatalf("Attempt result emitted before terminal boundary: %#v", got)
	}

	session.HandleEvent(SandboxBridgeEvent{
		Method: "pi/agent_end",
		Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","status":"completed","output":"must not escape"}`),
	}, nil)
	if got.RequestID != requestID || got.SessionID != "pi-session" || got.ProviderTurnID != "pi-turn" {
		t.Fatalf("Attempt result correlation = %#v", got)
	}
	if string(got.Validated.CanonicalJSON) != validPiAttemptResult {
		t.Fatalf("canonical result = %s", got.Validated.CanonicalJSON)
	}
}

func TestPiWorkTurnNeverProducesAttemptResult(t *testing.T) {
	session, _ := newAssistedPiSession(t, RuntimeTurnKindWork)
	callbacks := 0
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { callbacks++ })
	session.SetAttemptResultValidationFailureSink(func(ProviderSessionAttemptResultValidationFailure) { callbacks++ })
	session.HandleEvent(SandboxBridgeEvent{
		Method: "pi/message_end",
		Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","message":{"role":"assistant","content":[{"type":"text","text":` + mustRawJSONString(t, validPiAttemptResult) + `}]}}`),
	}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/agent_end", Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","status":"completed"}`)}, nil)
	if callbacks != 0 {
		t.Fatalf("work Turn produced %d Attempt result callbacks", callbacks)
	}
}

func TestPiRejectsEventsFromAnotherSession(t *testing.T) {
	session, _ := newAssistedPiSession(t, RuntimeTurnKindControl)
	var observations, results, failures int
	session.SetObservationSink(func(ProviderSessionObservation) { observations++ })
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { results++ })
	session.SetAttemptResultValidationFailureSink(func(ProviderSessionAttemptResultValidationFailure) { failures++ })
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/message_end", Params: json.RawMessage(`{"session_id":"pi-spoofed","turn_id":"pi-turn","message":{"role":"assistant","content":[{"type":"text","text":` + mustRawJSONString(t, validPiAttemptResult) + `}]}}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/tool_execution_start", Params: json.RawMessage(`{"session_id":"pi-spoofed","turn_id":"pi-turn","toolCallId":"call-1","toolName":"bash"}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/agent_end", Params: json.RawMessage(`{"session_id":"pi-spoofed","turn_id":"pi-turn","status":"completed"}`)}, nil)
	if observations != 0 || results != 0 || failures != 0 {
		t.Fatalf("wrong-session callbacks: observations=%d results=%d failures=%d", observations, results, failures)
	}
}

func TestPiEventsProjectBoundedCorrelatedObservationsWithoutRawPayloads(t *testing.T) {
	session, requestID := newAssistedPiSession(t, RuntimeTurnKindWork)
	var got []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { got = append(got, observation) })
	var events []task.EventPayload
	emit := func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) }

	for _, event := range []SandboxBridgeEvent{
		{Method: "pi/agent_start", Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","prompt":"secret prompt"}`)},
		{Method: "pi/tool_execution_start", Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","toolCallId":"call-1","toolName":"bash","args":{"token":"secret"}}`)},
		{Method: "pi/tool_execution_end", Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","toolCallId":"call-1","toolName":"bash","isError":false,"result":"secret output"}`)},
		{Method: "pi/agent_end", Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","status":"completed","messages":["secret"]}`)},
	} {
		session.HandleEvent(event, emit)
	}

	want := []ProviderSessionObservation{
		{Kind: ProviderSessionObservationToolUse, RequestID: requestID, SessionID: "pi-session", ProviderTurnID: "pi-turn", ToolCallID: "call-1", ToolName: "bash"},
		{Kind: ProviderSessionObservationToolResult, RequestID: requestID, SessionID: "pi-session", ProviderTurnID: "pi-turn", ToolCallID: "call-1", ToolName: "bash", Status: "succeeded"},
		{Kind: ProviderSessionObservationTurnCompleted, RequestID: requestID, SessionID: "pi-session", ProviderTurnID: "pi-turn", Status: "completed"},
	}
	if len(got) != len(want) {
		t.Fatalf("observations = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("observation %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if len(events) != 2 || events[0]["outcome"] != "started" || events[1]["outcome"] != "completed" ||
		events[0]["request_id"] != requestID || events[1]["request_id"] != requestID {
		t.Fatalf("Pi lifecycle events = %#v", events)
	}
	encoded, err := json.Marshal(struct {
		Observations []ProviderSessionObservation
		Events       []task.EventPayload
	}{got, events})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret output", "token", "must not escape"} {
		if containsJSONText(encoded, secret) {
			t.Fatalf("normalized Pi metadata leaked %q: %s", secret, encoded)
		}
	}
}

func TestPiInvalidControlResultReportsOnlyBoundedFailure(t *testing.T) {
	session, requestID := newAssistedPiSession(t, RuntimeTurnKindControl)
	var got ProviderSessionAttemptResultValidationFailure
	session.SetAttemptResultValidationFailureSink(func(failure ProviderSessionAttemptResultValidationFailure) { got = failure })
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/message_end", Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","message":{"role":"assistant","content":[{"type":"text","text":"not JSON and secret"}]}}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/agent_end", Params: json.RawMessage(`{"session_id":"pi-session","turn_id":"pi-turn","status":"completed"}`)}, nil)
	if got.RequestID != requestID || got.SessionID != "pi-session" || got.ProviderTurnID != "pi-turn" || got.ValidationErrorCode != ProviderSessionAttemptResultInvalid {
		t.Fatalf("validation failure = %#v", got)
	}
}

func TestPiTerminalStatusesAndDuplicateDeliveryMatchObservationContract(t *testing.T) {
	for _, test := range []struct {
		name, method, params, want string
	}{
		{name: "completed", method: "pi/agent_settled", params: `{"session_id":"pi-session","turn_id":"pi-turn"}`, want: "completed"},
		{name: "failed", method: "pi/agent_end", params: `{"session_id":"pi-session","turn_id":"pi-turn","status":"failed","error":"secret"}`, want: "failed"},
		{name: "interrupted", method: "pi/agent_end", params: `{"session_id":"pi-session","turn_id":"pi-turn","status":"aborted"}`, want: "interrupted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, requestID := newAssistedPiSession(t, RuntimeTurnKindWork)
			var got []ProviderSessionObservation
			var lifecycle []task.EventPayload
			session.SetObservationSink(func(observation ProviderSessionObservation) { got = append(got, observation) })
			event := SandboxBridgeEvent{Method: test.method, Params: json.RawMessage(test.params)}
			emit := func(_ task.EventKind, payload task.EventPayload) { lifecycle = append(lifecycle, payload) }
			session.HandleEvent(event, emit)
			session.HandleEvent(event, emit)
			if len(got) != 1 || got[0] != (ProviderSessionObservation{
				Kind: ProviderSessionObservationTurnCompleted, RequestID: requestID,
				SessionID: "pi-session", ProviderTurnID: "pi-turn", Status: test.want,
			}) {
				t.Fatalf("terminal observations = %#v", got)
			}
			if len(lifecycle) != 2 || lifecycle[0]["outcome"] != test.want || lifecycle[0]["request_id"] != requestID {
				t.Fatalf("terminal lifecycle events = %#v", lifecycle)
			}
		})
	}
}

func TestPiAndFakeProviderSessionsExposeTheSameAssistedSeams(t *testing.T) {
	piSession, _ := newAssistedPiSession(t, RuntimeTurnKindControl)
	fakeSession := NewFakeProviderSession(FakeProviderSessionConfig{
		SessionID: "fake-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, AssistedConclusion: true,
		},
	})
	for name, session := range map[string]ProviderSession{"pi": piSession, "fake": fakeSession} {
		capabilities := session.Capabilities()
		if !capabilities.PersistentSession || !capabilities.SendTurn || !capabilities.AssistedConclusion {
			t.Fatalf("%s assisted capabilities = %#v", name, capabilities)
		}
		if _, ok := session.(ProviderSessionObservationSink); !ok {
			t.Fatalf("%s lacks normalized observation seam", name)
		}
		if _, ok := session.(ProviderSessionCompleteTurnLineageResolver); !ok {
			t.Fatalf("%s lacks complete Turn lineage seam", name)
		}
		if _, ok := session.(ProviderSessionAttemptResultSource); !ok {
			t.Fatalf("%s lacks structured Attempt result seam", name)
		}
	}
}

func mustRawJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func containsJSONText(encoded []byte, value string) bool {
	for index := 0; index+len(value) <= len(encoded); index++ {
		if string(encoded[index:index+len(value)]) == value {
			return true
		}
	}
	return false
}
