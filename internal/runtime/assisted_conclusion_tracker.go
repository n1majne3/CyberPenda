package runtime

import (
	"sync"

	"pentest/internal/owner"
)

// AssistedConclusionTurnKey identifies one owner-local Work Turn. The owner
// ID is mandatory so a provider callback can never drain another aggregate's
// in-flight state even when provider request IDs collide in a test or adapter.
type AssistedConclusionTurnKey struct {
	OwnerID           string
	ContinuationID    string
	ProviderSessionID string
	TurnID            string
}

// AssistedConclusionObservedTurn is the bounded in-memory watermark window
// between provider Tool Results and the durable completed-Turn receipt.
type AssistedConclusionObservedTurn struct {
	SourceWorkWatermark          int
	SemanticPersistenceWatermark int
	CompletedToolCalls           map[string]struct{}
}

type AssistedConclusionQueuedResult struct {
	OwnerID string
	Result  ProviderSessionAttemptResult
}

type AssistedConclusionQueuedFailure struct {
	OwnerID           string
	ProviderSessionID string
	ProviderTurnID    string
	Code              string
	// Detail is the bounded public reason for a rejected closed result. It
	// never carries provider bytes, decoder text, or reasoning.
	Detail owner.ConclusionValidationDetail
}

type AssistedConclusionQueuedTerminal struct {
	OwnerID           string
	ProviderSessionID string
	ProviderTurnID    string
}

type assistedConclusionRequestKey struct {
	OwnerID   string
	RequestID string
}

// AssistedConclusionTracker is the shared owner-neutral callback protocol.
// Durable receipt state remains in the Task or Session service; this tracker
// only holds bounded observations until the owner-specific service commits
// them and callback payloads until the canonical control Turn is complete.
type AssistedConclusionTracker struct {
	mu       sync.Mutex
	turns    map[AssistedConclusionTurnKey]AssistedConclusionObservedTurn
	results  map[assistedConclusionRequestKey]AssistedConclusionQueuedResult
	failures map[assistedConclusionRequestKey]AssistedConclusionQueuedFailure
	terminal map[assistedConclusionRequestKey]AssistedConclusionQueuedTerminal
}

func NewAssistedConclusionTracker() *AssistedConclusionTracker {
	return &AssistedConclusionTracker{
		turns:    make(map[AssistedConclusionTurnKey]AssistedConclusionObservedTurn),
		results:  make(map[assistedConclusionRequestKey]AssistedConclusionQueuedResult),
		failures: make(map[assistedConclusionRequestKey]AssistedConclusionQueuedFailure),
		terminal: make(map[assistedConclusionRequestKey]AssistedConclusionQueuedTerminal),
	}
}

// RecordToolResult folds one provider Tool Result into the current bounded
// Work Turn. A semanticPersistence result sets the persistence watermark to
// the current source-work watermark; callers classify provider tool names
// before invoking this method.
func (tracker *AssistedConclusionTracker) RecordToolResult(key AssistedConclusionTurnKey, toolCallID string, sourceWork bool, semanticPersistence bool) (AssistedConclusionObservedTurn, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for observedKey := range tracker.turns {
		if observedKey.OwnerID == key.OwnerID && observedKey.ContinuationID == key.ContinuationID &&
			observedKey.ProviderSessionID == key.ProviderSessionID && observedKey.TurnID != key.TurnID {
			delete(tracker.turns, observedKey)
		}
	}
	state := tracker.turns[key]
	if state.CompletedToolCalls == nil {
		state.CompletedToolCalls = make(map[string]struct{})
	}
	if _, duplicate := state.CompletedToolCalls[toolCallID]; duplicate {
		return cloneObservedTurn(state), false
	}
	state.CompletedToolCalls[toolCallID] = struct{}{}
	if sourceWork {
		state.SourceWorkWatermark++
	} else if semanticPersistence {
		state.SemanticPersistenceWatermark = state.SourceWorkWatermark
	}
	tracker.turns[key] = state
	return cloneObservedTurn(state), true
}

func (tracker *AssistedConclusionTracker) SnapshotTurn(key AssistedConclusionTurnKey) (AssistedConclusionObservedTurn, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	state, ok := tracker.turns[key]
	if ok {
		state.CompletedToolCalls = cloneToolCallSet(state.CompletedToolCalls)
	}
	return state, ok
}

// ActiveWorkTurn returns the latest observed Work Turn for one owner and
// continuation, plus its source-work watermark. The boolean is false when no
// Work Turn has been observed yet. It is the daemon-owned provenance source for
// a Blackboard Finish Intent (ADR 0022): the caller never trusts provider input.
func (tracker *AssistedConclusionTracker) ActiveWorkTurn(ownerID, continuationID string) (AssistedConclusionTurnKey, AssistedConclusionObservedTurn, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	var match AssistedConclusionTurnKey
	var state AssistedConclusionObservedTurn
	found := false
	for key, observed := range tracker.turns {
		if key.OwnerID != ownerID || key.ContinuationID != continuationID {
			continue
		}
		if !found || key.TurnID > match.TurnID {
			match = key
			state = observed
			found = true
		}
	}
	if found {
		state.CompletedToolCalls = cloneToolCallSet(state.CompletedToolCalls)
	}
	return match, state, found
}

