package owner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// BlackboardConclusionReceiptState is the shared durable protocol state for
// one completed assisted Work Turn. Task and Session persist it in separate
// owner-local tables, but they must use the same transition vocabulary.
type BlackboardConclusionReceiptState string

const (
	BlackboardConclusionReceiptClean                                BlackboardConclusionReceiptState = "clean"
	BlackboardConclusionReceiptPending                              BlackboardConclusionReceiptState = "pending"
	BlackboardConclusionReceiptDispatchRequested                    BlackboardConclusionReceiptState = "dispatch_requested"
	BlackboardConclusionReceiptAwaitingResult                       BlackboardConclusionReceiptState = "awaiting_result"
	BlackboardConclusionReceiptRepairDispatchRequested              BlackboardConclusionReceiptState = "repair_dispatch_requested"
	BlackboardConclusionReceiptVersionSyncRequested                 BlackboardConclusionReceiptState = "version_sync_requested"
	BlackboardConclusionReceiptVersionRegenerationDispatchRequested BlackboardConclusionReceiptState = "version_regeneration_dispatch_requested"
	BlackboardConclusionReceiptActionRequired                       BlackboardConclusionReceiptState = "action_required"
	BlackboardConclusionReceiptValidated                            BlackboardConclusionReceiptState = "validated"
	BlackboardConclusionReceiptApplied                              BlackboardConclusionReceiptState = "applied"
)

// BlackboardConclusionErrorCode is the shared bounded error vocabulary for
// assisted conclusion recovery. An owner may expose the code through its own
// API type alias, but cannot invent a second semantic protocol.
type BlackboardConclusionErrorCode string

const (
	BlackboardConclusionErrorInvalidResult           BlackboardConclusionErrorCode = "semantic_conclusion_invalid_result"
	BlackboardConclusionErrorToolUseForbidden        BlackboardConclusionErrorCode = "conclude_tool_use_forbidden"
	BlackboardConclusionErrorRepairExhausted         BlackboardConclusionErrorCode = "semantic_conclusion_repair_exhausted"
	BlackboardConclusionErrorVersionConflict         BlackboardConclusionErrorCode = "semantic_conclusion_version_conflict"
	BlackboardConclusionErrorRuntimeRecoveryRequired BlackboardConclusionErrorCode = "semantic_conclusion_runtime_recovery_required"
	BlackboardConclusionErrorWorkTurnNeverSettled    BlackboardConclusionErrorCode = "semantic_conclusion_work_turn_never_settled"

	// The automatic repair and non-yielding Work Turn budgets are owner-neutral
	// protocol limits. They are deliberately not configurable per owner.
	BlackboardConclusionAutomaticTurnLimit    = 2
	BlackboardConclusionWorkTurnConflictLimit = 2
)

// SemanticDebtWatermarks compare terminal Work Tool Results with the latest
// successful semantic persistence that covers them.
type SemanticDebtWatermarks struct {
	SourceWork          int
	SemanticPersistence int
}

func (watermarks SemanticDebtWatermarks) Valid() bool {
	return watermarks.SemanticPersistence >= 0 && watermarks.SourceWork >= watermarks.SemanticPersistence
}

// ConclusionCheckpointInput is the owner-neutral input for one durable
// assisted-conclusion checkpoint. Owner adapters prove aggregate and
// Continuation ownership after this shared normalization step.
type ConclusionCheckpointInput struct {
	OwnerID         string
	ContinuationID  string
	SourceRequestID string
	SourceSessionID string
	SourceTurnID    string
	ModelProviderID string
	Model           string
	ReasoningEffort string
	Watermarks      SemanticDebtWatermarks
}

// Normalize removes transport whitespace and validates fields that have the
// same meaning for Task and Session owners.
func (input ConclusionCheckpointInput) Normalize() (ConclusionCheckpointInput, bool) {
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ContinuationID = strings.TrimSpace(input.ContinuationID)
	input.SourceRequestID = strings.TrimSpace(input.SourceRequestID)
	input.SourceSessionID = strings.TrimSpace(input.SourceSessionID)
	input.SourceTurnID = strings.TrimSpace(input.SourceTurnID)
	input.ModelProviderID = strings.TrimSpace(input.ModelProviderID)
	input.Model = strings.TrimSpace(input.Model)
	input.ReasoningEffort = strings.TrimSpace(input.ReasoningEffort)
	valid := input.OwnerID != "" && input.ContinuationID != "" && input.SourceRequestID != "" &&
		input.SourceSessionID != "" && input.SourceTurnID != "" && input.ModelProviderID != "" &&
		input.Model != "" && input.Watermarks.Valid()
	return input, valid
}

