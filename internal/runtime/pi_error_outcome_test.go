package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"pentest/internal/task"
)

func TestPiProviderSessionMarksAgentEndWithErrorMessageAsFailed(t *testing.T) {
	session := NewPiProviderSession(PiProviderSessionConfig{
		SessionID:    "pi-session-1",
		ActiveTurnID: "turn-1",
	})
	var events []task.EventPayload
	params, err := json.Marshal(map[string]any{
		"session_id": "pi-session-1",
		"turn_id":    "turn-1",
		"messages": []map[string]any{
			{"role": "user", "content": "Say hello"},
			{
				"role":         "assistant",
				"content":      []any{},
				"stopReason":   "error",
				"errorMessage": "400 request (13032 tokens) exceeds the available context size (8192 tokens)",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	session.HandleEvent(SandboxBridgeEvent{
		Method: "pi/agent_end",
		Params: params,
	}, func(kind task.EventKind, payload task.EventPayload) {
		if kind == task.EventKindLifecycle {
			events = append(events, payload)
		}
	})

	if len(events) != 1 {
		t.Fatalf("expected 1 lifecycle event, got %d (%#v)", len(events), events)
	}
	if outcome, _ := events[0]["outcome"].(string); outcome != "failed" {
		t.Fatalf("expected outcome=failed, got %#v", events[0])
	}
	errMsg, _ := events[0]["error"].(string)
	if !strings.Contains(errMsg, "exceeds the available context size") {
		t.Fatalf("expected error detail in lifecycle payload, got %#v", events[0])
	}
}

func TestPiProviderSessionMarksAgentEndWithoutErrorAsCompleted(t *testing.T) {
	session := NewPiProviderSession(PiProviderSessionConfig{
		SessionID:    "pi-session-2",
		ActiveTurnID: "turn-2",
	})
	var events []task.EventPayload
	params, err := json.Marshal(map[string]any{
		"session_id": "pi-session-2",
		"turn_id":    "turn-2",
		"messages": []map[string]any{
			{"role": "user", "content": "Say hello"},
			{"role": "assistant", "content": "Hello!", "stopReason": "stop"},
		},
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	session.HandleEvent(SandboxBridgeEvent{
		Method: "pi/agent_end",
		Params: params,
	}, func(kind task.EventKind, payload task.EventPayload) {
		if kind == task.EventKindLifecycle {
			events = append(events, payload)
		}
	})

	if len(events) != 1 {
		t.Fatalf("expected 1 lifecycle event, got %d (%#v)", len(events), events)
	}
	if outcome, _ := events[0]["outcome"].(string); outcome != "completed" {
		t.Fatalf("expected outcome=completed, got %#v", events[0])
	}
	if _, hasErr := events[0]["error"]; hasErr {
		t.Fatalf("expected no error field on healthy agent_end, got %#v", events[0])
	}
}
