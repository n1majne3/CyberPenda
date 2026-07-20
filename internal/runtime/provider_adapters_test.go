package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	mu            sync.Mutex
	calls         []providerTransportCall
	responses     map[string]SandboxBridgeResponse
	notifications map[string]SandboxBridgeEvent
	emitEvent     func(SandboxBridgeEvent)
	err           error
	send          func(context.Context, SandboxBridgeRequest) (SandboxBridgeResponse, error)
	closed        bool
}

func (t *fakeProviderTransport) Send(ctx context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
	t.mu.Lock()
	t.calls = append(t.calls, providerTransportCall{request: request})
	send := t.send
	notification, hasNotification := t.notifications[request.Method]
	emitEvent := t.emitEvent
	if send != nil {
		t.mu.Unlock()
		response, err := send(ctx, request)
		if err == nil && hasNotification && emitEvent != nil {
			emitEvent(notification)
		}
		return response, err
	}
	if t.err != nil {
		t.mu.Unlock()
		return SandboxBridgeResponse{}, t.err
	}
	if response, ok := t.responses[request.Method]; ok {
		response.ID = request.ID
		t.mu.Unlock()
		if hasNotification && emitEvent != nil {
			emitEvent(notification)
		}
		return response, nil
	}
	t.mu.Unlock()
	if hasNotification && emitEvent != nil {
		emitEvent(notification)
	}
	return SandboxBridgeResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"ok":true}`)}, nil
}

func bindFakeProviderEvents(transport *fakeProviderTransport, session ProviderSessionEventHandler) {
	transport.mu.Lock()
	transport.emitEvent = func(event SandboxBridgeEvent) { session.HandleEvent(event, nil) }
	transport.mu.Unlock()
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
		"turn/interrupt": {Result: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-2"}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"turn/interrupt": {Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2","status":"interrupted"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1"})
	bindFakeProviderEvents(transport, session)
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

func TestCodexProviderSessionMapsModelAndRequestedReasoningEffortOnTurnStart(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-effort"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1"})
	bindFakeProviderEvents(transport, session)

	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "send-effort",
		Message:                  "inspect the target",
		ModelProviderID:          "primary",
		Model:                    "gpt-test",
		RequestedReasoningEffort: "xhigh",
	}, nil)
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}
	requests := transport.snapshot()
	if len(requests) != 1 || requests[0].Method != "turn/start" {
		t.Fatalf("wire requests = %#v", requests)
	}
	var startParams map[string]any
	if err := json.Unmarshal(requests[0].Params, &startParams); err != nil {
		t.Fatal(err)
	}
	if startParams["threadId"] != "thread-1" {
		t.Fatalf("threadId = %#v, want thread-1 (same Codex thread)", startParams["threadId"])
	}
	if startParams["model"] != "gpt-test" {
		t.Fatalf("model param = %#v, want gpt-test", startParams["model"])
	}
	if startParams["effort"] != "xhigh" {
		t.Fatalf("effort param = %#v, want xhigh", startParams["effort"])
	}
}

func TestCodexProviderSessionInterruptThenReplaceMapsModelAndEffortOnSameThread(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/interrupt": {Result: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-old"}`)},
		"turn/start":     {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-new"}}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"turn/interrupt": {Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-old","status":"interrupted"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-old",
	})
	bindFakeProviderEvents(transport, session)

	result, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{
		RequestID:                "replace-effort",
		Message:                  "switch model mid-task",
		ModelProviderID:          "primary",
		Model:                    "gpt-strong",
		RequestedReasoningEffort: "max",
	}, nil)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if result.SessionID != "thread-1" {
		t.Fatalf("session moved to %q; want same thread", result.SessionID)
	}
	requests := transport.snapshot()
	if len(requests) != 2 || requests[0].Method != "turn/interrupt" || requests[1].Method != "turn/start" {
		t.Fatalf("requests = %#v", requests)
	}
	var startParams map[string]any
	if err := json.Unmarshal(requests[1].Params, &startParams); err != nil {
		t.Fatal(err)
	}
	if startParams["threadId"] != "thread-1" {
		t.Fatalf("replacement threadId = %#v", startParams["threadId"])
	}
	if startParams["model"] != "gpt-strong" || startParams["effort"] != "max" {
		t.Fatalf("replacement selection = %#v", startParams)
	}
}

