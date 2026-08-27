package daemon

import (
	"context"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

func TestRunNativeSteerTurnDefersAppliedProjectionUntilDurableSettlement(t *testing.T) {
	var persisted []task.EventPayload
	execution := runNativeSteerTurn(context.Background(), steerExecutionSpec{
		operation: func(_ context.Context, request runtime.ProviderSessionRequest, _ runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
			return runtime.ProviderSessionResult{
				RequestID: request.RequestID, SessionID: "session-1", ProviderTurnID: "turn-1",
				Mode: runtime.ProviderSessionModeInTurnSteer, Outcome: "acknowledged",
			}, nil
		},
		request: runtime.ProviderSessionRequest{RequestID: "steer-1", Message: "focus"},
		mode:    runtime.ProviderSessionModeInTurnSteer, providerSessionID: "session-1",
		initialContinuationID: "continuation-1", vocabulary: taskSteerEventVocabulary,
		persistEvent: func(_ string, _ task.EventKind, payload task.EventPayload) {
			persisted = append(persisted, payload)
		},
	})
	if execution.state != owner.SteeringApplied {
		t.Fatalf("execution = %#v", execution)
	}
	for _, payload := range persisted {
		if payload["outcome"] == "applied" {
			t.Fatalf("applied projection preceded durable settlement: %#v", persisted)
		}
	}
}

func TestSteerEmitProtocolAdvancesForInterruptFallbackMode(t *testing.T) {
	advanced := false
	protocol := newSteerEmitProtocol("continuation-1", steerEmitProtocolSpec{
		advanceOnSettled: true,
		persistEvent:     func(string, task.EventKind, task.EventPayload) {},
		advance: func(current string) (string, error) {
			advanced = true
			if current != "continuation-1" {
				t.Fatalf("current continuation = %q", current)
			}
			return "continuation-2", nil
		},
	})
	protocol.emit(task.EventKindSteering, task.EventPayload{
		"mode":    string(runtime.ProviderSessionModeInterruptThenReplace),
		"outcome": "settled",
	})
	if !advanced || protocol.currentID() != "continuation-2" {
		t.Fatalf("fallback continuation = %q, advanced=%v", protocol.currentID(), advanced)
	}
}
