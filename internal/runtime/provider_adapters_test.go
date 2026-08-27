package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

func TestCodexProviderSessionSteersTheExpectedActiveTurn(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/steer": {Result: json.RawMessage(`{"turnId":"turn-live"}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-live",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true,
			InterruptThenReplace: true, InTurnSteer: true,
		},
	})

	result, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "steer-1",
		Message:                  "focus on the auth path",
		ProviderTurnID:           "turn-live",
		Model:                    "must-not-be-sent",
		RequestedReasoningEffort: "must-not-be-sent",
	}, nil)
	if err != nil {
		t.Fatalf("steer active turn: %v", err)
	}
	if result.Mode != ProviderSessionModeInTurnSteer || result.ProviderTurnID != "turn-live" || result.Outcome != "acknowledged" {
		t.Fatalf("result = %#v", result)
	}
	requests := transport.snapshot()
	if len(requests) != 1 || requests[0].Method != "turn/steer" {
		t.Fatalf("wire requests = %#v", requests)
	}
	var params map[string]any
	if err := json.Unmarshal(requests[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params["threadId"] != "thread-1" || params["expectedTurnId"] != "turn-live" || params["clientUserMessageId"] != "steer-1" {
		t.Fatalf("steer params = %#v", params)
	}
	input, ok := params["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("steer input = %#v", params["input"])
	}
	item, ok := input[0].(map[string]any)
	if !ok || item["type"] != "text" || item["text"] != "focus on the auth path" {
		t.Fatalf("steer input item = %#v", input[0])
	}
	for _, forbidden := range []string{"turnId", "model", "effort", "approvalPolicy", "sandboxPolicy", "cwd"} {
		if _, exists := params[forbidden]; exists {
			t.Fatalf("turn/steer sent forbidden %q in %#v", forbidden, params)
		}
	}
	if !session.TurnBusy() {
		t.Fatal("same-turn steer cleared active Runtime Turn")
	}
}

func TestCodexProviderSessionRejectsMissingExpectedTurnFence(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/steer": {Result: json.RawMessage(`{"turnId":"turn-live"}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-live",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	_, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{RequestID: "steer-no-fence", Message: "focus"}, nil)
	if !errors.Is(err, ErrInvalidProviderSessionRequest) {
		t.Fatalf("steer error = %v, want invalid request", err)
	}
	if requests := transport.snapshot(); len(requests) != 0 {
		t.Fatalf("missing fence reached provider transport: %#v", requests)
	}
}

func TestCodexProviderSessionMapsStructuredNonSteerableError(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/steer": {Error: json.RawMessage(`{"code":-32000,"message":"provider detail must stay private","data":{"reason":"activeTurnNotSteerable","turnKind":"review"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-review",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true},
	})
	_, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{
		RequestID: "steer-review", Message: "focus", ProviderTurnID: "turn-review",
	}, nil)
	if !errors.Is(err, ErrProviderTurnNotSteerable) {
		t.Fatalf("steer error = %v, want active Turn not steerable", err)
	}
	if requests := transport.snapshot(); len(requests) != 1 || requests[0].Method != "turn/steer" {
		t.Fatalf("requests = %#v, want only turn/steer", requests)
	}
	if strings.Contains(err.Error(), "provider detail") {
		t.Fatalf("typed error leaked provider message: %v", err)
	}
}

