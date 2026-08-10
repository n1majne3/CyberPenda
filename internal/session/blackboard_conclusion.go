package session

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/owner"
)

// SemanticDebtWatermarks compare terminal Work Tool Results with the latest
// successful semantic persistence that covers them.
type SemanticDebtWatermarks = owner.SemanticDebtWatermarks

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

// PendingBlackboardConclusion is the durable obligation row, keyed by
// (session, source session, source turn) and independent of any Runtime
// Continuation.
type PendingBlackboardConclusion struct {
	ID                            string
	SessionID                     string
	SourceRequestID               string
	SourceRequestCorrelationExact bool
	SourceContinuationID          string
	SourceSessionID               string
	SourceTurnID                  string
	State                         BlackboardConclusionReceiptState
	SourceWorkWatermark           int
	SemanticPersistenceWatermark  int
	SourceSelection               RuntimeTurnSelection
	CanonicalResultJSON           []byte
	CanonicalResultSHA256         string
	ApplyIdempotencyKey           string
	AppliedRevision               *int
	BaseRevision                  *int
	AutomaticTurnCount            int
	RepairCount                   int
	VersionRegenerationCount      int
	ExplicitRetryCount            int
	OperatorRetryKey              string
	NextEligibleAt                *time.Time
	ErrorCode                     BlackboardConclusionErrorCode
	RecoveryReason                ConclusionRecoveryReason
	ValidationReason              string
	ValidationFieldPath           string
	ValidationExpected            string
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

// ConclusionDispatch is one immutable delivery attempt for an obligation. Its
// continuation and source session are written once at creation and never
// updated; recovery creates a new dispatch instead of rewriting an old one.
type ConclusionDispatch struct {
	ID                   string
	ObligationID         string
	Kind                 ConclusionDispatchKind
	ContinuationID       string
	SourceSessionID      string
	DispatchRequestID    string
	ControlTurnID        string
	BaseRevision         *int
	SynchronizedRevision *int
	DeliveryState        ConclusionDispatchState
	SendAttemptCount     int
	SendStartedAt        *time.Time
	TerminalOutcome      string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

const sessionPendingBlackboardConclusionColumns = `id,session_id,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,state,
	source_work_watermark,semantic_persistence_watermark,source_model_provider_id,source_model,source_reasoning_effort,
	canonical_result_json,canonical_result_sha256,apply_idempotency_key,applied_revision,base_revision,automatic_turn_count,
	repair_count,version_regeneration_count,explicit_retry_count,operator_retry_key,next_eligible_at,error_code,recovery_reason,
	validation_reason,validation_field_path,validation_expected,created_at,updated_at`

const sessionConclusionDispatchColumns = `id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,control_turn_id,base_revision,
	synchronized_revision,delivery_state,send_attempt_count,send_started_at,terminal_outcome,created_at,updated_at`

// RecordBlackboardConclusionCheckpoint creates the one durable Pending
// Blackboard Conclusion obligation for a completed assisted Work Runtime Turn.
// Replay returns the original obligation.
func (s *Service) RecordBlackboardConclusionCheckpoint(sessionID, continuationID, sourceRequestID, sourceSessionID, sourceTurnID string, sourceSelection RuntimeTurnSelection, watermarks SemanticDebtWatermarks) (BlackboardConclusionReceipt, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	continuationID = strings.TrimSpace(continuationID)
	sourceRequestID = strings.TrimSpace(sourceRequestID)
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	sourceTurnID = strings.TrimSpace(sourceTurnID)
	sourceSelection.ModelProviderID = strings.TrimSpace(sourceSelection.ModelProviderID)
	sourceSelection.Model = strings.TrimSpace(sourceSelection.Model)
	sourceSelection.ReasoningEffort = strings.TrimSpace(sourceSelection.ReasoningEffort)
	if sessionID == "" || continuationID == "" || sourceRequestID == "" || sourceSessionID == "" || sourceTurnID == "" ||
		sourceSelection.ModelProviderID == "" || sourceSelection.Model == "" || !watermarks.Valid() {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	found, err := s.Get(sessionID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if found.RunControls.BlackboardConclusionMode != BlackboardConclusionModeAssisted {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}

	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion obligation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var ownerSessionID string
	if err := tx.QueryRow(`SELECT session_id FROM session_continuations WHERE id=?`, continuationID).Scan(&ownerSessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BlackboardConclusionReceipt{}, false, ErrNotFound
		}
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("load Session Blackboard conclusion Continuation: %w", err)
	}
	if ownerSessionID != sessionID {
		return BlackboardConclusionReceipt{}, false, ErrNotFound
	}

	prior, err := scanSessionPendingBlackboardConclusion(tx.QueryRow(`SELECT `+sessionPendingBlackboardConclusionColumns+`
		FROM session_pending_blackboard_conclusions WHERE session_id=? AND source_session_id=? AND source_turn_id=?`, sessionID, sourceSessionID, sourceTurnID))
	if err == nil {
		view, scanErr := sessionCombinedConclusionView(tx, prior)
		if scanErr != nil {
			return BlackboardConclusionReceipt{}, false, scanErr
		}
		return view, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("load Session Blackboard conclusion obligation: %w", err)
	}

	now := time.Now().UTC()
	obligationState := BlackboardConclusionReceiptPending
	phase := "pending_detected"
	if watermarks.SourceWork <= watermarks.SemanticPersistence {
		obligationState = BlackboardConclusionReceiptClean
		phase = "persistence_current"
	}
	obligation := PendingBlackboardConclusion{
		ID: newIDMust(), SessionID: sessionID, SourceRequestID: sourceRequestID, SourceRequestCorrelationExact: true,
		SourceContinuationID: continuationID, SourceSessionID: sourceSessionID, SourceTurnID: sourceTurnID,
		State: obligationState, SourceWorkWatermark: watermarks.SourceWork,
		SemanticPersistenceWatermark: watermarks.SemanticPersistence,
		SourceSelection:              sourceSelection, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO session_pending_blackboard_conclusions
		(id,session_id,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,state,source_work_watermark,semantic_persistence_watermark,
		 source_model_provider_id,source_model,source_reasoning_effort,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, obligation.ID, obligation.SessionID, obligation.SourceRequestID,
		obligation.SourceRequestCorrelationExact, obligation.SourceContinuationID, obligation.SourceSessionID, obligation.SourceTurnID,
		string(obligation.State), obligation.SourceWorkWatermark, obligation.SemanticPersistenceWatermark,
		obligation.SourceSelection.ModelProviderID, obligation.SourceSelection.Model, obligation.SourceSelection.ReasoningEffort,
		formatTime(obligation.CreatedAt), formatTime(obligation.UpdatedAt)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session Blackboard conclusion obligation: %w", err)
	}

	view := sessionObligationView(obligation)
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
		"phase": phase, "receipt_id": obligation.ID, "source_turn_id": obligation.SourceTurnID,
		"source_work_watermark": obligation.SourceWorkWatermark, "semantic_persistence_watermark": obligation.SemanticPersistenceWatermark,
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session Blackboard conclusion obligation: %w", err)
	}
	return view, true, nil
}

// ClaimBlackboardConclusionDispatch creates the initial Conclusion Dispatch
// for a pending obligation, bound immutably to the source continuation and
// source session of the completed Work Runtime Turn.
func (s *Service) ClaimBlackboardConclusionDispatch(obligationID string, baseRevision int) (BlackboardConclusionReceipt, bool, error) {
	obligationID = strings.TrimSpace(obligationID)
	if obligationID == "" || baseRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := loadSessionPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if obligation.State != BlackboardConclusionReceiptPending {
		if obligation.State == BlackboardConclusionReceiptClean {
			view, viewErr := sessionCombinedConclusionView(tx, obligation)
			return view, false, viewErr
		}
		// Replay: the obligation already claimed the deterministic initial
		// dispatch. Return the existing ACTIVE dispatch unchanged when its
		// request lineage and base revision match this replay.
		dispatchID, _ := sessionBlackboardConclusionRequestLineage(obligation.SourceContinuationID, obligation.SourceTurnID)
		existing, err := sessionActiveConclusionDispatchTx(tx, obligation.ID)
		if err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if existing != nil && existing.DispatchRequestID == dispatchID && existing.BaseRevision != nil && *existing.BaseRevision == baseRevision {
			view, viewErr := sessionCombinedConclusionView(tx, obligation)
			return view, false, viewErr
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	dispatchID, applyKey := sessionBlackboardConclusionRequestLineage(obligation.SourceContinuationID, obligation.SourceTurnID)
	now := time.Now().UTC()
	dispatch := ConclusionDispatch{
		ID: newIDMust(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindInitial,
		ContinuationID: obligation.SourceContinuationID, SourceSessionID: obligation.SourceSessionID,
		DispatchRequestID: dispatchID, BaseRevision: intPointer(baseRevision),
		DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO session_conclusion_dispatches
		(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, dispatch.ID, dispatch.ObligationID, string(dispatch.Kind), dispatch.ContinuationID,
		dispatch.SourceSessionID, dispatch.DispatchRequestID, baseRevision, string(dispatch.DeliveryState),
		formatTime(now), formatTime(now)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session initial Conclusion Dispatch: %w", err)
	}
	obligation.State = BlackboardConclusionReceiptDispatchRequested
	obligation.ApplyIdempotencyKey = applyKey
	obligation.AutomaticTurnCount = 1
	obligation.BaseRevision = intPointer(baseRevision)
	obligation.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE session_pending_blackboard_conclusions
		SET state=?,apply_idempotency_key=?,automatic_turn_count=?,base_revision=?,updated_at=? WHERE id=?`,
		string(obligation.State), applyKey, obligation.AutomaticTurnCount, baseRevision, formatTime(now), obligation.ID); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("advance Session Blackboard conclusion obligation: %w", err)
	}
	view := sessionObligationView(obligation)
	view.ActiveDispatchID = dispatch.ID
	view.DispatchKind = dispatch.Kind
	view.DispatchRequestID = dispatch.DispatchRequestID
	view.ContinuationID = dispatch.ContinuationID
	view.SourceSessionID = dispatch.SourceSessionID
	view.BaseRevision = dispatch.BaseRevision
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
		"phase": "dispatch_requested", "receipt_id": obligation.ID, "source_turn_id": obligation.SourceTurnID,
		"request_id": dispatchID, "base_revision": baseRevision, "turn_kind": "control",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session Blackboard conclusion dispatch: %w", err)
	}
	return view, true, nil
}

// MarkBlackboardConclusionSendStarted closes the provider-acceptance ambiguity
// window on the ACTIVE dispatch before SendTurn. Only the active dispatch may
// claim the fence; replay observes the original timestamp.
func (s *Service) MarkBlackboardConclusionSendStarted(dispatchRequestID string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || now.IsZero() {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE session_conclusion_dispatches SET send_attempt_count=1,send_started_at=?,updated_at=?
		WHERE dispatch_request_id=? AND delivery_state=? AND send_attempt_count=0 AND send_started_at IS NULL`,
		formatTime(now), formatTime(now), dispatchRequestID, string(ConclusionDispatchRequested))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	changed, _ := result.RowsAffected()
	view, err := sessionConclusionViewByDispatchRequestIDTx(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if changed == 0 {
		return view, false, nil
	}
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// MarkBlackboardConclusionAwaiting records provider acceptance of the Conclude
// Turn on the ACTIVE dispatch. Replaying the same correlation is idempotent.
func (s *Service) MarkBlackboardConclusionAwaiting(dispatchRequestID, controlTurnID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID, controlTurnID = strings.TrimSpace(dispatchRequestID), strings.TrimSpace(controlTurnID)
	if dispatchRequestID == "" || controlTurnID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return s.advanceSessionActiveConclusionDispatch(dispatchRequestID, ConclusionDispatchRequested, ConclusionDispatchAwaitingResult,
		BlackboardConclusionReceiptAwaitingResult,
		func(receipt BlackboardConclusionReceipt) bool { return receipt.ControlTurnID == controlTurnID },
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, dispatch *ConclusionDispatch, now time.Time) error {
			dispatch.DeliveryState = ConclusionDispatchAwaitingResult
			dispatch.ControlTurnID = controlTurnID
			if _, err := tx.Exec(`UPDATE session_conclusion_dispatches SET delivery_state=?,control_turn_id=?,updated_at=? WHERE id=?`,
				string(dispatch.DeliveryState), controlTurnID, formatTime(now), dispatch.ID); err != nil {
				return err
			}
			return appendSessionBlackboardConclusionEventTx(tx, sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, *obligation), EventPayload{
				"phase": "awaiting_result", "receipt_id": dispatch.ObligationID, "request_id": dispatchRequestID,
				"source_turn_id": obligation.SourceTurnID, "control_turn_id": controlTurnID, "turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionFailure durably resolves a failed Conclude Turn.
// One invalid initial result may claim a single automatic repair, which
// supersedes the failed dispatch and creates a NEW repair dispatch on the same
// immutable continuation and source session.
func (s *Service) HandleBlackboardConclusionFailure(dispatchRequestID string, code BlackboardConclusionErrorCode, detail ConclusionValidationDetail, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 ||
		(code != BlackboardConclusionErrorInvalidResult && code != BlackboardConclusionErrorToolUseForbidden && code != BlackboardConclusionErrorRuntimeRecoveryRequired) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if detail.Valid() && code != BlackboardConclusionErrorInvalidResult {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := loadSessionActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired {
		return sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	if obligation.State != BlackboardConclusionReceiptAwaitingResult || dispatch.DeliveryState != ConclusionDispatchAwaitingResult {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if code == BlackboardConclusionErrorInvalidResult && obligation.AutomaticTurnCount < BlackboardConclusionAutomaticTurnLimit &&
		obligation.RepairCount == 0 && obligation.VersionRegenerationCount == 0 && obligation.ExplicitRetryCount == 0 {
		repairNumber := obligation.RepairCount + 1
		requestID := sessionBlackboardConclusionAttemptRequestID("repair", dispatch.ContinuationID, obligation.SourceTurnID, repairNumber, "")
		nextEligible := now.Add(cooldown)
		nowDispatch := ConclusionDispatch{
			ID: newIDMust(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRepair,
			ContinuationID: dispatch.ContinuationID, SourceSessionID: dispatch.SourceSessionID,
			DispatchRequestID: requestID, BaseRevision: dispatch.BaseRevision,
			DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
		}
		if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "superseded_by_repair", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if _, err := tx.Exec(`INSERT INTO session_conclusion_dispatches
			(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
			nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
			formatTime(now), formatTime(now)); err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session repair Conclusion Dispatch: %w", err)
		}
		obligation.State = BlackboardConclusionReceiptRepairDispatchRequested
		obligation.AutomaticTurnCount++
		obligation.RepairCount++
		obligation.ErrorCode = code
		obligation.NextEligibleAt = &nextEligible
		obligation.ValidationReason = detail.Reason
		obligation.ValidationFieldPath = detail.FieldPath
		obligation.ValidationExpected = detail.Expected
		obligation.UpdatedAt = now
		if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		view := sessionObligationView(obligation)
		view.ActiveDispatchID = nowDispatch.ID
		view.DispatchKind = nowDispatch.Kind
		view.DispatchRequestID = nowDispatch.DispatchRequestID
		view.ContinuationID = nowDispatch.ContinuationID
		view.SourceSessionID = nowDispatch.SourceSessionID
		view.BaseRevision = nowDispatch.BaseRevision
		payload := EventPayload{"phase": "repair_requested", "receipt_id": obligation.ID, "request_id": requestID,
			"error_code": string(code), "automatic_turn_count": obligation.AutomaticTurnCount, "repair_count": obligation.RepairCount, "turn_kind": "control"}
		owner.AppendConclusionValidationEventPayload(payload, detail)
		if err := appendSessionBlackboardConclusionEventTx(tx, view, payload, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return view, true, nil
	}
	actionCode := code
	if code == BlackboardConclusionErrorInvalidResult && obligation.RepairCount > 0 && obligation.VersionRegenerationCount == 0 {
		actionCode = BlackboardConclusionErrorRepairExhausted
	}
	nextEligible := now.Add(cooldown)
	if obligation.NextEligibleAt != nil {
		nextEligible = obligation.NextEligibleAt.UTC()
	}
	var failErr error
	obligation, failErr = sessionFailActiveDispatchTx(tx, obligation, dispatch, actionCode, nextEligible, detail, now)
	if failErr != nil {
		return BlackboardConclusionReceipt{}, false, failErr
	}
	view := sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	payload := EventPayload{"phase": "action_required", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
		"error_code": string(actionCode), "next_eligible_at": nextEligible.Format(time.RFC3339Nano)}
	owner.AppendConclusionValidationEventPayload(payload, detail)
	if err := appendSessionBlackboardConclusionEventTx(tx, view, payload, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// ClaimBlackboardConclusionVersionSync records the intent to synchronize the
// active dispatch's continuation before version regeneration can be claimed.
func (s *Service) ClaimBlackboardConclusionVersionSync(dispatchRequestID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return s.advanceSessionObligationStateByDispatchRequest(dispatchRequestID, BlackboardConclusionReceiptValidated,
		BlackboardConclusionReceiptVersionSyncRequested,
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, now time.Time) error {
			obligation.State = BlackboardConclusionReceiptVersionSyncRequested
			obligation.ErrorCode = BlackboardConclusionErrorVersionConflict
			obligation.UpdatedAt = now
			if _, err := tx.Exec(`UPDATE session_pending_blackboard_conclusions SET state=?,error_code=?,updated_at=? WHERE id=?`,
				string(obligation.State), string(obligation.ErrorCode), formatTime(now), obligation.ID); err != nil {
				return err
			}
			return appendSessionBlackboardConclusionEventTx(tx, sessionObligationView(*obligation), EventPayload{
				"phase": "version_sync_requested", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
				"turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionVersionConflict discards a validated result whose
// revision guard lost a real race and claims one fresh semantic generation.
// The old validated dispatch is superseded and a NEW version_regeneration
// dispatch is created; budgets belong to the obligation and never reset.
func (s *Service) HandleBlackboardConclusionVersionConflict(dispatchRequestID string, currentRevision int, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || currentRevision < 0 || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion version conflict: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := loadSessionActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired && obligation.ErrorCode == BlackboardConclusionErrorVersionConflict {
		return sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	if obligation.State != BlackboardConclusionReceiptVersionSyncRequested || dispatch.BaseRevision == nil || currentRevision <= *dispatch.BaseRevision {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	if obligation.VersionRegenerationCount == 0 && obligation.AutomaticTurnCount < BlackboardConclusionAutomaticTurnLimit && obligation.ExplicitRetryCount == 0 {
		requestID := sessionBlackboardConclusionAttemptRequestID("version", dispatch.ContinuationID, obligation.SourceTurnID, 1, fmt.Sprintf("%d", currentRevision))
		nowDispatch := ConclusionDispatch{
			ID: newIDMust(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindVersionRegeneration,
			ContinuationID: dispatch.ContinuationID, SourceSessionID: dispatch.SourceSessionID,
			DispatchRequestID: requestID, BaseRevision: intPointer(currentRevision),
			SynchronizedRevision: intPointer(currentRevision), DeliveryState: ConclusionDispatchRequested,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "superseded_by_version_regeneration", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if _, err := tx.Exec(`INSERT INTO session_conclusion_dispatches
			(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,synchronized_revision,delivery_state,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
			nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, currentRevision, currentRevision, string(nowDispatch.DeliveryState),
			formatTime(now), formatTime(now)); err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session version regeneration Conclusion Dispatch: %w", err)
		}
		obligation.State = BlackboardConclusionReceiptVersionRegenerationDispatchRequested
		obligation.VersionRegenerationCount = 1
		obligation.AutomaticTurnCount++
		obligation.ErrorCode = BlackboardConclusionErrorVersionConflict
		obligation.NextEligibleAt = &nextEligible
		obligation.CanonicalResultJSON = nil
		obligation.CanonicalResultSHA256 = ""
		obligation.UpdatedAt = now
		if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		view := sessionObligationView(obligation)
		view.ActiveDispatchID = nowDispatch.ID
		view.DispatchKind = nowDispatch.Kind
		view.DispatchRequestID = nowDispatch.DispatchRequestID
		view.ContinuationID = nowDispatch.ContinuationID
		view.SourceSessionID = nowDispatch.SourceSessionID
		view.BaseRevision = nowDispatch.BaseRevision
		view.SynchronizedRevision = nowDispatch.SynchronizedRevision
		if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
			"phase": "version_regeneration_requested", "receipt_id": obligation.ID, "request_id": requestID,
			"base_revision": currentRevision, "error_code": string(BlackboardConclusionErrorVersionConflict),
			"automatic_turn_count": obligation.AutomaticTurnCount, "version_regeneration_count": obligation.VersionRegenerationCount,
			"turn_kind": "control",
		}, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return view, true, nil
	}

	obligation.State = BlackboardConclusionReceiptActionRequired
	obligation.ErrorCode = BlackboardConclusionErrorVersionConflict
	obligation.NextEligibleAt = &nextEligible
	obligation.BaseRevision = intPointer(currentRevision)
	obligation.CanonicalResultJSON = nil
	obligation.CanonicalResultSHA256 = ""
	obligation.UpdatedAt = now
	if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
		"phase": "action_required", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
		"base_revision": currentRevision, "error_code": string(BlackboardConclusionErrorVersionConflict),
		"next_eligible_at": nextEligible.Format(time.RFC3339Nano), "reason": "version_regeneration_exhausted",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// MarkBlackboardConclusionVersionConflictActionRequired rejects a validated
// result whose record or change versions are semantically incompatible with
// the apply contract and requires operator action immediately.
func (s *Service) MarkBlackboardConclusionVersionConflictActionRequired(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	return s.MarkBlackboardConclusionApplyActionRequired(dispatchRequestID, BlackboardConclusionErrorVersionConflict, now, cooldown)
}

// MarkBlackboardConclusionApplyActionRequired fails closed when a validated
// result cannot be applied or its pre-regeneration synchronization fails.
func (s *Service) MarkBlackboardConclusionApplyActionRequired(dispatchRequestID string, code BlackboardConclusionErrorCode, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 || !validSessionBlackboardConclusionErrorCode(code) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin incompatible Session Blackboard conclusion version: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := loadSessionConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired && obligation.ErrorCode == code {
		return sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	if obligation.State != BlackboardConclusionReceiptValidated && obligation.State != BlackboardConclusionReceiptVersionSyncRequested {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	obligation.State = BlackboardConclusionReceiptActionRequired
	obligation.ErrorCode = code
	obligation.NextEligibleAt = &nextEligible
	obligation.CanonicalResultJSON = nil
	obligation.CanonicalResultSHA256 = ""
	obligation.UpdatedAt = now
	if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
		"phase": "action_required", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
		"base_revision": intPointerValue(dispatch.BaseRevision), "error_code": string(code),
		"next_eligible_at": nextEligible.Format(time.RFC3339Nano), "reason": "apply_failed",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// MarkBlackboardConclusionRecoveryActionRequired closes crash windows after a
// repair, version sync/regeneration, or operator-authorized retry was durably
// claimed but could not finish. The reason is a closed operator-visible token.
func (s *Service) MarkBlackboardConclusionRecoveryActionRequired(dispatchRequestID string, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || !owner.ValidConclusionRecoveryReason(reason) || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := loadSessionConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	return s.markSessionConclusionRecoveryActionRequiredTx(tx, obligation, dispatch, reason, now, cooldown)
}

// MarkBlackboardConclusionRecoveryActionRequiredByReceiptID resolves any
// restart-stranded pre-apply obligation, including pending obligations that do
// not yet have a dispatch. Replays preserve the original cooldown and counters.
func (s *Service) MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(obligationID string, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	obligationID = strings.TrimSpace(obligationID)
	if obligationID == "" || !owner.ValidConclusionRecoveryReason(reason) || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion dispatch recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := loadSessionPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	dispatch, err := sessionActiveConclusionDispatchTx(tx, obligation.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, err
	}
	return s.markSessionConclusionRecoveryActionRequiredTx(tx, obligation, dispatch, reason, now, cooldown)
}

func (s *Service) markSessionConclusionRecoveryActionRequiredTx(tx *sql.Tx, obligation PendingBlackboardConclusion, dispatch *ConclusionDispatch, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	if obligation.State == BlackboardConclusionReceiptActionRequired {
		return sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	eligiblePending := obligation.State == BlackboardConclusionReceiptPending
	eligibleInitial := obligation.State == BlackboardConclusionReceiptDispatchRequested
	eligibleRepair := obligation.State == BlackboardConclusionReceiptRepairDispatchRequested
	eligibleVersionSync := obligation.State == BlackboardConclusionReceiptVersionSyncRequested
	eligibleVersionRegeneration := obligation.State == BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	eligibleAwaiting := obligation.State == BlackboardConclusionReceiptAwaitingResult
	if !eligiblePending && !eligibleInitial && !eligibleRepair && !eligibleVersionSync && !eligibleVersionRegeneration && !eligibleAwaiting {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	if obligation.NextEligibleAt != nil {
		nextEligible = obligation.NextEligibleAt.UTC()
	}
	obligation.State = BlackboardConclusionReceiptActionRequired
	obligation.ErrorCode = BlackboardConclusionErrorRuntimeRecoveryRequired
	obligation.RecoveryReason = reason
	obligation.NextEligibleAt = &nextEligible
	obligation.UpdatedAt = now
	if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	// Supersede ANY active dispatch so a later provider callback resolves to a
	// terminal delivery and is recorded as a late outcome instead of being
	// silently dropped while the obligation is already action_required.
	if dispatch != nil && owner.ConclusionDispatchActive(dispatch.DeliveryState) {
		if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
	}
	view := sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
		"phase": "action_required", "receipt_id": obligation.ID, "request_id": sessionObligationDispatchRequestID(dispatch),
		"error_code": string(obligation.ErrorCode), "recovery_reason": string(reason),
		"next_eligible_at": nextEligible.Format(time.RFC3339Nano), "reason": "dispatch_recovery",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// MarkBlackboardConclusionWorkTurnConflict resolves a conclusion dispatch that
// a non-yielding provider work turn refused. Within the bounded conflict
// budget it stays operator-retryable; once exhausted it becomes the distinct,
// non-retryable never-settled terminal.
func (s *Service) MarkBlackboardConclusionWorkTurnConflict(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion work turn conflict: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := loadSessionActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired {
		return sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	if dispatch.DeliveryState != ConclusionDispatchRequested {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	errorCode := BlackboardConclusionErrorRuntimeRecoveryRequired
	reason := "work_turn_conflict"
	if obligation.ExplicitRetryCount >= BlackboardConclusionWorkTurnConflictLimit {
		errorCode = BlackboardConclusionErrorWorkTurnNeverSettled
		reason = "work_turn_never_settled"
	}
	nextEligible := now.Add(cooldown)
	if obligation.NextEligibleAt != nil {
		nextEligible = obligation.NextEligibleAt.UTC()
	}
	obligation.State = BlackboardConclusionReceiptActionRequired
	obligation.ErrorCode = errorCode
	obligation.NextEligibleAt = &nextEligible
	obligation.UpdatedAt = now
	if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
		"phase": "action_required", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
		"error_code": string(errorCode), "next_eligible_at": nextEligible.Format(time.RFC3339Nano),
		"reason": reason, "explicit_retry_count": obligation.ExplicitRetryCount,
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// BlackboardConclusionRecoveryCandidates lists obligations in pre-apply states
// for an ownership-aware daemon coordinator. It never mutates state.
func (s *Service) BlackboardConclusionRecoveryCandidates() ([]BlackboardConclusionReceipt, error) {
	rows, err := s.db.Query(`SELECT `+sessionPendingBlackboardConclusionColumns+` FROM session_pending_blackboard_conclusions
		WHERE state IN (?,?,?,?,?,?) ORDER BY created_at,id`,
		string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested),
		string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested),
		string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list Session Blackboard conclusion recovery candidates: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := scanSessionPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan Session Blackboard conclusion recovery candidate: %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate Session Blackboard conclusion recovery candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Session Blackboard conclusion recovery candidates: %w", err)
	}
	views := make([]BlackboardConclusionReceipt, 0, len(obligations))
	for _, obligation := range obligations {
		view, viewErr := sessionCombinedConclusionView(s.db, obligation)
		if viewErr != nil {
			return nil, fmt.Errorf("scan Session Blackboard conclusion recovery candidate view: %w", viewErr)
		}
		views = append(views, view)
	}
	return views, nil
}

// ReconcileStrandedBlackboardConclusionRecoveries makes durably claimed
// repair, version sync/regeneration, and explicit retry work
// operator-actionable after daemon restart.
func (s *Service) ReconcileStrandedBlackboardConclusionRecoveries(now time.Time, cooldown time.Duration) ([]BlackboardConclusionReceipt, error) {
	if cooldown < 0 {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	rows, err := s.db.Query(`SELECT `+sessionPendingBlackboardConclusionColumns+` FROM session_pending_blackboard_conclusions
		WHERE state IN (?,?,?,?,?,?) ORDER BY created_at,id`,
		string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested),
		string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested),
		string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list stranded Session Blackboard conclusion recoveries: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := scanSessionPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stranded Session Blackboard conclusion recovery: %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate stranded Session Blackboard conclusion recoveries: %w", err)
	}
	_ = rows.Close()
	reconciled := make([]BlackboardConclusionReceipt, 0, len(obligations))
	for _, obligation := range obligations {
		receipt, changed, err := s.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(obligation.ID, ConclusionRecoveryDispatchFailed, now, cooldown)
		if err != nil {
			return nil, err
		}
		if changed {
			reconciled = append(reconciled, receipt)
		}
	}
	return reconciled, nil
}

// RetryBlackboardConclusion atomically claims one operator-authorized retry for
// an action_required obligation. The retry supersedes the failed dispatch and
// creates a NEW retry dispatch bound to the same immutable continuation and
// source session. A never-dispatched obligation is re-armed to pending so the
// initial dispatch claim runs on the live runtime.
func (s *Service) RetryBlackboardConclusion(obligationID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	obligationID, idempotencyKey = strings.TrimSpace(obligationID), strings.TrimSpace(idempotencyKey)
	if obligationID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := loadSessionPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	receipt, won, err := retrySessionBlackboardConclusionTx(tx, obligation, idempotencyKey, now)
	if err != nil || !won {
		return receipt, won, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// RetryLatestBlackboardConclusion atomically applies a Session-scoped operator
// retry key to the latest durable conclusion obligation.
func (s *Service) RetryLatestBlackboardConclusion(sessionID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	sessionID, idempotencyKey = strings.TrimSpace(sessionID), strings.TrimSpace(idempotencyKey)
	if sessionID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := scanSessionPendingBlackboardConclusion(tx.QueryRow(`SELECT `+sessionPendingBlackboardConclusionColumns+`
		FROM session_pending_blackboard_conclusions WHERE session_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, sessionID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	receipt, won, err := retrySessionBlackboardConclusionTx(tx, obligation, idempotencyKey, now)
	if err != nil || !won {
		return receipt, won, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

func retrySessionBlackboardConclusionTx(tx *sql.Tx, obligation PendingBlackboardConclusion, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	var priorObligationID string
	err := tx.QueryRow(`SELECT obligation_id FROM session_assisted_conclusion_retry_keys
		WHERE session_id=? AND idempotency_key=?`, obligation.SessionID, idempotencyKey).Scan(&priorObligationID)
	if err == nil {
		prior, loadErr := loadSessionPendingBlackboardConclusionByID(tx, priorObligationID)
		if loadErr != nil {
			return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(loadErr)
		}
		view, viewErr := sessionCombinedConclusionView(tx, prior)
		if viewErr != nil {
			return BlackboardConclusionReceipt{}, false, viewErr
		}
		return view, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("read Session Blackboard conclusion retry idempotency history: %w", err)
	}
	if obligation.State != BlackboardConclusionReceiptActionRequired {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if obligation.ErrorCode == BlackboardConclusionErrorWorkTurnNeverSettled {
		return BlackboardConclusionReceipt{}, false, ErrBlackboardConclusionWorkTurnNeverSettled
	}
	if obligation.RecoveryReason == ConclusionRecoveryAcceptanceAmbiguous {
		// An acceptance-ambiguous provider delivery is never resent: a generic
		// Retry could duplicate a request the provider already accepted.
		return BlackboardConclusionReceipt{}, false, ErrBlackboardConclusionRetryCooldown
	}
	if obligation.NextEligibleAt == nil || now.Before(*obligation.NextEligibleAt) {
		return BlackboardConclusionReceipt{}, false, ErrBlackboardConclusionRetryCooldown
	}
	dispatch, err := sessionActiveConclusionDispatchTx(tx, obligation.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, err
	}
	if dispatch == nil {
		// No active dispatch (for example the failed repair was superseded).
		// Bind the retry to the LATEST dispatch's immutable binding, which is
		// either the live replacement (after a steer recovery) or the original
		// source binding of the completed Work Turn.
		dispatch, err = latestSessionConclusionDispatchTx(tx, obligation.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return BlackboardConclusionReceipt{}, false, err
		}
	}
	retryNumber := obligation.ExplicitRetryCount + 1
	if dispatch == nil {
		initialRequestID, _ := sessionBlackboardConclusionRequestLineage(obligation.SourceContinuationID, obligation.SourceTurnID)
		obligation.State = BlackboardConclusionReceiptPending
		obligation.ExplicitRetryCount++
		obligation.OperatorRetryKey = idempotencyKey
		obligation.ErrorCode = ""
		obligation.NextEligibleAt = nil
		obligation.UpdatedAt = now
		if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if _, err := tx.Exec(`INSERT INTO session_assisted_conclusion_retry_keys
			(session_id,obligation_id,idempotency_key,dispatch_request_id,created_at) VALUES (?,?,?,?,?)`,
			obligation.SessionID, obligation.ID, idempotencyKey, initialRequestID, formatTime(now)); err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session pending Blackboard conclusion retry history: %w", err)
		}
		if err := appendSessionBlackboardConclusionEventTx(tx, sessionObligationView(obligation), EventPayload{
			"phase": "retry_requested", "receipt_id": obligation.ID, "request_id": initialRequestID,
			"explicit_retry_count": obligation.ExplicitRetryCount, "turn_kind": "control",
		}, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return sessionObligationView(obligation), true, nil
	}
	requestID := sessionBlackboardConclusionAttemptRequestID("retry", dispatch.ContinuationID, obligation.SourceTurnID, retryNumber, idempotencyKey)
	nowDispatch := ConclusionDispatch{
		ID: newIDMust(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRetry,
		ContinuationID: dispatch.ContinuationID, SourceSessionID: dispatch.SourceSessionID,
		DispatchRequestID: requestID, BaseRevision: dispatch.BaseRevision,
		DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "superseded_by_retry", now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO session_conclusion_dispatches
		(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
		nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
		formatTime(now), formatTime(now)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session retry Conclusion Dispatch: %w", err)
	}
	obligation.State = BlackboardConclusionReceiptDispatchRequested
	obligation.ExplicitRetryCount++
	obligation.OperatorRetryKey = idempotencyKey
	obligation.ErrorCode = ""
	obligation.NextEligibleAt = nil
	// The operator retry starts a fresh generation: the previous validated
	// result and its bounded rejection detail are no longer current.
	obligation.CanonicalResultJSON = nil
	obligation.CanonicalResultSHA256 = ""
	obligation.ValidationReason = ""
	obligation.ValidationFieldPath = ""
	obligation.ValidationExpected = ""
	obligation.UpdatedAt = now
	if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO session_assisted_conclusion_retry_keys
		(session_id,obligation_id,idempotency_key,dispatch_request_id,created_at) VALUES (?,?,?,?,?)`,
		obligation.SessionID, obligation.ID, idempotencyKey, requestID, formatTime(now)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session Blackboard conclusion retry idempotency history: %w", err)
	}
	view := sessionObligationView(obligation)
	view.ActiveDispatchID = nowDispatch.ID
	view.DispatchKind = nowDispatch.Kind
	view.DispatchRequestID = nowDispatch.DispatchRequestID
	view.ContinuationID = nowDispatch.ContinuationID
	view.SourceSessionID = nowDispatch.SourceSessionID
	view.BaseRevision = nowDispatch.BaseRevision
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{"phase": "retry_requested", "receipt_id": obligation.ID, "request_id": requestID, "explicit_retry_count": obligation.ExplicitRetryCount, "turn_kind": "control"}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// BlackboardConclusionByDispatchRequestID resolves the obligation whose ACTIVE
// dispatch owns the provider request correlation. A callback that resolves to
// a superseded or late dispatch is durably recorded as a late terminal delivery
// outcome and returns ErrBlackboardConclusionDispatchInactive so the
// coordinator drops it; it can never mutate the obligation or Blackboard.
func (s *Service) BlackboardConclusionByDispatchRequestID(dispatchRequestID string) (BlackboardConclusionReceipt, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, ErrInvalidBlackboardConclusionReceipt
	}
	dispatch, err := scanSessionConclusionDispatch(s.db.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
		FROM session_conclusion_dispatches WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, sessionBlackboardConclusionLookupError(err)
	}
	obligation, err := loadSessionPendingBlackboardConclusionByID(s.db, dispatch.ObligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, sessionBlackboardConclusionLookupError(err)
	}
	if !owner.ConclusionDispatchActive(dispatch.DeliveryState) {
		if dispatch.DeliveryState == ConclusionDispatchSuperseded {
			_, _ = s.db.Exec(`UPDATE session_conclusion_dispatches SET delivery_state=?,terminal_outcome=?,updated_at=?
				WHERE dispatch_request_id=? AND delivery_state=?`,
				string(ConclusionDispatchLateTerminal), "late_result", formatTime(time.Now().UTC()),
				dispatchRequestID, string(ConclusionDispatchSuperseded))
		}
		return BlackboardConclusionReceipt{}, ErrBlackboardConclusionDispatchInactive
	}
	return sessionBlackboardConclusionReceiptFromObligationDispatch(&dispatch, obligation), nil
}

// RecordLateConclusionDispatchOutcome durably records a callback that resolved
// to a superseded dispatch: the dispatch becomes late_terminal and can never
// advance the obligation. It is idempotent.
func (s *Service) RecordLateConclusionDispatchOutcome(dispatchRequestID string) error {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return ErrInvalidBlackboardConclusionReceipt
	}
	_, err := s.db.Exec(`UPDATE session_conclusion_dispatches SET delivery_state=?,terminal_outcome=?,updated_at=?
		WHERE dispatch_request_id=? AND delivery_state=?`,
		string(ConclusionDispatchLateTerminal), "late_result", formatTime(time.Now().UTC()),
		dispatchRequestID, string(ConclusionDispatchSuperseded))
	return err
}

// MarkBlackboardConclusionValidated persists canonical closed result bytes on
// the obligation before Blackboard application.
func (s *Service) MarkBlackboardConclusionValidated(dispatchRequestID string, canonicalResult []byte) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || len(canonicalResult) == 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	canonicalResult = append([]byte(nil), canonicalResult...)
	sum := sha256.Sum256(canonicalResult)
	hash := hex.EncodeToString(sum[:])
	return s.advanceSessionObligationStateByDispatchRequest(dispatchRequestID, BlackboardConclusionReceiptAwaitingResult,
		BlackboardConclusionReceiptValidated,
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, now time.Time) error {
			obligation.State = BlackboardConclusionReceiptValidated
			obligation.CanonicalResultJSON = canonicalResult
			obligation.CanonicalResultSHA256 = hash
			obligation.ErrorCode = ""
			obligation.NextEligibleAt = nil
			obligation.UpdatedAt = now
			if _, err := tx.Exec(`UPDATE session_pending_blackboard_conclusions
				SET state=?,canonical_result_json=?,canonical_result_sha256=?,error_code=NULL,next_eligible_at=NULL,updated_at=? WHERE id=?`,
				string(obligation.State), canonicalResult, hash, formatTime(now), obligation.ID); err != nil {
				return err
			}
			dispatch, err := sessionActiveConclusionDispatchTx(tx, obligation.ID)
			if err != nil {
				return err
			}
			if dispatch == nil {
				return ErrInvalidBlackboardConclusionReceipt
			}
			dispatch.DeliveryState = ConclusionDispatchValidated
			if _, err := tx.Exec(`UPDATE session_conclusion_dispatches SET delivery_state=?,updated_at=? WHERE id=?`,
				string(dispatch.DeliveryState), formatTime(now), dispatch.ID); err != nil {
				return err
			}
			view := sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, *obligation)
			return appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
				"phase": "result_validated", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
				"source_turn_id": obligation.SourceTurnID, "control_turn_id": dispatch.ControlTurnID, "result_hash": hash,
			}, now)
		})
}

// MarkBlackboardConclusionApplied completes the obligation with the exact
// Blackboard revision returned by ApplyForSessionContinuation.
func (s *Service) MarkBlackboardConclusionApplied(dispatchRequestID string, appliedRevision int) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || appliedRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return s.advanceSessionObligationStateByDispatchRequest(dispatchRequestID, BlackboardConclusionReceiptValidated,
		BlackboardConclusionReceiptApplied,
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, now time.Time) error {
			obligation.State = BlackboardConclusionReceiptApplied
			obligation.AppliedRevision = intPointer(appliedRevision)
			obligation.UpdatedAt = now
			if _, err := tx.Exec(`UPDATE session_pending_blackboard_conclusions SET state=?,applied_revision=?,updated_at=? WHERE id=?`,
				string(obligation.State), appliedRevision, formatTime(now), obligation.ID); err != nil {
				return err
			}
			dispatch, err := sessionActiveConclusionDispatchTx(tx, obligation.ID)
			if err != nil {
				return err
			}
			if dispatch == nil {
				return ErrInvalidBlackboardConclusionReceipt
			}
			dispatch.DeliveryState = ConclusionDispatchApplied
			dispatch.TerminalOutcome = "applied"
			if _, err := tx.Exec(`UPDATE session_conclusion_dispatches SET delivery_state=?,terminal_outcome=?,updated_at=? WHERE id=?`,
				string(dispatch.DeliveryState), dispatch.TerminalOutcome, formatTime(now), dispatch.ID); err != nil {
				return err
			}
			view := sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, *obligation)
			return appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
				"phase": "applied", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
				"source_turn_id": obligation.SourceTurnID, "control_turn_id": dispatch.ControlTurnID, "applied_revision": appliedRevision,
			}, now)
		})
}

// LatestBlackboardConclusion returns the newest durable obligation for a
// Session together with its ACTIVE dispatch.
func (s *Service) LatestBlackboardConclusion(sessionID string) (*BlackboardConclusionReceipt, error) {
	if _, err := s.Get(sessionID); err != nil {
		return nil, err
	}
	obligation, err := scanSessionPendingBlackboardConclusion(s.db.QueryRow(`
		SELECT `+sessionPendingBlackboardConclusionColumns+`
		FROM session_pending_blackboard_conclusions WHERE session_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest Session Blackboard conclusion obligation: %w", err)
	}
	view, err := sessionCombinedConclusionView(s.db, obligation)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// ValidatedBlackboardConclusions returns durable apply intents for daemon
// startup replay. It is read-only and preserves canonical and idempotency
// lineage exactly as stored.
func (s *Service) ValidatedBlackboardConclusions() ([]BlackboardConclusionReceipt, error) {
	rows, err := s.db.Query(`SELECT `+sessionPendingBlackboardConclusionColumns+`
		FROM session_pending_blackboard_conclusions WHERE state=? ORDER BY created_at,id`, string(BlackboardConclusionReceiptValidated))
	if err != nil {
		return nil, fmt.Errorf("list validated Session Blackboard conclusions: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := scanSessionPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan validated Session Blackboard conclusion: %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate validated Session Blackboard conclusions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close validated Session Blackboard conclusions: %w", err)
	}
	views := make([]BlackboardConclusionReceipt, 0, len(obligations))
	for _, obligation := range obligations {
		view, viewErr := sessionCombinedConclusionView(s.db, obligation)
		if viewErr != nil {
			return nil, fmt.Errorf("scan validated Session Blackboard conclusion view: %w", viewErr)
		}
		views = append(views, view)
	}
	return views, nil
}

// ConclusionDispatches returns the immutable dispatch history for an
// obligation, newest first.
func (s *Service) ConclusionDispatches(obligationID string) ([]ConclusionDispatch, error) {
	obligationID = strings.TrimSpace(obligationID)
	if obligationID == "" {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	rows, err := s.db.Query(`SELECT `+sessionConclusionDispatchColumns+` FROM session_conclusion_dispatches
		WHERE obligation_id=? ORDER BY rowid DESC`, obligationID)
	if err != nil {
		return nil, fmt.Errorf("list Session Conclusion Dispatches: %w", err)
	}
	defer rows.Close()
	var dispatches []ConclusionDispatch
	for rows.Next() {
		dispatch, scanErr := scanSessionConclusionDispatch(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Session Conclusion Dispatch: %w", scanErr)
		}
		dispatches = append(dispatches, dispatch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Session Conclusion Dispatches: %w", err)
	}
	return dispatches, nil
}

// CreateRecoveryConclusionDispatches supersedes every in-flight Conclusion
// Dispatch of a Session that is bound to the old Continuation and creates a NEW
// recovery dispatch bound immutably to the replacement Continuation and
// session. Historical dispatch identity is never rewritten (ADR 0021).
func (s *Service) CreateRecoveryConclusionDispatches(sessionID, oldContinuationID, replacementContinuationID, replacementSessionID string) ([]BlackboardConclusionReceipt, error) {
	sessionID, oldContinuationID, replacementContinuationID, replacementSessionID =
		strings.TrimSpace(sessionID), strings.TrimSpace(oldContinuationID), strings.TrimSpace(replacementContinuationID), strings.TrimSpace(replacementSessionID)
	if sessionID == "" || oldContinuationID == "" || replacementContinuationID == "" || replacementSessionID == "" || oldContinuationID == replacementContinuationID {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin Session recovery Conclusion Dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT `+sessionPendingBlackboardConclusionColumns+` FROM session_pending_blackboard_conclusions
		WHERE session_id=? AND state IN (?,?,?,?,?,?) ORDER BY created_at,id`,
		sessionID, string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested),
		string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested),
		string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list Session recovery Conclusion Dispatches: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := scanSessionPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan Session recovery Conclusion Dispatch obligation: %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate Session recovery Conclusion Dispatch obligations: %w", err)
	}
	_ = rows.Close()

	var views []BlackboardConclusionReceipt
	for _, obligation := range obligations {
		dispatch, dispatchErr := sessionActiveConclusionDispatchTx(tx, obligation.ID)
		if dispatchErr != nil && !errors.Is(dispatchErr, sql.ErrNoRows) {
			return nil, dispatchErr
		}
		boundToOld := dispatch != nil && dispatch.ContinuationID == oldContinuationID
		boundToReplacement := dispatch != nil && dispatch.ContinuationID == replacementContinuationID
		pendingBoundToOld := dispatch == nil && obligation.State == BlackboardConclusionReceiptPending && obligation.SourceContinuationID == oldContinuationID
		if !boundToOld && !pendingBoundToOld {
			if boundToReplacement {
				view, viewErr := sessionCombinedConclusionView(tx, obligation)
				if viewErr != nil {
					return nil, viewErr
				}
				views = append(views, view)
			}
			continue
		}
		if dispatch != nil {
			if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "superseded_by_recovery", now); err != nil {
				return nil, err
			}
		}
		number := sessionConclusionDispatchSequence(tx, obligation.ID) + 1
		requestID := sessionBlackboardConclusionAttemptRequestID("recovery", replacementContinuationID, obligation.SourceTurnID, number, "")
		nowDispatch := ConclusionDispatch{
			ID: newIDMust(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRecovery,
			ContinuationID: replacementContinuationID, SourceSessionID: replacementSessionID,
			DispatchRequestID: requestID, BaseRevision: sessionDispatchBaseRevision(dispatch),
			DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
		}
		if _, err := tx.Exec(`INSERT INTO session_conclusion_dispatches
			(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
			nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
			formatTime(now), formatTime(now)); err != nil {
			return nil, fmt.Errorf("store Session recovery Conclusion Dispatch: %w", err)
		}
		switch obligation.State {
		case BlackboardConclusionReceiptPending, BlackboardConclusionReceiptAwaitingResult:
			obligation.State = BlackboardConclusionReceiptDispatchRequested
		}
		obligation.UpdatedAt = now
		if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
			return nil, err
		}
		view := sessionObligationView(obligation)
		view.ActiveDispatchID = nowDispatch.ID
		view.DispatchKind = nowDispatch.Kind
		view.DispatchRequestID = nowDispatch.DispatchRequestID
		view.ContinuationID = nowDispatch.ContinuationID
		view.SourceSessionID = nowDispatch.SourceSessionID
		view.BaseRevision = nowDispatch.BaseRevision
		if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
			"phase": "recovery_dispatch_created", "receipt_id": obligation.ID, "request_id": requestID,
			"replacement_continuation_id": replacementContinuationID, "replacement_session_id": replacementSessionID,
			"turn_kind": "control",
		}, now); err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Session recovery Conclusion Dispatches: %w", err)
	}
	return views, nil
}

// CreateRecoveryConclusionDispatch supersedes the active dispatch (if any) of
// one obligation and creates a NEW recovery dispatch bound immutably to the
// proven-live replacement continuation + session. The old dispatch identity is
// never rewritten. Won is false when the obligation is already bound to the
// target continuation or is terminal.
func (s *Service) CreateRecoveryConclusionDispatch(obligationID, continuationID, sessionID string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	obligationID, continuationID, sessionID = strings.TrimSpace(obligationID), strings.TrimSpace(continuationID), strings.TrimSpace(sessionID)
	if obligationID == "" || continuationID == "" || sessionID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session recovery Conclusion Dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := loadSessionPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	dispatch, err := sessionActiveConclusionDispatchTx(tx, obligation.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, err
	}
	if dispatch != nil && dispatch.ContinuationID == continuationID && dispatch.SourceSessionID == sessionID {
		view, viewErr := sessionCombinedConclusionView(tx, obligation)
		if viewErr != nil {
			return BlackboardConclusionReceipt{}, false, viewErr
		}
		return view, false, nil
	}
	if obligation.State == BlackboardConclusionReceiptApplied || obligation.State == BlackboardConclusionReceiptClean {
		view, viewErr := sessionCombinedConclusionView(tx, obligation)
		if viewErr != nil {
			return BlackboardConclusionReceipt{}, false, viewErr
		}
		return view, false, nil
	}
	if dispatch != nil {
		if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "superseded_by_recovery", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
	}
	number := sessionConclusionDispatchSequence(tx, obligation.ID) + 1
	requestID := sessionBlackboardConclusionAttemptRequestID("recovery", continuationID, obligation.SourceTurnID, number, "")
	nowDispatch := ConclusionDispatch{
		ID: newIDMust(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRecovery,
		ContinuationID: continuationID, SourceSessionID: sessionID,
		DispatchRequestID: requestID, BaseRevision: sessionDispatchBaseRevision(dispatch),
		DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO session_conclusion_dispatches
		(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
		nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
		formatTime(now), formatTime(now)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session recovery Conclusion Dispatch: %w", err)
	}
	switch obligation.State {
	case BlackboardConclusionReceiptPending, BlackboardConclusionReceiptAwaitingResult:
		obligation.State = BlackboardConclusionReceiptDispatchRequested
	}
	obligation.UpdatedAt = now
	if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := sessionObligationView(obligation)
	view.ActiveDispatchID = nowDispatch.ID
	view.DispatchKind = nowDispatch.Kind
	view.DispatchRequestID = nowDispatch.DispatchRequestID
	view.ContinuationID = nowDispatch.ContinuationID
	view.SourceSessionID = nowDispatch.SourceSessionID
	view.BaseRevision = nowDispatch.BaseRevision
	if err := appendSessionBlackboardConclusionEventTx(tx, view, EventPayload{
		"phase": "recovery_dispatch_created", "receipt_id": obligation.ID, "request_id": requestID,
		"replacement_continuation_id": continuationID, "replacement_session_id": sessionID,
		"turn_kind": "control",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session recovery Conclusion Dispatch: %w", err)
	}
	return view, true, nil
}

// --- internal helpers ---

func sessionObligationView(obligation PendingBlackboardConclusion) BlackboardConclusionReceipt {
	view := BlackboardConclusionReceipt{
		ID: obligation.ID, SessionID: obligation.SessionID,
		ContinuationID:  obligation.SourceContinuationID,
		SourceRequestID: obligation.SourceRequestID, SourceRequestCorrelationExact: obligation.SourceRequestCorrelationExact,
		SourceSessionID: obligation.SourceSessionID, SourceTurnID: obligation.SourceTurnID,
		InternalState: obligation.State, SourceWorkWatermark: obligation.SourceWorkWatermark,
		SemanticPersistenceWatermark: obligation.SemanticPersistenceWatermark,
		SourceSelection:              obligation.SourceSelection, CanonicalResultJSON: obligation.CanonicalResultJSON,
		CanonicalResultSHA256: obligation.CanonicalResultSHA256, ApplyIdempotencyKey: obligation.ApplyIdempotencyKey,
		AppliedRevision: obligation.AppliedRevision, BaseRevision: obligation.BaseRevision, AutomaticTurnCount: obligation.AutomaticTurnCount,
		RepairCount: obligation.RepairCount, VersionRegenerationCount: obligation.VersionRegenerationCount,
		ExplicitRetryCount: obligation.ExplicitRetryCount, OperatorRetryKey: obligation.OperatorRetryKey,
		NextEligibleAt: obligation.NextEligibleAt, ErrorCode: obligation.ErrorCode,
		RecoveryReason: string(obligation.RecoveryReason), ValidationReason: obligation.ValidationReason,
		ValidationFieldPath: obligation.ValidationFieldPath, ValidationExpected: obligation.ValidationExpected,
		CreatedAt: obligation.CreatedAt, UpdatedAt: obligation.UpdatedAt,
	}
	return view
}

func sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch *ConclusionDispatch, obligation PendingBlackboardConclusion) BlackboardConclusionReceipt {
	view := sessionObligationView(obligation)
	if dispatch == nil {
		return view
	}
	view.ContinuationID = dispatch.ContinuationID
	view.SourceSessionID = dispatch.SourceSessionID
	view.DispatchRequestID = dispatch.DispatchRequestID
	view.ControlTurnID = dispatch.ControlTurnID
	// The dispatch carries its own immutable base revision. For an active
	// dispatch it is the current protocol base; for a superseded or terminal
	// dispatch the obligation's latest claimed base revision is authoritative
	// (for example the exhausted version-regeneration action_required state).
	if owner.ConclusionDispatchActive(dispatch.DeliveryState) {
		view.BaseRevision = dispatch.BaseRevision
	}
	if view.BaseRevision == nil {
		view.BaseRevision = obligation.BaseRevision
	}
	view.SynchronizedRevision = dispatch.SynchronizedRevision
	view.SendAttemptCount = dispatch.SendAttemptCount
	view.SendStartedAt = dispatch.SendStartedAt
	view.ActiveDispatchID = dispatch.ID
	view.DispatchKind = dispatch.Kind
	return view
}

type sessionQueryer interface {
	QueryRow(string, ...any) *sql.Row
}

func sessionCombinedConclusionView(queryer sessionQueryer, obligation PendingBlackboardConclusion) (BlackboardConclusionReceipt, error) {
	dispatch, err := scanSessionConclusionDispatch(queryer.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
		FROM session_conclusion_dispatches WHERE obligation_id=? AND delivery_state IN (?,?,?)
		ORDER BY created_at DESC,id DESC LIMIT 1`, obligation.ID,
		string(ConclusionDispatchRequested), string(ConclusionDispatchAwaitingResult), string(ConclusionDispatchValidated)))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, fmt.Errorf("load Session active Conclusion Dispatch: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Terminal obligations (applied / action_required) keep their last
		// dispatch as the visible delivery lineage.
		latest, latestErr := scanSessionConclusionDispatch(queryer.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
			FROM session_conclusion_dispatches WHERE obligation_id=? ORDER BY rowid DESC LIMIT 1`, obligation.ID))
		if latestErr != nil && !errors.Is(latestErr, sql.ErrNoRows) {
			return BlackboardConclusionReceipt{}, fmt.Errorf("load Session latest Conclusion Dispatch: %w", latestErr)
		}
		if latestErr == nil {
			return sessionBlackboardConclusionReceiptFromObligationDispatch(&latest, obligation), nil
		}
		return sessionBlackboardConclusionReceiptFromObligationDispatch(nil, obligation), nil
	}
	return sessionBlackboardConclusionReceiptFromObligationDispatch(&dispatch, obligation), nil
}

func loadSessionPendingBlackboardConclusionByID(queryer sessionQueryer, obligationID string) (PendingBlackboardConclusion, error) {
	return scanSessionPendingBlackboardConclusion(queryer.QueryRow(`SELECT `+sessionPendingBlackboardConclusionColumns+`
		FROM session_pending_blackboard_conclusions WHERE id=?`, obligationID))
}

// loadSessionConclusionByDispatchRequestID resolves the obligation for any
// dispatch request id, active or terminal. Callers that advance the protocol
// must gate on the active dispatch themselves; this loader is used for
// idempotent replay of terminal states (for example applied).
func loadSessionConclusionByDispatchRequestID(tx *sql.Tx, dispatchRequestID string) (PendingBlackboardConclusion, *ConclusionDispatch, error) {
	dispatch, err := scanSessionConclusionDispatch(tx.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
		FROM session_conclusion_dispatches WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	obligation, err := loadSessionPendingBlackboardConclusionByID(tx, dispatch.ObligationID)
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	return obligation, &dispatch, nil
}

func loadSessionActiveConclusionByDispatchRequestID(tx *sql.Tx, dispatchRequestID string) (PendingBlackboardConclusion, *ConclusionDispatch, error) {
	dispatch, err := scanSessionConclusionDispatch(tx.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
		FROM session_conclusion_dispatches WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	if !owner.ConclusionDispatchActive(dispatch.DeliveryState) {
		// A callback for a superseded or late dispatch is obsolete: it must
		// fail closed (ErrNotFound) and can never advance the obligation.
		return PendingBlackboardConclusion{}, nil, sql.ErrNoRows
	}
	obligation, err := loadSessionPendingBlackboardConclusionByID(tx, dispatch.ObligationID)
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	return obligation, &dispatch, nil
}

func sessionConclusionViewByDispatchRequestIDTx(tx *sql.Tx, dispatchRequestID string) (BlackboardConclusionReceipt, error) {
	dispatch, err := scanSessionConclusionDispatch(tx.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
		FROM session_conclusion_dispatches WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, err
	}
	obligation, err := loadSessionPendingBlackboardConclusionByID(tx, dispatch.ObligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, err
	}
	return sessionBlackboardConclusionReceiptFromObligationDispatch(&dispatch, obligation), nil
}

func latestSessionConclusionDispatchTx(tx *sql.Tx, obligationID string) (*ConclusionDispatch, error) {
	dispatch, err := scanSessionConclusionDispatch(tx.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
		FROM session_conclusion_dispatches WHERE obligation_id=? ORDER BY rowid DESC LIMIT 1`, obligationID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dispatch, nil
}

func sessionActiveConclusionDispatchTx(tx *sql.Tx, obligationID string) (*ConclusionDispatch, error) {
	dispatch, err := scanSessionConclusionDispatch(tx.QueryRow(`SELECT `+sessionConclusionDispatchColumns+`
		FROM session_conclusion_dispatches WHERE obligation_id=? AND delivery_state IN (?,?,?)
		ORDER BY created_at DESC,id DESC LIMIT 1`, obligationID,
		string(ConclusionDispatchRequested), string(ConclusionDispatchAwaitingResult), string(ConclusionDispatchValidated)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dispatch, nil
}

func sessionSupersedeActiveDispatchTx(tx *sql.Tx, dispatch *ConclusionDispatch, outcome string, now time.Time) error {
	dispatch.DeliveryState = ConclusionDispatchSuperseded
	dispatch.TerminalOutcome = outcome
	dispatch.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE session_conclusion_dispatches SET delivery_state=?,terminal_outcome=?,updated_at=? WHERE id=?`,
		string(dispatch.DeliveryState), outcome, formatTime(now), dispatch.ID); err != nil {
		return fmt.Errorf("supersede Session Conclusion Dispatch: %w", err)
	}
	return nil
}

func sessionFailActiveDispatchTx(tx *sql.Tx, obligation PendingBlackboardConclusion, dispatch *ConclusionDispatch, code BlackboardConclusionErrorCode, nextEligible time.Time, detail ConclusionValidationDetail, now time.Time) (PendingBlackboardConclusion, error) {
	obligation.State = BlackboardConclusionReceiptActionRequired
	obligation.ErrorCode = code
	obligation.NextEligibleAt = &nextEligible
	obligation.ValidationReason = detail.Reason
	obligation.ValidationFieldPath = detail.FieldPath
	obligation.ValidationExpected = detail.Expected
	obligation.UpdatedAt = now
	if err := updateSessionObligationProtocolTx(tx, obligation); err != nil {
		return PendingBlackboardConclusion{}, err
	}
	if dispatch != nil {
		if err := sessionSupersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
			return PendingBlackboardConclusion{}, err
		}
	}
	return obligation, nil
}

func updateSessionObligationProtocolTx(tx *sql.Tx, obligation PendingBlackboardConclusion) error {
	if _, err := tx.Exec(`UPDATE session_pending_blackboard_conclusions
		SET state=?,canonical_result_json=?,canonical_result_sha256=?,applied_revision=?,base_revision=?,automatic_turn_count=?,repair_count=?,
		version_regeneration_count=?,explicit_retry_count=?,operator_retry_key=?,error_code=?,recovery_reason=?,next_eligible_at=?,
		validation_reason=?,validation_field_path=?,validation_expected=?,updated_at=? WHERE id=?`,
		string(obligation.State), obligation.CanonicalResultJSON, obligation.CanonicalResultSHA256,
		intPointerValue(obligation.AppliedRevision), intPointerValue(obligation.BaseRevision), obligation.AutomaticTurnCount, obligation.RepairCount,
		obligation.VersionRegenerationCount, obligation.ExplicitRetryCount, obligation.OperatorRetryKey,
		string(obligation.ErrorCode), string(obligation.RecoveryReason),
		formatTimePtr(obligation.NextEligibleAt), obligation.ValidationReason, obligation.ValidationFieldPath,
		obligation.ValidationExpected, formatTime(obligation.UpdatedAt), obligation.ID); err != nil {
		return fmt.Errorf("update Session Blackboard conclusion obligation: %w", err)
	}
	return nil
}

func (s *Service) advanceSessionObligationStateByDispatchRequest(dispatchRequestID string, from, to BlackboardConclusionReceiptState, advance func(*sql.Tx, *PendingBlackboardConclusion, time.Time) error) (BlackboardConclusionReceipt, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion %s: %w", to, err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := loadSessionConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if obligation.State != from {
		if obligation.State == to {
			return sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	if err := advance(tx, &obligation, now); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("mark Session Blackboard conclusion %s: %w", to, err)
	}
	view, err := sessionConclusionViewByDispatchRequestIDTx(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session Blackboard conclusion %s: %w", to, err)
	}
	return view, true, nil
}

func (s *Service) advanceSessionActiveConclusionDispatch(dispatchRequestID string, fromDispatch, toDispatch ConclusionDispatchState, toObligation BlackboardConclusionReceiptState,
	replayMatches func(BlackboardConclusionReceipt) bool,
	advance func(*sql.Tx, *PendingBlackboardConclusion, *ConclusionDispatch, time.Time) error,
) (BlackboardConclusionReceipt, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion %s: %w", toDispatch, err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := loadSessionActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if dispatch == nil || dispatch.DeliveryState != fromDispatch {
		if dispatch != nil && dispatch.DeliveryState == toDispatch && replayMatches(sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)) {
			return sessionBlackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	if err := advance(tx, &obligation, dispatch, now); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("mark Session Blackboard conclusion %s: %w", toDispatch, err)
	}
	obligation.State = toObligation
	obligation.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE session_pending_blackboard_conclusions SET state=?,updated_at=? WHERE id=?`,
		string(obligation.State), formatTime(now), obligation.ID); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view, err := sessionConclusionViewByDispatchRequestIDTx(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session Blackboard conclusion %s: %w", toDispatch, err)
	}
	return view, true, nil
}

func scanSessionPendingBlackboardConclusion(row interface{ Scan(...any) error }) (PendingBlackboardConclusion, error) {
	var obligation PendingBlackboardConclusion
	var state, createdAt, updatedAt string
	var nextEligibleAt, errorCode, recoveryReason, validationReason, validationFieldPath, validationExpected, applyKey, resultHash, operatorRetryKey sql.NullString
	var appliedRevision, baseRevision sql.NullInt64
	var canonicalResult []byte
	if err := row.Scan(&obligation.ID, &obligation.SessionID, &obligation.SourceRequestID, &obligation.SourceRequestCorrelationExact,
		&obligation.SourceContinuationID, &obligation.SourceSessionID, &obligation.SourceTurnID, &state,
		&obligation.SourceWorkWatermark, &obligation.SemanticPersistenceWatermark,
		&obligation.SourceSelection.ModelProviderID, &obligation.SourceSelection.Model, &obligation.SourceSelection.ReasoningEffort,
		&canonicalResult, &resultHash, &applyKey, &appliedRevision, &baseRevision, &obligation.AutomaticTurnCount,
		&obligation.RepairCount, &obligation.VersionRegenerationCount, &obligation.ExplicitRetryCount,
		&operatorRetryKey, &nextEligibleAt, &errorCode, &recoveryReason,
		&validationReason, &validationFieldPath, &validationExpected, &createdAt, &updatedAt); err != nil {
		return PendingBlackboardConclusion{}, err
	}
	obligation.State = BlackboardConclusionReceiptState(state)
	obligation.CanonicalResultJSON = append([]byte(nil), canonicalResult...)
	obligation.CanonicalResultSHA256 = resultHash.String
	obligation.ApplyIdempotencyKey = applyKey.String
	obligation.OperatorRetryKey = operatorRetryKey.String
	obligation.ErrorCode = BlackboardConclusionErrorCode(errorCode.String)
	obligation.RecoveryReason = ConclusionRecoveryReason(recoveryReason.String)
	obligation.ValidationReason = validationReason.String
	obligation.ValidationFieldPath = validationFieldPath.String
	obligation.ValidationExpected = validationExpected.String
	if appliedRevision.Valid {
		obligation.AppliedRevision = intPointer(int(appliedRevision.Int64))
	}
	if baseRevision.Valid {
		obligation.BaseRevision = intPointer(int(baseRevision.Int64))
	}
	if nextEligibleAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, nextEligibleAt.String)
		if err != nil {
			return PendingBlackboardConclusion{}, fmt.Errorf("parse Session Blackboard conclusion next_eligible_at: %w", err)
		}
		obligation.NextEligibleAt = &parsed
	}
	var err error
	if obligation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return PendingBlackboardConclusion{}, fmt.Errorf("parse Session Blackboard conclusion created_at: %w", err)
	}
	if obligation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return PendingBlackboardConclusion{}, fmt.Errorf("parse Session Blackboard conclusion updated_at: %w", err)
	}
	return obligation, nil
}

func scanSessionConclusionDispatch(row interface{ Scan(...any) error }) (ConclusionDispatch, error) {
	var dispatch ConclusionDispatch
	var kind, deliveryState, createdAt, updatedAt string
	var dispatchRequestID, controlTurnID, sendStartedAt, terminalOutcome sql.NullString
	var baseRevision, synchronizedRevision sql.NullInt64
	if err := row.Scan(&dispatch.ID, &dispatch.ObligationID, &kind, &dispatch.ContinuationID, &dispatch.SourceSessionID,
		&dispatchRequestID, &controlTurnID, &baseRevision, &synchronizedRevision, &deliveryState,
		&dispatch.SendAttemptCount, &sendStartedAt, &terminalOutcome, &createdAt, &updatedAt); err != nil {
		return ConclusionDispatch{}, err
	}
	dispatch.Kind = ConclusionDispatchKind(kind)
	dispatch.DeliveryState = ConclusionDispatchState(deliveryState)
	dispatch.DispatchRequestID = dispatchRequestID.String
	dispatch.ControlTurnID = controlTurnID.String
	dispatch.TerminalOutcome = terminalOutcome.String
	if baseRevision.Valid {
		dispatch.BaseRevision = intPointer(int(baseRevision.Int64))
	}
	if synchronizedRevision.Valid {
		dispatch.SynchronizedRevision = intPointer(int(synchronizedRevision.Int64))
	}
	if sendStartedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, sendStartedAt.String)
		if err != nil {
			return ConclusionDispatch{}, fmt.Errorf("parse Session Conclusion Dispatch send_started_at: %w", err)
		}
		dispatch.SendStartedAt = &parsed
	}
	var err error
	if dispatch.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return ConclusionDispatch{}, fmt.Errorf("parse Session Conclusion Dispatch created_at: %w", err)
	}
	if dispatch.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return ConclusionDispatch{}, fmt.Errorf("parse Session Conclusion Dispatch updated_at: %w", err)
	}
	return dispatch, nil
}

func validSessionBlackboardConclusionErrorCode(code BlackboardConclusionErrorCode) bool {
	return owner.ValidBlackboardConclusionErrorCode(code)
}

func sessionBlackboardConclusionLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("load Session Blackboard conclusion obligation: %w", err)
}

func sessionBlackboardConclusionRequestLineage(continuationID, sourceTurnID string) (string, string) {
	lineage := fmt.Sprintf("%d:%s%d:%s", len(continuationID), continuationID, len(sourceTurnID), sourceTurnID)
	sum := sha256.Sum256([]byte(lineage))
	digest := hex.EncodeToString(sum[:])
	return "conclude:v1:" + digest, "assisted-apply:v1:" + digest
}

func sessionBlackboardConclusionAttemptRequestID(kind, continuationID, sourceTurnID string, number int, key string) string {
	lineage := fmt.Sprintf("%s:%d:%s:%d:%s:%d:%s", kind, len(continuationID), continuationID, len(sourceTurnID), sourceTurnID, number, key)
	sum := sha256.Sum256([]byte(lineage))
	return "conclude-" + kind + ":v1:" + hex.EncodeToString(sum[:])
}

func appendSessionBlackboardConclusionEventTx(tx *sql.Tx, receipt BlackboardConclusionReceipt, payload EventPayload, now time.Time) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Session Blackboard conclusion Event: %w", err)
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM session_events WHERE session_id=?`, receipt.SessionID).Scan(&maxSeq); err != nil {
		return fmt.Errorf("read Session Blackboard conclusion Event sequence: %w", err)
	}
	eventID := newIDMust()
	if _, err := tx.Exec(`INSERT INTO session_events (id,session_id,seq,kind,payload_json,created_at)
		VALUES (?,?,?,?,?,?)`, eventID, receipt.SessionID, int(maxSeq.Int64)+1,
		string(EventKindBlackboardConclusion), string(payloadJSON), formatTime(now)); err != nil {
		return fmt.Errorf("store Session Blackboard conclusion Event: %w", err)
	}
	return nil
}

func sessionConclusionDispatchSequence(tx *sql.Tx, obligationID string) int {
	var count int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM session_conclusion_dispatches WHERE obligation_id=?`, obligationID).Scan(&count)
	return count
}

func sessionDispatchBaseRevision(dispatch *ConclusionDispatch) *int {
	if dispatch == nil {
		return nil
	}
	return dispatch.BaseRevision
}

func sessionObligationDispatchRequestID(dispatch *ConclusionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.DispatchRequestID
}

func intPointer(value int) *int { return &value }

func intPointerValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func formatTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
