package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// The Codex live-event handler must project subAgentActivity thread items as
// runtime output instead of dropping them at the item-type default.
func TestCodexHandleEventEmitsSubAgentActivityRuntimeOutput(t *testing.T) {
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "thread-parent",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	var events []task.EventPayload
	session.SetEventSink(func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) })

	session.HandleEvent(SandboxBridgeEvent{Method: "item/started", Params: json.RawMessage(`{
		"threadId":"thread-parent","turnId":"turn-1",
		"item":{"id":"item-sa-1","type":"subAgentActivity","agentThreadId":"thread-child-1","agentPath":"security/recon","kind":"started"}
	}`)}, nil)

	var found task.EventPayload
	for _, event := range events {
		if event["provider_event"] == "item/started" {
			if text, _ := event["text"].(string); strings.Contains(text, "subAgentActivity") {
				found = event
			}
		}
	}
	if found == nil {
		t.Fatalf("expected subAgentActivity runtime output, got %#v", events)
	}
	if found["stream"] != "codex_app_server" {
		t.Fatalf("stream = %#v", found["stream"])
	}
}

// collabAgentToolCall thread items must likewise reach runtime output so the
// timeline can project the target child's coarse state.
func TestCodexHandleEventEmitsCollabAgentToolCallRuntimeOutput(t *testing.T) {
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "thread-parent",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	var events []task.EventPayload
	session.SetEventSink(func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) })

	session.HandleEvent(SandboxBridgeEvent{Method: "item/started", Params: json.RawMessage(`{
		"threadId":"thread-parent","turnId":"turn-1",
		"item":{"id":"collab-1","type":"collabAgentToolCall","tool":"spawnAgent","senderThreadId":"thread-parent","receiverThreadIds":["thread-child-9"],"status":"inProgress","agentsStates":{"thread-child-9":{"status":"running"}}}
	}`)}, nil)

	var found bool
	for _, event := range events {
		if text, _ := event["text"].(string); strings.Contains(text, "collabAgentToolCall") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected collabAgentToolCall runtime output, got %#v", events)
	}
}

// Unknown item types must still be dropped: only the recognized subagent
// activity shapes are un-suppressed.
func TestCodexHandleEventStillDropsUnknownItemTypes(t *testing.T) {
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "thread-parent",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	var events []task.EventPayload
	session.SetEventSink(func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) })

	session.HandleEvent(SandboxBridgeEvent{Method: "item/started", Params: json.RawMessage(`{
		"threadId":"thread-parent","turnId":"turn-1",
		"item":{"id":"x-1","type":"hookPrompt","fragments":[]}
	}`)}, nil)

	for _, event := range events {
		if text, _ := event["text"].(string); strings.Contains(text, "hookPrompt") {
			t.Fatalf("expected unknown item type to be dropped, got %#v", events)
		}
	}
}