func TestCodexProviderSessionFallsBackAfterSteerMethodNotFound(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/steer":     {Error: json.RawMessage(`{"code":-32601,"message":"Method not found"}`)},
		"turn/interrupt": {Result: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-live"}`)},
		"turn/start":     {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-replacement"}}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"turn/interrupt": {Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-live","status":"interrupted"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-live",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true},
	})
	bindFakeProviderEvents(transport, session)
	var emits []task.EventPayload
	result, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{
		RequestID: "steer-old-server", Message: "focus", ProviderTurnID: "turn-live", TurnKind: RuntimeTurnKindControl,
	}, func(_ task.EventKind, payload task.EventPayload) { emits = append(emits, payload) })
	if err != nil {
		t.Fatalf("steer fallback: %v", err)
	}
	if result.Mode != ProviderSessionModeInterruptThenReplace || result.RequestID != "steer-old-server" || result.ProviderTurnID != "turn-replacement" {
		t.Fatalf("fallback result = %#v", result)
	}
	if kind, ok := session.ResolveProviderSessionTurnKind("steer-old-server", "turn-replacement"); !ok || kind != RuntimeTurnKindWork {
		t.Fatalf("fallback replacement Turn kind = %q, ok=%v, want work", kind, ok)
	}
	if session.Capabilities().InTurnSteer {
		t.Fatal("turn/steer capability remained enabled after method-not-found")
	}
	requests := transport.snapshot()
	if len(requests) != 3 || requests[0].Method != "turn/steer" || requests[1].Method != "turn/interrupt" || requests[2].Method != "turn/start" {
		t.Fatalf("fallback requests = %#v", requests)
	}
	for _, event := range emits {
		if event["outcome"] == "failed" {
			t.Fatalf("safe fallback projected a failed event: %#v", emits)
		}
		if event["request_id"] != "steer-old-server" {
			t.Fatalf("fallback event lost public request identity: %#v", event)
		}
	}
	replayed, replayErr := session.SteerInTurn(context.Background(), ProviderSessionRequest{
		RequestID: "steer-old-server", Message: "focus", ProviderTurnID: "turn-live", TurnKind: RuntimeTurnKindControl,
	}, nil)
	if replayErr != nil || replayed.Mode != ProviderSessionModeInterruptThenReplace || replayed.ProviderTurnID != "turn-replacement" {
		t.Fatalf("fallback replay = %#v, err=%v", replayed, replayErr)
	}
	if got := len(transport.snapshot()); got != 3 {
		t.Fatalf("fallback replay sent %d wire requests, want 3 total", got)
	}
}

func TestCodexProviderSessionSteerResponseDoesNotResurrectCompletedTurn(t *testing.T) {
	var session *CodexProviderSession
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		if request.Method != "turn/steer" {
			return SandboxBridgeResponse{}, errors.New("unexpected method " + request.Method)
		}
		session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-live","status":"completed"}}`)}, nil)
		return SandboxBridgeResponse{Result: json.RawMessage(`{"turnId":"turn-live"}`)}, nil
	}}
	session = NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-live",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true,
			InterruptThenReplace: true, InTurnSteer: true,
		},
	})

	if _, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{
		RequestID: "steer-race", ProviderTurnID: "turn-live", Message: "one more check",
	}, nil); err != nil {
		t.Fatalf("steer active turn: %v", err)
	}
	if session.TurnBusy() {
		t.Fatal("turn/steer response resurrected a provider Turn completed before the response")
	}
}

func TestCodexProviderSessionRejectsSteerWithoutTheExpectedActiveTurn(t *testing.T) {
	tests := []struct {
		name       string
		activeTurn string
		expected   string
		wantErr    error
	}{
		{name: "no active turn", expected: "turn-finished", wantErr: ErrProviderTurnUnavailable},
		{name: "target changed", activeTurn: "turn-new", expected: "turn-old", wantErr: ErrProviderTurnChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeProviderTransport{}
			session := NewCodexProviderSession(CodexProviderSessionConfig{
				Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: test.activeTurn,
				Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
			})
			_, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{
				RequestID: "steer-target", ProviderTurnID: test.expected, Message: "continue",
			}, nil)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if calls := transport.snapshot(); len(calls) != 0 {
				t.Fatalf("invalid target sent provider request: %#v", calls)
			}
		})
	}
}

