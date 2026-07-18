package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

type providerTransportCall struct {
	request SandboxBridgeRequest
}

type fakeProviderTransport struct {
	mu        sync.Mutex
	calls     []providerTransportCall
	responses map[string]SandboxBridgeResponse
	err       error
	send      func(context.Context, SandboxBridgeRequest) (SandboxBridgeResponse, error)
	closed    bool
}

func (t *fakeProviderTransport) Send(ctx context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
	t.mu.Lock()
	t.calls = append(t.calls, providerTransportCall{request: request})
	send := t.send
	if send != nil {
		t.mu.Unlock()
		return send(ctx, request)
	}
	defer t.mu.Unlock()
	if t.err != nil {
		return SandboxBridgeResponse{}, t.err
	}
	if response, ok := t.responses[request.Method]; ok {
		response.ID = request.ID
		return response, nil
	}
	return SandboxBridgeResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"ok":true}`)}, nil
}

func (t *fakeProviderTransport) Close(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

func (t *fakeProviderTransport) snapshot() []SandboxBridgeRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	requests := make([]SandboxBridgeRequest, 0, len(t.calls))
	for _, call := range t.calls {
		requests = append(requests, call.request)
	}
	return requests
}

func TestCodexProviderSessionMapsTurnStartAndInterrupt(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start":     {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2"}}`)},
		"turn/interrupt": {Result: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-2","status":"interrupted"}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1"})
	emits := []task.EventPayload{}
	emit := func(_ task.EventKind, payload task.EventPayload) { emits = append(emits, payload) }

	started, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "send-1", Message: "inspect the target"}, emit)
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}
	if started.SessionID != "thread-1" || started.ProviderTurnID != "turn-2" || started.Outcome != "started" {
		t.Fatalf("started result = %#v", started)
	}
	interrupted, err := session.InterruptTurn(context.Background(), ProviderSessionRequest{RequestID: "interrupt-1", ProviderTurnID: "turn-2"}, emit)
	if err != nil {
		t.Fatalf("interrupt turn: %v", err)
	}
	if interrupted.ProviderTurnID != "turn-2" || interrupted.Outcome != "settled" {
		t.Fatalf("interrupt result = %#v", interrupted)
	}
	requests := transport.snapshot()
	if len(requests) != 2 || requests[0].Method != "turn/start" || requests[1].Method != "turn/interrupt" {
		t.Fatalf("wire requests = %#v", requests)
	}
	var startParams map[string]any
	if err := json.Unmarshal(requests[0].Params, &startParams); err != nil {
		t.Fatal(err)
	}
	input, ok := startParams["input"].([]any)
	if startParams["threadId"] != "thread-1" || !ok || len(input) != 1 {
		t.Fatalf("start params = %#v", startParams)
	}
	inputItem, ok := input[0].(map[string]any)
	if !ok || inputItem["type"] != "text" || inputItem["text"] != "inspect the target" {
		t.Fatalf("structured start input = %#v", input)
	}
	if len(emits) < 4 || emits[0]["outcome"] != "requested" || emits[len(emits)-1]["outcome"] != "settled" {
		t.Fatalf("events = %#v", emits)
	}
	for _, event := range emits {
		if _, leaked := event["message"]; leaked {
			t.Fatalf("event leaked message: %#v", event)
		}
	}
}