func (tracker *AssistedConclusionTracker) DeleteTurn(key AssistedConclusionTurnKey) {
	tracker.mu.Lock()
	delete(tracker.turns, key)
	tracker.mu.Unlock()
}

func (tracker *AssistedConclusionTracker) DeleteOwner(ownerID string) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for key := range tracker.turns {
		if key.OwnerID == ownerID {
			delete(tracker.turns, key)
		}
	}
	for key, queued := range tracker.results {
		if queued.OwnerID == ownerID {
			delete(tracker.results, key)
		}
	}
	for key, queued := range tracker.failures {
		if queued.OwnerID == ownerID {
			delete(tracker.failures, key)
		}
	}
	for key, queued := range tracker.terminal {
		if queued.OwnerID == ownerID {
			delete(tracker.terminal, key)
		}
	}
}

func (tracker *AssistedConclusionTracker) QueueResult(ownerID string, result ProviderSessionAttemptResult) {
	tracker.mu.Lock()
	key := assistedConclusionRequestKey{OwnerID: ownerID, RequestID: result.RequestID}
	if _, exists := tracker.results[key]; !exists {
		tracker.results[key] = AssistedConclusionQueuedResult{OwnerID: ownerID, Result: result}
	}
	tracker.mu.Unlock()
}

func (tracker *AssistedConclusionTracker) TakeActionableResult(ownerID, requestID string) (ProviderSessionAttemptResult, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	key := assistedConclusionRequestKey{OwnerID: ownerID, RequestID: requestID}
	queued, ok := tracker.results[key]
	if !ok || queued.OwnerID != ownerID {
		return ProviderSessionAttemptResult{}, false
	}
	terminal := tracker.terminal[key]
	if terminal.OwnerID != ownerID || terminal.ProviderSessionID != queued.Result.SessionID || terminal.ProviderTurnID != queued.Result.ProviderTurnID {
		return ProviderSessionAttemptResult{}, false
	}
	delete(tracker.results, key)
	return queued.Result, true
}

func (tracker *AssistedConclusionTracker) QueueFailure(requestID string, failure AssistedConclusionQueuedFailure) {
	tracker.mu.Lock()
	key := assistedConclusionRequestKey{OwnerID: failure.OwnerID, RequestID: requestID}
	prior, exists := tracker.failures[key]
	if !exists || prior == failure || assistedConclusionFailurePriority(failure.Code) > assistedConclusionFailurePriority(prior.Code) {
		tracker.failures[key] = failure
	}
	tracker.mu.Unlock()
}

func (tracker *AssistedConclusionTracker) TakeActionableFailure(ownerID, requestID string) (AssistedConclusionQueuedFailure, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	key := assistedConclusionRequestKey{OwnerID: ownerID, RequestID: requestID}
	queued, ok := tracker.failures[key]
	if !ok || queued.OwnerID != ownerID {
		return AssistedConclusionQueuedFailure{}, false
	}
	terminal := tracker.terminal[key]
	terminalMatches := terminal.OwnerID == queued.OwnerID && terminal.ProviderSessionID == queued.ProviderSessionID && terminal.ProviderTurnID == queued.ProviderTurnID
	if queued.Code != string(owner.BlackboardConclusionErrorToolUseForbidden) && !terminalMatches {
		return AssistedConclusionQueuedFailure{}, false
	}
	return queued, true
}

func (tracker *AssistedConclusionTracker) MarkTerminal(requestID string, terminal AssistedConclusionQueuedTerminal) {
	tracker.mu.Lock()
	key := assistedConclusionRequestKey{OwnerID: terminal.OwnerID, RequestID: requestID}
	tracker.terminal[key] = terminal
	tracker.mu.Unlock()
}

func (tracker *AssistedConclusionTracker) ClearRequest(ownerID, requestID string) {
	tracker.mu.Lock()
	key := assistedConclusionRequestKey{OwnerID: ownerID, RequestID: requestID}
	delete(tracker.results, key)
	delete(tracker.failures, key)
	delete(tracker.terminal, key)
	tracker.mu.Unlock()
}

// HasRequest is intentionally a narrow diagnostic used by owner-level tests
// to prove callback state is cleared after a terminal conclusion.
func (tracker *AssistedConclusionTracker) HasRequest(ownerID, requestID string) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	key := assistedConclusionRequestKey{OwnerID: ownerID, RequestID: requestID}
	_, result := tracker.results[key]
	_, failure := tracker.failures[key]
	_, terminal := tracker.terminal[key]
	return result || failure || terminal
}

func cloneToolCallSet(calls map[string]struct{}) map[string]struct{} {
	if calls == nil {
		return nil
	}
	clone := make(map[string]struct{}, len(calls))
	for callID := range calls {
		clone[callID] = struct{}{}
	}
	return clone
}

func cloneObservedTurn(state AssistedConclusionObservedTurn) AssistedConclusionObservedTurn {
	state.CompletedToolCalls = cloneToolCallSet(state.CompletedToolCalls)
	return state
}

func assistedConclusionFailurePriority(code string) int {
	switch code {
	case string(owner.BlackboardConclusionErrorToolUseForbidden):
		return 3
	case string(owner.BlackboardConclusionErrorRuntimeRecoveryRequired):
		return 2
	default:
		return 1
	}
}