func TestCodexProviderSessionSteerPreservesTheActiveWorkTurnLineage(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-live"}}`)},
		"turn/steer": {Result: json.RawMessage(`{"turnId":"turn-live"}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true,
			InterruptThenReplace: true, InTurnSteer: true,
		},
	})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: "work-1", Message: "inspect", TurnKind: RuntimeTurnKindWork,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{
		RequestID: "steer-control", ProviderTurnID: "turn-live", Message: "focus", TurnKind: RuntimeTurnKindControl,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if kind, ok := session.ResolveProviderSessionTurnKind("", "turn-live"); !ok || kind != RuntimeTurnKindWork {
		t.Fatalf("active provider Turn kind = %q, ok=%v", kind, ok)
	}
	lineage, ok := session.ResolveProviderSessionTurnLineage("", "turn-live")
	if !ok || lineage.RequestID != "work-1" || lineage.ProviderTurnID != "turn-live" {
		t.Fatalf("active provider Turn lineage = %#v, ok=%v", lineage, ok)
	}
}

func TestCodexProviderSessionRejectsSteerResponseForAnotherTurn(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/steer": {Result: json.RawMessage(`{"turnId":"turn-other"}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: transport, SessionID: "thread-1", ThreadID: "thread-1", ActiveTurnID: "turn-live",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InTurnSteer: true},
	})
	_, err := session.SteerInTurn(context.Background(), ProviderSessionRequest{
		RequestID: "steer-response-mismatch", ProviderTurnID: "turn-live", Message: "continue",
	}, nil)
	if !errors.Is(err, ErrProviderTurnChanged) {
		t.Fatalf("error = %v, want provider Turn changed", err)
	}
	if state := session.TurnState(); state.ActiveTurnID != "turn-live" {
		t.Fatalf("mismatched response changed active Turn: %#v", state)
	}
}