func TestCodexProviderSessionSurfacesUnsupportedEffortWithoutRewriting(t *testing.T) {
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		if request.Method != "turn/start" {
			return SandboxBridgeResponse{}, errors.New("unexpected method")
		}
		var params map[string]any
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params["effort"] != "max" {
			t.Fatalf("effort rewritten before provider saw it: %#v", params["effort"])
		}
		return SandboxBridgeResponse{}, errors.New("unsupported reasoning effort: max")
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1"})
	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "effort-reject",
		Message:                  "try max",
		Model:                    "gpt-test",
		RequestedReasoningEffort: "max",
	}, nil)
	if err == nil {
		t.Fatal("expected provider effort rejection")
	}
	var opErr *ProviderSessionOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error type = %T (%v), want ProviderSessionOperationError", err, err)
	}
	if !strings.Contains(opErr.Cause.Error(), "unsupported reasoning effort") {
		t.Fatalf("cause = %v", opErr.Cause)
	}
}

func TestCodexProviderSessionInterruptThenReplaceUsesSameThread(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/interrupt": {Result: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-old"}`)},
		"turn/start":     {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-new"}}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"turn/interrupt": {Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-old","status":"interrupted"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-old"})
	bindFakeProviderEvents(transport, session)
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

func TestProviderSessionInterruptThenReplaceWaitsForMatchingTerminalNotification(t *testing.T) {
	interruptAcknowledged := make(chan struct{})
	releaseInterruptResponse := make(chan struct{})
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		switch request.Method {
		case "turn/interrupt":
			close(interruptAcknowledged)
			<-releaseInterruptResponse
			return SandboxBridgeResponse{Result: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-old"}`)}, nil
		case "turn/start":
			return SandboxBridgeResponse{Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-new"}}`)}, nil
		default:
			return SandboxBridgeResponse{}, errors.New("unexpected method")
		}
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-old"})
	done := make(chan error, 1)
	go func() {
		_, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{RequestID: "replace-wait", Message: "new direction"}, nil)
		done <- err
	}()
	<-interruptAcknowledged

	// A terminal event may race ahead of the interrupt response. Events for a
	// different provider session or turn must not release the replacement.
	session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-other","turn":{"id":"turn-old","status":"interrupted"}}`)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-other","status":"interrupted"}}`)}, nil)
	close(releaseInterruptResponse)
	select {
	case err := <-done:
		t.Fatalf("replacement completed before matching settlement: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if requests := transport.snapshot(); len(requests) != 1 || requests[0].Method != "turn/interrupt" {
		t.Fatalf("replacement started before matching settlement: %#v", requests)
	}

	session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-old","status":"interrupted"}}`)}, nil)
	if err := <-done; err != nil {
		t.Fatalf("replacement after settlement: %v", err)
	}
	if requests := transport.snapshot(); len(requests) != 2 || requests[1].Method != "turn/start" {
		t.Fatalf("replacement requests = %#v", requests)
	}
}

func TestClaudeProviderSessionMapsInputInterruptAndPermission(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/input":              {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1"}`)},
		"claude/interrupt":          {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1"}`)},
		"claude/permission/respond": {Result: json.RawMessage(`{"session_id":"claude-1","permission_request_id":"perm-1","decision":"allow"}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"claude/interrupt": {Method: "claude/turn/completed", Params: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1","status":"interrupted"}`)},
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1", ActiveTurnID: "turn-1"})
	bindFakeProviderEvents(transport, session)
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

