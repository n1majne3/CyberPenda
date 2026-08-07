package daemon

import (
	"fmt"
	"strings"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

// invalidAttemptKeyResult is structurally complete except that attempt.key
// uses a slash instead of the closed attempt: prefix.
const invalidAttemptKeyResult = `{
	"schema":"runtime-attempt-result/v1",
	"base_revision":0,
	"attempt":{"key":"attempt/arena-2923","create":true,"summary":"Tested the search surface.","outcome":"inconclusive"},
	"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the search surface."}}],
	"produced_targets":[]
}`

const validRepairResult = `{
	"schema":"runtime-attempt-result/v1",
	"base_revision":0,
	"attempt":{"key":"attempt:arena-2923","create":true,"summary":"Tested the search surface.","outcome":"inconclusive"},
	"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the search surface."}}],
	"produced_targets":[]
}`

func prepareInvalidConcludeTurn(t *testing.T, invalid []byte) (server *Server, projectID string, created task.Task, session *runtime.FakeProviderSession, conclude requestSnapshot) {
	t.Helper()
	server, projectID, profileID, session := newAssistedConclusionFixture(t, true)
	created = launchConclusionTask(t, server, projectID, profileID, "assisted")
	waitForAssistedProviderRequests(t, session, 1)
	workRequest := session.LastRequests()[0]
	for _, observation := range []runtime.ProviderSessionObservation{
		{Kind: runtime.ProviderSessionObservationToolResult, RequestID: workRequest.RequestID, ProviderTurnID: "work-turn-validation-feedback", ToolCallID: "tool-1", ToolName: "shell", Status: "succeeded"},
		{Kind: runtime.ProviderSessionObservationTurnCompleted, RequestID: workRequest.RequestID, ProviderTurnID: "work-turn-validation-feedback", Status: "completed"},
	} {
		if err := session.EmitObservation(observation); err != nil {
			t.Fatal(err)
		}
	}
	waitForAssistedProviderRequests(t, session, 2)
	conclude = snapshotRequest(session.LastRequests()[1])
	if err := emitAttemptResultAndComplete(t, session, invalid); err == nil {
		t.Fatal("invalid Conclude result unexpectedly decoded")
	}
	return server, projectID, created, session, conclude
}

type requestSnapshot struct {
	RequestID string
	Message   string
}

func snapshotRequest(request runtime.ProviderSessionRequest) requestSnapshot {
	return requestSnapshot{RequestID: request.RequestID, Message: request.Message}
}

func TestAssistedConclusionRepairDirectiveCarriesBoundedValidationReason(t *testing.T) {
	server, projectID, created, session, conclude := prepareInvalidConcludeTurn(t, []byte(invalidAttemptKeyResult))
	waitForAssistedProviderRequests(t, session, 3)
	repair := snapshotRequest(session.LastRequests()[2])
	if repair.RequestID == conclude.RequestID {
		t.Fatalf("repair reused the Conclude request ID %q", repair.RequestID)
	}
	for _, required := range []string{"invalid_key_format", "attempt.key", "attempt: prefix", "runtime-attempt-result/v1"} {
		if !strings.Contains(repair.Message, required) {
			t.Fatalf("repair directive missing %q: %s", required, repair.Message)
		}
	}
	for _, forbidden := range []string{"arena-2923", invalidAttemptKeyResult} {
		if strings.Contains(repair.Message, forbidden) {
			t.Fatalf("repair directive leaked raw result content %q: %s", forbidden, repair.Message)
		}
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateConcluding)
	if found.Status != task.StatusRunning {
		t.Fatalf("repair changed Task lifecycle: %#v", found)
	}
}

func TestAssistedConclusionRepeatedInvalidResultExposesBoundedReason(t *testing.T) {
	server, projectID, created, session, _ := prepareInvalidConcludeTurn(t, []byte(invalidAttemptKeyResult))
	waitForAssistedProviderRequests(t, session, 3)
	if err := emitAttemptResultAndComplete(t, session, []byte(invalidAttemptKeyResult)); err == nil {
		t.Fatal("invalid repair result unexpectedly decoded")
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateActionRequired)
	conclusion := found.BlackboardConclusion
	if conclusion.ErrorCode != task.BlackboardConclusionErrorRepairExhausted {
		t.Fatalf("action-required error code = %q", conclusion.ErrorCode)
	}
	if conclusion.ValidationReason != "invalid_key_format" || conclusion.ValidationFieldPath != "attempt.key" {
		t.Fatalf("action-required validation detail = %#v", conclusion)
	}
	if !strings.Contains(conclusion.ValidationExpected, "attempt: prefix") {
		t.Fatalf("action-required validation expected = %q, want attempt: prefix", conclusion.ValidationExpected)
	}
	if len(session.LastRequests()) != 3 {
		t.Fatalf("automatic provider requests = %d, want work, Conclude, repair", len(session.LastRequests()))
	}
	actionEvents := 0
	for _, event := range assistedTaskEvents(t, server, projectID, created.ID) {
		if event.Kind != task.EventKindBlackboardConclusion {
			continue
		}
		payload := fmt.Sprintf("%#v", event.Payload)
		if strings.Contains(payload, "arena-2923") || strings.Contains(payload, invalidAttemptKeyResult) {
			t.Fatalf("raw provider result leaked into Task Event: %s", payload)
		}
		if event.Payload["phase"] != "action_required" {
			continue
		}
		actionEvents++
		if event.Payload["validation_reason"] != "invalid_key_format" || event.Payload["validation_field_path"] != "attempt.key" {
			t.Fatalf("action-required Event payload = %#v", event.Payload)
		}
	}
	if actionEvents == 0 {
		t.Fatal("no action_required conclusion Event found")
	}
}

func TestAssistedConclusionValidRepairedResultAppliesNormally(t *testing.T) {
	server, projectID, created, session, _ := prepareInvalidConcludeTurn(t, []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"unexpected":true}`))
	waitForAssistedProviderRequests(t, session, 3)
	if err := emitAttemptResultAndComplete(t, session, []byte(validRepairResult)); err != nil {
		t.Fatalf("valid repaired result: %v", err)
	}
	found := waitForBlackboardConclusionState(t, server, projectID, created.ID, task.BlackboardConclusionStateClean)
	if found.BlackboardConclusion.AppliedRevision == nil {
		t.Fatalf("repaired result did not apply: %#v", found.BlackboardConclusion)
	}
}
