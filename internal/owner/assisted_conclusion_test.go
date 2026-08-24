package owner

import (
	"testing"
	"time"
)

func TestConclusionCheckpointInputNormalizesAndValidatesOwnerNeutralFields(t *testing.T) {
	input := ConclusionCheckpointInput{
		OwnerID: " task-1 ", ContinuationID: " continuation-1 ", SourceRequestID: " request-1 ",
		SourceSessionID: " session-1 ", SourceTurnID: " turn-1 ", ModelProviderID: " provider-1 ",
		Model: " model-1 ", ReasoningEffort: " high ",
		Watermarks: SemanticDebtWatermarks{SourceWork: 3, SemanticPersistence: 1},
	}
	normalized, valid := input.Normalize()
	if !valid || normalized.OwnerID != "task-1" || normalized.ContinuationID != "continuation-1" ||
		normalized.ModelProviderID != "provider-1" || normalized.ReasoningEffort != "high" {
		t.Fatalf("normalized checkpoint = %#v, valid=%v", normalized, valid)
	}
	input.Model = " "
	if _, valid := input.Normalize(); valid {
		t.Fatal("checkpoint without a model is valid")
	}
}

func TestConclusionProtocolRulesAreSharedAcrossOwners(t *testing.T) {
	state, phase := InitialConclusionCheckpoint(SemanticDebtWatermarks{SourceWork: 2, SemanticPersistence: 2})
	if state != BlackboardConclusionReceiptClean || phase != "persistence_current" {
		t.Fatalf("current checkpoint = %q, %q", state, phase)
	}
	state, phase = InitialConclusionCheckpoint(SemanticDebtWatermarks{SourceWork: 3, SemanticPersistence: 2})
	if state != BlackboardConclusionReceiptPending || phase != "pending_detected" {
		t.Fatalf("debt checkpoint = %q, %q", state, phase)
	}

	if !ConclusionRepairAllowed(BlackboardConclusionErrorInvalidResult, 1, 0, 0, 0) {
		t.Fatal("first invalid result must allow one bounded repair")
	}
	if ConclusionRepairAllowed(BlackboardConclusionErrorInvalidResult, 2, 0, 0, 0) ||
		ConclusionRepairAllowed(BlackboardConclusionErrorToolUseForbidden, 1, 0, 0, 0) {
		t.Fatal("repair rule exceeded its bounded protocol")
	}
	if !ConclusionVersionRegenerationAllowed(0, 1, 0) || ConclusionVersionRegenerationAllowed(1, 1, 0) {
		t.Fatal("version-regeneration rule did not preserve its one-turn budget")
	}
	if !ConclusionRecoveryEligible(BlackboardConclusionReceiptRepairDispatchRequested) ||
		ConclusionRecoveryEligible(BlackboardConclusionReceiptApplied) {
		t.Fatal("recovery eligibility accepted a terminal protocol state")
	}
}

func TestConclusionLineageIsStableAndUnambiguous(t *testing.T) {
	requestID, applyKey := ConclusionRequestLineage("ab", "c")
	otherRequestID, _ := ConclusionRequestLineage("a", "bc")
	if requestID == otherRequestID || requestID == "" || applyKey == "" {
		t.Fatalf("lineage request=%q other=%q apply=%q", requestID, otherRequestID, applyKey)
	}
	if got := ConclusionAttemptRequestID("repair", "ab", "c", 1, ""); got == requestID || got == "" {
		t.Fatalf("repair request id = %q", got)
	}
}

func TestConclusionFailureAndRetryDecisionsAreOwnerNeutral(t *testing.T) {
	if got := ConclusionFailureActionCode(BlackboardConclusionErrorInvalidResult, 1, 0); got != BlackboardConclusionErrorRepairExhausted {
		t.Fatalf("second invalid result code = %q", got)
	}
	code, reason := ConclusionWorkTurnConflictOutcome(BlackboardConclusionWorkTurnConflictLimit)
	if code != BlackboardConclusionErrorWorkTurnNeverSettled || reason != "work_turn_never_settled" {
		t.Fatalf("exhausted conflict outcome = %q, %q", code, reason)
	}
	now := time.Now().UTC()
	eligible := now.Add(-time.Second)
	if decision := ConclusionRetryDecisionFor(
		BlackboardConclusionReceiptActionRequired, BlackboardConclusionErrorInvalidResult, "", &eligible, now,
	); decision != ConclusionRetryAllowed {
		t.Fatalf("eligible retry decision = %q", decision)
	}
	if decision := ConclusionRetryDecisionFor(
		BlackboardConclusionReceiptActionRequired, BlackboardConclusionErrorWorkTurnNeverSettled, "", &eligible, now,
	); decision != ConclusionRetryNeverSettled {
		t.Fatalf("never-settled retry decision = %q", decision)
	}
	if decision := ConclusionRetryDecisionFor(
		BlackboardConclusionReceiptActionRequired, BlackboardConclusionErrorRuntimeRecoveryRequired,
		ConclusionRecoveryAcceptanceAmbiguous, &eligible, now,
	); decision != ConclusionRetryAcceptanceAmbiguous {
		t.Fatalf("ambiguous retry decision = %q", decision)
	}
}

func TestConclusionRecoveryRequiresRuntimeBinding(t *testing.T) {
	for _, reason := range []ConclusionRecoveryReason{
		ConclusionRecoveryRuntimeOwnershipNotProven,
		ConclusionRecoveryWritableReplacementUnavailable,
		ConclusionRecoveryDispatchFailed,
		ConclusionRecoveryLegacyCorrelationUnproven,
	} {
		if !ConclusionRecoveryRequiresRuntimeBinding(reason) {
			t.Fatalf("reason %q must require a proven replacement Runtime binding", reason)
		}
	}
	if ConclusionRecoveryRequiresRuntimeBinding(ConclusionRecoveryAcceptanceAmbiguous) {
		t.Fatal("acceptance_ambiguous must remain non-retriable, not Runtime-rebindable")
	}
}