// InitialConclusionCheckpoint selects the shared initial obligation state and
// Timeline phase from semantic-debt watermarks.
func InitialConclusionCheckpoint(watermarks SemanticDebtWatermarks) (BlackboardConclusionReceiptState, string) {
	if watermarks.SourceWork <= watermarks.SemanticPersistence {
		return BlackboardConclusionReceiptClean, "persistence_current"
	}
	return BlackboardConclusionReceiptPending, "pending_detected"
}

// ConclusionRepairAllowed applies the one-repair automatic-turn budget.
func ConclusionRepairAllowed(code BlackboardConclusionErrorCode, automaticTurns, repairs, versionRegenerations, explicitRetries int) bool {
	return code == BlackboardConclusionErrorInvalidResult && automaticTurns < BlackboardConclusionAutomaticTurnLimit &&
		repairs == 0 && versionRegenerations == 0 && explicitRetries == 0
}

// ConclusionVersionRegenerationAllowed applies the one-regeneration
// automatic-turn budget.
func ConclusionVersionRegenerationAllowed(versionRegenerations, automaticTurns, explicitRetries int) bool {
	return versionRegenerations == 0 && automaticTurns < BlackboardConclusionAutomaticTurnLimit && explicitRetries == 0
}

// ConclusionRecoveryEligible reports whether a pre-apply obligation can move
// to action_required during fail-closed recovery.
func ConclusionRecoveryEligible(state BlackboardConclusionReceiptState) bool {
	switch state {
	case BlackboardConclusionReceiptPending, BlackboardConclusionReceiptDispatchRequested,
		BlackboardConclusionReceiptRepairDispatchRequested, BlackboardConclusionReceiptVersionSyncRequested,
		BlackboardConclusionReceiptVersionRegenerationDispatchRequested, BlackboardConclusionReceiptAwaitingResult:
		return true
	default:
		return false
	}
}

// ConclusionFailureActionCode maps a terminal failed result to its stable
// operator-visible error code.
func ConclusionFailureActionCode(code BlackboardConclusionErrorCode, repairs, versionRegenerations int) BlackboardConclusionErrorCode {
	if code == BlackboardConclusionErrorInvalidResult && repairs > 0 && versionRegenerations == 0 {
		return BlackboardConclusionErrorRepairExhausted
	}
	return code
}

// ConclusionWorkTurnConflictOutcome maps the shared conflict budget to its
// stable error code and Timeline reason.
func ConclusionWorkTurnConflictOutcome(explicitRetries int) (BlackboardConclusionErrorCode, string) {
	if explicitRetries >= BlackboardConclusionWorkTurnConflictLimit {
		return BlackboardConclusionErrorWorkTurnNeverSettled, "work_turn_never_settled"
	}
	return BlackboardConclusionErrorRuntimeRecoveryRequired, "work_turn_conflict"
}

// ConclusionRetryDecision is the shared validation result for an
// operator-authorized conclusion retry.
type ConclusionRetryDecision string

const (
	ConclusionRetryAllowed             ConclusionRetryDecision = "allowed"
	ConclusionRetryInvalidState        ConclusionRetryDecision = "invalid_state"
	ConclusionRetryNeverSettled        ConclusionRetryDecision = "never_settled"
	ConclusionRetryAcceptanceAmbiguous ConclusionRetryDecision = "acceptance_ambiguous"
	ConclusionRetryCooldown            ConclusionRetryDecision = "cooldown"
)

// ConclusionRetryDecisionFor validates retry state, terminal reasons, and the
// durable cooldown without owner-specific storage concerns.
func ConclusionRetryDecisionFor(state BlackboardConclusionReceiptState, code BlackboardConclusionErrorCode, recoveryReason ConclusionRecoveryReason, nextEligibleAt *time.Time, now time.Time) ConclusionRetryDecision {
	if state != BlackboardConclusionReceiptActionRequired {
		return ConclusionRetryInvalidState
	}
	if code == BlackboardConclusionErrorWorkTurnNeverSettled {
		return ConclusionRetryNeverSettled
	}
	if recoveryReason == ConclusionRecoveryAcceptanceAmbiguous {
		return ConclusionRetryAcceptanceAmbiguous
	}
	if nextEligibleAt == nil || now.Before(*nextEligibleAt) {
		return ConclusionRetryCooldown
	}
	return ConclusionRetryAllowed
}

