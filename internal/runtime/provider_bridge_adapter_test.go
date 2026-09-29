package runtime

import (
	"context"
	"testing"
	"time"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

// contentEmitSession delegates control semantics to FakeProviderSession but
// emits one LLM output line through the turn emit callback, so the bridge
// adapter's emit wrapper can be observed directly.
type contentEmitSession struct {
	*FakeProviderSession
	text string
}

func (s contentEmitSession) SendTurn(ctx context.Context, request ProviderSessionRequest, emit ProviderSessionEmit) (ProviderSessionResult, error) {
	emit(task.EventKindRuntimeOutput, task.EventPayload{"stream": "stdout", "text": s.text})
	return s.FakeProviderSession.SendTurn(ctx, request, emit)
}

// TestProviderSessionRunAdapterKeepsContentUnredacted pins issue #288: LLM
// output relayed by the provider-session bridge passes through byte-for-byte,
// even when it matches a secret shape.
func TestProviderSessionRunAdapterKeepsContentUnredacted(t *testing.T) {
	const line = "assistant: bearer secret-bridge-token-123456"
	closed := make(chan struct{})
	session := contentEmitSession{
		FakeProviderSession: NewFakeProviderSession(FakeProviderSessionConfig{
			SessionID:    "bridge-1",
			Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
		}),
		text: line,
	}
	adapter := NewProviderSessionRunAdapter(session, closed)
	adapter.BindContinuation("continuation-1")

	var events []task.EventPayload
	emit := func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, "goal", emit) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(events) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if len(events) == 0 {
		t.Fatal("expected the session content event")
	}
	if text, _ := events[0]["text"].(string); text != line {
		t.Fatalf("bridge emit rewrote LLM content: %q want %q", events[0]["text"], line)
	}
}
