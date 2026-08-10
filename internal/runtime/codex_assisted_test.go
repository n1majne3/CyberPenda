package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

func TestCodexAssistedObservationsAreCorrelatedBoundedAndRedacted(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-work"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	var observed []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { observed = append(observed, observation) })
	request := ProviderSessionRequest{RequestID: "work-request", Message: "inspect", TurnKind: RuntimeTurnKindWork}
	if _, err := session.SendTurn(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}

	session.HandleEvent(SandboxBridgeEvent{Method: "item/started", Params: json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-work","requestId":"spoofed",
		"item":{"id":"call-1","type":"mcpToolCall","server":"pentest","tool":"blackboard_attempt","status":"inProgress","arguments":{"token":"secret"}}
	}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "item/completed", Params: json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-work",
		"item":{"id":"call-1","type":"mcpToolCall","server":"pentest","tool":"blackboard_attempt","status":"completed","result":{"secret":"must-not-leak"}}
	}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{
		"threadId":"thread-1","turn":{"id":"turn-work","status":"completed","output":"must-not-leak"}
	}`)}, nil)

	want := []ProviderSessionObservation{
		{Kind: ProviderSessionObservationToolUse, RequestID: "work-request", SessionID: "thread-1", ProviderTurnID: "turn-work", ToolCallID: "call-1", ToolName: "mcp__pentest__blackboard_attempt"},
		{Kind: ProviderSessionObservationToolResult, RequestID: "work-request", SessionID: "thread-1", ProviderTurnID: "turn-work", ToolCallID: "call-1", ToolName: "mcp__pentest__blackboard_attempt", Status: "succeeded"},
		{Kind: ProviderSessionObservationTurnCompleted, RequestID: "work-request", SessionID: "thread-1", ProviderTurnID: "turn-work", Status: "completed"},
	}
	if len(observed) != len(want) {
		t.Fatalf("observations = %#v", observed)
	}
	for index := range want {
		if observed[index] != want[index] {
			t.Fatalf("observation %d = %#v, want %#v", index, observed[index], want[index])
		}
		encoded, _ := json.Marshal(observed[index])
		if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "spoofed") {
			t.Fatalf("observation leaked provider payload: %s", encoded)
		}
	}
}

// #192 cross-provider contract: a Codex MCP tool call under the trusted
// Project Interface registration carries its canonical Blackboard operation
// identity; a similar display name from any other server never does.
func TestCodexToolObservationsCarryCanonicalBlackboardOperationIdentity(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-work"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	var observed []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { observed = append(observed, observation) })
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: "work-request", Message: "inspect", TurnKind: RuntimeTurnKindWork,
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, event := range []SandboxBridgeEvent{
		{Method: "item/started", Params: json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-work",
			"item":{"id":"call-1","type":"mcpToolCall","server":"pentest","tool":"blackboard_change","status":"inProgress"}
		}`)},
		{Method: "item/completed", Params: json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-work",
			"item":{"id":"call-1","type":"mcpToolCall","server":"pentest","tool":"blackboard_change","status":"completed"}
		}`)},
		{Method: "item/completed", Params: json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-work",
			"item":{"id":"call-2","type":"mcpToolCall","server":"evil","tool":"blackboard_change","status":"completed"}
		}`)},
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
	}
	if len(observed) != len(want) {
		t.Fatalf("observations = %#v, want %d", observed, len(want))
	}
	for index, expected := range want {
		got := observed[index]
		if got.BlackboardOperation != expected.operation || got.ToolName != expected.toolName {
			t.Fatalf("observation %d = %#v, want operation %q tool %q", index, got, expected.operation, expected.toolName)
		}
	}
}