func TestCodexProviderSessionInterruptThenReplaceUsesSameThread(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/interrupt": {Result: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-old","status":"interrupted"}`)},
		"turn/start":     {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-new"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-old"})
	result, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{RequestID: "replace-1", Message: "stop and focus on auth"}, nil)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if result.SessionID != "thread-1" || result.ProviderTurnID != "turn-new" || result.Outcome != "started" {
		t.Fatalf("result = %#v", result)
	}
	requests := transport.snapshot()
	if len(requests) != 2 || requests[0].Method != "turn/interrupt" || requests[1].Method != "turn/start" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestClaudeProviderSessionMapsInputInterruptAndPermission(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/input":              {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1"}`)},
		"claude/interrupt":          {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1","status":"interrupted"}`)},
		"claude/permission/respond": {Result: json.RawMessage(`{"session_id":"claude-1","permission_request_id":"perm-1","decision":"allow"}`)},
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1", ActiveTurnID: "turn-1"})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "send-1", Message: "continue"}, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := session.InterruptTurn(context.Background(), ProviderSessionRequest{RequestID: "interrupt-1"}, nil); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if _, err := session.RespondPermission(context.Background(), ProviderSessionRequest{RequestID: "permission-1", PermissionRequestID: "perm-1", PermissionDecision: "allow"}, nil); err != nil {
		t.Fatalf("permission: %v", err)
	}
	requests := transport.snapshot()
	if len(requests) != 3 || requests[0].Method != "claude/input" || requests[1].Method != "claude/interrupt" || requests[2].Method != "claude/permission/respond" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestPiProviderSessionMapsPromptAbortAndReplacement(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/prompt": {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-new"}`)},
		"pi/abort":  {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-old","status":"aborted"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1", ActiveTurnID: "turn-old"})
	result, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{RequestID: "replace-1", Message: "continue with evidence"}, nil)
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

func TestProviderSessionAdapterErrorsAreTypedAndCapabilitiesAreHonest(t *testing.T) {
	transport := &fakeProviderTransport{err: errors.New("wire unavailable")}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1"})
	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "send-1", Message: "hello"}, nil)
	var operationErr *ProviderSessionOperationError
	if !errors.As(err, &operationErr) || operationErr.Mode != ProviderSessionModeSendTurn {
		t.Fatalf("error = %v, want typed send error", err)
	}
	noSteer := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	if noSteer.Capabilities().InTurnSteer {
		t.Fatal("codex should not claim direct in-turn steer")
	}
	_, err = noSteer.SteerInTurn(context.Background(), ProviderSessionRequest{RequestID: "steer-1", Message: "hi"}, nil)
	var unsupported *UnsupportedProviderSessionCapabilityError
	if !errors.As(err, &unsupported) || unsupported.Capability != ProviderSessionCapabilityInTurnSteer {
		t.Fatalf("steer error = %v", err)
	}
}

func TestProviderSessionAdapterRejectTimeoutAndDuplicateAreTruthful(t *testing.T) {
	t.Run("provider rejection", func(t *testing.T) {
		transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
			"turn/start": {Error: json.RawMessage(`{"code":-32000,"message":"sensitive provider detail"}`)},
		}}
		session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1"})
		var events []task.EventPayload
		request := ProviderSessionRequest{RequestID: "send-rejected", Message: "secret prompt"}
		_, err := session.SendTurn(context.Background(), request, func(_ task.EventKind, payload task.EventPayload) {
			events = append(events, payload)
		})
		var operationErr *ProviderSessionOperationError
		if !errors.As(err, &operationErr) {
			t.Fatalf("error = %v, want typed operation error", err)
		}
		if len(events) != 2 || events[1]["outcome"] != "failed" {
			t.Fatalf("events = %#v", events)
		}
		if _, leaked := events[1]["message"]; leaked {
			t.Fatalf("failure event leaked prompt: %#v", events[1])
		}
		if _, retryErr := session.SendTurn(context.Background(), request, nil); !errors.As(retryErr, &operationErr) {
			t.Fatalf("cached rejection retry error = %v, want typed operation error", retryErr)
		}
		if len(transport.snapshot()) != 1 {
			t.Fatalf("cached provider rejection wrote a second request: %#v", transport.snapshot())
		}
	})

	t.Run("timeout", func(t *testing.T) {
		var attempts atomic.Int32
		transport := &fakeProviderTransport{send: func(ctx context.Context, _ SandboxBridgeRequest) (SandboxBridgeResponse, error) {
			if attempts.Add(1) == 1 {
				<-ctx.Done()
				return SandboxBridgeResponse{}, ctx.Err()
			}
			return SandboxBridgeResponse{Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-1","status":"interrupted"}`)}, nil
		}}
		session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1"})
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		request := ProviderSessionRequest{RequestID: "timeout-1", ProviderTurnID: "turn-1"}
		_, err := session.InterruptTurn(ctx, request, nil)
		var operationErr *ProviderSessionOperationError
		if !errors.As(err, &operationErr) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want typed deadline", err)
		}
		changed := request
		changed.Message = "changed after timeout"
		_, err = session.InterruptTurn(context.Background(), changed, nil)
		var conflict *ProviderSessionRequestConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("post-timeout payload drift error = %v, want request conflict", err)
		}
		result, err := session.InterruptTurn(context.Background(), request, nil)
		if err != nil {
			t.Fatalf("retry after local timeout: %v", err)
		}
		if result.Outcome != "settled" || attempts.Load() != 2 {
			t.Fatalf("retry result/attempts = %#v/%d", result, attempts.Load())
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
			"claude/input": {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1"}`)},
		}}
		session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1"})
		request := ProviderSessionRequest{RequestID: "same-request", Message: "only once"}
		first, err := session.SendTurn(context.Background(), request, nil)
		if err != nil {
			t.Fatal(err)
		}
		second, err := session.SendTurn(context.Background(), request, nil)
		if err != nil {
			t.Fatal(err)
		}
		if first != second || len(transport.snapshot()) != 1 {
			t.Fatalf("duplicate result/calls = %#v %#v %d", first, second, len(transport.snapshot()))
		}
	})
}

