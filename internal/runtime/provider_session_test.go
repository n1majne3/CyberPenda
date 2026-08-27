package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/blackboardconclusion"
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

func TestFakeProviderSessionInTurnSteerPreservesWorkTurnLineage(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-1",
		Capabilities: runtimeplugin.Capabilities{SendTurn: true, InTurnSteer: true},
	})
	work, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "work-1", Message: "inspect", TurnKind: runtime.RuntimeTurnKindWork,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, ok := session.ResolveProviderSessionTurnLineage("", work.ProviderTurnID)
	if !ok || before.RequestID != "work-1" || before.Kind != runtime.RuntimeTurnKindWork {
		t.Fatalf("work lineage before steer = %#v ok=%v", before, ok)
	}
	steered, err := session.SteerInTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "steer-1", Message: "focus", ProviderTurnID: work.ProviderTurnID, TurnKind: runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if steered.ProviderTurnID != work.ProviderTurnID {
		t.Fatalf("steered Turn = %q, want %q", steered.ProviderTurnID, work.ProviderTurnID)
	}
	after, ok := session.ResolveProviderSessionTurnLineage("", work.ProviderTurnID)
	if !ok || after != before {
		t.Fatalf("work lineage after steer = %#v ok=%v, want %#v", after, ok, before)
	}
}

func TestFakeProviderSessionRejectsChangedInTurnTarget(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", ActiveTurnID: "turn-live",
		Capabilities: runtimeplugin.Capabilities{InTurnSteer: true},
	})
	_, err := session.SteerInTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "steer-1", Message: "focus", ProviderTurnID: "turn-old",
	}, nil)
	if !errors.Is(err, runtime.ErrProviderTurnChanged) {
		t.Fatalf("steer error = %v, want provider Turn changed", err)
	}
}

func TestFakeProviderSessionTurnBusyTracksActiveRuntimeTurn(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1",
		Capabilities: runtimeplugin.Capabilities{
			SendTurn: true,
		},
	})
	result, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "send-1", Message: "inspect"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session.ControlBusy() {
		t.Fatal("ControlBusy remained true after SendTurn returned")
	}
	if !session.TurnBusy() {
		t.Fatal("TurnBusy = false while fake Runtime Turn is active")
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind:           runtime.ProviderSessionObservationTurnCompleted,
		SessionID:      "session-1",
		ProviderTurnID: result.ProviderTurnID,
		RequestID:      "send-1",
		Status:         "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if session.TurnBusy() {
		t.Fatal("TurnBusy = true after matching fake terminal observation")
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

func TestFakeProviderSessionFreezesCompleteTurnLineage(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", Capabilities: runtimeplugin.Capabilities{SendTurn: true},
	})
	request := runtime.ProviderSessionRequest{
		RequestID: "control-1", Message: "conclude prior work", TurnKind: runtime.RuntimeTurnKindControl,
		ModelProviderID: "provider-1", Model: "model-1", RequestedReasoningEffort: "high",
		EffectiveReasoningEffort: "medium",
	}
	result, err := session.SendTurn(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := runtime.ProviderSessionTurnLineage{
		RequestID: "control-1", ProviderTurnID: result.ProviderTurnID,
		Kind:            runtime.RuntimeTurnKindControl,
		ModelProviderID: "provider-1", Model: "model-1", RequestedReasoningEffort: "high",
		EffectiveReasoningEffort: "medium",
	}
	for _, correlation := range []struct {
		requestID, providerTurnID string
	}{
		{requestID: "control-1"},
		{providerTurnID: result.ProviderTurnID},
		{requestID: "control-1", providerTurnID: "provider-spoofed-turn"},
	} {
		got, ok := session.ResolveProviderSessionTurnLineage(correlation.requestID, correlation.providerTurnID)
		if !ok || got != want {
			t.Fatalf("lineage for request=%q turn=%q = %#v, %v; want %#v", correlation.requestID, correlation.providerTurnID, got, ok, want)
		}
	}
}