// ConclusionRequestLineage derives the initial dispatch and apply keys from
// length-framed provider correlation values.
func ConclusionRequestLineage(continuationID, sourceTurnID string) (string, string) {
	lineage := fmt.Sprintf("%d:%s%d:%s", len(continuationID), continuationID, len(sourceTurnID), sourceTurnID)
	digest := CanonicalConclusionSHA256([]byte(lineage))
	return "conclude:v1:" + digest, "assisted-apply:v1:" + digest
}

// ConclusionAttemptRequestID derives a stable identity for a repair, version,
// retry, or recovery dispatch.
func ConclusionAttemptRequestID(kind, continuationID, sourceTurnID string, number int, key string) string {
	lineage := fmt.Sprintf("%s:%d:%s:%d:%s:%d:%s", kind, len(continuationID), continuationID, len(sourceTurnID), sourceTurnID, number, key)
	return "conclude-" + kind + ":v1:" + CanonicalConclusionSHA256([]byte(lineage))
}

// CanonicalConclusionSHA256 returns the shared lower-case content digest.
func CanonicalConclusionSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// ConclusionValidationDetail is the bounded public reason for one rejected
// closed conclusion result. The reason is a closed token and the field path
// and expected form are static or bounded strings; raw provider output,
// decoder text, and model reasoning never enter this detail.
type ConclusionValidationDetail struct {
	Reason    string
	FieldPath string
	Expected  string
}

// Valid reports whether the detail carries a bounded reason.
func (detail ConclusionValidationDetail) Valid() bool {
	return strings.TrimSpace(detail.Reason) != ""
}

// AppendConclusionValidationEventPayload adds the bounded rejection detail to
// a Timeline Event payload when present. Raw provider output, decoder text,
// and reasoning never enter Timeline entries.
func AppendConclusionValidationEventPayload(payload map[string]any, detail ConclusionValidationDetail) {
	if detail.Reason != "" {
		payload["validation_reason"] = detail.Reason
	}
	if detail.FieldPath != "" {
		payload["validation_field_path"] = detail.FieldPath
	}
	if detail.Expected != "" {
		payload["validation_expected"] = detail.Expected
	}
}

// BlackboardConclusionReceiptStateAfter reports whether current is a durable
// forward state relative to target. The repair/version states intentionally
// remain outside this compact replay ordering; their transitions are claimed
// by their explicit owner-local operations.
func BlackboardConclusionReceiptStateAfter(current, target BlackboardConclusionReceiptState) bool {
	order := map[BlackboardConclusionReceiptState]int{
		BlackboardConclusionReceiptPending:           0,
		BlackboardConclusionReceiptDispatchRequested: 1,
		BlackboardConclusionReceiptAwaitingResult:    2,
		BlackboardConclusionReceiptValidated:         3,
		BlackboardConclusionReceiptApplied:           4,
	}
	return order[current] > order[target]
}

func ValidBlackboardConclusionErrorCode(code BlackboardConclusionErrorCode) bool {
	switch code {
	case BlackboardConclusionErrorInvalidResult, BlackboardConclusionErrorToolUseForbidden,
		BlackboardConclusionErrorRepairExhausted, BlackboardConclusionErrorVersionConflict,
		BlackboardConclusionErrorRuntimeRecoveryRequired, BlackboardConclusionErrorWorkTurnNeverSettled:
		return true
	default:
		return false
	}
}

// ConclusionDispatchKind is the immutable attempt category of one Conclusion
// Dispatch. Recovery-created dispatches are always kind recovery; repair,
// version regeneration, and operator retry dispatches keep their own kind.
type ConclusionDispatchKind string

const (
	ConclusionDispatchKindInitial             ConclusionDispatchKind = "initial"
	ConclusionDispatchKindRepair              ConclusionDispatchKind = "repair"
	ConclusionDispatchKindVersionRegeneration ConclusionDispatchKind = "version_regeneration"
	ConclusionDispatchKindRetry               ConclusionDispatchKind = "retry"
	ConclusionDispatchKindRecovery            ConclusionDispatchKind = "recovery"
)

