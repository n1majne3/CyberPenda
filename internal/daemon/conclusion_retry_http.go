package daemon

import (
	"errors"
	"net/http"
	"strings"

	"pentest/internal/owner"
	"pentest/internal/owner/conclusion"
)

// conclusionRetrySpec binds one Runtime Owner to the shared Blackboard
// conclusion Retry ladder. The ladder is owner-neutral (ADR 0021): one
// idempotent operator retry per obligation through the shared engine, never
// resent after an acceptance-ambiguous delivery, and the winning caller
// schedules exactly one follow-up dispatch.
type conclusionRetryDispatchMode uint8

const (
	conclusionRetryDispatchNone conclusionRetryDispatchMode = iota
	conclusionRetryDispatchPending
	conclusionRetryDispatchInitial
	conclusionRetryDispatchRepair
)

type conclusionRetrySpec struct {
	// latest returns the latest receipt's recovery reason for the owner.
	latest func() (recoveryReason string, found bool, err error)
	// retry performs the idempotent retry and selects the one follow-up action
	// owned by the winning caller.
	retry func(idempotencyKey string) (won bool, mode conclusionRetryDispatchMode, err error)
	// dispatchPending claims a pending obligation; dispatchInitial delivers an
	// already-created first Conclude dispatch; dispatchRepair delivers repair.
	dispatchPending func()
	dispatchInitial func()
	dispatchRepair  func()
	// detail renders the owner's detail response payload.
	detail func() (any, error)
	// writeOwnerError renders owner-local lookup errors.
	writeOwnerError func(http.ResponseWriter, error)
}

func (server *Server) serveBlackboardConclusionRetry(response http.ResponseWriter, request *http.Request, spec conclusionRetrySpec) {
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(response, http.StatusBadRequest, "Idempotency-Key is required")
		return
	}
	recoveryReason, found, err := spec.latest()
	if err != nil {
		spec.writeOwnerError(response, err)
		return
	}
	if !found {
		writeError(response, http.StatusNotFound, "Blackboard conclusion not found")
		return
	}
	if recoveryReason == string(owner.ConclusionRecoveryAcceptanceAmbiguous) {
		// An acceptance-ambiguous provider delivery is never resent: a generic
		// Retry could duplicate a request the provider already accepted.
		writeError(response, http.StatusConflict, "Blackboard conclusion cannot be retried after an acceptance-ambiguous delivery")
		return
	}
	won, mode, retryErr := spec.retry(idempotencyKey)
	if retryErr != nil {
		if errors.Is(retryErr, conclusion.ErrBlackboardConclusionRetryCooldown) {
			writeError(response, http.StatusConflict, "Blackboard conclusion retry is not yet available")
			return
		}
		writeError(response, http.StatusConflict, "Blackboard conclusion cannot be retried")
		return
	}
	if won {
		switch mode {
		case conclusionRetryDispatchPending:
			spec.dispatchPending()
		case conclusionRetryDispatchInitial:
			spec.dispatchInitial()
		case conclusionRetryDispatchRepair:
			spec.dispatchRepair()
		default:
			writeError(response, http.StatusInternalServerError, "Blackboard conclusion retry dispatch mode is invalid")
			return
		}
	}
	payload, err := spec.detail()
	if err != nil {
		spec.writeOwnerError(response, err)
		return
	}
	status := http.StatusOK
	if won {
		status = http.StatusAccepted
	}
	writeJSON(response, status, payload)
}