func TestProviderSessionTurnBusyTracksActiveRuntimeTurn(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1"})

	result, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "send-busy", Message: "inspect"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session.ControlBusy() {
		t.Fatal("ControlBusy remained true after turn/start returned")
	}
	if !session.TurnBusy() {
		t.Fatal("TurnBusy = false while provider Runtime Turn is active")
	}
	if state := session.TurnState(); state.SessionID != "thread-1" || state.ActiveTurnID != "turn-1" || !state.TurnBusy() {
		t.Fatalf("active Turn state = %#v", state)
	}

	session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`)}, nil)
	if session.TurnBusy() {
		t.Fatalf("TurnBusy = true after matching terminal event for %q", result.ProviderTurnID)
	}
	if state := session.TurnState(); state.ActiveTurnID != "" || state.TurnBusy() {
		t.Fatalf("completed Turn state = %#v", state)
	}
}

func TestProviderSessionTurnBusyConsumesTerminalNotificationBeforeTurnStartResponse(t *testing.T) {
	var session *CodexProviderSession
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		if request.Method != "turn/start" {
			return SandboxBridgeResponse{}, errors.New("unexpected method " + request.Method)
		}
		session.HandleEvent(SandboxBridgeEvent{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-fast","status":"completed"}}`)}, nil)
		return SandboxBridgeResponse{Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-fast"}}`)}, nil
	}}
	session = NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1"})

	result, err := session.SendTurn(context.Background(), ProviderSessionRequest{RequestID: "send-fast", Message: "finish quickly"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderTurnID != "turn-fast" {
		t.Fatalf("provider turn = %q, want turn-fast", result.ProviderTurnID)
	}
	if session.TurnBusy() {
		t.Fatal("TurnBusy = true after terminal notification raced before turn/start response")
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

func TestCodexProviderSessionAppliesNonInteractiveSandboxOnTurnStart(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-sandbox"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1", ThreadID: "thread-1"})
	bindFakeProviderEvents(transport, session)

	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: "send-sandbox", Message: "list challenges",
	}, nil); err != nil {
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
	if startParams["approvalPolicy"] != "never" {
		t.Fatalf("approvalPolicy = %#v", startParams["approvalPolicy"])
	}
	policy, _ := startParams["sandboxPolicy"].(map[string]any)
	if policy["type"] != "dangerFullAccess" {
		t.Fatalf("sandboxPolicy = %#v", startParams["sandboxPolicy"])
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
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1", ActiveTurnID: "turn-live"})
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
	if params["turn_kind"] != "work" {
		t.Fatalf("turn_kind = %#v, want work", params["turn_kind"])
	}
	// Effective effort is never inferred onto the wire from the request.
	if _, ok := params["effective_reasoning_effort"]; ok {
		t.Fatalf("must not send effective_reasoning_effort: %#v", params)
	}
}

func TestClaudeProviderSessionMapsHarnessControlTurnKindOnInput(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/input": {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-control"}`)},
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1"})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: "conclude-1", Message: "return conclusion", TurnKind: RuntimeTurnKindControl,
	}, nil); err != nil {
		t.Fatal(err)
	}
	requests := transport.snapshot()
	var params map[string]any
	if len(requests) != 1 || json.Unmarshal(requests[0].Params, &params) != nil || params["turn_kind"] != "control" {
		t.Fatalf("control wire params = %#v", requests)
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

// Idle Claude sessions (no active turn) must not call claude/interrupt.
// After a completed Work or Conclude turn the SDK bridge has activeTurnID="",
// and interrupt_then_replace should degrade to a plain send on the same Query.
func TestClaudeProviderSessionInterruptThenReplaceWhenIdleSkipsInterrupt(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/input": {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-followup"}`)},
	}, send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		if request.Method == "claude/interrupt" {
			return SandboxBridgeResponse{}, errors.New("Claude active turn identity mismatch")
		}
		if request.Method == "claude/input" {
			return SandboxBridgeResponse{Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"turn-followup"}`)}, nil
		}
		return SandboxBridgeResponse{}, errors.New("unexpected method " + request.Method)
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
		Transport: transport, SessionID: "claude-1", // no ActiveTurnID → idle
	})

	result, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{
		RequestID: "idle-followup",
		Message:   "continue after conclusion",
	}, nil)
	if err != nil {
		t.Fatalf("idle replace: %v", err)
	}
	if result.SessionID != "claude-1" || result.ProviderTurnID != "turn-followup" || result.Outcome != "started" {
		t.Fatalf("result = %#v", result)
	}
	requests := transport.snapshot()
	if len(requests) != 1 || requests[0].Method != "claude/input" {
		t.Fatalf("idle replace must send only; requests=%#v", requests)
	}
}

// Completing a turn clears the active turn so a later operator message on a
// live·idle Runtime uses the idle path instead of interrupting a finished turn.
func TestClaudeProviderSessionClearsActiveTurnOnCompletionThenIdleReplace(t *testing.T) {
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		switch request.Method {
		case "claude/input":
			var params map[string]any
			_ = json.Unmarshal(request.Params, &params)
			// First send is the work turn; second is the idle follow-up.
			if strings.Contains(request.ID, "work") || strings.HasSuffix(request.ID, "work-1") {
				return SandboxBridgeResponse{Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"work-1"}`)}, nil
			}
			return SandboxBridgeResponse{Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"follow-1"}`)}, nil
		case "claude/interrupt":
			return SandboxBridgeResponse{}, errors.New("Claude active turn identity mismatch")
		default:
			return SandboxBridgeResponse{}, errors.New("unexpected method " + request.Method)
		}
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
		Transport: transport, SessionID: "claude-1",
	})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: "work-1", Message: "do work",
	}, nil); err != nil {
		t.Fatalf("work turn: %v", err)
	}
	session.HandleEvent(SandboxBridgeEvent{
		Method: "claude/turn/completed",
		Params: json.RawMessage(`{"request_id":"work-1","session_id":"claude-1","turn_id":"work-1","status":"completed"}`),
	}, nil)

	result, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{
		RequestID: "follow-1", Message: "next phase",
	}, nil)
	if err != nil {
		t.Fatalf("post-completion replace: %v", err)
	}
	if result.ProviderTurnID != "follow-1" {
		t.Fatalf("result = %#v", result)
	}
	var interruptCalls int
	for _, req := range transport.snapshot() {
		if req.Method == "claude/interrupt" {
			interruptCalls++
		}
	}
	if interruptCalls != 0 {
		t.Fatalf("completed turn left interrupt path active; interrupts=%d requests=%#v", interruptCalls, transport.snapshot())
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

func TestClaudeProviderSessionEmitsAssistedObservationsAndValidatedControlResult(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"claude/input": {Result: json.RawMessage(`{"session_id":"claude-1","turn_id":"control-1"}`)},
	}}
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{
		Transport: transport, SessionID: "claude-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true,
			InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true,
			AssistedConclusion: true,
		},
	})
	var observations []ProviderSessionObservation
	session.SetObservationSink(func(observation ProviderSessionObservation) {
		observations = append(observations, observation)
	})
	var results []ProviderSessionAttemptResult
	session.SetAttemptResultSink(func(result ProviderSessionAttemptResult) { results = append(results, result) })

	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: "control-1", Message: "conclude", TurnKind: RuntimeTurnKindControl,
		ModelProviderID: "anthropic-primary", Model: "claude-sonnet", RequestedReasoningEffort: "high",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	session.HandleEvent(SandboxBridgeEvent{Method: "claude/tool/used", Params: json.RawMessage(
		`{"request_id":"control-1","session_id":"claude-1","turn_id":"control-1","tool_call_id":"tool-1","tool_name":"Read","input":{"secret":"never-store"}}`,
	)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "claude/tool/result", Params: json.RawMessage(
		`{"request_id":"control-1","session_id":"claude-1","turn_id":"control-1","tool_call_id":"tool-1","tool_name":"Read","status":"succeeded","content":"never-store"}`,
	)}, nil)
	raw := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:claude","create":true,"summary":"No reusable result","outcome":"failed"},"tested_targets":[{"key":"objective:auth","create_objective":{"objective":"Inspect authentication"}}],"produced_targets":[]}`
	session.HandleEvent(SandboxBridgeEvent{Method: "claude/attempt_result", Params: json.RawMessage(
		`{"request_id":"control-1","session_id":"claude-1","turn_id":"control-1","result":` + strconv.Quote(raw) + `}`,
	)}, nil)
	session.HandleEvent(SandboxBridgeEvent{Method: "claude/turn/completed", Params: json.RawMessage(
		`{"request_id":"control-1","session_id":"claude-1","turn_id":"control-1","status":"completed"}`,
	)}, nil)

	wantKinds := []ProviderSessionObservationKind{
		ProviderSessionObservationToolUse,
		ProviderSessionObservationToolResult,
		ProviderSessionObservationTurnCompleted,
	}
	if len(observations) != len(wantKinds) {
		t.Fatalf("observations = %#v", observations)
	}
	for index, wantKind := range wantKinds {
		if observations[index].Kind != wantKind || observations[index].RequestID != "control-1" ||
			observations[index].SessionID != "claude-1" || observations[index].ProviderTurnID != "control-1" {
			t.Fatalf("observation %d = %#v", index, observations[index])
		}
	}
	if len(results) != 1 || results[0].RequestID != "control-1" || results[0].SessionID != "claude-1" ||
		results[0].ProviderTurnID != "control-1" || results[0].Validated.Result.Attempt.Key != "attempt:claude" {
		t.Fatalf("results = %#v", results)
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

func TestHermesProviderSessionAppliesSetModelBeforePrompt(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"session/set_model": {Result: json.RawMessage(`{}`)},
		"session/prompt":    {Result: json.RawMessage(`{"sessionId":"hermes-1","turn_id":"turn-2"}`)},
	}}
	session := NewHermesProviderSession(HermesProviderSessionConfig{Transport: transport, SessionID: "hermes-1"})
	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:       "send-selection",
		Message:         "hi",
		ModelProviderID: "hub",
		Model:           "deepseek-v4-flash-free",
	}, nil)
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}
	requests := transport.snapshot()
	if len(requests) < 2 {
		t.Fatalf("wire requests = %#v", requests)
	}
	if requests[0].Method != "session/set_model" || requests[1].Method != "session/prompt" {
		t.Fatalf("order = %q, %q", requests[0].Method, requests[1].Method)
	}
	var setModel map[string]any
	if err := json.Unmarshal(requests[0].Params, &setModel); err != nil {
		t.Fatal(err)
	}
	if setModel["sessionId"] != "hermes-1" || setModel["modelId"] != "custom:hub:deepseek-v4-flash-free" {
		t.Fatalf("set_model params = %#v", setModel)
	}
}

