package daemon

import (
	"context"
	"errors"
	"sync"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/task"
)

// steerEventVocabulary names the owner-local Timeline projection of the shared
// steer protocol. Owners differ only in event kind and phase; the payloads and
// control flow are shared.
type steerEventVocabulary struct {
	failureKind    task.EventKind
	failurePhase   string
	ambiguousPhase string
	appliedKind    task.EventKind
	appliedPhase   string
}

var taskSteerEventVocabulary = steerEventVocabulary{
	failureKind:    task.EventKindSteering,
	failurePhase:   "steering_failed",
	ambiguousPhase: "steering_action_required",
	appliedKind:    task.EventKindSteering,
	appliedPhase:   "steering_applied",
}

var sessionSteerEventVocabulary = steerEventVocabulary{
	failureKind:    task.EventKindLifecycle,
	failurePhase:   "provider_turn_failed",
	ambiguousPhase: "provider_turn_failed",
	appliedKind:    task.EventKindConversation,
	appliedPhase:   "provider_turn_applied",
}

// steerEmitProtocolSpec binds one Runtime Owner to the shared emit protocol:
// provider events persist bound to the current Runtime Continuation, and an
// interrupt_then_replace steering settlement advances the Continuation under
// the same mutex so the next provider event lands on the replacement.
type steerEmitProtocolSpec struct {
	mode runtime.ProviderSessionMode
	// advanceOnSettled is false when the Continuation was created fresh by this
	// dispatch and must not be advanced again.
	advanceOnSettled bool
	// persistEvent stores one event bound to the given Continuation id. The
	// empty id means the owner has no Continuation.
	persistEvent func(continuationID string, kind task.EventKind, payload task.EventPayload)
	// persistOwnerEvent stores one owner-level event when no Continuation is
	// bound (legacy owners without continuation authority).
	persistOwnerEvent func(kind task.EventKind, payload task.EventPayload)
	// advance settles the old Continuation and returns the replacement id.
	advance func(currentID string) (string, error)
	// onAdvanceSettled projects the owner's replacement boundary events; it
	// runs under the emit mutex with the outcome of advance.
	onAdvanceSettled func(currentID, replacementID string, advanceErr error)
}

type steerEmitProtocol struct {
	emit          func(kind task.EventKind, payload task.EventPayload)
	currentID     func() string
	transitionErr func() error
}

func newSteerEmitProtocol(initialContinuationID string, spec steerEmitProtocolSpec) steerEmitProtocol {
	var continuationMu sync.Mutex
	currentContinuationID := initialContinuationID
	var transitionErr error
	currentID := func() string {
		continuationMu.Lock()
		defer continuationMu.Unlock()
		return currentContinuationID
	}
	emit := func(kind task.EventKind, payload task.EventPayload) {
		continuationMu.Lock()
		current := currentContinuationID
		spec.persistEvent(current, kind, payload)
		if transitionErr == nil && spec.advanceOnSettled &&
			spec.mode == runtime.ProviderSessionModeInterruptThenReplace &&
			kind == task.EventKindSteering && payloadOutcome(payload) == "settled" && current != "" {
			next, advanceErr := spec.advance(current)
			if advanceErr != nil {
				transitionErr = advanceErr
			} else {
				currentContinuationID = next
			}
			if spec.onAdvanceSettled != nil {
				spec.onAdvanceSettled(current, next, advanceErr)
			}
		}
		continuationMu.Unlock()
		if current == "" && spec.persistOwnerEvent != nil {
			spec.persistOwnerEvent(kind, payload)
		}
	}
	return steerEmitProtocol{
		emit: emit, currentID: currentID,
		transitionErr: func() error {
			continuationMu.Lock()
			defer continuationMu.Unlock()
			return transitionErr
		},
	}
}

