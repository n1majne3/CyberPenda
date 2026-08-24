package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"pentest/internal/owner/conclusion"
)

// SemanticDebtWatermarks compare terminal Work Tool Results with the latest
// successful semantic persistence that covers them.
type SemanticDebtWatermarks = conclusion.SemanticDebtWatermarks

// BlackboardConclusionReceipt is the owner-local durable coordinator record.
// Canonical result bytes never appear in the Session API projection.
type BlackboardConclusionReceipt struct {
	ID                            string                           `json:"id"`
	SessionID                     string                           `json:"session_id"`
	ContinuationID                string                           `json:"continuation_id"`
	SourceRequestID               string                           `json:"source_request_id"`
	SourceRequestCorrelationExact bool                             `json:"source_request_correlation_exact"`
	SourceSessionID               string                           `json:"source_session_id"`
	SourceTurnID                  string                           `json:"source_turn_id"`
	InternalState                 BlackboardConclusionReceiptState `json:"internal_state"`
	SourceWorkWatermark           int                              `json:"source_work_watermark"`
	SemanticPersistenceWatermark  int                              `json:"semantic_persistence_watermark"`
	DispatchRequestID             string                           `json:"dispatch_request_id,omitempty"`
	ControlTurnID                 string                           `json:"control_turn_id,omitempty"`
	BaseRevision                  *int                             `json:"base_revision,omitempty"`
	SynchronizedRevision          *int                             `json:"synchronized_revision,omitempty"`
	SourceSelection               RuntimeTurnSelection             `json:"source_selection"`
	CanonicalResultJSON           []byte                           `json:"-"`
	CanonicalResultSHA256         string                           `json:"canonical_result_sha256,omitempty"`
	ApplyIdempotencyKey           string                           `json:"apply_idempotency_key,omitempty"`
	AppliedRevision               *int                             `json:"applied_revision,omitempty"`
	AutomaticTurnCount            int                              `json:"automatic_turn_count"`
	RepairCount                   int                              `json:"repair_count"`
	VersionRegenerationCount      int                              `json:"version_regeneration_count"`
	ExplicitRetryCount            int                              `json:"explicit_retry_count"`
	OperatorRetryKey              string                           `json:"-"`
	SendAttemptCount              int                              `json:"send_attempt_count"`
	SendStartedAt                 *time.Time                       `json:"send_started_at,omitempty"`
	NextEligibleAt                *time.Time                       `json:"next_eligible_at,omitempty"`
	ErrorCode                     BlackboardConclusionErrorCode    `json:"error_code,omitempty"`
	// RecoveryReason is the closed operator-visible reason for a fail-closed
	// action_required obligation (ADR 0021). It is never free-form text.
	RecoveryReason string `json:"recovery_reason,omitempty"`
	// ActiveDispatchID and DispatchKind expose the active Conclusion Dispatch
	// so recovery can create a new dispatch without rewriting history.
	ActiveDispatchID    string                 `json:"-"`
	DispatchKind        ConclusionDispatchKind `json:"-"`
	ValidationReason    string                 `json:"validation_reason,omitempty"`
	ValidationFieldPath string                 `json:"validation_field_path,omitempty"`
	ValidationExpected  string                 `json:"validation_expected,omitempty"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// View projects internal coordinator progress into the compact Session API
// vocabulary. A never-settled Work Turn is terminal and cannot be retried.
func (receipt BlackboardConclusionReceipt) View(mode BlackboardConclusionMode) BlackboardConclusion {
	return receipt.ViewAt(mode, time.Now().UTC())
}

func (receipt BlackboardConclusionReceipt) ViewAt(mode BlackboardConclusionMode, now time.Time) BlackboardConclusion {
	view := BlackboardConclusion{
		Mode: mode, SourceTurnID: receipt.SourceTurnID,
		SourceWorkWatermark:          receipt.SourceWorkWatermark,
		SemanticPersistenceWatermark: receipt.SemanticPersistenceWatermark,
	}
	switch receipt.InternalState {
	case BlackboardConclusionReceiptClean:
		view.State = BlackboardConclusionStateClean
	case BlackboardConclusionReceiptPending:
		view.State = BlackboardConclusionStatePending
	case BlackboardConclusionReceiptApplied:
		view.State = BlackboardConclusionStateClean
		if receipt.AppliedRevision != nil {
			view.AppliedRevision = intPointer(*receipt.AppliedRevision)
		}
	case BlackboardConclusionReceiptActionRequired:
		view.State = BlackboardConclusionStateActionRequired
		view.ErrorCode = receipt.ErrorCode
		view.RecoveryReason = receipt.RecoveryReason
		view.ValidationReason = receipt.ValidationReason
		view.ValidationFieldPath = receipt.ValidationFieldPath
		view.ValidationExpected = receipt.ValidationExpected
		if receipt.ErrorCode == BlackboardConclusionErrorWorkTurnNeverSettled {
			return view
		}
		if receipt.RecoveryReason == string(ConclusionRecoveryAcceptanceAmbiguous) {
			// An acceptance-ambiguous provider delivery is never resent: the
			// obligation is terminal-actionable and offers no generic Retry.
			return view
		}
		if receipt.NextEligibleAt != nil {
			eligible := receipt.NextEligibleAt.UTC()
			view.NextEligibleAt = &eligible
			view.RetryAvailable = !now.Before(eligible)
		}
	default:
		view.State = BlackboardConclusionStateConcluding
	}
	return view
}

var (
	ErrInvalidBlackboardConclusionReceipt       = errors.New("invalid Session Blackboard conclusion checkpoint receipt")
	ErrBlackboardConclusionRetryCooldown        = errors.New("Session Blackboard conclusion retry is not yet available")
	ErrBlackboardConclusionWorkTurnNeverSettled = errors.New("Session Work Turn never settled")
	// ErrBlackboardConclusionDispatchInactive reports a callback correlation
	// that resolved to a superseded or late Conclusion Dispatch. Only the
	// ACTIVE dispatch of an obligation may validate or apply a provider result
	// (ADR 0021).
	ErrBlackboardConclusionDispatchInactive = errors.New("Session Blackboard conclusion dispatch is not the active delivery")
)

// The Pending Blackboard Conclusion state machine is shared with Project Tasks
// through internal/owner/conclusion. This file keeps the Session-facing API:
// owner eligibility checks, the Session receipt projection, and error
// translation. Storage rules, dispatch lifecycle, and recovery settlement
// belong to the shared engine.

// PendingBlackboardConclusion is the durable obligation row, keyed by
// (session, source session, source turn) and independent of any Runtime
// Continuation. The shared engine names the owner identity OwnerID.
type PendingBlackboardConclusion = conclusion.PendingBlackboardConclusion

// ConclusionDispatch is one immutable delivery attempt for an obligation. Its
// continuation and source session are written once at creation and never
// updated; recovery creates a new dispatch instead of rewriting an old one.
type ConclusionDispatch = conclusion.ConclusionDispatch

// sessionConclusionDialect stores conclusion state against the Session tables.
func sessionConclusionDialect() conclusion.Dialect {
	return conclusion.Dialect{
		Subject:                     "Session Blackboard conclusion",
		ObligationsTable:            "session_pending_blackboard_conclusions",
		DispatchesTable:             "session_conclusion_dispatches",
		OwnerColumn:                 "session_id",
		ContinuationsTable:          "session_continuations",
		ContinuationOwnerColumn:     "session_id",
		EventKind:                   string(EventKindBlackboardConclusion),
		ReconcilePendingObligations: true,
		AppendEvent:                 appendSessionConclusionEventTx,
		RetryKeysTable:              "session_assisted_conclusion_retry_keys",
		RetryKeysOwnerColumn:        "session_id",
		RetryKeysReceiptColumn:      "obligation_id",
		NewID:                       newIDMust,
	}
}

func appendSessionConclusionEventTx(tx *sql.Tx, ownerID, _ string, kind string, payload map[string]any, now time.Time) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Session Blackboard conclusion Event: %w", err)
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM session_events WHERE session_id=?`, ownerID).Scan(&maxSeq); err != nil {
		return fmt.Errorf("read Session Blackboard conclusion Event sequence: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO session_events (id,session_id,seq,kind,payload_json,created_at)
		VALUES (?,?,?,?,?,?)`, newIDMust(), ownerID, int(maxSeq.Int64)+1, kind, string(payloadJSON), formatTime(now)); err != nil {
		return fmt.Errorf("store Session Blackboard conclusion Event: %w", err)
	}
	return nil
}

// conclusions returns the shared conclusion engine bound to the Session tables.
func (s *Service) conclusions() *conclusion.Engine {
	return &conclusion.Engine{DB: s.db.DB, Dialect: sessionConclusionDialect()}
}

func intPointer(value int) *int { return &value }

// mapConclusionError restamps engine sentinels as the Session-package
// sentinels callers already match with errors.Is, preserving message and chain.
func mapConclusionError(err error) error {
	if err == nil {
		return nil
	}
	return conclusion.MapSentinels(err,
		conclusion.ErrOwnerNotFound, ErrNotFound,
		conclusion.ErrInvalidBlackboardConclusionReceipt, ErrInvalidBlackboardConclusionReceipt,
		conclusion.ErrBlackboardConclusionRetryCooldown, ErrBlackboardConclusionRetryCooldown,
		conclusion.ErrBlackboardConclusionWorkTurnNeverSettled, ErrBlackboardConclusionWorkTurnNeverSettled,
		conclusion.ErrBlackboardConclusionDispatchInactive, ErrBlackboardConclusionDispatchInactive,
	)
}

func receiptFromConclusion(rec conclusion.BlackboardConclusionReceipt) BlackboardConclusionReceipt {
	return BlackboardConclusionReceipt{
		ID:                            rec.ID,
		SessionID:                     rec.OwnerID,
		ContinuationID:                rec.ContinuationID,
		SourceRequestID:               rec.SourceRequestID,
		SourceRequestCorrelationExact: rec.SourceRequestCorrelationExact,
		SourceSessionID:               rec.SourceSessionID,
		SourceTurnID:                  rec.SourceTurnID,
		InternalState:                 rec.InternalState,
		SourceWorkWatermark:           rec.SourceWorkWatermark,
		SemanticPersistenceWatermark:  rec.SemanticPersistenceWatermark,
		DispatchRequestID:             rec.DispatchRequestID,
		ControlTurnID:                 rec.ControlTurnID,
		BaseRevision:                  rec.BaseRevision,
		SynchronizedRevision:          rec.SynchronizedRevision,
		SourceSelection: RuntimeTurnSelection{
			ModelProviderID: rec.SourceSelection.ModelProviderID,
			Model:           rec.SourceSelection.Model,
			ReasoningEffort: rec.SourceSelection.ReasoningEffort,
		},
		CanonicalResultJSON:      rec.CanonicalResultJSON,
		CanonicalResultSHA256:    rec.CanonicalResultSHA256,
		ApplyIdempotencyKey:      rec.ApplyIdempotencyKey,
		AppliedRevision:          rec.AppliedRevision,
		AutomaticTurnCount:       rec.AutomaticTurnCount,
		RepairCount:              rec.RepairCount,
		VersionRegenerationCount: rec.VersionRegenerationCount,
		ExplicitRetryCount:       rec.ExplicitRetryCount,
		OperatorRetryKey:         rec.OperatorRetryKey,
		SendAttemptCount:         rec.SendAttemptCount,
		SendStartedAt:            rec.SendStartedAt,
		NextEligibleAt:           rec.NextEligibleAt,
		ErrorCode:                rec.ErrorCode,
		RecoveryReason:           rec.RecoveryReason,
		ActiveDispatchID:         rec.ActiveDispatchID,
		DispatchKind:             rec.DispatchKind,
		ValidationReason:         rec.ValidationReason,
		ValidationFieldPath:      rec.ValidationFieldPath,
		ValidationExpected:       rec.ValidationExpected,
		CreatedAt:                rec.CreatedAt,
		UpdatedAt:                rec.UpdatedAt,
	}
}

func receiptsFromConclusion(recs []conclusion.BlackboardConclusionReceipt) []BlackboardConclusionReceipt {
	out := make([]BlackboardConclusionReceipt, 0, len(recs))
	for _, rec := range recs {
		out = append(out, receiptFromConclusion(rec))
	}
	return out
}

// RecordBlackboardConclusionCheckpoint creates the one durable Pending
// Blackboard Conclusion obligation for a completed assisted Work Runtime Turn.
// Replay returns the original obligation.
func (s *Service) RecordBlackboardConclusionCheckpoint(sessionID, continuationID, sourceRequestID, sourceSessionID, sourceTurnID string, sourceSelection RuntimeTurnSelection, watermarks SemanticDebtWatermarks) (BlackboardConclusionReceipt, bool, error) {
	found, err := s.Get(sessionID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if found.RunControls.BlackboardConclusionMode != BlackboardConclusionModeAssisted {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	rec, created, err := s.conclusions().RecordBlackboardConclusionCheckpoint(sessionID, continuationID, sourceRequestID, sourceSessionID, sourceTurnID,
		conclusion.Selection{ModelProviderID: sourceSelection.ModelProviderID, Model: sourceSelection.Model, ReasoningEffort: sourceSelection.ReasoningEffort}, watermarks)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), created, nil
}

func (s *Service) ClaimBlackboardConclusionDispatch(obligationID string, baseRevision int) (BlackboardConclusionReceipt, bool, error) {
	rec, created, err := s.conclusions().ClaimBlackboardConclusionDispatch(obligationID, baseRevision)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), created, nil
}

func (s *Service) MarkBlackboardConclusionSendStarted(dispatchRequestID string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionSendStarted(dispatchRequestID, now)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) MarkBlackboardConclusionAwaiting(dispatchRequestID, controlTurnID string) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionAwaiting(dispatchRequestID, controlTurnID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) HandleBlackboardConclusionFailure(dispatchRequestID string, code BlackboardConclusionErrorCode, detail ConclusionValidationDetail, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().HandleBlackboardConclusionFailure(dispatchRequestID, code, detail, now, cooldown)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) ClaimBlackboardConclusionVersionSync(dispatchRequestID string) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().ClaimBlackboardConclusionVersionSync(dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) HandleBlackboardConclusionVersionConflict(dispatchRequestID string, currentRevision int, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().HandleBlackboardConclusionVersionConflict(dispatchRequestID, currentRevision, now, cooldown)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) MarkBlackboardConclusionVersionConflictActionRequired(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionVersionConflictActionRequired(dispatchRequestID, now, cooldown)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) MarkBlackboardConclusionApplyActionRequired(dispatchRequestID string, code BlackboardConclusionErrorCode, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionApplyActionRequired(dispatchRequestID, code, now, cooldown)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) MarkBlackboardConclusionRecoveryActionRequired(dispatchRequestID string, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionRecoveryActionRequired(dispatchRequestID, reason, now, cooldown)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(obligationID string, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(obligationID, reason, now, cooldown)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) MarkBlackboardConclusionWorkTurnConflict(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionWorkTurnConflict(dispatchRequestID, now, cooldown)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) BlackboardConclusionRecoveryCandidates() ([]BlackboardConclusionReceipt, error) {
	recs, err := s.conclusions().BlackboardConclusionRecoveryCandidates()
	if err != nil {
		return nil, mapConclusionError(err)
	}
	return receiptsFromConclusion(recs), nil
}

func (s *Service) ReconcileStrandedBlackboardConclusionRecoveries(now time.Time, cooldown time.Duration) ([]BlackboardConclusionReceipt, error) {
	recs, err := s.conclusions().ReconcileStrandedBlackboardConclusionRecoveries(now, cooldown)
	if err != nil {
		return nil, mapConclusionError(err)
	}
	return receiptsFromConclusion(recs), nil
}

func (s *Service) RetryBlackboardConclusion(obligationID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	rec, won, err := s.conclusions().RetryBlackboardConclusion(obligationID, idempotencyKey, now)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), won, nil
}

func (s *Service) RetryLatestBlackboardConclusion(sessionID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	rec, won, err := s.conclusions().RetryLatestBlackboardConclusion(sessionID, idempotencyKey, now)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), won, nil
}

// RetryLatestBlackboardConclusionFailClosedOnDispatchFailure retries only when
// the transaction confirms that no replacement Runtime binding is required.
func (s *Service) RetryLatestBlackboardConclusionFailClosedOnDispatchFailure(sessionID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	rec, won, err := s.conclusions().RetryLatestBlackboardConclusionFailClosedOnDispatchFailure(sessionID, idempotencyKey, now)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), won, nil
}

// RetryLatestBlackboardConclusionForRuntime binds a Runtime-recovery retry to
// one daemon-proven live replacement Runtime.
func (s *Service) RetryLatestBlackboardConclusionForRuntime(sessionID, idempotencyKey, continuationID, providerSessionID string, baseRevision int, now time.Time) (BlackboardConclusionReceipt, bool, bool, error) {
	rec, won, initial, err := s.conclusions().RetryLatestBlackboardConclusionForRuntime(
		sessionID, idempotencyKey, conclusion.RetryRuntimeBinding{
			ContinuationID: continuationID,
			SessionID:      providerSessionID,
			BaseRevision:   baseRevision,
		}, now,
	)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), won, initial, nil
}

func (s *Service) BlackboardConclusionByDispatchRequestID(dispatchRequestID string) (BlackboardConclusionReceipt, error) {
	rec, err := s.conclusions().BlackboardConclusionByDispatchRequestID(dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), nil
}

func (s *Service) RecordLateConclusionDispatchOutcome(dispatchRequestID string) error {
	return mapConclusionError(s.conclusions().RecordLateConclusionDispatchOutcome(dispatchRequestID))
}

func (s *Service) MarkBlackboardConclusionValidated(dispatchRequestID string, canonicalResult []byte) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionValidated(dispatchRequestID, canonicalResult)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) MarkBlackboardConclusionApplied(dispatchRequestID string, appliedRevision int) (BlackboardConclusionReceipt, bool, error) {
	rec, changed, err := s.conclusions().MarkBlackboardConclusionApplied(dispatchRequestID, appliedRevision)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), changed, nil
}

func (s *Service) LatestBlackboardConclusion(sessionID string) (*BlackboardConclusionReceipt, error) {
	if _, err := s.Get(sessionID); err != nil {
		return nil, err
	}
	rec, err := s.conclusions().LatestBlackboardConclusion(sessionID)
	if err != nil {
		return nil, mapConclusionError(err)
	}
	if rec == nil {
		return nil, nil
	}
	view := receiptFromConclusion(*rec)
	return &view, nil
}

func (s *Service) ValidatedBlackboardConclusions() ([]BlackboardConclusionReceipt, error) {
	recs, err := s.conclusions().ValidatedBlackboardConclusions()
	if err != nil {
		return nil, mapConclusionError(err)
	}
	return receiptsFromConclusion(recs), nil
}

func (s *Service) ConclusionDispatches(obligationID string) ([]ConclusionDispatch, error) {
	dispatches, err := s.conclusions().ConclusionDispatches(obligationID)
	if err != nil {
		return nil, mapConclusionError(err)
	}
	return dispatches, nil
}

func (s *Service) CreateRecoveryConclusionDispatches(sessionID, oldContinuationID, replacementContinuationID, replacementSessionID string) ([]BlackboardConclusionReceipt, error) {
	recs, err := s.conclusions().CreateRecoveryConclusionDispatches(sessionID, oldContinuationID, replacementContinuationID, replacementSessionID)
	if err != nil {
		return nil, mapConclusionError(err)
	}
	return receiptsFromConclusion(recs), nil
}

func (s *Service) CreateRecoveryConclusionDispatch(obligationID, continuationID, sessionID string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	rec, created, err := s.conclusions().CreateRecoveryConclusionDispatch(obligationID, continuationID, sessionID, now)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), created, nil
}