func TestFakeProviderSessionMintsNewTurnAfterValidatedCompletion(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", ActiveTurnID: "work-turn-1",
		Capabilities: runtimeplugin.Capabilities{SendTurn: true},
	})
	work, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "work-1", Message: "inspect", TurnKind: runtime.RuntimeTurnKindWork,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.EmitObservation(runtime.ProviderSessionObservation{
		Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: "work-1",
		ProviderTurnID: work.ProviderTurnID, Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	control, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "control-1", Message: "conclude", TurnKind: runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if control.ProviderTurnID == "" || control.ProviderTurnID == work.ProviderTurnID {
		t.Fatalf("control Turn ID = %q, want distinct from completed work Turn %q", control.ProviderTurnID, work.ProviderTurnID)
	}
	if lineage, ok := session.ResolveProviderSessionTurnLineage("", control.ProviderTurnID); !ok || lineage.Kind != runtime.RuntimeTurnKindControl {
		t.Fatalf("control lineage = %#v, %v", lineage, ok)
	}
}

func TestFakeProviderSessionEmitsTypedCanonicalAttemptResult(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", Capabilities: runtimeplugin.Capabilities{SendTurn: true},
	})
	turn, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "conclude-1", Message: "conclude", TurnKind: runtime.RuntimeTurnKindControl,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got runtime.ProviderSessionAttemptResult
	session.SetAttemptResultSink(func(result runtime.ProviderSessionAttemptResult) { got = result })
	raw := []byte(`{
		"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the search endpoint."}}],
		"attempt":{"outcome":"failed","summary":"The endpoint rejected the tested payloads.","create":true,"key":"attempt:search"},
		"base_revision":0,
		"schema":"runtime-attempt-result/v1",
		"produced_targets":[]
	}`)
	if err := session.EmitAttemptResult(raw); err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "conclude-1" || got.SessionID != "session-1" || got.ProviderTurnID != turn.ProviderTurnID {
		t.Fatalf("attempt result correlation = %#v", got)
	}
	wantCanonical := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:search","create":true,"summary":"The endpoint rejected the tested payloads.","outcome":"failed"},"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the search endpoint."}}],"produced_targets":[]}`
	if string(got.Validated.CanonicalJSON) != wantCanonical || got.Validated.SHA256 == "" {
		t.Fatalf("validated result = %#v", got.Validated)
	}
}

func TestFakeProviderSessionRejectsUnknownAttemptResultBeforeCallback(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", Capabilities: runtimeplugin.Capabilities{SendTurn: true},
	})
	if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "conclude-1", Message: "conclude", TurnKind: runtime.RuntimeTurnKindControl,
	}, nil); err != nil {
		t.Fatal(err)
	}
	callbacks := 0
	session.SetAttemptResultSink(func(runtime.ProviderSessionAttemptResult) { callbacks++ })
	raw := []byte(`{
		"schema":"runtime-attempt-result/v1","base_revision":0,
		"attempt":{"key":"attempt:search","create":true,"summary":"Tested search.","outcome":"failed"},
		"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test search."}}],
		"produced_targets":[],"task_id":"model-supplied-identity"
	}`)
	if err := session.EmitAttemptResult(raw); err == nil {
		t.Fatal("EmitAttemptResult accepted an unknown identity field")
	}
	if callbacks != 0 {
		t.Fatalf("attempt result callbacks = %d, want zero", callbacks)
	}
}

func TestFakeProviderSessionEmitsBoundedInvalidAttemptResultNotification(t *testing.T) {
	valid := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:search","create":true,"summary":"Tested search.","outcome":"failed"},"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test search."}}],"produced_targets":[]}`
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: `{"schema":`},
		{name: "unknown field", raw: strings.Replace(valid, `"schema":`, `"surprise":"do-not-leak-unknown","schema":`, 1)},
		{name: "interrupted outcome", raw: strings.Replace(valid, `"outcome":"failed"`, `"outcome":"interrupted"`, 1)},
		{name: "model supplied identity", raw: strings.Replace(valid, `"schema":`, `"task_id":"do-not-leak-identity","schema":`, 1)},
		{name: "empty tested targets", raw: strings.Replace(valid, `"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test search."}}]`, `"tested_targets":[]`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
				SessionID: "session-1", Capabilities: runtimeplugin.Capabilities{SendTurn: true},
			})
			turn, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
				RequestID: "conclude-1", Message: "conclude", TurnKind: runtime.RuntimeTurnKindControl,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			validCallbacks := 0
			invalidCallbacks := 0
			var got runtime.ProviderSessionAttemptResultValidationFailure
			session.SetAttemptResultSink(func(runtime.ProviderSessionAttemptResult) { validCallbacks++ })
			session.SetAttemptResultValidationFailureSink(func(failure runtime.ProviderSessionAttemptResultValidationFailure) {
				invalidCallbacks++
				got = failure
			})

			err = session.EmitAttemptResult([]byte(test.raw))
			if err == nil {
				t.Fatal("EmitAttemptResult accepted an invalid result")
			}
			if validCallbacks != 0 || invalidCallbacks != 1 {
				t.Fatalf("callbacks = valid %d, invalid %d; want valid 0, invalid 1", validCallbacks, invalidCallbacks)
			}
			if got.RequestID != "conclude-1" || got.SessionID != "session-1" || got.ProviderTurnID != turn.ProviderTurnID {
				t.Fatalf("invalid result correlation = %#v", got)
			}
			if got.ValidationErrorCode != runtime.ProviderSessionAttemptResultInvalid {
				t.Fatalf("validation error code = %q", got.ValidationErrorCode)
			}
			bounded := fmt.Sprintf("%#v", got)
			for _, forbidden := range []string{test.raw, err.Error(), "do-not-leak"} {
				if forbidden != "" && strings.Contains(bounded, forbidden) {
					t.Fatalf("invalid notification leaked %q: %s", forbidden, bounded)
				}
			}
		})
	}
}