// #146: Claude Code maps the complete Runtime Turn Selection onto claude/input
// so the long-lived Query can apply model and Requested Reasoning Effort before
// the turn without recreating the session.
func TestClaudeProviderSessionMapsModelProviderModelAndEffortOnInput(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/input": {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-effort"}`)},
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1"})
	bindFakeProviderEvents(transport, session)

	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "send-effort",
		Message:                  "inspect auth",
		ModelProviderID:          "anthropic-primary",
		Model:                    "claude-opus-strong",
		RequestedReasoningEffort: "xhigh",
	}, nil)
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}
	requests := transport.snapshot()
	if len(requests) != 1 || requests[0].Method != "claude/input" {
		t.Fatalf("wire requests = %#v", requests)
	}
	var params map[string]any
	if err := json.Unmarshal(requests[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params["session_id"] != "claude-1" {
		t.Fatalf("session_id = %#v, want claude-1 (same Query session)", params["session_id"])
	}
	if params["message"] != "inspect auth" {
		t.Fatalf("message = %#v", params["message"])
	}
	if params["model_provider_id"] != "anthropic-primary" {
		t.Fatalf("model_provider_id = %#v, want anthropic-primary", params["model_provider_id"])
	}
	if params["model"] != "claude-opus-strong" {
		t.Fatalf("model = %#v, want claude-opus-strong", params["model"])
	}
	if params["requested_reasoning_effort"] != "xhigh" {
		t.Fatalf("requested_reasoning_effort = %#v, want xhigh", params["requested_reasoning_effort"])
	}
	// Effective effort is never inferred onto the wire from the request.
	if _, ok := params["effective_reasoning_effort"]; ok {
		t.Fatalf("must not send effective_reasoning_effort: %#v", params)
	}
}

func TestClaudeProviderSessionInterruptThenReplaceMapsSelectionOnSameSession(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/interrupt": {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-old"}`)},
		"claude/input":     {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-new"}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"claude/interrupt": {Method: "claude/turn/completed", Params: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-old","status":"interrupted"}`)},
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
		Transport: transport, SessionID: "claude-1", ActiveTurnID: "turn-old",
	})
	bindFakeProviderEvents(transport, session)

	result, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{
		RequestID:                "replace-effort",
		Message:                  "switch model mid-task",
		ModelProviderID:          "anthropic-primary",
		Model:                    "claude-sonnet-fast",
		RequestedReasoningEffort: "max",
	}, nil)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if result.SessionID != "claude-1" {
		t.Fatalf("session moved to %q; want same Query session", result.SessionID)
	}
	requests := transport.snapshot()
	if len(requests) != 2 || requests[0].Method != "claude/interrupt" || requests[1].Method != "claude/input" {
		t.Fatalf("requests = %#v", requests)
	}
	var params map[string]any
	if err := json.Unmarshal(requests[1].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params["session_id"] != "claude-1" {
		t.Fatalf("replacement session_id = %#v", params["session_id"])
	}
	if params["model_provider_id"] != "anthropic-primary" || params["model"] != "claude-sonnet-fast" || params["requested_reasoning_effort"] != "max" {
		t.Fatalf("replacement selection = %#v", params)
	}
}

func TestClaudeProviderSessionSurfacesUnsupportedEffortWithoutRewriting(t *testing.T) {
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		if request.Method != "claude/input" {
			return SandboxBridgeResponse{}, errors.New("unexpected method")
		}
		var params map[string]any
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params["requested_reasoning_effort"] != "max" {
			t.Fatalf("effort rewritten before provider saw it: %#v", params["requested_reasoning_effort"])
		}
		if params["model"] != "claude-haiku" {
			t.Fatalf("model rewritten: %#v", params["model"])
		}
		return SandboxBridgeResponse{}, errors.New("unsupported reasoning effort: max")
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1"})
	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "effort-reject",
		Message:                  "try max",
		ModelProviderID:          "anthropic-primary",
		Model:                    "claude-haiku",
		RequestedReasoningEffort: "max",
	}, nil)
	if err == nil {
		t.Fatal("expected provider effort rejection")
	}
	var opErr *ProviderSessionOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error type = %T (%v), want ProviderSessionOperationError", err, err)
	}
	if !strings.Contains(opErr.Cause.Error(), "unsupported reasoning effort") {
		t.Fatalf("cause = %v", opErr.Cause)
	}
}

func TestPiProviderSessionMapsPromptAbortAndReplacement(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/prompt": {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-new"}`)},
		"pi/abort":  {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-old"}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"pi/abort": {Method: "pi/turn/aborted", Params: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-old","status":"aborted"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1", ActiveTurnID: "turn-old"})
	bindFakeProviderEvents(transport, session)
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
		transport := &fakeProviderTransport{notifications: map[string]SandboxBridgeEvent{
			"pi/abort": {Method: "pi/turn/aborted", Params: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-1","status":"aborted"}`)},
		}, send: func(ctx context.Context, _ SandboxBridgeRequest) (SandboxBridgeResponse, error) {
			if attempts.Add(1) == 1 {
				<-ctx.Done()
				return SandboxBridgeResponse{}, ctx.Err()
			}
			return SandboxBridgeResponse{Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-1"}`)}, nil
		}}
		session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1"})
		bindFakeProviderEvents(transport, session)
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

func TestClaudeProviderSessionProjectsVisibleRuntimeOutput(t *testing.T) {
	var kinds []task.EventKind
	var events []task.EventPayload
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "claude-1", ActiveTurnID: "turn-1",
	})
	session.HandleEvent(SandboxBridgeEvent{
		Method: "claude/runtime_output",
		Params: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-1","stream":"assistant","text":"{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ready\"}]}}"}`),
	}, func(kind task.EventKind, payload task.EventPayload) {
		kinds = append(kinds, kind)
		events = append(events, payload)
	})
	if len(events) != 1 || len(kinds) != 1 || kinds[0] != task.EventKindRuntimeOutput {
		t.Fatalf("events = %#v kinds = %#v", events, kinds)
	}
	if events[0]["stream"] != "assistant" || events[0]["text"] == "" {
		t.Fatalf("runtime output = %#v", events[0])
	}
	if _, leaked := events[0]["params"]; leaked {
		t.Fatalf("runtime output leaked protocol params: %#v", events[0])
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
