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

// SemanticDebtWatermarks compare terminal Work Tool Results with the latest
// successful semantic persistence that covers them.
type SemanticDebtWatermarks struct {
	SourceWork          int
	SemanticPersistence int
}

func (watermarks SemanticDebtWatermarks) Valid() bool {
	return watermarks.SemanticPersistence >= 0 && watermarks.SourceWork >= watermarks.SemanticPersistence
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
