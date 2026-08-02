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
	CreatedAt                     time.Time                        `json:"created_at"`
	UpdatedAt                     time.Time                        `json:"updated_at"`
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
		if receipt.ErrorCode == BlackboardConclusionErrorWorkTurnNeverSettled {
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
)

const sessionBlackboardConclusionReceiptColumns = `id,session_id,continuation_id,source_request_id,source_request_correlation_exact,source_session_id,source_turn_id,state,
    source_work_watermark,semantic_persistence_watermark,dispatch_request_id,control_turn_id,base_revision,synchronized_revision,source_model_provider_id,source_model,
    source_reasoning_effort,canonical_result_json,canonical_result_sha256,apply_idempotency_key,applied_revision,automatic_turn_count,
    repair_count,version_regeneration_count,explicit_retry_count,operator_retry_key,send_attempt_count,send_started_at,next_eligible_at,error_code,created_at,updated_at`

// RecordBlackboardConclusionCheckpoint creates one idempotent Session-local
// debt receipt at the completed Work Turn boundary.
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
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion receipt: %w", err)
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
	prior, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE session_id=? AND continuation_id=? AND source_turn_id=?`, sessionID, continuationID, sourceTurnID))
	if err == nil {
		return prior, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("load Session Blackboard conclusion checkpoint: %w", err)
	}

	now := time.Now().UTC()
	receiptState := BlackboardConclusionReceiptPending
	phase := "pending_detected"
	if watermarks.SourceWork <= watermarks.SemanticPersistence {
		receiptState = BlackboardConclusionReceiptClean
		phase = "persistence_current"
	}
	receipt := BlackboardConclusionReceipt{
		ID: newIDMust(), SessionID: sessionID, ContinuationID: continuationID,
		SourceRequestID: sourceRequestID, SourceRequestCorrelationExact: true,
		SourceSessionID: sourceSessionID, SourceTurnID: sourceTurnID, InternalState: receiptState,
		SourceWorkWatermark: watermarks.SourceWork, SemanticPersistenceWatermark: watermarks.SemanticPersistence,
		SourceSelection: sourceSelection, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO session_assisted_conclusion_receipts
        (id,session_id,continuation_id,source_request_id,source_request_correlation_exact,source_session_id,source_turn_id,state,source_work_watermark,semantic_persistence_watermark,
         source_model_provider_id,source_model,source_reasoning_effort,created_at,updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.SessionID, receipt.ContinuationID, receipt.SourceRequestID,
		receipt.SourceRequestCorrelationExact, receipt.SourceSessionID, receipt.SourceTurnID, string(receipt.InternalState),
		receipt.SourceWorkWatermark, receipt.SemanticPersistenceWatermark, receipt.SourceSelection.ModelProviderID,
		receipt.SourceSelection.Model, receipt.SourceSelection.ReasoningEffort, formatTime(now), formatTime(now)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store Session Blackboard conclusion checkpoint: %w", err)
	}
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": phase, "receipt_id": receipt.ID, "source_turn_id": receipt.SourceTurnID,
		"source_work_watermark": receipt.SourceWorkWatermark, "semantic_persistence_watermark": receipt.SemanticPersistenceWatermark,
	}); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session Blackboard conclusion checkpoint: %w", err)
	}
	return receipt, true, nil
}

// MarkBlackboardConclusionSendStarted closes the provider-acceptance ambiguity
// window before SendTurn. It is an exact-once claim.
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
	result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET send_attempt_count=1,send_started_at=?,updated_at=?
        WHERE dispatch_request_id=? AND send_attempt_count=0 AND send_started_at IS NULL AND state IN (?,?,?)`,
		formatTime(now), formatTime(now), dispatchRequestID,
		string(BlackboardConclusionReceiptDispatchRequested), string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	changed, _ := result.RowsAffected()
	receipt, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+` FROM session_assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	eligible := receipt.InternalState == BlackboardConclusionReceiptDispatchRequested || receipt.InternalState == BlackboardConclusionReceiptRepairDispatchRequested || receipt.InternalState == BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	if !eligible || receipt.SendAttemptCount != 1 || receipt.SendStartedAt == nil {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if changed == 0 {
		return receipt, false, nil
	}
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// ClaimBlackboardConclusionDispatch persists deterministic provider and
// Session Blackboard idempotency lineage before any external SendTurn.
func (s *Service) ClaimBlackboardConclusionDispatch(receiptID string, baseRevision int) (BlackboardConclusionReceipt, bool, error) {
	receiptID = strings.TrimSpace(receiptID)
	if receiptID == "" || baseRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := loadSessionBlackboardConclusionReceiptByID(tx, receiptID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	dispatchID, applyKey := sessionBlackboardConclusionRequestLineage(receipt.ContinuationID, receipt.SourceTurnID)
	if receipt.InternalState != BlackboardConclusionReceiptPending {
		if receipt.DispatchRequestID == dispatchID && receipt.ApplyIdempotencyKey == applyKey && receipt.BaseRevision != nil && *receipt.BaseRevision == baseRevision {
			return receipt, false, nil
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts
        SET state=?,dispatch_request_id=?,base_revision=?,apply_idempotency_key=?,automatic_turn_count=1,updated_at=?
        WHERE id=? AND state=?`, string(BlackboardConclusionReceiptDispatchRequested), dispatchID, baseRevision, applyKey,
		formatTime(now), receipt.ID, string(BlackboardConclusionReceiptPending))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Session Blackboard conclusion dispatch: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Session Blackboard conclusion dispatch lost update")
	}
	receipt.InternalState = BlackboardConclusionReceiptDispatchRequested
	receipt.DispatchRequestID, receipt.ApplyIdempotencyKey = dispatchID, applyKey
	receipt.BaseRevision, receipt.AutomaticTurnCount, receipt.UpdatedAt = intPointer(baseRevision), 1, now
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "dispatch_requested", "receipt_id": receipt.ID, "source_turn_id": receipt.SourceTurnID,
		"request_id": dispatchID, "base_revision": baseRevision, "turn_kind": "control",
	}); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session Blackboard conclusion dispatch: %w", err)
	}
	return receipt, true, nil
}