func TestProviderSessionAdapterCloseIsIdempotentAndTimeoutDoesNotLeakMessage(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// A transport that honors the canceled context is covered by the shared
	// operation wrapper; this assertion ensures Close remains a public control.
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("repeat close: %v", err)
	}
	if _, err := session.SendTurn(ctx, ProviderSessionRequest{RequestID: "late", Message: "secret"}, nil); !errors.Is(err, ErrProviderSessionClosed) {
		t.Fatalf("late send error = %v", err)
	}
}

func TestProviderSessionAdapterRejectsRequestPayloadDrift(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
			"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1"}}`)},
		}}
		session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1"})
		request := ProviderSessionRequest{RequestID: "same-request", Message: "inspect auth"}
		if _, err := session.SendTurn(context.Background(), request, nil); err != nil {
			t.Fatalf("first request: %v", err)
		}
		request.Message = "inspect billing"
		_, err := session.SendTurn(context.Background(), request, nil)
		var conflict *ProviderSessionRequestConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("payload drift error = %v, want request conflict", err)
		}
		if len(transport.snapshot()) != 1 {
			t.Fatalf("payload drift wrote a second native request: %#v", transport.snapshot())
		}
	})

	t.Run("permission decision", func(t *testing.T) {
		transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
			"claude/permission/respond": {Result: json.RawMessage(`{"session_id":"claude-1","permission_request_id":"perm-1","decision":"allow"}`)},
		}}
		session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1"})
		request := ProviderSessionRequest{RequestID: "same-permission", PermissionRequestID: "perm-1", PermissionDecision: "allow"}
		if _, err := session.RespondPermission(context.Background(), request, nil); err != nil {
			t.Fatalf("first permission response: %v", err)
		}
		request.PermissionDecision = "deny"
		_, err := session.RespondPermission(context.Background(), request, nil)
		var conflict *ProviderSessionRequestConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("permission drift error = %v, want request conflict", err)
		}
		if len(transport.snapshot()) != 1 {
			t.Fatalf("permission drift wrote a second native request: %#v", transport.snapshot())
		}
	})

	t.Run("in flight", func(t *testing.T) {
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		transport := &fakeProviderTransport{send: func(_ context.Context, _ SandboxBridgeRequest) (SandboxBridgeResponse, error) {
			started <- struct{}{}
			<-release
			return SandboxBridgeResponse{Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-1"}`)}, nil
		}}
		session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1"})
		firstDone := make(chan error, 1)
		go func() {
			_, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "same-request", Message: "inspect auth"}, nil)
			firstDone <- err
		}()
		<-started
		_, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "same-request", Message: "inspect billing"}, nil)
		var conflict *ProviderSessionRequestConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("in-flight payload drift error = %v, want request conflict", err)
		}
		close(release)
		if err := <-firstDone; err != nil {
			t.Fatalf("first in-flight request: %v", err)
		}
		if len(transport.snapshot()) != 1 {
			t.Fatalf("in-flight payload drift wrote a second native request: %#v", transport.snapshot())
		}
	})
}

