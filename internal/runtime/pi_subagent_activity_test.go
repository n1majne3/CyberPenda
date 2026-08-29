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