// MarkBlackboardConclusionAwaiting records provider acceptance of a control
// Turn. Replaying the same provider correlation is idempotent.
func (s *Service) MarkBlackboardConclusionAwaiting(dispatchRequestID, controlTurnID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID, controlTurnID = strings.TrimSpace(dispatchRequestID), strings.TrimSpace(controlTurnID)
	if dispatchRequestID == "" || controlTurnID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	from := BlackboardConclusionReceiptDispatchRequested
	current, err := s.BlackboardConclusionByDispatchRequestID(dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if current.InternalState == BlackboardConclusionReceiptRepairDispatchRequested {
		from = BlackboardConclusionReceiptRepairDispatchRequested
	} else if current.InternalState == BlackboardConclusionReceiptVersionRegenerationDispatchRequested {
		from = BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	}
	return s.advanceSessionBlackboardConclusion(dispatchRequestID, from, BlackboardConclusionReceiptAwaitingResult,
		func(receipt BlackboardConclusionReceipt) bool { return receipt.ControlTurnID == controlTurnID },
		func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,control_turn_id=?,next_eligible_at=NULL,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptAwaitingResult), controlTurnID, formatTime(now), receipt.ID, string(from)); err != nil {
				return err
			}
			receipt.InternalState, receipt.ControlTurnID, receipt.NextEligibleAt = BlackboardConclusionReceiptAwaitingResult, controlTurnID, nil
			return appendSessionBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "awaiting_result", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"source_turn_id": receipt.SourceTurnID, "control_turn_id": controlTurnID, "turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionFailure permits one automatic repair for an
// invalid result. Forbidden control tools, runtime failures, and later invalid
// results become stable operator-actionable states without recursion.
func (s *Service) HandleBlackboardConclusionFailure(dispatchRequestID string, code BlackboardConclusionErrorCode, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 ||
		(code != BlackboardConclusionErrorInvalidResult && code != BlackboardConclusionErrorToolUseForbidden && code != BlackboardConclusionErrorRuntimeRecoveryRequired) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+` FROM session_assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired {
		return receipt, false, nil
	}
	if receipt.InternalState != BlackboardConclusionReceiptAwaitingResult {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if code == BlackboardConclusionErrorInvalidResult && receipt.AutomaticTurnCount < BlackboardConclusionAutomaticTurnLimit && receipt.RepairCount == 0 && receipt.VersionRegenerationCount == 0 && receipt.ExplicitRetryCount == 0 {
		requestID := sessionBlackboardConclusionAttemptRequestID("repair", receipt.ContinuationID, receipt.SourceTurnID, receipt.RepairCount+1, "")
		nextEligible := now.Add(cooldown)
		result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,dispatch_request_id=?,control_turn_id=NULL,
            send_attempt_count=0,send_started_at=NULL,automatic_turn_count=automatic_turn_count+1,repair_count=repair_count+1,error_code=?,next_eligible_at=?,updated_at=?
            WHERE id=? AND state=?`, string(BlackboardConclusionReceiptRepairDispatchRequested), requestID, string(code), formatTime(nextEligible), formatTime(now), receipt.ID, string(BlackboardConclusionReceiptAwaitingResult))
		if err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Session Blackboard conclusion repair: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
		}
		receipt.InternalState, receipt.DispatchRequestID, receipt.ControlTurnID = BlackboardConclusionReceiptRepairDispatchRequested, requestID, ""
		receipt.SendAttemptCount, receipt.SendStartedAt = 0, nil
		receipt.AutomaticTurnCount++
		receipt.RepairCount++
		receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = code, &nextEligible, now
		if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
			"phase": "repair_requested", "receipt_id": receipt.ID, "request_id": requestID,
			"error_code": string(code), "automatic_turn_count": receipt.AutomaticTurnCount,
			"repair_count": receipt.RepairCount, "turn_kind": "control",
		}, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return receipt, true, nil
	}
	actionCode := code
	if code == BlackboardConclusionErrorInvalidResult && receipt.RepairCount > 0 && receipt.VersionRegenerationCount == 0 {
		actionCode = BlackboardConclusionErrorRepairExhausted
	}
	nextEligible := now.Add(cooldown)
	if receipt.NextEligibleAt != nil {
		nextEligible = receipt.NextEligibleAt.UTC()
	}
	result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), string(actionCode), formatTime(nextEligible), formatTime(now), receipt.ID, string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("require Session Blackboard conclusion action: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState, receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = BlackboardConclusionReceiptActionRequired, actionCode, &nextEligible, now
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
		"error_code": string(actionCode), "next_eligible_at": formatTime(nextEligible),
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// ClaimBlackboardConclusionVersionSync records the intent to synchronize the
// Session working snapshot before version regeneration.
func (s *Service) ClaimBlackboardConclusionVersionSync(dispatchRequestID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return s.advanceSessionBlackboardConclusion(dispatchRequestID, BlackboardConclusionReceiptValidated, BlackboardConclusionReceiptVersionSyncRequested,
		func(BlackboardConclusionReceipt) bool { return true },
		func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,error_code=?,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptVersionSyncRequested), string(BlackboardConclusionErrorVersionConflict), formatTime(now), receipt.ID, string(BlackboardConclusionReceiptValidated)); err != nil {
				return err
			}
			receipt.InternalState, receipt.ErrorCode = BlackboardConclusionReceiptVersionSyncRequested, BlackboardConclusionErrorVersionConflict
			return appendSessionBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "version_sync_requested", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"base_revision": receipt.BaseRevision, "turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionVersionConflict claims one bounded regenerated
// conclusion against the current Session Blackboard revision.
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
	receipt, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired && receipt.ErrorCode == BlackboardConclusionErrorVersionConflict && receipt.BaseRevision != nil && *receipt.BaseRevision == currentRevision {
		return receipt, false, nil
	}
	if receipt.InternalState != BlackboardConclusionReceiptVersionSyncRequested || receipt.BaseRevision == nil || currentRevision <= *receipt.BaseRevision {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	if receipt.VersionRegenerationCount == 0 && receipt.AutomaticTurnCount < BlackboardConclusionAutomaticTurnLimit && receipt.ExplicitRetryCount == 0 {
		requestID := sessionBlackboardConclusionAttemptRequestID("version", receipt.ContinuationID, receipt.SourceTurnID, 1, fmt.Sprintf("%d", currentRevision))
		result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,dispatch_request_id=?,control_turn_id=NULL,
            base_revision=?,canonical_result_json=NULL,canonical_result_sha256=NULL,automatic_turn_count=automatic_turn_count+1,
            version_regeneration_count=1,synchronized_revision=?,send_attempt_count=0,send_started_at=NULL,error_code=?,next_eligible_at=?,updated_at=?
            WHERE id=? AND state=?`, string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), requestID, currentRevision, currentRevision,
			string(BlackboardConclusionErrorVersionConflict), formatTime(nextEligible), formatTime(now), receipt.ID, string(BlackboardConclusionReceiptVersionSyncRequested))
		if err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("claim Session Blackboard conclusion version regeneration: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
		}
		receipt.InternalState, receipt.DispatchRequestID, receipt.ControlTurnID = BlackboardConclusionReceiptVersionRegenerationDispatchRequested, requestID, ""
		receipt.BaseRevision, receipt.SynchronizedRevision = intPointer(currentRevision), intPointer(currentRevision)
		receipt.CanonicalResultJSON, receipt.CanonicalResultSHA256 = nil, ""
		receipt.SendAttemptCount, receipt.SendStartedAt = 0, nil
		receipt.AutomaticTurnCount++
		receipt.VersionRegenerationCount = 1
		receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = BlackboardConclusionErrorVersionConflict, &nextEligible, now
		if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
			"phase": "version_regeneration_requested", "receipt_id": receipt.ID, "request_id": requestID,
			"base_revision": currentRevision, "error_code": string(BlackboardConclusionErrorVersionConflict),
			"automatic_turn_count": receipt.AutomaticTurnCount, "version_regeneration_count": receipt.VersionRegenerationCount,
			"turn_kind": "control",
		}, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return receipt, true, nil
	}
	result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,base_revision=?,canonical_result_json=NULL,
        canonical_result_sha256=NULL,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), currentRevision, string(BlackboardConclusionErrorVersionConflict), formatTime(nextEligible), formatTime(now), receipt.ID, string(BlackboardConclusionReceiptVersionSyncRequested))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("require Session Blackboard version-conflict action: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState, receipt.BaseRevision = BlackboardConclusionReceiptActionRequired, intPointer(currentRevision)
	receipt.CanonicalResultJSON, receipt.CanonicalResultSHA256 = nil, ""
	receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = BlackboardConclusionErrorVersionConflict, &nextEligible, now
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
		"base_revision": currentRevision, "error_code": string(BlackboardConclusionErrorVersionConflict),
		"next_eligible_at": formatTime(nextEligible), "reason": "version_regeneration_exhausted",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

func (s *Service) MarkBlackboardConclusionVersionConflictActionRequired(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	return s.MarkBlackboardConclusionApplyActionRequired(dispatchRequestID, BlackboardConclusionErrorVersionConflict, now, cooldown)
}

// MarkBlackboardConclusionApplyActionRequired fails closed after a validated
// result cannot be applied or synchronized.
func (s *Service) MarkBlackboardConclusionApplyActionRequired(dispatchRequestID string, code BlackboardConclusionErrorCode, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	if strings.TrimSpace(dispatchRequestID) == "" || cooldown < 0 || !validSessionBlackboardConclusionErrorCode(code) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+` FROM session_assisted_conclusion_receipts WHERE dispatch_request_id=?`, strings.TrimSpace(dispatchRequestID)))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired && receipt.ErrorCode == code {
		return receipt, false, nil
	}
	from := receipt.InternalState
	if from != BlackboardConclusionReceiptValidated && from != BlackboardConclusionReceiptVersionSyncRequested {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,canonical_result_json=NULL,canonical_result_sha256=NULL,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), string(code), formatTime(nextEligible), formatTime(now), receipt.ID, string(from))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState, receipt.CanonicalResultJSON, receipt.CanonicalResultSHA256 = BlackboardConclusionReceiptActionRequired, nil, ""
	receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = code, &nextEligible, now
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
		"base_revision": receipt.BaseRevision, "error_code": string(code), "next_eligible_at": formatTime(nextEligible), "reason": "apply_failed",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// MarkBlackboardConclusionRecoveryActionRequired resolves a restart-stranded
// pre-apply receipt. Session recovery deliberately remains owner-local and
// never attempts to bind the receipt to a Task or another Session.
func (s *Service) MarkBlackboardConclusionRecoveryActionRequired(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	receipt, err := s.BlackboardConclusionByDispatchRequestID(dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return s.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(receipt.ID, now, cooldown)
}

func (s *Service) MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(receiptID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	receiptID = strings.TrimSpace(receiptID)
	if receiptID == "" || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := loadSessionBlackboardConclusionReceiptByID(tx, receiptID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired {
		return receipt, false, nil
	}
	switch receipt.InternalState {
	case BlackboardConclusionReceiptPending, BlackboardConclusionReceiptDispatchRequested,
		BlackboardConclusionReceiptRepairDispatchRequested, BlackboardConclusionReceiptVersionSyncRequested,
		BlackboardConclusionReceiptVersionRegenerationDispatchRequested, BlackboardConclusionReceiptAwaitingResult:
	default:
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	if receipt.NextEligibleAt != nil {
		nextEligible = receipt.NextEligibleAt.UTC()
	}
	result, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), string(BlackboardConclusionErrorRuntimeRecoveryRequired), formatTime(nextEligible), formatTime(now), receipt.ID, string(receipt.InternalState))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if changed != 1 {
		current, currentErr := loadSessionBlackboardConclusionReceiptByID(tx, receipt.ID)
		if currentErr == nil && current.InternalState == BlackboardConclusionReceiptActionRequired {
			return current, false, nil
		}
		if currentErr != nil {
			return BlackboardConclusionReceipt{}, false, currentErr
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	receipt.InternalState, receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = BlackboardConclusionReceiptActionRequired, BlackboardConclusionErrorRuntimeRecoveryRequired, &nextEligible, now
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": receipt.DispatchRequestID,
		"error_code": string(BlackboardConclusionErrorRuntimeRecoveryRequired), "next_eligible_at": formatTime(nextEligible), "reason": "dispatch_recovery",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// MarkBlackboardConclusionWorkTurnConflict bounds explicit retries when a
// provider refuses a control Turn because its Work Turn never yielded.
func (s *Service) MarkBlackboardConclusionWorkTurnConflict(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+` FROM session_assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if receipt.InternalState == BlackboardConclusionReceiptActionRequired {
		return receipt, false, nil
	}
	if receipt.InternalState != BlackboardConclusionReceiptDispatchRequested && receipt.InternalState != BlackboardConclusionReceiptRepairDispatchRequested && receipt.InternalState != BlackboardConclusionReceiptVersionRegenerationDispatchRequested {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	code := BlackboardConclusionErrorRuntimeRecoveryRequired
	reason := "work_turn_conflict"
	if receipt.ExplicitRetryCount >= BlackboardConclusionWorkTurnConflictLimit {
		code = BlackboardConclusionErrorWorkTurnNeverSettled
		reason = "work_turn_never_settled"
	}
	nextEligible := now.Add(cooldown)
	if receipt.NextEligibleAt != nil {
		nextEligible = receipt.NextEligibleAt.UTC()
	}
	if _, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,error_code=?,next_eligible_at=?,updated_at=? WHERE id=? AND state=?`,
		string(BlackboardConclusionReceiptActionRequired), string(code), formatTime(nextEligible), formatTime(now), receipt.ID, string(receipt.InternalState)); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	fromState := receipt.InternalState
	receipt.InternalState, receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = BlackboardConclusionReceiptActionRequired, code, &nextEligible, now
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "action_required", "receipt_id": receipt.ID, "request_id": receipt.DispatchRequestID,
		"error_code": string(code), "next_eligible_at": formatTime(nextEligible), "reason": reason,
		"explicit_retry_count": receipt.ExplicitRetryCount, "from_state": string(fromState),
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// BlackboardConclusionRecoveryCandidates returns only durable pre-apply
// states. It is read-only and leaves recovery policy to the daemon.
func (s *Service) BlackboardConclusionRecoveryCandidates() ([]BlackboardConclusionReceipt, error) {
	rows, err := s.db.Query(`SELECT `+sessionBlackboardConclusionReceiptColumns+` FROM session_assisted_conclusion_receipts
        WHERE state IN (?,?,?,?,?,?) ORDER BY created_at,id`,
		string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested), string(BlackboardConclusionReceiptRepairDispatchRequested),
		string(BlackboardConclusionReceiptVersionSyncRequested), string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list Session Blackboard conclusion recovery candidates: %w", err)
	}
	defer rows.Close()
	var receipts []BlackboardConclusionReceipt
	for rows.Next() {
		receipt, scanErr := scanSessionBlackboardConclusionReceipt(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Session Blackboard conclusion recovery candidate: %w", scanErr)
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Session Blackboard conclusion recovery candidates: %w", err)
	}
	return receipts, nil
}

// ReconcileStrandedBlackboardConclusionRecoveries closes every pre-apply
// conclusion after a restart. Session provider ownership is not implicitly
// reopened, so even an unclaimed pending checkpoint becomes explicit,
// bounded operator action instead of waiting forever for a lost Runtime.
func (s *Service) ReconcileStrandedBlackboardConclusionRecoveries(now time.Time, cooldown time.Duration) ([]BlackboardConclusionReceipt, error) {
	if cooldown < 0 {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	rows, err := s.db.Query(`SELECT id FROM session_assisted_conclusion_receipts
	        WHERE state IN (?,?,?,?,?,?) ORDER BY created_at,id`, string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested),
		string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested),
		string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list stranded Session Blackboard conclusion recoveries: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]BlackboardConclusionReceipt, 0, len(ids))
	for _, id := range ids {
		receipt, changed, err := s.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(id, now, cooldown)
		if err != nil {
			return nil, err
		}
		if changed {
			result = append(result, receipt)
		}
	}
	return result, nil
}

// RetryBlackboardConclusion atomically claims an operator-authorized retry.
// The idempotency key is scoped to this Session and cannot target another
// owner or a newer receipt accidentally.
func (s *Service) RetryBlackboardConclusion(receiptID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	receiptID, idempotencyKey = strings.TrimSpace(receiptID), strings.TrimSpace(idempotencyKey)
	if receiptID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := loadSessionBlackboardConclusionReceiptByID(tx, receiptID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	receipt, won, err := retrySessionBlackboardConclusionTx(tx, receipt, idempotencyKey, now)
	if err != nil || !won {
		return receipt, won, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// RetryLatestBlackboardConclusion serializes the operator key and latest
// Session debt lookup in one transaction.
func (s *Service) RetryLatestBlackboardConclusion(sessionID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	sessionID, idempotencyKey = strings.TrimSpace(sessionID), strings.TrimSpace(idempotencyKey)
	if sessionID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if _, err := s.Get(sessionID); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var priorReceiptID string
	err = tx.QueryRow(`SELECT receipt_id FROM session_assisted_conclusion_retry_keys WHERE session_id=? AND idempotency_key=?`, sessionID, idempotencyKey).Scan(&priorReceiptID)
	if err == nil {
		prior, loadErr := loadSessionBlackboardConclusionReceiptByID(tx, priorReceiptID)
		if loadErr != nil {
			return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(loadErr)
		}
		return prior, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, err
	}
	receipt, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE session_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, sessionID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	receipt, won, err := retrySessionBlackboardConclusionTx(tx, receipt, idempotencyKey, now)
	if err != nil || !won {
		return receipt, won, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

func retrySessionBlackboardConclusionTx(tx *sql.Tx, receipt BlackboardConclusionReceipt, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	var priorReceiptID string
	err := tx.QueryRow(`SELECT receipt_id FROM session_assisted_conclusion_retry_keys WHERE session_id=? AND idempotency_key=?`, receipt.SessionID, idempotencyKey).Scan(&priorReceiptID)
	if err == nil {
		prior, loadErr := loadSessionBlackboardConclusionReceiptByID(tx, priorReceiptID)
		if loadErr != nil {
			return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(loadErr)
		}
		return prior, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, err
	}
	if receipt.InternalState != BlackboardConclusionReceiptActionRequired {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if receipt.ErrorCode == BlackboardConclusionErrorWorkTurnNeverSettled {
		return BlackboardConclusionReceipt{}, false, ErrBlackboardConclusionWorkTurnNeverSettled
	}
	if receipt.NextEligibleAt == nil || now.Before(receipt.NextEligibleAt.UTC()) {
		return BlackboardConclusionReceipt{}, false, ErrBlackboardConclusionRetryCooldown
	}
	retryNumber := receipt.ExplicitRetryCount + 1
	requestID := ""
	if receipt.DispatchRequestID == "" && receipt.BaseRevision == nil && receipt.ApplyIdempotencyKey == "" {
		requestID, _ = sessionBlackboardConclusionRequestLineage(receipt.ContinuationID, receipt.SourceTurnID)
		if _, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,explicit_retry_count=explicit_retry_count+1,operator_retry_key=?,error_code=NULL,next_eligible_at=NULL,updated_at=? WHERE id=? AND state=?`,
			string(BlackboardConclusionReceiptPending), idempotencyKey, formatTime(now), receipt.ID, string(BlackboardConclusionReceiptActionRequired)); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		receipt.InternalState, receipt.ExplicitRetryCount, receipt.OperatorRetryKey = BlackboardConclusionReceiptPending, receipt.ExplicitRetryCount+1, idempotencyKey
		receipt.ErrorCode, receipt.NextEligibleAt, receipt.UpdatedAt = "", nil, now
	} else {
		requestID = sessionBlackboardConclusionAttemptRequestID("retry", receipt.ContinuationID, receipt.SourceTurnID, retryNumber, idempotencyKey)
		if _, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,dispatch_request_id=?,control_turn_id=NULL,canonical_result_json=NULL,canonical_result_sha256=NULL,send_attempt_count=0,send_started_at=NULL,explicit_retry_count=explicit_retry_count+1,operator_retry_key=?,updated_at=? WHERE id=? AND state=?`,
			string(BlackboardConclusionReceiptDispatchRequested), requestID, idempotencyKey, formatTime(now), receipt.ID, string(BlackboardConclusionReceiptActionRequired)); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		receipt.InternalState, receipt.DispatchRequestID, receipt.ControlTurnID = BlackboardConclusionReceiptDispatchRequested, requestID, ""
		receipt.CanonicalResultJSON, receipt.CanonicalResultSHA256 = nil, ""
		receipt.SendAttemptCount, receipt.SendStartedAt = 0, nil
		receipt.ExplicitRetryCount++
		receipt.OperatorRetryKey, receipt.ErrorCode, receipt.UpdatedAt = idempotencyKey, "", now
	}
	if _, err := tx.Exec(`INSERT INTO session_assisted_conclusion_retry_keys(session_id,receipt_id,idempotency_key,dispatch_request_id,created_at) VALUES (?,?,?,?,?)`, receipt.SessionID, receipt.ID, idempotencyKey, requestID, formatTime(now)); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := appendSessionBlackboardConclusionEventTx(tx, receipt, EventPayload{
		"phase": "retry_requested", "receipt_id": receipt.ID, "request_id": requestID,
		"explicit_retry_count": receipt.ExplicitRetryCount, "turn_kind": "control",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

func (s *Service) BlackboardConclusionByDispatchRequestID(dispatchRequestID string) (BlackboardConclusionReceipt, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, ErrInvalidBlackboardConclusionReceipt
	}
	receipt, err := scanSessionBlackboardConclusionReceipt(s.db.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, sessionBlackboardConclusionLookupError(err)
	}
	return receipt, nil
}

func (s *Service) MarkBlackboardConclusionValidated(dispatchRequestID string, canonicalResult []byte) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || len(canonicalResult) == 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	canonicalResult = append([]byte(nil), canonicalResult...)
	sum := sha256.Sum256(canonicalResult)
	hash := hex.EncodeToString(sum[:])
	return s.advanceSessionBlackboardConclusion(dispatchRequestID, BlackboardConclusionReceiptAwaitingResult, BlackboardConclusionReceiptValidated,
		func(receipt BlackboardConclusionReceipt) bool {
			return receipt.CanonicalResultSHA256 == hash && string(receipt.CanonicalResultJSON) == string(canonicalResult)
		},
		func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,canonical_result_json=?,canonical_result_sha256=?,error_code=NULL,next_eligible_at=NULL,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptValidated), canonicalResult, hash, formatTime(now), receipt.ID, string(BlackboardConclusionReceiptAwaitingResult)); err != nil {
				return err
			}
			receipt.InternalState, receipt.CanonicalResultJSON, receipt.CanonicalResultSHA256 = BlackboardConclusionReceiptValidated, canonicalResult, hash
			receipt.ErrorCode, receipt.NextEligibleAt = "", nil
			return appendSessionBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "result_validated", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"source_turn_id": receipt.SourceTurnID, "control_turn_id": receipt.ControlTurnID, "result_hash": hash,
			}, now)
		})
}

func (s *Service) MarkBlackboardConclusionApplied(dispatchRequestID string, appliedRevision int) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || appliedRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return s.advanceSessionBlackboardConclusion(dispatchRequestID, BlackboardConclusionReceiptValidated, BlackboardConclusionReceiptApplied,
		func(receipt BlackboardConclusionReceipt) bool {
			return receipt.AppliedRevision != nil && *receipt.AppliedRevision == appliedRevision
		},
		func(tx *sql.Tx, receipt *BlackboardConclusionReceipt, now time.Time) error {
			if _, err := tx.Exec(`UPDATE session_assisted_conclusion_receipts SET state=?,applied_revision=?,updated_at=? WHERE id=? AND state=?`,
				string(BlackboardConclusionReceiptApplied), appliedRevision, formatTime(now), receipt.ID, string(BlackboardConclusionReceiptValidated)); err != nil {
				return err
			}
			receipt.InternalState, receipt.AppliedRevision = BlackboardConclusionReceiptApplied, intPointer(appliedRevision)
			return appendSessionBlackboardConclusionEventTx(tx, *receipt, EventPayload{
				"phase": "applied", "receipt_id": receipt.ID, "request_id": dispatchRequestID,
				"source_turn_id": receipt.SourceTurnID, "control_turn_id": receipt.ControlTurnID, "applied_revision": appliedRevision,
			}, now)
		})
}

func (s *Service) LatestBlackboardConclusion(sessionID string) (*BlackboardConclusionReceipt, error) {
	if _, err := s.Get(sessionID); err != nil {
		return nil, err
	}
	receipt, err := scanSessionBlackboardConclusionReceipt(s.db.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE session_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest Session Blackboard conclusion: %w", err)
	}
	return &receipt, nil
}

func (s *Service) ValidatedBlackboardConclusions() ([]BlackboardConclusionReceipt, error) {
	rows, err := s.db.Query(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE state=? ORDER BY created_at,id`, string(BlackboardConclusionReceiptValidated))
	if err != nil {
		return nil, fmt.Errorf("list validated Session Blackboard conclusions: %w", err)
	}
	defer rows.Close()
	var receipts []BlackboardConclusionReceipt
	for rows.Next() {
		receipt, scanErr := scanSessionBlackboardConclusionReceipt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return receipts, nil
}

func scanSessionBlackboardConclusionReceipt(row interface{ Scan(...any) error }) (BlackboardConclusionReceipt, error) {
	var receipt BlackboardConclusionReceipt
	var state, createdAt, updatedAt string
	var dispatchID, controlTurnID, applyKey, resultHash, retryKey, sendStartedAt, nextEligibleAt, errorCode sql.NullString
	var baseRevision, synchronizedRevision, appliedRevision sql.NullInt64
	var canonicalResult []byte
	if err := row.Scan(&receipt.ID, &receipt.SessionID, &receipt.ContinuationID, &receipt.SourceRequestID, &receipt.SourceRequestCorrelationExact,
		&receipt.SourceSessionID, &receipt.SourceTurnID, &state, &receipt.SourceWorkWatermark, &receipt.SemanticPersistenceWatermark,
		&dispatchID, &controlTurnID, &baseRevision, &synchronizedRevision, &receipt.SourceSelection.ModelProviderID, &receipt.SourceSelection.Model,
		&receipt.SourceSelection.ReasoningEffort, &canonicalResult, &resultHash, &applyKey, &appliedRevision, &receipt.AutomaticTurnCount,
		&receipt.RepairCount, &receipt.VersionRegenerationCount, &receipt.ExplicitRetryCount, &retryKey, &receipt.SendAttemptCount,
		&sendStartedAt, &nextEligibleAt, &errorCode, &createdAt, &updatedAt); err != nil {
		return BlackboardConclusionReceipt{}, err
	}
	receipt.InternalState = BlackboardConclusionReceiptState(state)
	receipt.DispatchRequestID, receipt.ControlTurnID = dispatchID.String, controlTurnID.String
	receipt.CanonicalResultJSON, receipt.CanonicalResultSHA256 = append([]byte(nil), canonicalResult...), resultHash.String
	receipt.ApplyIdempotencyKey, receipt.OperatorRetryKey = applyKey.String, retryKey.String
	receipt.ErrorCode = BlackboardConclusionErrorCode(errorCode.String)
	if baseRevision.Valid {
		receipt.BaseRevision = intPointer(int(baseRevision.Int64))
	}
	if synchronizedRevision.Valid {
		receipt.SynchronizedRevision = intPointer(int(synchronizedRevision.Int64))
	}
	if appliedRevision.Valid {
		receipt.AppliedRevision = intPointer(int(appliedRevision.Int64))
	}
	var err error
	if sendStartedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, sendStartedAt.String)
		if parseErr != nil {
			return BlackboardConclusionReceipt{}, fmt.Errorf("parse Session conclusion send_started_at: %w", parseErr)
		}
		receipt.SendStartedAt = &parsed
	}
	if nextEligibleAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, nextEligibleAt.String)
		if parseErr != nil {
			return BlackboardConclusionReceipt{}, fmt.Errorf("parse Session conclusion next_eligible_at: %w", parseErr)
		}
		receipt.NextEligibleAt = &parsed
	}
	if receipt.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return BlackboardConclusionReceipt{}, fmt.Errorf("parse Session conclusion created_at: %w", err)
	}
	if receipt.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return BlackboardConclusionReceipt{}, fmt.Errorf("parse Session conclusion updated_at: %w", err)
	}
	return receipt, nil
}

