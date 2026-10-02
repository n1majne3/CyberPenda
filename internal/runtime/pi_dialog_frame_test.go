package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// A blocking extension_ui_request dialog frame must reach runtime output raw:
// the frame line is the only carrier of who asked what, and run 24370 stalled
// on a dialog whose identity no projected event retained.
func TestPiHandleEventForwardsDialogFrameRaw(t *testing.T) {
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

	frame := `{"session_id":"pi-1","turn_id":"turn-1","type":"extension_ui_request","id":"uuid-2","method":"confirm","title":"Trust this project?","message":"Project resources are waiting."}`
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/extension_ui_request", Params: json.RawMessage(frame)}, nil)

	var raw task.EventPayload
	var lifecycle task.EventPayload
	for _, event := range events {
		if text, _ := event["text"].(string); strings.Contains(text, "Trust this project?") {
			raw = event
		}
		if event["mode"] == string(ProviderSessionModePermissionResponse) {
			lifecycle = event
		}
	}
	if raw == nil {
		t.Fatalf("expected raw dialog runtime output, got %#v", events)
	}
	if raw["provider"] != "pi" || raw["provider_event"] != "pi/extension_ui_request" || raw["stream"] != "pi_rpc" {
		t.Fatalf("dialog frame metadata = %#v", raw)
	}
	if !strings.Contains(raw["text"].(string), `"id":"uuid-2"`) || !strings.Contains(raw["text"].(string), "Project resources are waiting.") {
		t.Fatalf("dialog frame text lost identity fields: %#v", raw["text"])
	}
	if lifecycle == nil || lifecycle["outcome"] != "requested" {
		t.Fatalf("expected the permission lifecycle event to keep firing, got %#v", events)
	}
	if lifecycle["permission_request_id"] != "uuid-2" {
		t.Fatalf("permission identity must fall back to the frame id: %#v", lifecycle)
	}
	if lifecycle["permission_title"] != "Trust this project?" || lifecycle["permission_method"] != "confirm" {
		t.Fatalf("permission identity fields = %#v", lifecycle)
	}
}

// Fire-and-forget extension UI frames (notify, setStatus, ...) are not raw
// forwarded: only dialog frames block a turn and carry an answerable id.
func TestPiHandleEventSkipsFireAndForgetUIFrames(t *testing.T) {
	session := NewPiProviderSession(PiProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "pi-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	var events []task.EventPayload
	session.SetEventSink(func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) })

	session.HandleEvent(SandboxBridgeEvent{Method: "pi/extension_ui_request", Params: json.RawMessage(`{
		"session_id":"pi-1","turn_id":"turn-1","type":"extension_ui_request","id":"uuid-5","method":"notify","message":"Background agent group completed","notifyType":"info"
	}`)}, nil)

	for _, event := range events {
		if text, _ := event["text"].(string); strings.Contains(text, "uuid-5") {
			t.Fatalf("fire-and-forget frame must not become raw runtime output: %#v", events)
		}
	}
}