func TestHermesProviderSessionProjectsRequestedReasoningEffort(t *testing.T) {
	home := t.TempDir()
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"session/set_model": {Result: json.RawMessage(`{}`)},
		"session/prompt":    {Result: json.RawMessage(`{"sessionId":"hermes-1","turn_id":"turn-2"}`)},
	}}
	session := NewHermesProviderSession(HermesProviderSessionConfig{
		Transport: transport, SessionID: "hermes-1", HermesHome: home,
	})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "send-effort",
		Message:                  "hi",
		ModelProviderID:          "hub",
		Model:                    "deepseek-v4-flash-free",
		RequestedReasoningEffort: "max",
	}, nil); err != nil {
		t.Fatalf("send turn: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "cyberpenda-requested-reasoning-effort"))
	if err != nil {
		t.Fatalf("read projected Reasoning Effort: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "max" {
		t.Fatalf("projected Reasoning Effort = %q, want max", raw)
	}
}

// TestPiProviderSessionAppliesModelThenEffortThenPrompt proves Pi native
// controls are issued in mandatory order: set_model → set_thinking_level → prompt.
func TestPiProviderSessionAppliesModelThenEffortThenPrompt(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/set_model":          {Result: json.RawMessage(`{"ok":true}`)},
		"pi/set_thinking_level": {Result: json.RawMessage(`{"ok":true}`)},
		"pi/prompt":             {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-2"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1"})
	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "send-selection",
		Message:                  "inspect with alternate",
		ModelProviderID:          "alternate",
		Model:                    "claude-alt",
		RequestedReasoningEffort: "xhigh",
	}, nil)
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}
	requests := transport.snapshot()
	if len(requests) != 3 {
		t.Fatalf("wire requests = %#v", requests)
	}
	if requests[0].Method != "pi/set_model" || requests[1].Method != "pi/set_thinking_level" || requests[2].Method != "pi/prompt" {
		t.Fatalf("order = %q, %q, %q", requests[0].Method, requests[1].Method, requests[2].Method)
	}
	var setModel map[string]any
	if err := json.Unmarshal(requests[0].Params, &setModel); err != nil {
		t.Fatal(err)
	}
	if setModel["provider"] != "alternate" || setModel["modelId"] != "claude-alt" {
		t.Fatalf("set_model params = %#v", setModel)
	}
	var setThinking map[string]any
	if err := json.Unmarshal(requests[1].Params, &setThinking); err != nil {
		t.Fatal(err)
	}
	if setThinking["level"] != "xhigh" {
		t.Fatalf("thinking level rewritten or missing: %#v", setThinking)
	}
	var prompt map[string]any
	if err := json.Unmarshal(requests[2].Params, &prompt); err != nil {
		t.Fatal(err)
	}
	if prompt["message"] != "inspect with alternate" {
		t.Fatalf("prompt params = %#v", prompt)
	}
}