// steerExecutionSpec binds one Runtime Owner to the shared native steer
// executor. The executor owns the owner-neutral delivery protocol: the request
// is always lineage-assigned as a Harness Control Turn (ADR 0018), events
// persist through the owner sink, a lost Continuation transition fails closed,
// and a post-fence cancellation settles action_required instead of replaying.
type steerExecutionSpec struct {
	operation             nativeSteerOperationFunc
	request               runtime.ProviderSessionRequest
	mode                  runtime.ProviderSessionMode
	providerSessionID     string
	initialContinuationID string
	advanceOnSettled      bool
	vocabulary            steerEventVocabulary
	// persistEvent stores one event bound to the given Continuation; an empty
	// id falls back to persistOwnerEvent.
	persistEvent      func(continuationID string, kind task.EventKind, payload task.EventPayload)
	persistOwnerEvent func(kind task.EventKind, payload task.EventPayload)
	advance           func(currentID string) (string, error)
	onAdvanceSettled  func(currentID, replacementID string, advanceErr error)
	// failClosed closes the owner's provider session and fails the Continuation
	// (and the owner itself when it has a lifecycle status) after a transition
	// failure.
	failClosed func(currentID string)
	// failOwnerExecution drains the owner failure through the Accepted
	// Steering dispatcher (Task owners fail their owner status).
	failOwnerExecution bool
}

// runNativeSteerTurn delivers one native steer operation under the shared
// owner-neutral protocol and returns the durable terminal outcome. The caller
// owns provider control for the whole operation.
func runNativeSteerTurn(ctx context.Context, spec steerExecutionSpec) steeringExecution {
	// The Harness owns the closed work|control Runtime Turn kind and assigns it
	// from request lineage; provider payloads cannot choose or override it
	// (ADR 0018).
	request := spec.request
	request.TurnKind = runtime.RuntimeTurnKindControl

	protocol := newSteerEmitProtocol(spec.initialContinuationID, steerEmitProtocolSpec{
		mode:              spec.mode,
		advanceOnSettled:  spec.advanceOnSettled,
		persistEvent:      spec.persistEvent,
		persistOwnerEvent: spec.persistOwnerEvent,
		advance:           spec.advance,
		onAdvanceSettled:  spec.onAdvanceSettled,
	})
	result, operationErr := spec.operation(ctx, request, protocol.emit)
	// A failed Continuation transition is fail-closed even when the provider
	// operation also failed: the delivery authority for later events is gone.
	if transitionErr := protocol.transitionErr(); transitionErr != nil {
		if spec.failClosed != nil {
			spec.failClosed(protocol.currentID())
		}
		return steeringExecution{
			state: owner.SteeringFailed, reason: owner.SteeringReasonContinuationUnavailable,
			message: transitionErr.Error(), failOwner: spec.failOwnerExecution,
		}
	}
	if operationErr != nil {
		errorCode, errorMessage := nativeSteerFailurePresentation(operationErr)
		if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
			// Post-fence with no provider outcome: delivery is ambiguous. The
			// request is never replayed automatically; it settles
			// action_required with a reason-specific recovery path.
			protocol.emit(spec.vocabulary.failureKind, task.EventPayload{
				"request_id": request.RequestID, "session_id": spec.providerSessionID, "mode": string(spec.mode),
				"outcome": "action_required", "phase": spec.vocabulary.ambiguousPhase,
				"error_code": string(owner.SteeringReasonDeliveryAmbiguous), "error": errorMessage,
				"model_provider_id": request.ModelProviderID, "model": request.Model,
				"requested_reasoning_effort": request.RequestedReasoningEffort,
			})
			return steeringExecution{state: owner.SteeringActionRequired, reason: owner.SteeringReasonDeliveryAmbiguous, message: errorMessage}
		}
		// Public Events carry only redacted, stable failure fields. Raw
		// provider text stays out of the conversation surface; each owner's
		// persistence sink applies its own allowlist.
		protocol.emit(spec.vocabulary.failureKind, task.EventPayload{
			"request_id": request.RequestID, "session_id": spec.providerSessionID, "mode": string(spec.mode),
			"outcome": "failed", "phase": spec.vocabulary.failurePhase, "error_code": errorCode,
			"error":             errorMessage,
			"model_provider_id": request.ModelProviderID, "model": request.Model,
			"requested_reasoning_effort": request.RequestedReasoningEffort,
		})
		return steeringExecution{state: owner.SteeringFailed, reason: steerReasonFromFailureCode(errorCode), message: errorMessage}
	}
	payload := result.Payload()
	payload["outcome"] = "applied"
	payload["phase"] = spec.vocabulary.appliedPhase
	// The provider result is a structured Runtime turn result, not a
	// transcript message. Conversation contains only explicit user/runtime
	// messages; provider control/result data stays on the owner Timeline.
	protocol.emit(spec.vocabulary.appliedKind, payload)
	return steeringExecution{state: owner.SteeringApplied, result: result.Payload()}
}