func TestCodexAssistedControlResultUsesHarnessLineageAndExplicitSelection(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-control"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	var results []ProviderSessionAttemptResult
	var failures []ProviderSessionAttemptResultValidationFailure
	var observed []ProviderSessionObservation
	session.SetAttemptResultSink(func(result ProviderSessionAttemptResult) { results = append(results, result) })
	session.SetAttemptResultValidationFailureSink(func(failure ProviderSessionAttemptResultValidationFailure) { failures = append(failures, failure) })
	session.SetObservationSink(func(observation ProviderSessionObservation) { observed = append(observed, observation) })
	request := ProviderSessionRequest{
		RequestID: "conclude-request", Message: "conclude", TurnKind: RuntimeTurnKindControl,
		ModelProviderID: "provider-1", Model: "gpt-test", RequestedReasoningEffort: "high",
	}
	if _, err := session.SendTurn(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	raw := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:codex","create":true,"summary":"Tested Codex.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:codex","create_objective":{"objective":"Test Codex."}}],"produced_targets":[]}`
	session.HandleEvent(SandboxBridgeEvent{Method: "item/completed", Params: json.RawMessage(`{
		"threadId":"thread-1","turnId":"turn-control","requestId":"provider-spoof",
		"turnKind":"work","item":{"id":"answer-1","type":"agentMessage","text":` + quoteJSON(raw) + `}
	}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{
		"threadId":"thread-1","turn":{"id":"turn-control","status":"completed"}
	}`)}, nil)

	if len(results) != 1 || len(failures) != 0 {
		t.Fatalf("results=%#v failures=%#v", results, failures)
	}
	if results[0].RequestID != request.RequestID || results[0].SessionID != "thread-1" || results[0].ProviderTurnID != "turn-control" ||
		results[0].Validated.Result.Attempt.Key != "attempt:codex" {
		t.Fatalf("result correlation = %#v", results[0])
	}
	lineage, ok := session.ResolveProviderSessionTurnLineage("", "turn-control")
	if !ok || lineage.Kind != RuntimeTurnKindControl || lineage.ModelProviderID != "provider-1" || lineage.Model != "gpt-test" || lineage.RequestedReasoningEffort != "high" {
		t.Fatalf("control lineage = %#v, %v", lineage, ok)
	}
	if len(observed) != 1 || observed[0].Kind != ProviderSessionObservationTurnCompleted || observed[0].RequestID != request.RequestID {
		t.Fatalf("terminal observations = %#v", observed)
	}
}

func TestCodexAssistedIgnoresDuplicateTerminalDelivery(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-control"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	var results, failures, terminals int
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { results++ })
	session.SetAttemptResultValidationFailureSink(func(ProviderSessionAttemptResultValidationFailure) { failures++ })
	session.SetObservationSink(func(observation ProviderSessionObservation) {
		if observation.Kind == ProviderSessionObservationTurnCompleted {
			terminals++
		}
	})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "control", Message: "conclude", TurnKind: RuntimeTurnKindControl}, nil); err != nil {
		t.Fatal(err)
	}
	raw := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:codex","create":true,"summary":"Tested Codex.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:codex","create_objective":{"objective":"Test Codex."}}],"produced_targets":[]}`
	session.HandleEvent(SandboxBridgeEvent{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-control","item":{"id":"answer","type":"agentMessage","text":` + quoteJSON(raw) + `}}`)}, nil)
	terminal := SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-control","status":"completed"}}`)}
	session.HandleEvent(terminal, nil)
	session.HandleEvent(terminal, nil)
	if results != 1 || failures != 0 || terminals != 1 {
		t.Fatalf("duplicate terminal callbacks: results=%d failures=%d terminals=%d", results, failures, terminals)
	}
}

func TestCodexAssistedRejectsInvalidControlResultButIgnoresWorkText(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-control"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	var valid, invalid int
	session.SetAttemptResultSink(func(ProviderSessionAttemptResult) { valid++ })
	session.SetAttemptResultValidationFailureSink(func(failure ProviderSessionAttemptResultValidationFailure) {
		invalid++
		if failure.RequestID != "control" || failure.ProviderTurnID != "turn-control" || failure.ValidationErrorCode != ProviderSessionAttemptResultInvalid {
			t.Fatalf("failure = %#v", failure)
		}
	})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "control", Message: "conclude", TurnKind: RuntimeTurnKindControl}, nil); err != nil {
		t.Fatal(err)
	}
	session.HandleEvent(SandboxBridgeEvent{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-control","item":{"id":"answer","type":"agentMessage","text":"not closed json"}}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-control","status":"completed"}}`)}, nil)
	if valid != 0 || invalid != 1 {
		t.Fatalf("valid=%d invalid=%d", valid, invalid)
	}
}

func TestCodexAssistedQueuesSafeNotificationsUntilTurnStartReturns(t *testing.T) {
	var session *CodexProviderSession
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		if request.Method != "turn/start" {
			t.Fatalf("method = %q", request.Method)
		}
		session.HandleEvent(SandboxBridgeEvent{Method: "item/started", Params: json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-race",
			"item":{"id":"call-race","type":"commandExecution","command":"secret command"}
		}`)}, nil)
		session.HandleEvent(SandboxBridgeEvent{Method: "item/completed", Params: json.RawMessage(`{
			"threadId":"thread-1","turnId":"turn-race",
			"item":{"id":"call-race","type":"commandExecution","status":"failed","output":"secret result"}
		}`)}, nil)
		return SandboxBridgeResponse{Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-race"}}`)}, nil
	}}
	session = NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	var observed []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) { observed = append(observed, observation) })
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "race-request", Message: "inspect"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 || observed[0].RequestID != "race-request" || observed[0].Kind != ProviderSessionObservationToolUse ||
		observed[1].Kind != ProviderSessionObservationToolResult || observed[1].Status != "failed" {
		t.Fatalf("raced observations = %#v", observed)
	}
}

func TestCodexLifecycleUsesTerminalStatusInsteadOfCompletedMethodName(t *testing.T) {
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: &fakeProviderTransport{}, SessionID: "thread-1", ActiveTurnID: "turn-1"})
	for _, test := range []struct{ status, want string }{{"completed", "completed"}, {"failed", "failed"}, {"interrupted", "interrupted"}} {
		t.Run(test.status, func(t *testing.T) {
			var events []task.EventPayload
			session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"` + test.status + `"}}`)}, func(_ task.EventKind, payload task.EventPayload) {
				events = append(events, payload)
			})
			if len(events) != 1 || events[0]["outcome"] != test.want {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestCodexAdvertisesAssistedConclusionOnlyWithPersistentSendContract(t *testing.T) {
	capable := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	if !capable.Capabilities().AssistedConclusion {
		t.Fatal("complete Codex App Server contract did not advertise assisted conclusion")
	}
	if _, ok := any(capable).(ProviderSessionObservationSink); !ok {
		t.Fatal("Codex lacks observation sink")
	}
	if _, ok := any(capable).(ProviderSessionCompleteTurnLineageResolver); !ok {
		t.Fatal("Codex lacks complete Turn lineage")
	}
	if _, ok := any(capable).(ProviderSessionAttemptResultSource); !ok {
		t.Fatal("Codex lacks structured Attempt result source")
	}

	for _, caps := range []runtimeplugin.Capabilities{
		{PersistentSession: true, AssistedConclusion: true},
		{SendTurn: true, AssistedConclusion: true},
	} {
		session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: &fakeProviderTransport{}, SessionID: "thread-1", Capabilities: caps})
		if session.Capabilities().AssistedConclusion {
			t.Fatalf("incomplete Codex contract advertised assisted conclusion: %#v", session.Capabilities())
		}
	}
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