func TestFakeProviderSessionInvalidNotificationCarriesBoundedValidationDetail(t *testing.T) {
	valid := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:search","create":true,"summary":"Tested search.","outcome":"failed"},"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test search."}}],"produced_targets":[]}`
	tests := []struct {
		name      string
		raw       string
		reason    blackboardconclusion.ValidationReason
		fieldPath string
		expected  string
	}{
		{name: "empty attempt key", raw: strings.Replace(valid, `"key":"attempt:search"`, `"key":""`, 1), reason: blackboardconclusion.ValidationReasonRuleViolation, fieldPath: "attempt.key", expected: "non-empty"},
		{name: "unknown field", raw: strings.Replace(valid, `"schema":`, `"unexpected":true,"schema":`, 1), reason: blackboardconclusion.ValidationReasonUnknownField, fieldPath: "unexpected"},
		{name: "invalid enum", raw: strings.Replace(valid, `"outcome":"failed"`, `"outcome":"interrupted"`, 1), reason: blackboardconclusion.ValidationReasonInvalidEnumValue, fieldPath: "attempt.outcome", expected: "succeeded, failed, blocked, or inconclusive"},
		{name: "oversized result", raw: strings.Repeat("x", (64<<10)+1), reason: blackboardconclusion.ValidationReasonResultTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
				SessionID: "session-detail", Capabilities: runtimeplugin.Capabilities{SendTurn: true},
			})
			if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
				RequestID: "conclude-detail", Message: "conclude", TurnKind: runtime.RuntimeTurnKindControl,
			}, nil); err != nil {
				t.Fatal(err)
			}
			var got runtime.ProviderSessionAttemptResultValidationFailure
			session.SetAttemptResultValidationFailureSink(func(failure runtime.ProviderSessionAttemptResultValidationFailure) { got = failure })
			if err := session.EmitAttemptResult([]byte(test.raw)); err == nil {
				t.Fatal("EmitAttemptResult accepted an invalid result")
			}
			if got.ValidationErrorCode != runtime.ProviderSessionAttemptResultInvalid {
				t.Fatalf("validation error code = %q", got.ValidationErrorCode)
			}
			if got.Reason != test.reason {
				t.Fatalf("validation reason = %q, want %q", got.Reason, test.reason)
			}
			if got.FieldPath != test.fieldPath {
				t.Fatalf("validation field path = %q, want %q", got.FieldPath, test.fieldPath)
			}
			if test.expected != "" && !strings.Contains(got.Expected, test.expected) {
				t.Fatalf("validation expected = %q, want it to contain %q", got.Expected, test.expected)
			}
			bounded := fmt.Sprintf("%#v", got)
			if strings.Contains(bounded, test.raw) {
				t.Fatalf("invalid notification leaked raw result bytes: %s", bounded)
			}
		})
	}
}

func TestFakeProviderSessionValidAttemptResultDoesNotEmitValidationFailure(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", Capabilities: runtimeplugin.Capabilities{SendTurn: true},
	})
	if _, err := session.SendTurn(context.Background(), runtime.ProviderSessionRequest{
		RequestID: "conclude-1", Message: "conclude", TurnKind: runtime.RuntimeTurnKindControl,
	}, nil); err != nil {
		t.Fatal(err)
	}
	validCallbacks := 0
	invalidCallbacks := 0
	session.SetAttemptResultSink(func(runtime.ProviderSessionAttemptResult) { validCallbacks++ })
	session.SetAttemptResultValidationFailureSink(func(runtime.ProviderSessionAttemptResultValidationFailure) { invalidCallbacks++ })
	raw := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:search","create":true,"summary":"Tested search.","outcome":"failed"},"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test search."}}],"produced_targets":[]}`)
	if err := session.EmitAttemptResult(raw); err != nil {
		t.Fatal(err)
	}
	if validCallbacks != 1 || invalidCallbacks != 0 {
		t.Fatalf("callbacks = valid %d, invalid %d; want valid 1, invalid 0", validCallbacks, invalidCallbacks)
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

	_, err = session.SteerInTurn(context.Background(), runtime.ProviderSessionRequest{RequestID: "steer-1", Message: "redirect", ProviderTurnID: "turn-1"}, recorder.emit)
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

func TestFakeProviderSessionCloseClearsActiveTurn(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "session-1", ActiveTurnID: "turn-live",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.TurnBusy() || session.TurnState().ActiveTurnID != "" {
		t.Fatalf("closed fake session retained active Turn: %#v", session.TurnState())
	}
}
