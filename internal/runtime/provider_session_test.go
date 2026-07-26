package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/task"
)

func interactiveCapabilities() runtimeplugin.Capabilities {
	return runtimeplugin.Capabilities{
		PersistentSession:    true,
		SendTurn:             true,
		InterruptTurn:        true,
		InterruptThenReplace: true,
		InTurnSteer:          true,
		PermissionResponse:   true,
		ResumeSession:        true,
	}
}

func TestFakeProviderSessionEmitsBoundedObservations(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", ActiveTurnID: "turn-1",
	})
	var observed []runtime.ProviderSessionObservation
	session.SetObservationSink(func(observation runtime.ProviderSessionObservation) {
		observed = append(observed, observation)
	})

	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolUse, RequestID: "request-1", ToolCallID: "call-1", ToolName: "curl"},
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: "request-1", ToolCallID: "call-1", ToolName: "curl", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: "request-1", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatalf("emit observation: %v", err)
		}
	}

	if len(observed) != 3 {
		t.Fatalf("observations = %#v", observed)
	}
	for _, observation := range observed {
		if observation.SessionID != "session-1" || observation.ProviderTurnID != "turn-1" {
			t.Fatalf("observation lost bounded correlation: %#v", observation)
		}
	}
}

func TestFakeProviderSessionRejectsMalformedObservations(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{SessionID: "session-1", ActiveTurnID: "turn-1"})
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: "raw_output"},
		{Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "call-1"},
		{Kind: runtime.ProviderSessionObservationToolResult, ToolCallID: "call-1", ToolName: "curl"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, Status: "completed", ToolName: "curl"},
		{Kind: runtime.ProviderSessionObservationToolUse, ToolCallID: "call-1", ToolName: strings.Repeat("x", 257)},
	} {
		if err := session.EmitObservation(observation); !errors.Is(err, runtime.ErrInvalidProviderSessionObservation) {
			t.Fatalf("observation %#v error = %v", observation, err)
		}
	}
}

func TestFakeProviderSessionDefaultsRequestTurnKindToWork(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", Capabilities: runtimeplugin.Capabilities{SendTurn: true},
	})
	if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "request-1", Message: "inspect"}, nil); err != nil {
		t.Fatal(err)
	}
	requests := session.LastRequests()
	if len(requests) != 1 || requests[0].TurnKind != runtime.RuntimeTurnKindWork {
		t.Fatalf("requests = %#v", requests)
	}
	if kind, ok := session.ResolveProviderSessionTurnKind("request-1", ""); !ok || kind != runtime.RuntimeTurnKindWork {
		t.Fatalf("Harness Turn lineage = %q, resolved=%v", kind, ok)
	}
}

type sessionEventRecorder struct {
	mu     sync.Mutex
	events []task.EventPayload
}

func (r *sessionEventRecorder) emit(_ task.EventKind, payload task.EventPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := task.EventPayload{}
	for key, value := range payload {
		copy[key] = value
	}
	r.events = append(r.events, copy)
}

func (r *sessionEventRecorder) snapshot() []task.EventPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]task.EventPayload(nil), r.events...)
}

func waitForSessionEvents(t *testing.T, recorder *sessionEventRecorder, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(recorder.snapshot()) >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d session events; got %#v", count, recorder.snapshot())
}

func TestFakeProviderSessionInterruptThenReplaceWaitsForAcknowledgement(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:         "session-1",
		ActiveTurnID:      "turn-1",
		Capabilities:      interactiveCapabilities(),
		ManualAcknowledge: true,
	})
	recorder := &sessionEventRecorder{}

	result := make(chan runtime.ProviderSessionResult, 1)
	errs := make(chan error, 1)
	go func() {
		got, err := session.InterruptThenReplace(context.Background(), runtime.ProviderSessionRequest{
			RequestID: "request-1",
			Message:   "focus on the admin surface",
		}, recorder.emit)
		result <- got
		errs <- err
	}()

	waitForSessionEvents(t, recorder, 1)
	select {
	case got := <-result:
		t.Fatalf("replacement completed before provider acknowledgement: %#v", got)
	default:
	}
	if err := session.Acknowledge("request-1"); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	got := <-result
	if err := <-errs; err != nil {
		t.Fatalf("interrupt then replace: %v", err)
	}
	if got.SessionID != "session-1" || got.ProviderTurnID == "" || got.ProviderTurnID == "turn-1" {
		t.Fatalf("replacement result = %#v", got)
	}

	events := recorder.snapshot()
	wantOutcomes := []string{"requested", "acknowledged", "settled", "started"}
	if len(events) != len(wantOutcomes) {
		t.Fatalf("events = %#v", events)
	}
	for i, want := range wantOutcomes {
		if events[i]["request_id"] != "request-1" || events[i]["session_id"] != "session-1" ||
			events[i]["provider_turn_id"] == "" || events[i]["mode"] != "interrupt_then_replace" ||
			events[i]["outcome"] != want {
			t.Fatalf("event %d = %#v, want outcome %q with stable correlation fields", i, events[i], want)
		}
	}
}