func TestPiProviderSessionInterruptThenReplaceAppliesSelectionBeforePrompt(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/abort":              {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-old"}`)},
		"pi/set_model":          {Result: json.RawMessage(`{"ok":true}`)},
		"pi/set_thinking_level": {Result: json.RawMessage(`{"ok":true}`)},
		"pi/prompt":             {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-new"}`)},
	}, notifications: map[string]SandboxBridgeEvent{
		"pi/abort": {Method: "pi/turn/aborted", Params: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-old","status":"aborted"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1", ActiveTurnID: "turn-old"})
	bindFakeProviderEvents(transport, session)
	_, err := session.InterruptThenReplace(context.Background(), ProviderSessionRequest{
		RequestID:                "replace-selection",
		Message:                  "switch mid-task",
		ModelProviderID:          "primary",
		Model:                    "gpt-strong",
		RequestedReasoningEffort: "max",
	}, nil)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	requests := transport.snapshot()
	if len(requests) != 4 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].Method != "pi/abort" ||
		requests[1].Method != "pi/set_model" ||
		requests[2].Method != "pi/set_thinking_level" ||
		requests[3].Method != "pi/prompt" {
		t.Fatalf("order = %#v", []string{requests[0].Method, requests[1].Method, requests[2].Method, requests[3].Method})
	}
}

