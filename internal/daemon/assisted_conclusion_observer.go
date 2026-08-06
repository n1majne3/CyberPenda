package daemon

import (
	"strings"

	"pentest/internal/runtime"
)

type assistedConclusionObservationHooks struct {
	ControlFailure  func()
	ControlTerminal func(string)
	WorkCompleted   func(runtime.AssistedConclusionObservedTurn, runtime.AssistedConclusionTurnKey, string) (bool, error)
	OnError         func(error)
}

type assistedConclusionRecoveryReceipt struct {
	State              string
	SendAttemptCount   int
	SendStarted        bool
	BaseRevision       *int
	ExplicitRetryCount int
}

type assistedConclusionRecoveryHooks struct {
	Pending     func()
	Dispatch    func(state string, baseRevision int, explicitRetryCount int)
	VersionSync func()
	Require     func()
}

// recoverLiveAssistedConclusion is the shared restart state machine for a
// proven-live owner Runtime. Owner adapters supply persistence and dispatch;
// the safety decisions about replayable and ambiguous states stay identical.
func recoverLiveAssistedConclusion(receipt assistedConclusionRecoveryReceipt, hooks assistedConclusionRecoveryHooks) {
	switch receipt.State {
	case "pending":
		hooks.Pending()
	case "dispatch_requested", "repair_dispatch_requested", "version_regeneration_dispatch_requested":
		if receipt.SendAttemptCount != 0 || receipt.SendStarted || receipt.BaseRevision == nil {
			hooks.Require()
			return
		}
		hooks.Dispatch(receipt.State, *receipt.BaseRevision, receipt.ExplicitRetryCount)
	case "version_sync_requested":
		hooks.VersionSync()
	case "awaiting_result":
		hooks.Require()
	default:
		hooks.Require()
	}
}

// observeAssistedConclusion is the owner-neutral Work/Control Turn detector.
// Task and Session adapters persist owner-local receipts, but tool
// classification, semantic-debt watermarks, terminal handling, and bounded
// tracker lifecycle are shared here.
func observeAssistedConclusion(tracker *runtime.AssistedConclusionTracker, ownerID, continuationID, providerID string, lineage runtime.ProviderSessionTurnLineage, observation runtime.ProviderSessionObservation, hooks assistedConclusionObservationHooks) {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(continuationID) == "" {
		return
	}
	if lineage.Kind == runtime.RuntimeTurnKindControl {
		switch observation.Kind {
		case runtime.ProviderSessionObservationToolUse, runtime.ProviderSessionObservationToolResult:
			if hooks.ControlFailure != nil {
				hooks.ControlFailure()
			}
		case runtime.ProviderSessionObservationTurnCompleted:
			if hooks.ControlTerminal != nil {
				hooks.ControlTerminal(observation.Status)
			}
		}
		return
	}
	if lineage.Kind != runtime.RuntimeTurnKindWork {
		return
	}
	turnID := strings.TrimSpace(observation.ProviderTurnID)
	if turnID == "" {
		return
	}
	key := runtime.AssistedConclusionTurnKey{
		OwnerID: ownerID, ContinuationID: continuationID,
		ProviderSessionID: strings.TrimSpace(providerID), TurnID: turnID,
	}
	if observation.Kind == runtime.ProviderSessionObservationToolResult {
		canonical, trusted := runtime.ClassifyTrustedBlackboardTool(observation.ToolName)
		if canonical != observation.BlackboardOperation {
			// Trusted origin is carried by the canonical identity, never by a
			// display-name match alone. An inconsistent observation is untrusted.
			trusted = false
		}
		tracker.RecordToolResult(key, strings.TrimSpace(observation.ToolCallID), !trusted,
			trusted && observation.Status == "succeeded" && canonical.CoversSourceWork())
		return
	}
	if observation.Kind != runtime.ProviderSessionObservationTurnCompleted {
		return
	}
	state, ok := tracker.SnapshotTurn(key)
	if !ok {
		return
	}
	if len(state.CompletedToolCalls) == 0 {
		tracker.DeleteTurn(key)
		return
	}
	remove, err := hooks.WorkCompleted(state, key, observation.Status)
	if err != nil {
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return
	}
	if remove {
		tracker.DeleteTurn(key)
	}
}