func TestProviderSessionAdaptersParseProtocolNotificationsAsRedactedEvents(t *testing.T) {
	tests := []struct {
		name    string
		session interface {
			HandleEvent(SandboxBridgeEvent, ProviderSessionEmit)
		}
		event SandboxBridgeEvent
		want  string
	}{
		{
			name: "codex turn completed",
			session: NewCodexProviderSession(CodexProviderSessionConfig{
				Transport: &fakeProviderTransport{}, SessionID: "thread-1", ActiveTurnID: "turn-1",
			}),
			event: SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"},"message":"sensitive output"}`)},
			want:  "completed",
		},
		{
			name: "claude permission request",
			session: NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
				Transport: &fakeProviderTransport{}, SessionID: "claude-1", ActiveTurnID: "turn-1",
			}),
			event: SandboxBridgeEvent{Method: "claude/permission/requested", Params: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1","permission_request_id":"perm-1","tool_input":{"token":"secret"}}`)},
			want:  "requested",
		},
		{
			name: "pi abort settled",
			session: NewPiProviderSession(PiProviderSessionConfig{
				Transport: &fakeProviderTransport{}, SessionID: "pi-1", ActiveTurnID: "turn-1",
			}),
			event: SandboxBridgeEvent{Method: "pi/turn/aborted", Params: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-1","status":"aborted","text":"secret"}`)},
			want:  "settled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []task.EventPayload
			tt.session.HandleEvent(tt.event, func(_ task.EventKind, payload task.EventPayload) {
				events = append(events, payload)
			})
			if len(events) != 1 || events[0]["outcome"] != tt.want {
				t.Fatalf("events = %#v", events)
			}
			if tt.name == "claude permission request" && events[0]["permission_request_id"] != "perm-1" {
				t.Fatalf("permission correlation = %#v", events[0])
			}
			for _, forbidden := range []string{"message", "text", "tool_input", "params", "raw"} {
				if _, leaked := events[0][forbidden]; leaked {
					t.Fatalf("event leaked %s: %#v", forbidden, events[0])
				}
			}
		})
	}
}

func TestProviderSessionAdapterUsesDaemonEventSinkForUnsolicitedPermission(t *testing.T) {
	var events []task.EventPayload
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: &fakeProviderTransport{}, SessionID: "pi-1", ActiveTurnID: "turn-1"})
	session.SetEventSink(func(_ task.EventKind, payload task.EventPayload) { events = append(events, payload) })
	session.HandleEvent(SandboxBridgeEvent{Method: "pi/permission/requested", Params: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-1","permission_request_id":"perm-2","tool_input":{"secret":"do-not-store"}}`)}, nil)
	if len(events) != 1 || events[0]["permission_request_id"] != "perm-2" || events[0]["outcome"] != "requested" {
		t.Fatalf("sink events = %#v", events)
	}
	if _, leaked := events[0]["tool_input"]; leaked {
		t.Fatalf("sink event leaked provider wire details: %#v", events[0])
	}
}
