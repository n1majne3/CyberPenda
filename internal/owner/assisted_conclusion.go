package owner

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

// ConclusionValidationDetail is the bounded public reason for one rejected
// closed conclusion result. The reason is a closed token and the field path
// and expected form are static or bounded strings; raw provider output,
// decoder text, and model reasoning never enter this detail.
type ConclusionValidationDetail struct {
	Reason    string
	FieldPath string
	Expected  string
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

// ConclusionDirectiveKind is the semantic control instruction carried by one
// Conclusion Dispatch. Runtime recovery changes the immutable delivery
// lineage, but it does not change this instruction.
type ConclusionDirectiveKind string

const (
	ConclusionDirectiveKindInitial             ConclusionDirectiveKind = "initial"
	ConclusionDirectiveKindRepair              ConclusionDirectiveKind = "repair"
	ConclusionDirectiveKindVersionRegeneration ConclusionDirectiveKind = "version_regeneration"
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