func loadSessionBlackboardConclusionReceiptByID(tx *sql.Tx, receiptID string) (BlackboardConclusionReceipt, error) {
	return scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE id=?`, receiptID))
}

func (s *Service) advanceSessionBlackboardConclusion(dispatchRequestID string, from, to BlackboardConclusionReceiptState,
	replayMatches func(BlackboardConclusionReceipt) bool,
	advance func(*sql.Tx, *BlackboardConclusionReceipt, time.Time) error,
) (BlackboardConclusionReceipt, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin Session Blackboard conclusion %s: %w", to, err)
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanSessionBlackboardConclusionReceipt(tx.QueryRow(`SELECT `+sessionBlackboardConclusionReceiptColumns+`
        FROM session_assisted_conclusion_receipts WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, sessionBlackboardConclusionLookupError(err)
	}
	if receipt.InternalState != from {
		if receipt.InternalState == to || sessionReceiptStateAfter(receipt.InternalState, to) {
			if replayMatches(receipt) {
				return receipt, false, nil
			}
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	if err := advance(tx, &receipt, now); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("mark Session Blackboard conclusion %s: %w", to, err)
	}
	receipt.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit Session Blackboard conclusion %s: %w", to, err)
	}
	return receipt, true, nil
}

func sessionReceiptStateAfter(current, target BlackboardConclusionReceiptState) bool {
	return owner.BlackboardConclusionReceiptStateAfter(current, target)
}

func sessionBlackboardConclusionLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("load Session Blackboard conclusion receipt: %w", err)
}

func validSessionBlackboardConclusionErrorCode(code BlackboardConclusionErrorCode) bool {
	return owner.ValidBlackboardConclusionErrorCode(code)
}

func appendSessionBlackboardConclusionEventTx(tx *sql.Tx, receipt BlackboardConclusionReceipt, payload EventPayload, timestamps ...time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Session Blackboard conclusion Event: %w", err)
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM session_events WHERE session_id=?`, receipt.SessionID).Scan(&maxSeq); err != nil {
		return fmt.Errorf("read Session Blackboard conclusion Event sequence: %w", err)
	}
	now := time.Now().UTC()
	if len(timestamps) > 0 && !timestamps[0].IsZero() {
		now = timestamps[0].UTC()
	}
	_, err = tx.Exec(`INSERT INTO session_events(id,session_id,seq,kind,payload_json,created_at) VALUES (?,?,?,?,?,?)`,
		newIDMust(), receipt.SessionID, int(maxSeq.Int64)+1, string(EventKindBlackboardConclusion), string(encoded), formatTime(now))
	if err != nil {
		return fmt.Errorf("store Session Blackboard conclusion Event: %w", err)
	}
	return nil
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

func intPointer(value int) *int { return &value }
