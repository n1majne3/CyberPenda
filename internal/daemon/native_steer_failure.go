package daemon

import (
	"context"
	"errors"
	"strings"

	"pentest/internal/owner"
	"pentest/internal/runtime"
)

// nativeSteerFailure is the single redacted failure taxonomy used by durable
// Steering state, Timeline projection, and operator recovery controls.
type nativeSteerFailure struct {
	state   owner.SteeringState
	reason  owner.SteeringFailureReason
	code    string
	message string
}

func classifyNativeSteerFailure(operationErr error) nativeSteerFailure {
	switch {
	case errors.Is(operationErr, context.DeadlineExceeded):
		return nativeSteerFailure{owner.SteeringActionRequired, owner.SteeringReasonDeliveryAmbiguous, string(owner.SteeringReasonDeliveryAmbiguous), "native steer timed out"}
	case errors.Is(operationErr, context.Canceled):
		return nativeSteerFailure{owner.SteeringActionRequired, owner.SteeringReasonDeliveryAmbiguous, string(owner.SteeringReasonDeliveryAmbiguous), "native steer canceled"}
	case errors.Is(operationErr, runtime.ErrProviderTurnUnavailable):
		return nativeSteerFailure{owner.SteeringActionRequired, owner.SteeringReasonTargetTurnCompleted, string(owner.SteeringReasonTargetTurnCompleted), "target provider Runtime Turn completed"}
	case errors.Is(operationErr, runtime.ErrProviderTurnChanged):
		return nativeSteerFailure{owner.SteeringActionRequired, owner.SteeringReasonTargetTurnChanged, string(owner.SteeringReasonTargetTurnChanged), "target provider Runtime Turn changed"}
	case errors.Is(operationErr, runtime.ErrProviderTurnNotSteerable):
		return nativeSteerFailure{owner.SteeringActionRequired, owner.SteeringReasonActiveTurnNotSteerable, string(owner.SteeringReasonActiveTurnNotSteerable), "active provider Runtime Turn is not steerable"}
	case errors.Is(operationErr, runtime.ErrProviderSessionClosed):
		return nativeSteerFailure{owner.SteeringFailed, owner.SteeringReasonSessionClosed, string(owner.SteeringReasonSessionClosed), "provider session is closed"}
	case errors.Is(operationErr, runtime.ErrProviderSessionControlConflict):
		return nativeSteerFailure{owner.SteeringFailed, owner.SteeringReasonControlConflict, string(owner.SteeringReasonControlConflict), "provider session control conflict"}
	case errors.Is(operationErr, runtime.ErrProviderMethodUnavailable):
		return nativeSteerFailure{owner.SteeringFailed, owner.SteeringReasonProviderControlUnavailable, string(owner.SteeringReasonProviderControlUnavailable), "provider native steering is unavailable"}
	}
	raw := ""
	if cause := errors.Unwrap(operationErr); cause != nil {
		raw = cause.Error()
	} else if operationErr != nil {
		raw = operationErr.Error()
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "effort") || strings.Contains(lower, "reasoning") {
		return nativeSteerFailure{owner.SteeringFailed, owner.SteeringReasonUnsupportedReasoningEffort, string(owner.SteeringReasonUnsupportedReasoningEffort), "requested reasoning effort is not supported"}
	}
	return nativeSteerFailure{owner.SteeringFailed, owner.SteeringReasonProviderRejected, string(owner.SteeringReasonProviderRejected), "provider rejected the turn"}
}
