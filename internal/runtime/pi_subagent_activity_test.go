package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// The Pi live-event handler must project an entry_appended subagents:record
// frame as runtime output so the Timeline shows the settled subagent.
func TestPiHandleEventEmitsSubagentRecordRuntimeOutput(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/prompt": {Result: json.RawMessage(`{"turn_id":"turn-1"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{
		Transport: transport, SessionID: "pi-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	var events []task.EventPayload
	session.SetEventSink(func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) })

	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "req-1", Message: "work", TurnKind: RuntimeTurnKindWork}, nil); err != nil {
		t.Fatal(err)
	}

	session.HandleEvent(SandboxBridgeEvent{Method: "pi/entry_appended", Params: json.RawMessage(`{
		"session_id":"pi-1","turn_id":"turn-1",
		"entry":{"type":"custom","customType":"subagents:record","data":{"id":"agent-abc123","type":"Explore","description":"Scan the target","status":"completed"}}
	}`)}, nil)

	var found task.EventPayload
	for _, event := range events {
		if text, _ := event["text"].(string); strings.Contains(text, "subagents:record") {
			found = event
		}
	}
	if found == nil {
		t.Fatalf("expected subagents:record runtime output, got %#v", events)
	}
	if found["provider"] != "pi" || found["provider_event"] != "pi/entry_appended" {
		t.Fatalf("provider metadata = %#v", found)
	}
}

// Other entry_appended custom entries must not produce subagent output.
func TestPiHandleEventIgnoresNonSubagentEntries(t *testing.T) {
	session := NewPiProviderSession(PiProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "pi-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	var events []task.EventPayload
	session.SetEventSink(func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) })

	session.HandleEvent(SandboxBridgeEvent{Method: "pi/entry_appended", Params: json.RawMessage(`{
		"session_id":"pi-1","turn_id":"turn-1",
		"entry":{"type":"custom","customType":"workflow:record","data":{"id":"wf-1"}}
	}`)}, nil)

	for _, event := range events {
		if text, _ := event["text"].(string); strings.Contains(text, "workflow:record") {
			t.Fatalf("expected non-subagent entry to be ignored, got %#v", events)
		}
	}
}

// A bridge-relayed child transcript line becomes runtime output for this
// session even between Work Runtime Turns; only the child-attributed lines of
// the session's own subagents qualify.
func TestPiHandleEventEmitsSubagentOutputRuntimeOutput(t *testing.T) {
	session := NewPiProviderSession(PiProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "pi-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	var kinds []task.EventKind
	var events []task.EventPayload
	session.SetEventSink(func(kind task.EventKind, payload task.EventPayload) {
		kinds = append(kinds, kind)
		events = append(events, payload)
	})

	line := `{"isSidechain":true,"agentId":"d62e4d35-5898-450","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"child progress"}]}}`
	params, err := json.Marshal(map[string]any{"line": line, "session_id": "pi-1"})
	if err != nil {
		t.Fatal(err)
	}
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/subagent_output", Params: params}, nil)

	if len(events) != 1 || kinds[0] != task.EventKindRuntimeOutput {
		t.Fatalf("expected one runtime_output event, got %#v", kinds)
	}
	found := events[0]
	if found["text"] != line || found["provider"] != "pi" || found["provider_event"] != "pi/subagent_output" || found["stream"] != "pi_rpc" {
		t.Fatalf("subagent output payload = %#v", found)
	}

	for name, params := range map[string]map[string]any{
		"other session":   {"line": line, "session_id": "pi-2"},
		"missing agentId": {"line": `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`, "session_id": "pi-1"},
		"invalid line":    {"line": "not json", "session_id": "pi-1"},
	} {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		session.HandleEvent(SandboxBridgeEvent{Method: "pi/subagent_output", Params: raw}, nil)
		if len(events) != 1 {
			t.Fatalf("%s: expected the line to be dropped, got %#v", name, events)
		}
	}
}