func TestFakeProviderSessionRejectsUnsupportedCapability(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-1",
		ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true},
	})
	_, err := session.InterruptThenReplace(context.Background(), runtime.ProviderSessionRequest{RequestID: "request-1", Message: "redirect"}, nil)
	var unsupported *runtime.UnsupportedProviderSessionCapabilityError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want typed unsupported capability error", err)
	}
	if unsupported.Capability != runtime.ProviderSessionCapabilityInterruptThenReplace {
		t.Fatalf("capability = %q", unsupported.Capability)
	}
}

func TestFakeProviderSessionDuplicateRequestIsIdempotent(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-1",
		ActiveTurnID: "turn-1",
		Capabilities: interactiveCapabilities(),
	})
	recorder := &sessionEventRecorder{}
	req := runtime.ProviderSessionRequest{RequestID: "request-1", Message: "redirect"}
	first, err := session.InterruptThenReplace(context.Background(), req, recorder.emit)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := session.InterruptThenReplace(context.Background(), req, recorder.emit)
	if err != nil {
		t.Fatalf("duplicate request: %v", err)
	}
	if first != second {
		t.Fatalf("duplicate result = %#v, want %#v", second, first)
	}
	if len(recorder.snapshot()) != 4 {
		t.Fatalf("duplicate emitted events: %#v", recorder.snapshot())
	}
}

func TestFakeProviderSessionLocalTimeoutCanRetrySameRequest(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:         "session-1",
		ActiveTurnID:      "turn-1",
		Capabilities:      interactiveCapabilities(),
		ManualAcknowledge: true,
	})
	request := runtime.ProviderSessionRequest{RequestID: "request-1", Message: "redirect"}
	recorder := &sessionEventRecorder{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := session.InterruptThenReplace(ctx, request, recorder.emit); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first request error = %v, want deadline", err)
	}

	result := make(chan runtime.ProviderSessionResult, 1)
	errs := make(chan error, 1)
	go func() {
		got, err := session.InterruptThenReplace(context.Background(), request, recorder.emit)
		result <- got
		errs <- err
	}()
	waitForSessionEvents(t, recorder, 2)
	if err := session.Acknowledge(request.RequestID); err != nil {
		t.Fatalf("acknowledge retry: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("retry request: %v", err)
	}
	if got := <-result; got.Outcome != "started" {
		t.Fatalf("retry result = %#v", got)
	}
}

func TestFakeProviderSessionSerializesConcurrentControls(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:         "session-1",
		ActiveTurnID:      "turn-1",
		Capabilities:      interactiveCapabilities(),
		ManualAcknowledge: true,
	})
	recorder := &sessionEventRecorder{}
	done := make(chan error, 1)
	go func() {
		_, err := session.InterruptThenReplace(context.Background(), runtime.ProviderSessionRequest{RequestID: "request-1", Message: "redirect"}, recorder.emit)
		done <- err
	}()
	waitForSessionEvents(t, recorder, 1)

	_, err := session.InterruptTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "request-2"}, recorder.emit)
	if !errors.Is(err, runtime.ErrProviderSessionControlConflict) {
		t.Fatalf("concurrent control error = %v", err)
	}
	if err := session.Close(context.Background()); !errors.Is(err, runtime.ErrProviderSessionControlConflict) {
		t.Fatalf("concurrent close error = %v", err)
	}
	if err := session.Acknowledge("request-1"); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("first control: %v", err)
	}
}

func TestFakeProviderSessionBindsRequestIDToOneOperation(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", Capabilities: interactiveCapabilities(),
	})
	if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "request-1", Message: "continue"}, nil); err != nil {
		t.Fatalf("send turn: %v", err)
	}
	if _, err := session.InterruptTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "request-1"}, nil); !errors.Is(err, runtime.ErrProviderSessionControlConflict) {
		t.Fatalf("reused request id error = %v", err)
	}
}

func TestFakeProviderSessionPermissionResponseAndTypedFailure(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-1",
		ActiveTurnID: "turn-1",
		Capabilities: interactiveCapabilities(),
		Failures: map[runtime.ProviderSessionMode]error{
			runtime.ProviderSessionModeInTurnSteer: errors.New("provider rejected steer"),
		},
	})
	recorder := &sessionEventRecorder{}
	permission, err := session.RespondPermission(context.Background(), runtime.ProviderSessionRequest{
		RequestID:           "permission-response-1",
		ProviderTurnID:      "turn-1",
		PermissionRequestID: "permission-1",
		PermissionDecision:  "allow",
	}, recorder.emit)
	if err != nil {
		t.Fatalf("permission response: %v", err)
	}
	if permission.Outcome != "acknowledged" {
		t.Fatalf("permission result = %#v", permission)
	}

	_, err = session.SteerInTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "steer-1", Message: "redirect"}, recorder.emit)
	var failed *runtime.ProviderSessionOperationError
	if !errors.As(err, &failed) || failed.Mode != runtime.ProviderSessionModeInTurnSteer {
		t.Fatalf("steer error = %v, want typed operation error", err)
	}
	events := recorder.snapshot()
	last := events[len(events)-1]
	if last["request_id"] != "steer-1" || last["session_id"] != "session-1" ||
		last["provider_turn_id"] != "turn-1" || last["mode"] != "in_turn_steer" || last["outcome"] != "failed" {
		t.Fatalf("failure event = %#v", last)
	}
	if _, leaked := last["message"]; leaked {
		t.Fatalf("failure event leaked user/provider data: %#v", last)
	}
}