func TestPiProviderSessionSurfacesUnsupportedEffortWithoutRewriting(t *testing.T) {
	transport := &fakeProviderTransport{send: func(_ context.Context, request SandboxBridgeRequest) (SandboxBridgeResponse, error) {
		switch request.Method {
		case "pi/set_model":
			return SandboxBridgeResponse{Result: json.RawMessage(`{"ok":true}`)}, nil
		case "pi/set_thinking_level":
			var params map[string]any
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params["level"] != "max" {
				t.Fatalf("effort rewritten before provider saw it: %#v", params["level"])
			}
			return SandboxBridgeResponse{}, errors.New("unsupported thinking level: max")
		default:
			return SandboxBridgeResponse{}, errors.New("unexpected method " + request.Method)
		}
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1"})
	_, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID:                "effort-reject",
		Message:                  "try max",
		ModelProviderID:          "primary",
		Model:                    "gpt-test",
		RequestedReasoningEffort: "max",
	}, nil)
	if err == nil {
		t.Fatal("expected provider effort rejection")
	}
	var opErr *ProviderSessionOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if !strings.Contains(opErr.Cause.Error(), "unsupported thinking level") {
		t.Fatalf("cause = %v", opErr.Cause)
	}
	// Prompt must not run when thinking level was rejected.
	for _, req := range transport.snapshot() {
		if req.Method == "pi/prompt" {
			t.Fatalf("prompt issued after thinking rejection: %#v", transport.snapshot())
		}
	}
}

