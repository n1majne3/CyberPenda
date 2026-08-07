package owner

import "time"

// SteeringState is the shared durable protocol state for one Accepted Steering
// request. Task and Session owners persist the same state vocabulary in one
// owner-neutral table; the Runtime Harness drives every transition.
type SteeringState string

const (
	// SteeringPending means the request is durably accepted and never reached
	// the irreversible provider delivery step. Recovery may dispatch it.
	SteeringPending SteeringState = "pending"
	// SteeringDispatchStarted means the durable send-start fence was recorded
	// immediately before provider control. A request in this state with no
	// terminal outcome is delivery-ambiguous and is never replayed.
	SteeringDispatchStarted SteeringState = "dispatch_started"
	// SteeringApplied is the terminal delivered outcome.
	SteeringApplied SteeringState = "applied"
	// SteeringFailed is a terminal outcome with a stable reason; the request
	// was never delivered or the provider rejected it deterministically.
	SteeringFailed SteeringState = "failed"
	// SteeringActionRequired is a terminal operator-actionable outcome. It is
	// used when delivery is ambiguous (post-fence with no durable outcome) and
	// exposes only reason-specific safe actions, never a generic Retry.
	SteeringActionRequired SteeringState = "action_required"
)

// Terminal reports whether the state is a durable terminal outcome.
func (state SteeringState) Terminal() bool {
	return state == SteeringApplied || state == SteeringFailed || state == SteeringActionRequired
}

// ValidSteeringState reports whether state is a known protocol state.
func ValidSteeringState(state SteeringState) bool {
	switch state {
	case SteeringPending, SteeringDispatchStarted, SteeringApplied, SteeringFailed, SteeringActionRequired:
		return true
	default:
		return false
	}
}

// SteeringMode is the provider-native steer operation chosen at acceptance.
type SteeringMode string

const (
	SteeringModeInTurnSteer         SteeringMode = "in_turn_steer"
	SteeringModeInterruptThenReplace SteeringMode = "interrupt_then_replace"
)

// SteeringFailureReason is the closed reason vocabulary for Steering failure
// and action-required outcomes. The owner boundary rejects arbitrary reason
// strings so the UI can present stable, bounded recovery actions.
type SteeringFailureReason string

const (
	// SteeringReasonProviderControlUnavailable means the provider control queue
	// was closed (for example during daemon shutdown) before dispatch.
	SteeringReasonProviderControlUnavailable SteeringFailureReason = "provider_control_unavailable"
	// SteeringReasonOwnerStateChanged means the owner left the active state
	// (stopped, failed, completed, archived) before the request could dispatch.
	SteeringReasonOwnerStateChanged SteeringFailureReason = "owner_state_changed"
	// SteeringReasonContinuationUnavailable means no writable Runtime
	// Continuation could be resolved for delivery.
	SteeringReasonContinuationUnavailable SteeringFailureReason = "continuation_unavailable"
	// SteeringReasonProviderRejected means the provider deterministically
	// rejected the steer operation.
	SteeringReasonProviderRejected SteeringFailureReason = "provider_rejected"
	// SteeringReasonUnsupportedReasoningEffort means the provider rejected the
	// requested reasoning effort.
	SteeringReasonUnsupportedReasoningEffort SteeringFailureReason = "unsupported_reasoning_effort"
	// SteeringReasonSessionClosed means the provider session was closed before
	// delivery completed.
	SteeringReasonSessionClosed SteeringFailureReason = "session_closed"
	// SteeringReasonControlConflict means the provider session reported a
	// control conflict.
	SteeringReasonControlConflict SteeringFailureReason = "control_conflict"
	// SteeringReasonDeliveryTimedOut means provider delivery did not complete
	// within the bounded dispatch window.
	SteeringReasonDeliveryTimedOut SteeringFailureReason = "delivery_timed_out"
	// SteeringReasonDeliveryAmbiguous is the action-required reason for a
	// post-fence request with no durable outcome. It is never replayed.
	SteeringReasonDeliveryAmbiguous SteeringFailureReason = "delivery_ambiguous"
	// SteeringReasonOwnerStopped means Stop settled the queued request before
	// delivery.
	SteeringReasonOwnerStopped SteeringFailureReason = "owner_stopped"
	// SteeringReasonOwnerFinished means Task Finish settled the queued request
	// before delivery.
	SteeringReasonOwnerFinished SteeringFailureReason = "owner_finished"
	// SteeringReasonOwnerRuntimeLost means permanent Runtime loss settled the
	// queued request before delivery.
	SteeringReasonOwnerRuntimeLost SteeringFailureReason = "owner_runtime_lost"
	// SteeringReasonOwnerArchived means the Session owner was archived with
	// queued requests still undelivered.
	SteeringReasonOwnerArchived SteeringFailureReason = "owner_archived"
)

// ValidSteeringFailureReason reports whether reason is a member of the closed
// Steering failure vocabulary.
func ValidSteeringFailureReason(reason SteeringFailureReason) bool {
	switch reason {
	case SteeringReasonProviderControlUnavailable, SteeringReasonOwnerStateChanged,
		SteeringReasonContinuationUnavailable, SteeringReasonProviderRejected,
		SteeringReasonUnsupportedReasoningEffort, SteeringReasonSessionClosed,
		SteeringReasonControlConflict, SteeringReasonDeliveryTimedOut,
		SteeringReasonDeliveryAmbiguous, SteeringReasonOwnerStopped,
		SteeringReasonOwnerFinished, SteeringReasonOwnerRuntimeLost,
		SteeringReasonOwnerArchived:
		return true
	default:
		return false
	}
}

// SteeringRecord is the owner-neutral durable record for one Accepted Steering
// request. Task and Session owners share this shape; owner identity is carried
// by OwnerKind and OwnerID. Conversation Events remain projections of the
// request and outcome, never the dispatch source of truth.
type SteeringRecord struct {
	ID                       string            `json:"id"`
	OwnerKind                Kind              `json:"owner_kind"`
	OwnerID                  string            `json:"owner_id"`
	RequestID                string            `json:"request_id"`
	Message                  string            `json:"message"`
	Mode                     SteeringMode      `json:"mode"`
	ModelProviderID          string            `json:"model_provider_id,omitempty"`
	Model                    string            `json:"model,omitempty"`
	RequestedReasoningEffort string            `json:"requested_reasoning_effort,omitempty"`
	State                    SteeringState     `json:"state"`
	QueueOrder               int               `json:"queue_order"`
	ConversationEventID      string            `json:"conversation_event_id,omitempty"`
	ContinuationID           string            `json:"continuation_id,omitempty"`
	SessionID                string            `json:"session_id,omitempty"`
	SendStartedAt            *time.Time        `json:"send_started_at,omitempty"`
	Result                   map[string]any    `json:"result,omitempty"`
	ErrorCode                SteeringFailureReason `json:"error_code,omitempty"`
	ErrorMessage             string            `json:"error_message,omitempty"`
	CreatedAt                time.Time         `json:"created_at"`
	UpdatedAt                time.Time         `json:"updated_at"`
}