// ConclusionDispatchState is the immutable attempt's own delivery lifecycle.
// Only dispatch_requested, awaiting_result, and validated are active: the
// storage partial unique index permits at most one active dispatch per
// obligation. Superseded and late_terminal are terminal delivery outcomes that
// can never advance the obligation or Blackboard.
type ConclusionDispatchState string

const (
	ConclusionDispatchRequested      ConclusionDispatchState = "dispatch_requested"
	ConclusionDispatchAwaitingResult ConclusionDispatchState = "awaiting_result"
	ConclusionDispatchValidated      ConclusionDispatchState = "validated"
	ConclusionDispatchApplied        ConclusionDispatchState = "applied"
	ConclusionDispatchActionRequired ConclusionDispatchState = "action_required"
	ConclusionDispatchSuperseded     ConclusionDispatchState = "superseded"
	ConclusionDispatchLateTerminal   ConclusionDispatchState = "late_terminal"
)

// ConclusionDispatchActive reports whether a delivery state is the active
// protocol position for its obligation. Only these states participate in the
// at-most-one-active storage constraint and may validate or apply results.
func ConclusionDispatchActive(state ConclusionDispatchState) bool {
	switch state {
	case ConclusionDispatchRequested, ConclusionDispatchAwaitingResult, ConclusionDispatchValidated:
		return true
	default:
		return false
	}
}

// ConclusionRecoveryReason is the closed operator-visible reason vocabulary for
// fail-closed conclusion recovery. Raw runtime errors never enter storage; the
// daemon maps each failed recovery path to exactly one bounded token.
type ConclusionRecoveryReason string

const (
	// ConclusionRecoveryRuntimeOwnershipNotProven means the daemon could not
	// prove live ownership of the current Task-scoped Runtime, so it must not
	// create a replacement Conclusion Dispatch (ADR 0021).
	ConclusionRecoveryRuntimeOwnershipNotProven ConclusionRecoveryReason = "runtime_ownership_not_proven"
	// ConclusionRecoveryWritableReplacementUnavailable means the owner has no
	// writable replacement Runtime Continuation for a new dispatch.
	ConclusionRecoveryWritableReplacementUnavailable ConclusionRecoveryReason = "writable_replacement_unavailable"
	// ConclusionRecoveryAcceptanceAmbiguous means a provider request may have
	// been accepted; the obligation is never replayed or regenerated
	// automatically after a durable send-start fence.
	ConclusionRecoveryAcceptanceAmbiguous ConclusionRecoveryReason = "acceptance_ambiguous"
	// ConclusionRecoveryDispatchFailed means the active dispatch could not be
	// delivered and no safe replay exists.
	ConclusionRecoveryDispatchFailed ConclusionRecoveryReason = "dispatch_failed"
	// ConclusionRecoveryLegacyCorrelationUnproven means a legacy receipt's
	// source correlation cannot be proven and no ownership probe may run.
	ConclusionRecoveryLegacyCorrelationUnproven ConclusionRecoveryReason = "legacy_correlation_unproven"
)

// ValidConclusionRecoveryReason reports whether a reason is a closed token.
func ValidConclusionRecoveryReason(reason ConclusionRecoveryReason) bool {
	switch reason {
	case ConclusionRecoveryRuntimeOwnershipNotProven, ConclusionRecoveryWritableReplacementUnavailable,
		ConclusionRecoveryAcceptanceAmbiguous, ConclusionRecoveryDispatchFailed,
		ConclusionRecoveryLegacyCorrelationUnproven:
		return true
	default:
		return false
	}
}

// ConclusionRecoveryRequiresRuntimeBinding reports whether an operator retry
// must create a new dispatch on a proven live replacement Runtime. An
// acceptance-ambiguous delivery is deliberately excluded because it cannot be
// retried safely at all.
func ConclusionRecoveryRequiresRuntimeBinding(reason ConclusionRecoveryReason) bool {
	switch reason {
	case ConclusionRecoveryRuntimeOwnershipNotProven,
		ConclusionRecoveryWritableReplacementUnavailable,
		ConclusionRecoveryDispatchFailed,
		ConclusionRecoveryLegacyCorrelationUnproven:
		return true
	default:
		return false
	}
}
