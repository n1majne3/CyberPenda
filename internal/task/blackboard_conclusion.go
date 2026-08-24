package task

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"pentest/internal/owner/conclusion"
)

// The Pending Blackboard Conclusion state machine is shared with Non-Project
// Sessions through internal/owner/conclusion. This file keeps the Task-facing
// API: owner eligibility checks, the Task receipt projection, and error
// translation. Storage rules, dispatch lifecycle, and recovery settlement
// belong to the shared engine.

// PendingBlackboardConclusion is the durable obligation row, keyed by
// (task, source session, source turn) and independent of any Runtime
// Continuation. The shared engine names the owner identity OwnerID.
type PendingBlackboardConclusion = conclusion.PendingBlackboardConclusion

// ConclusionDispatch is one immutable delivery attempt for an obligation. Its
// continuation and source session are written once at creation and never
// updated; recovery creates a new dispatch instead of rewriting an old one.
type ConclusionDispatch = conclusion.ConclusionDispatch

// ErrBlackboardConclusionDispatchInactive reports a callback correlation that
// resolved to a superseded or late Conclusion Dispatch. Only the ACTIVE
// dispatch of an obligation may validate or apply a provider result, so a late
// or duplicate result from an obsolete dispatch can never settle or corrupt
// the obligation (ADR 0021).
var ErrBlackboardConclusionDispatchInactive = conclusion.ErrBlackboardConclusionDispatchInactive

// taskConclusionDialect stores conclusion state against the Task tables.
func taskConclusionDialect() conclusion.Dialect {
	return conclusion.Dialect{
		Subject:                 "Blackboard conclusion",
		ObligationsTable:        "pending_blackboard_conclusions",
		DispatchesTable:         "conclusion_dispatches",
		OwnerColumn:             "task_id",
		ContinuationsTable:      "task_continuations",
		ContinuationOwnerColumn: "task_id",
		EventKind:               string(EventKindBlackboardConclusion),
		AppendEvent:             appendTaskConclusionEventTx,
		RetryKeysTable:          "assisted_conclusion_retry_keys",
		RetryKeysOwnerColumn:    "task_id",
		RetryKeysReceiptColumn:  "receipt_id",
		NewID:                   newID,
	}
}

func appendTaskConclusionEventTx(tx *sql.Tx, ownerID, continuationID, kind string, payload map[string]any, now time.Time) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Blackboard conclusion Event: %w", err)
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id=?`, ownerID).Scan(&maxSeq); err != nil {
		return fmt.Errorf("read Blackboard conclusion Event sequence: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO task_events (id,task_id,continuation_id,seq,kind,payload_json,created_at)
		VALUES (?,?,?,?,?,?,?)`, newID(), ownerID, continuationID, int(maxSeq.Int64)+1, kind, string(payloadJSON), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("store Blackboard conclusion Event: %w", err)
	}
	return nil
}

// conclusions returns the shared conclusion engine bound to the Task tables.
func (s *Service) conclusions() *conclusion.Engine {
	return &conclusion.Engine{DB: s.db.DB, Dialect: taskConclusionDialect()}
}

// mapConclusionError restamps engine sentinels as the Task-package sentinels
// callers already match with errors.Is, preserving message and chain.
func mapConclusionError(err error) error {
	if err == nil {
		return nil
	}
	return conclusion.MapSentinels(err,
		conclusion.ErrOwnerNotFound, ErrNotFound,
		conclusion.ErrInvalidBlackboardConclusionReceipt, ErrInvalidBlackboardConclusionReceipt,
		conclusion.ErrBlackboardConclusionRetryCooldown, ErrBlackboardConclusionRetryCooldown,
		conclusion.ErrBlackboardConclusionWorkTurnNeverSettled, ErrBlackboardConclusionWorkTurnNeverSettled,
	)
}

func receiptFromConclusion(rec conclusion.BlackboardConclusionReceipt) BlackboardConclusionReceipt {
	return BlackboardConclusionReceipt{
		ID:                            rec.ID,
		TaskID:                        rec.OwnerID,
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
		SourceSelection: TurnSelection{
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
func (s *Service) RecordBlackboardConclusionCheckpoint(taskID, continuationID, sourceRequestID, sourceSessionID, sourceTurnID string, sourceSelection TurnSelection, watermarks SemanticDebtWatermarks) (BlackboardConclusionReceipt, bool, error) {
	found, err := s.Get(taskID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if found.RunControls.BlackboardConclusionMode != BlackboardConclusionModeAssisted {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	rec, created, err := s.conclusions().RecordBlackboardConclusionCheckpoint(taskID, continuationID, sourceRequestID, sourceSessionID, sourceTurnID,
		conclusion.Selection{ModelProviderID: sourceSelection.ModelProviderID, Model: sourceSelection.Model, ReasoningEffort: sourceSelection.ReasoningEffort}, watermarks)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), created, nil
}

// ClaimBlackboardConclusionDispatch creates the initial Conclusion Dispatch
// for a pending obligation, bound immutably to the source continuation and
// source session of the completed Work Runtime Turn. Replaying the same claim
// returns the existing active dispatch unchanged.
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

// RetryLatestBlackboardConclusion atomically applies a Task-scoped operator
// retry key to the latest durable conclusion obligation.
func (s *Service) RetryLatestBlackboardConclusion(taskID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	rec, won, err := s.conclusions().RetryLatestBlackboardConclusion(taskID, idempotencyKey, now)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, mapConclusionError(err)
	}
	return receiptFromConclusion(rec), won, nil
}

// RetryLatestBlackboardConclusionForRuntime binds a Runtime-recovery retry to
// one daemon-proven live replacement Runtime. Initial reports that no prior
// Conclusion Dispatch existed for the obligation.
func (s *Service) RetryLatestBlackboardConclusionForRuntime(taskID, idempotencyKey, continuationID, sessionID string, baseRevision int, now time.Time) (BlackboardConclusionReceipt, bool, bool, error) {
	rec, won, initial, err := s.conclusions().RetryLatestBlackboardConclusionForRuntime(
		taskID, idempotencyKey, conclusion.RetryRuntimeBinding{
			ContinuationID: continuationID,
			SessionID:      sessionID,
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

// LatestBlackboardConclusion returns the newest durable obligation for a Task
// together with its ACTIVE dispatch.
func (s *Service) LatestBlackboardConclusion(taskID string) (*BlackboardConclusionReceipt, error) {
	if _, err := s.Get(taskID); err != nil {
		return nil, err
	}
	rec, err := s.conclusions().LatestBlackboardConclusion(taskID)
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

func (s *Service) CreateRecoveryConclusionDispatches(taskID, oldContinuationID, replacementContinuationID, replacementSessionID string) ([]BlackboardConclusionReceipt, error) {
	recs, err := s.conclusions().CreateRecoveryConclusionDispatches(taskID, oldContinuationID, replacementContinuationID, replacementSessionID)
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
