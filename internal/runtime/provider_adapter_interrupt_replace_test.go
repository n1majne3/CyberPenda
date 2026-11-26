package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"pentest/internal/task"
)

func TestPiInterruptThenReplaceBaselineCoversTerminalAfterActiveTurnRead(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/abort":  {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-old"}`)},
		"pi/prompt": {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-new"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1", ActiveTurnID: "turn-old"})
	emit := func(_ task.EventKind, payload task.EventPayload) {
		if payload["outcome"] == "requested" {
			reenactTerminalAfterActiveTurnRead(session, "pi-1", "turn-old")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := session.InterruptThenReplace(ctx, ProviderSessionRequest{RequestID: "replace-race", Message: "replace with new evidence"}, emit)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if result.SessionID != "pi-1" || result.ProviderTurnID != "turn-new" {
		t.Fatalf("result = %#v", result)
	}
	requests := transport.snapshot()
	if len(requests) != 2 || requests[0].Method != "pi/abort" || requests[1].Method != "pi/prompt" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestPiInterruptTurnBaselineCoversTerminalAfterOperationStart(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/abort": {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-old"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1", ActiveTurnID: "turn-old"})
	emit := func(_ task.EventKind, payload task.EventPayload) {
		if payload["outcome"] == "requested" {
			session.recordProviderTurnEvent("pi-1", "turn-old", true)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := session.InterruptTurn(ctx, ProviderSessionRequest{RequestID: "interrupt-race", ProviderTurnID: "turn-old"}, emit)
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if result.SessionID != "pi-1" || result.ProviderTurnID != "turn-old" || result.Outcome != "settled" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPiInterruptThenReplaceAfterTerminalSkipsAbort(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/prompt": {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-new"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1", ActiveTurnID: "turn-old"})
	session.recordProviderTurnEvent("pi-1", "turn-old", true)
	result, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{RequestID: "replace-idle", Message: "replace after terminal"}, nil)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if result.SessionID != "pi-1" || result.ProviderTurnID != "turn-new" {
		t.Fatalf("result = %#v", result)
	}
	requests := transport.snapshot()
	if len(requests) != 1 || requests[0].Method != "pi/prompt" {
		t.Fatalf("requests = %#v", requests)
	}
}

func reenactTerminalAfterActiveTurnRead(session *PiProviderSession, sessionID, turnID string) {
	session.recordProviderTurnEvent(sessionID, turnID, true)
	session.mu.Lock()
	session.activeTurnID = turnID
	session.mu.Unlock()
}