func TestPiProviderSessionOmitsSelectionControlsWhenUnset(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"pi/prompt": {Result: json.RawMessage(`{"session_id":"pi-1","turn_id":"turn-1"}`)},
	}}
	session := NewPiProviderSession(PiProviderSessionConfig{Transport: transport, SessionID: "pi-1"})
	if _, err := session.SendTurn(context.Background(), ProviderSessionRequest{
		RequestID: "send-plain",
		Message:   "hello",
	}, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	requests := transport.snapshot()
	if len(requests) != 1 || requests[0].Method != "pi/prompt" {
		t.Fatalf("expected only prompt when selection unset, got %#v", requests)
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
	if !noSteer.Capabilities().InTurnSteer {
		t.Fatal("codex should advertise direct in-turn steer")
	}
	_, err = noSteer.SteerInTurn(context.Background(), ProviderSessionRequest{RequestID: "steer-1", Message: "hi"}, nil)
	if !errors.Is(err, ErrProviderTurnUnavailable) {
		t.Fatalf("steer error = %v, want provider Turn unavailable", err)
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
	session := NewClaudeCodeProviderSession(ClaudeCodeProviderSessionConfig{Transport: transport, SessionID: "claude-1", ActiveTurnID: "turn-live"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// A transport that honors the canceled context is covered by the shared
	// operation wrapper; this assertion ensures Close remains a public control.
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if session.TurnBusy() || session.TurnState().ActiveTurnID != "" {
		t.Fatalf("closed session retained active Turn: %#v", session.TurnState())
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

func TestProviderSessionAdapterFreezesCompleteTurnLineage(t *testing.T) {
	transport := &fakeProviderTransport{responses: map[string]SandboxBridgeResponse{
		"turn/start": {Result: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-control"}}`)},
	}}
	session := NewCodexProviderSession(CodexProviderSessionConfig{Transport: transport, SessionID: "thread-1"})
	request := ProviderSessionRequest{
		RequestID: "conclude-1", Message: "return the closed conclusion", TurnKind: RuntimeTurnKindControl,
		ModelProviderID: "provider-1", Model: "gpt-test", RequestedReasoningEffort: "high",
		EffectiveReasoningEffort: "medium",
	}
	result, err := session.SendTurn(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ProviderSessionTurnLineage{
		RequestID: request.RequestID, ProviderTurnID: result.ProviderTurnID,
		Kind:            RuntimeTurnKindControl,
		ModelProviderID: "provider-1", Model: "gpt-test", RequestedReasoningEffort: "high",
		EffectiveReasoningEffort: "medium",
	}
	for _, correlation := range []struct {
		requestID, providerTurnID string
	}{
		{requestID: request.RequestID},
		{providerTurnID: result.ProviderTurnID},
		{requestID: request.RequestID, providerTurnID: "provider-spoofed-turn"},
	} {
		got, ok := session.ResolveProviderSessionTurnLineage(correlation.requestID, correlation.providerTurnID)
		if !ok || got != want {
			t.Fatalf("lineage for request=%q turn=%q = %#v, %v; want %#v", correlation.requestID, correlation.providerTurnID, got, ok, want)
		}
	}
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

func TestCodexProviderSessionProjectsVisibleRuntimeOutput(t *testing.T) {
	session := NewCodexProviderSession(CodexProviderSessionConfig{
		Transport: &fakeProviderTransport{}, SessionID: "thread-1", ActiveTurnID: "turn-1",
	})
	var kinds []task.EventKind
	var events []task.EventPayload
	emit := func(kind task.EventKind, payload task.EventPayload) {
		kinds = append(kinds, kind)
		events = append(events, payload)
	}
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"item-msg","text":"VPN检测未通过"}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/started",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"commandExecution","id":"item-cmd","command":"curl http://10.0.100.58","status":"inProgress"}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"commandExecution","id":"item-cmd","command":"curl http://10.0.100.58","aggregatedOutput":"curl: (52) Empty reply from server\n","status":"failed","exitCode":52}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/started",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"mcpToolCall","id":"item-mcp","server":"browser","tool":"navigate","status":"inProgress"}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"mcpToolCall","id":"item-mcp","server":"browser","tool":"navigate","status":"completed","result":"ok"}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/started",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"reasoning","id":"item-reasoning","summary":[],"content":["SECRET_RAW_REASONING"]}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"reasoning","id":"item-reasoning","summary":["Checked the active challenge.","Prepared the next command."],"content":["SECRET_RAW_REASONING"]}}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/agentMessage/delta",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-msg","delta":"VPN"}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/reasoning/summaryTextDelta",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-reasoning","delta":"Checked"}`),
	}, emit)
	session.HandleEvent(SandboxBridgeEvent{
		Method: "item/completed",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"type":"userMessage","id":"item-user","content":[{"type":"text","text":"operator prompt"}]}}`),
	}, emit)

	if len(events) != 6 {
		t.Fatalf("runtime events = %#v", events)
	}
	for i, kind := range kinds {
		if kind != task.EventKindRuntimeOutput {
			t.Fatalf("kind[%d] = %q", i, kind)
		}
		if events[i]["provider"] != "codex" || events[i]["stream"] != "codex_app_server" {
			t.Fatalf("payload[%d] = %#v", i, events[i])
		}
		if _, leaked := events[i]["params"]; leaked {
			t.Fatalf("runtime output leaked protocol params: %#v", events[i])
		}
	}
	if text, _ := events[0]["text"].(string); !strings.Contains(text, "VPN检测未通过") {
		t.Fatalf("assistant text = %q", events[0]["text"])
	}
	if events[1]["provider_event"] != "item/started" || events[2]["provider_event"] != "item/completed" {
		t.Fatalf("command lifecycle = %#v %#v", events[1], events[2])
	}
	if text, _ := events[2]["text"].(string); !strings.Contains(text, "curl http://10.0.100.58") || !strings.Contains(text, "Empty reply") {
		t.Fatalf("command text = %q", events[2]["text"])
	}
	if events[3]["provider_event"] != "item/started" || events[4]["provider_event"] != "item/completed" {
		t.Fatalf("MCP lifecycle = %#v %#v", events[3], events[4])
	}
	text, _ := events[5]["text"].(string)
	if events[5]["provider_event"] != "item/completed" || !strings.Contains(text, "Checked the active challenge.") {
		t.Fatalf("completed reasoning summary = %#v", events[5])
	}
	if strings.Contains(text, "SECRET_RAW_REASONING") || strings.Contains(text, `"content"`) {
		t.Fatalf("reasoning event leaked raw content: %s", text)
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

func TestSandboxBridgeRPCErrorDistinguishesCompletedAndChangedTargets(t *testing.T) {
	completed := sandboxBridgeRPCError("steer-completed", json.RawMessage(`{"code":-32000,"message":"no active turn"}`))
	if !errors.Is(completed, ErrProviderTurnUnavailable) || errors.Is(completed, ErrProviderTurnChanged) {
		t.Fatalf("completed target classification = %#v", completed)
	}
	changed := sandboxBridgeRPCError("steer-changed", json.RawMessage(`{"code":-32000,"message":"expectedTurnId does not match active turn"}`))
	if !errors.Is(changed, ErrProviderTurnChanged) || errors.Is(changed, ErrProviderTurnUnavailable) {
		t.Fatalf("changed target classification = %#v", changed)
	}
}
