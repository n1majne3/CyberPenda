package conclusion

import (
	"database/sql"

	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/owner"
)

// Dialect names the owner-specific storage and wording one Engine instance
// persists against. All identifiers are package constants, never user input,
// so interpolated SQL stays a fixed statement shape.
type Dialect struct {
	// Subject prefixes error strings, e.g. "Blackboard conclusion dispatch".
	Subject string
	// ObligationsTable stores Pending Blackboard Conclusion rows.
	ObligationsTable string
	// DispatchesTable stores immutable Conclusion Dispatch rows.
	DispatchesTable string
	// OwnerColumn is the owner identity column of ObligationsTable.
	OwnerColumn string
	// ContinuationsTable maps a Continuation id to its owning owner id.
	ContinuationsTable string
	// ContinuationOwnerColumn is the owner identity column of ContinuationsTable.
	ContinuationOwnerColumn string
	// EventKind is the event kind string used for conclusion events.
	EventKind string
	// ReconcilePendingObligations makes daemon-restart reconciliation treat a
	// never-dispatched pending obligation as stranded. Non-Project Sessions
	// need this because a stopped Session cannot resume its dispatch loop.
	ReconcilePendingObligations bool
	// AppendEvent stores one owner-local event inside the caller's transaction.
	// ownerID identifies the owner, continuationID may be empty for owners
	// whose event rows carry no continuation column.
	AppendEvent func(tx *sql.Tx, ownerID, continuationID, kind string, payload map[string]any, now time.Time) error
	// RetryKeysTable stores operator retry idempotency keys.
	RetryKeysTable string
	// RetryKeysOwnerColumn is the owner identity column of RetryKeysTable.
	RetryKeysOwnerColumn string
	// RetryKeysReceiptColumn names the obligation column of RetryKeysTable.
	RetryKeysReceiptColumn string
	// NewID mints row identifiers.
	NewID func() string
}

// Engine is the owner-agnostic Pending Blackboard Conclusion state machine
// shared by Task and Session owners. It owns obligation identity, dispatch
// lifecycle, watermarks, and recovery settlement; owner eligibility checks
// (assisted mode, owner existence) stay with the owning package.
type Engine struct {
	DB      *sql.DB
	Dialect Dialect
}

// RetryRuntimeBinding is a daemon-proven live Runtime target for one
// operator-authorized retry. The Engine revalidates the durable owner,
// Continuation, and native session binding inside the retry transaction.
type RetryRuntimeBinding struct {
	ContinuationID string
	SessionID      string
	BaseRevision   int
}

// Selection is the owner-neutral Runtime Turn Selection snapshot retained on
// an obligation.
type Selection struct {
	ModelProviderID string
	Model           string
	ReasoningEffort string
}

// SemanticDebtWatermarks compare terminal Work Tool Results with the latest
// successful semantic persistence that covers them.
type SemanticDebtWatermarks = owner.SemanticDebtWatermarks
type ConclusionValidationDetail = owner.ConclusionValidationDetail

type (
	BlackboardConclusionReceiptState = owner.BlackboardConclusionReceiptState
	BlackboardConclusionErrorCode    = owner.BlackboardConclusionErrorCode
	ConclusionDispatchKind           = owner.ConclusionDispatchKind
	ConclusionDispatchState          = owner.ConclusionDispatchState
	ConclusionRecoveryReason         = owner.ConclusionRecoveryReason
)

const (
	BlackboardConclusionAutomaticTurnLimit                          = owner.BlackboardConclusionAutomaticTurnLimit
	BlackboardConclusionErrorInvalidResult                          = owner.BlackboardConclusionErrorInvalidResult
	BlackboardConclusionErrorRepairExhausted                        = owner.BlackboardConclusionErrorRepairExhausted
	BlackboardConclusionErrorRuntimeRecoveryRequired                = owner.BlackboardConclusionErrorRuntimeRecoveryRequired
	BlackboardConclusionErrorToolUseForbidden                       = owner.BlackboardConclusionErrorToolUseForbidden
	BlackboardConclusionErrorVersionConflict                        = owner.BlackboardConclusionErrorVersionConflict
	BlackboardConclusionErrorWorkTurnNeverSettled                   = owner.BlackboardConclusionErrorWorkTurnNeverSettled
	BlackboardConclusionReceiptActionRequired                       = owner.BlackboardConclusionReceiptActionRequired
	BlackboardConclusionReceiptApplied                              = owner.BlackboardConclusionReceiptApplied
	BlackboardConclusionReceiptAwaitingResult                       = owner.BlackboardConclusionReceiptAwaitingResult
	BlackboardConclusionReceiptClean                                = owner.BlackboardConclusionReceiptClean
	BlackboardConclusionReceiptDispatchRequested                    = owner.BlackboardConclusionReceiptDispatchRequested
	BlackboardConclusionReceiptPending                              = owner.BlackboardConclusionReceiptPending
	BlackboardConclusionReceiptRepairDispatchRequested              = owner.BlackboardConclusionReceiptRepairDispatchRequested
	BlackboardConclusionReceiptValidated                            = owner.BlackboardConclusionReceiptValidated
	BlackboardConclusionReceiptVersionRegenerationDispatchRequested = owner.BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	BlackboardConclusionReceiptVersionSyncRequested                 = owner.BlackboardConclusionReceiptVersionSyncRequested
	BlackboardConclusionWorkTurnConflictLimit                       = owner.BlackboardConclusionWorkTurnConflictLimit
	ConclusionDispatchActionRequired                                = owner.ConclusionDispatchActionRequired
	ConclusionDispatchApplied                                       = owner.ConclusionDispatchApplied
	ConclusionDispatchAwaitingResult                                = owner.ConclusionDispatchAwaitingResult
	ConclusionDispatchKindInitial                                   = owner.ConclusionDispatchKindInitial
	ConclusionDispatchKindRecovery                                  = owner.ConclusionDispatchKindRecovery
	ConclusionDispatchKindRepair                                    = owner.ConclusionDispatchKindRepair
	ConclusionDispatchKindRetry                                     = owner.ConclusionDispatchKindRetry
	ConclusionDispatchKindVersionRegeneration                       = owner.ConclusionDispatchKindVersionRegeneration
	ConclusionDispatchLateTerminal                                  = owner.ConclusionDispatchLateTerminal
	ConclusionDispatchRequested                                     = owner.ConclusionDispatchRequested
	ConclusionDispatchSuperseded                                    = owner.ConclusionDispatchSuperseded
	ConclusionDispatchValidated                                     = owner.ConclusionDispatchValidated
	ConclusionRecoveryAcceptanceAmbiguous                           = owner.ConclusionRecoveryAcceptanceAmbiguous
	ConclusionRecoveryDispatchFailed                                = owner.ConclusionRecoveryDispatchFailed
	ConclusionRecoveryLegacyCorrelationUnproven                     = owner.ConclusionRecoveryLegacyCorrelationUnproven
	ConclusionRecoveryRuntimeOwnershipNotProven                     = owner.ConclusionRecoveryRuntimeOwnershipNotProven
	ConclusionRecoveryWritableReplacementUnavailable                = owner.ConclusionRecoveryWritableReplacementUnavailable
)

var (
	ErrInvalidBlackboardConclusionReceipt       = errors.New("invalid Blackboard conclusion checkpoint receipt")
	ErrBlackboardConclusionRetryCooldown        = errors.New("Blackboard conclusion retry is not yet eligible")
	ErrBlackboardConclusionWorkTurnNeverSettled = errors.New("Blackboard conclusion work turn never settled")
	// ErrBlackboardConclusionDispatchInactive reports a callback correlation
	// that resolved to a superseded or late Conclusion Dispatch. Only the
	// ACTIVE dispatch of an obligation may validate or apply a provider result,
	// so a late or duplicate result from an obsolete dispatch can never settle
	// or corrupt the obligation (ADR 0021).
	ErrBlackboardConclusionDispatchInactive = errors.New("Blackboard conclusion dispatch is not the active delivery")
	// ErrOwnerNotFound reports that the owning owner or Continuation does not
	// exist; callers map it to their owner-specific not-found error.
	ErrOwnerNotFound = errors.New("Blackboard conclusion owner not found")
)

// PendingBlackboardConclusion is the durable obligation row. It is keyed by
// (owner, source session, source turn) and independent of any Runtime
// Continuation; every delivery attempt is an immutable Conclusion Dispatch
// child row.
type PendingBlackboardConclusion struct {
	ID                            string
	OwnerID                       string
	SourceRequestID               string
	SourceRequestCorrelationExact bool
	SourceContinuationID          string
	SourceSessionID               string
	SourceTurnID                  string
	State                         BlackboardConclusionReceiptState
	SourceWorkWatermark           int
	SemanticPersistenceWatermark  int
	SourceSelection               Selection
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

// BlackboardConclusionReceipt is the owner-neutral coordinator projection of
// an obligation and its active dispatch. Owning packages map OwnerID onto
// their Task or Session identity and own the API-facing projection.
type BlackboardConclusionReceipt struct {
	ID                            string
	OwnerID                       string
	ContinuationID                string
	SourceRequestID               string
	SourceRequestCorrelationExact bool
	SourceSessionID               string
	SourceTurnID                  string
	InternalState                 BlackboardConclusionReceiptState
	SourceWorkWatermark           int
	SemanticPersistenceWatermark  int
	DispatchRequestID             string
	ControlTurnID                 string
	BaseRevision                  *int
	SynchronizedRevision          *int
	SourceSelection               Selection
	CanonicalResultJSON           []byte
	CanonicalResultSHA256         string
	ApplyIdempotencyKey           string
	AppliedRevision               *int
	AutomaticTurnCount            int
	RepairCount                   int
	VersionRegenerationCount      int
	ExplicitRetryCount            int
	OperatorRetryKey              string
	SendAttemptCount              int
	SendStartedAt                 *time.Time
	NextEligibleAt                *time.Time
	ErrorCode                     BlackboardConclusionErrorCode
	RecoveryReason                string
	ActiveDispatchID              string
	DispatchKind                  ConclusionDispatchKind
	ValidationReason              string
	ValidationFieldPath           string
	ValidationExpected            string
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

// obligationColumns returns the fixed obligation column list with the owner
// column in owner-identity position.
func (e *Engine) obligationColumns() string {
	return `id,` + e.Dialect.OwnerColumn + `,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,state,
		source_work_watermark,semantic_persistence_watermark,source_model_provider_id,source_model,source_reasoning_effort,
		canonical_result_json,canonical_result_sha256,apply_idempotency_key,applied_revision,base_revision,automatic_turn_count,
		repair_count,version_regeneration_count,explicit_retry_count,operator_retry_key,next_eligible_at,error_code,recovery_reason,
		validation_reason,validation_field_path,validation_expected,created_at,updated_at`
}

const conclusionDispatchColumns = `id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,control_turn_id,base_revision,
	synchronized_revision,delivery_state,send_attempt_count,send_started_at,terminal_outcome,created_at,updated_at`

// RecordBlackboardConclusionCheckpoint creates the one durable Pending
// Blackboard Conclusion obligation for a completed assisted Work Runtime Turn.
// The obligation is independent of any Runtime Continuation: the source
// continuation is retained only as correlation for the initial dispatch.
// Replay returns the original obligation.
func (e *Engine) RecordBlackboardConclusionCheckpoint(ownerID, continuationID, sourceRequestID, sourceSessionID, sourceTurnID string, sourceSelection Selection, watermarks SemanticDebtWatermarks) (BlackboardConclusionReceipt, bool, error) {
	checkpoint, valid := (owner.ConclusionCheckpointInput{
		OwnerID: ownerID, ContinuationID: continuationID, SourceRequestID: sourceRequestID,
		SourceSessionID: sourceSessionID, SourceTurnID: sourceTurnID,
		ModelProviderID: sourceSelection.ModelProviderID, Model: sourceSelection.Model,
		ReasoningEffort: sourceSelection.ReasoningEffort, Watermarks: watermarks,
	}).Normalize()
	if !valid {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	ownerID, continuationID, sourceRequestID = checkpoint.OwnerID, checkpoint.ContinuationID, checkpoint.SourceRequestID
	sourceSessionID, sourceTurnID, watermarks = checkpoint.SourceSessionID, checkpoint.SourceTurnID, checkpoint.Watermarks
	sourceSelection = Selection{ModelProviderID: checkpoint.ModelProviderID, Model: checkpoint.Model, ReasoningEffort: checkpoint.ReasoningEffort}
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" obligation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var continuationOwnerID string
	if err := tx.QueryRow(`SELECT `+e.Dialect.ContinuationOwnerColumn+` FROM `+e.Dialect.ContinuationsTable+` WHERE id=?`, continuationID).Scan(&continuationOwnerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BlackboardConclusionReceipt{}, false, ErrOwnerNotFound
		}
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("load "+e.Dialect.Subject+" Continuation: %w", err)
	}
	if continuationOwnerID != ownerID {
		return BlackboardConclusionReceipt{}, false, ErrOwnerNotFound
	}

	prior, err := e.scanPendingBlackboardConclusion(tx.QueryRow(`SELECT `+e.obligationColumns()+`
		FROM `+e.Dialect.ObligationsTable+` WHERE `+e.Dialect.OwnerColumn+`=? AND source_session_id=? AND source_turn_id=?`, ownerID, sourceSessionID, sourceTurnID))
	if err == nil {
		view, scanErr := e.combinedConclusionView(tx, prior)
		if scanErr != nil {
			return BlackboardConclusionReceipt{}, false, scanErr
		}
		return view, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("load "+e.Dialect.Subject+" obligation: %w", err)
	}

	now := time.Now().UTC()
	obligationState, phase := owner.InitialConclusionCheckpoint(watermarks)
	obligation := PendingBlackboardConclusion{
		ID: e.Dialect.NewID(), OwnerID: ownerID, SourceRequestID: sourceRequestID, SourceRequestCorrelationExact: true,
		SourceContinuationID: continuationID, SourceSessionID: sourceSessionID, SourceTurnID: sourceTurnID,
		State: obligationState, SourceWorkWatermark: watermarks.SourceWork,
		SemanticPersistenceWatermark: watermarks.SemanticPersistence,
		SourceSelection:              sourceSelection, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO `+e.Dialect.ObligationsTable+`
		(id,`+e.Dialect.OwnerColumn+`,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,state,source_work_watermark,semantic_persistence_watermark,
		 source_model_provider_id,source_model,source_reasoning_effort,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, obligation.ID, obligation.OwnerID, obligation.SourceRequestID,
		obligation.SourceRequestCorrelationExact, obligation.SourceContinuationID, obligation.SourceSessionID, obligation.SourceTurnID,
		string(obligation.State), obligation.SourceWorkWatermark, obligation.SemanticPersistenceWatermark,
		obligation.SourceSelection.ModelProviderID, obligation.SourceSelection.Model, obligation.SourceSelection.ReasoningEffort,
		obligation.CreatedAt.Format(time.RFC3339Nano), obligation.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store "+e.Dialect.Subject+" obligation: %w", err)
	}

	view := obligationView(obligation)
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
		"phase": phase, "receipt_id": obligation.ID, "source_turn_id": obligation.SourceTurnID,
		"source_work_watermark": obligation.SourceWorkWatermark, "semantic_persistence_watermark": obligation.SemanticPersistenceWatermark,
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit "+e.Dialect.Subject+" obligation: %w", err)
	}
	return view, true, nil
}

// ClaimBlackboardConclusionDispatch creates the initial Conclusion Dispatch
// for a pending obligation, bound immutably to the source continuation and
// source session of the completed Work Runtime Turn. Replaying the same claim
// returns the existing active dispatch unchanged.
func (e *Engine) ClaimBlackboardConclusionDispatch(obligationID string, baseRevision int) (BlackboardConclusionReceipt, bool, error) {
	obligationID = strings.TrimSpace(obligationID)
	if obligationID == "" || baseRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := e.loadPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	if obligation.State != BlackboardConclusionReceiptPending {
		if obligation.State == BlackboardConclusionReceiptClean {
			view, viewErr := e.combinedConclusionView(tx, obligation)
			return view, false, viewErr
		}
		// Replay: the obligation already claimed the deterministic initial
		// dispatch. Return the existing ACTIVE dispatch unchanged when its
		// request lineage and base revision match this replay.
		dispatchID, _ := blackboardConclusionRequestLineage(obligation.SourceContinuationID, obligation.SourceTurnID)
		existing, err := e.activeConclusionDispatchTx(tx, obligation.ID)
		if err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if existing != nil && existing.DispatchRequestID == dispatchID && existing.BaseRevision != nil && *existing.BaseRevision == baseRevision {
			view, viewErr := e.combinedConclusionView(tx, obligation)
			return view, false, viewErr
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	dispatchID, applyKey := blackboardConclusionRequestLineage(obligation.SourceContinuationID, obligation.SourceTurnID)
	now := time.Now().UTC()
	dispatch := ConclusionDispatch{
		ID: e.Dialect.NewID(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindInitial,
		ContinuationID: obligation.SourceContinuationID, SourceSessionID: obligation.SourceSessionID,
		DispatchRequestID: dispatchID, BaseRevision: intPointer(baseRevision),
		DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO `+e.Dialect.DispatchesTable+`
		(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, dispatch.ID, dispatch.ObligationID, string(dispatch.Kind), dispatch.ContinuationID,
		dispatch.SourceSessionID, dispatch.DispatchRequestID, baseRevision, string(dispatch.DeliveryState),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store initial Conclusion Dispatch: %w", err)
	}
	obligation.State = BlackboardConclusionReceiptDispatchRequested
	obligation.ApplyIdempotencyKey = applyKey
	obligation.AutomaticTurnCount = 1
	obligation.BaseRevision = intPointer(baseRevision)
	obligation.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE `+e.Dialect.ObligationsTable+`
		SET state=?,apply_idempotency_key=?,automatic_turn_count=?,base_revision=?,updated_at=? WHERE id=?`,
		string(obligation.State), applyKey, obligation.AutomaticTurnCount, baseRevision, now.Format(time.RFC3339Nano), obligation.ID); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("advance "+e.Dialect.Subject+" obligation: %w", err)
	}
	view := obligationView(obligation)
	view.ActiveDispatchID = dispatch.ID
	view.DispatchKind = dispatch.Kind
	view.DispatchRequestID = dispatch.DispatchRequestID
	view.ContinuationID = dispatch.ContinuationID
	view.SourceSessionID = dispatch.SourceSessionID
	view.BaseRevision = dispatch.BaseRevision
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
		"phase": "dispatch_requested", "receipt_id": obligation.ID, "source_turn_id": obligation.SourceTurnID,
		"request_id": dispatchID, "base_revision": baseRevision, "turn_kind": "control",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit "+e.Dialect.Subject+" dispatch: %w", err)
	}
	return view, true, nil
}

// MarkBlackboardConclusionSendStarted closes the provider-acceptance ambiguity
// window on the ACTIVE dispatch before SendTurn. Only the active dispatch may
// claim the fence; replay observes the original timestamp.
func (e *Engine) MarkBlackboardConclusionSendStarted(dispatchRequestID string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || now.IsZero() {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE `+e.Dialect.DispatchesTable+` SET send_attempt_count=1,send_started_at=?,updated_at=?
		WHERE dispatch_request_id=? AND delivery_state=? AND send_attempt_count=0 AND send_started_at IS NULL`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), dispatchRequestID, string(ConclusionDispatchRequested))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	changed, _ := result.RowsAffected()
	view, err := e.conclusionViewByDispatchRequestIDTx(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
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
func (e *Engine) MarkBlackboardConclusionAwaiting(dispatchRequestID, controlTurnID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID, controlTurnID = strings.TrimSpace(dispatchRequestID), strings.TrimSpace(controlTurnID)
	if dispatchRequestID == "" || controlTurnID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return e.advanceActiveConclusionDispatch(dispatchRequestID, ConclusionDispatchRequested, ConclusionDispatchAwaitingResult,
		BlackboardConclusionReceiptAwaitingResult,
		func(receipt BlackboardConclusionReceipt) bool { return receipt.ControlTurnID == controlTurnID },
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, dispatch *ConclusionDispatch, now time.Time) error {
			dispatch.DeliveryState = ConclusionDispatchAwaitingResult
			dispatch.ControlTurnID = controlTurnID
			if _, err := tx.Exec(`UPDATE `+e.Dialect.DispatchesTable+` SET delivery_state=?,control_turn_id=?,updated_at=? WHERE id=?`,
				string(dispatch.DeliveryState), controlTurnID, now.Format(time.RFC3339Nano), dispatch.ID); err != nil {
				return err
			}
			return e.appendBlackboardConclusionEventTx(tx, blackboardConclusionReceiptFromObligationDispatch(dispatch, *obligation), map[string]any{
				"phase": "awaiting_result", "receipt_id": dispatch.ObligationID, "request_id": dispatchRequestID,
				"source_turn_id": obligation.SourceTurnID, "control_turn_id": controlTurnID, "turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionFailure durably resolves a failed Conclude Turn.
// One invalid initial result may claim a single automatic repair, which
// supersedes the failed dispatch and creates a NEW repair dispatch on the same
// immutable continuation and source session. Forbidden tool use, runtime
// failures, and later invalid results become stable operator-actionable states.
func (e *Engine) HandleBlackboardConclusionFailure(dispatchRequestID string, code BlackboardConclusionErrorCode, detail ConclusionValidationDetail, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 ||
		(code != BlackboardConclusionErrorInvalidResult && code != BlackboardConclusionErrorToolUseForbidden && code != BlackboardConclusionErrorRuntimeRecoveryRequired) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if detail.Valid() && code != BlackboardConclusionErrorInvalidResult {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := e.loadActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired {
		return blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	if obligation.State != BlackboardConclusionReceiptAwaitingResult || dispatch.DeliveryState != ConclusionDispatchAwaitingResult {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	if owner.ConclusionRepairAllowed(code, obligation.AutomaticTurnCount, obligation.RepairCount, obligation.VersionRegenerationCount, obligation.ExplicitRetryCount) {
		repairNumber := obligation.RepairCount + 1
		requestID := blackboardConclusionAttemptRequestID("repair", dispatch.ContinuationID, obligation.SourceTurnID, repairNumber, "")
		nextEligible := now.Add(cooldown)
		nowDispatch := ConclusionDispatch{
			ID: e.Dialect.NewID(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRepair,
			ContinuationID: dispatch.ContinuationID, SourceSessionID: dispatch.SourceSessionID,
			DispatchRequestID: requestID, BaseRevision: dispatch.BaseRevision,
			DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
		}
		if err := e.supersedeActiveDispatchTx(tx, dispatch, "superseded_by_repair", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if _, err := tx.Exec(`INSERT INTO `+e.Dialect.DispatchesTable+`
			(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
			nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("store repair Conclusion Dispatch: %w", err)
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
		if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		view := obligationView(obligation)
		view.ActiveDispatchID = nowDispatch.ID
		view.DispatchKind = nowDispatch.Kind
		view.DispatchRequestID = nowDispatch.DispatchRequestID
		view.ContinuationID = nowDispatch.ContinuationID
		view.SourceSessionID = nowDispatch.SourceSessionID
		view.BaseRevision = nowDispatch.BaseRevision
		payload := map[string]any{"phase": "repair_requested", "receipt_id": obligation.ID, "request_id": requestID,
			"error_code": string(code), "automatic_turn_count": obligation.AutomaticTurnCount, "repair_count": obligation.RepairCount, "turn_kind": "control"}
		owner.AppendConclusionValidationEventPayload(payload, detail)
		if err := e.appendBlackboardConclusionEventTx(tx, view, payload, now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		return view, true, nil
	}
	actionCode := owner.ConclusionFailureActionCode(code, obligation.RepairCount, obligation.VersionRegenerationCount)
	nextEligible := now.Add(cooldown)
	if obligation.NextEligibleAt != nil {
		nextEligible = obligation.NextEligibleAt.UTC()
	}
	var failErr error
	obligation, failErr = e.failActiveDispatchTx(tx, obligation, dispatch, actionCode, nextEligible, detail, now)
	if failErr != nil {
		return BlackboardConclusionReceipt{}, false, failErr
	}
	view := blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	payload := map[string]any{"phase": "action_required", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
		"error_code": string(actionCode), "next_eligible_at": nextEligible.Format(time.RFC3339Nano)}
	owner.AppendConclusionValidationEventPayload(payload, detail)
	if err := e.appendBlackboardConclusionEventTx(tx, view, payload, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return view, true, nil
}

// ClaimBlackboardConclusionVersionSync records the intent to synchronize the
// active dispatch's continuation before version regeneration can be claimed.
func (e *Engine) ClaimBlackboardConclusionVersionSync(dispatchRequestID string) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return e.advanceObligationStateByDispatchRequest(dispatchRequestID, BlackboardConclusionReceiptValidated,
		BlackboardConclusionReceiptVersionSyncRequested,
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, now time.Time) error {
			obligation.State = BlackboardConclusionReceiptVersionSyncRequested
			obligation.ErrorCode = BlackboardConclusionErrorVersionConflict
			obligation.UpdatedAt = now
			if _, err := tx.Exec(`UPDATE `+e.Dialect.ObligationsTable+` SET state=?,error_code=?,updated_at=? WHERE id=?`,
				string(obligation.State), string(obligation.ErrorCode), now.Format(time.RFC3339Nano), obligation.ID); err != nil {
				return err
			}
			return e.appendBlackboardConclusionEventTx(tx, obligationView(*obligation), map[string]any{
				"phase": "version_sync_requested", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
				"turn_kind": "control",
			}, now)
		})
}

// HandleBlackboardConclusionVersionConflict discards a validated result whose
// revision guard lost a real race and claims one fresh semantic generation.
// The old validated dispatch is superseded and a NEW version_regeneration
// dispatch is created; budgets belong to the obligation and never reset.
func (e *Engine) HandleBlackboardConclusionVersionConflict(dispatchRequestID string, currentRevision int, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || currentRevision < 0 || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" version conflict: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := e.loadActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired && obligation.ErrorCode == BlackboardConclusionErrorVersionConflict {
		return blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	if obligation.State != BlackboardConclusionReceiptVersionSyncRequested || dispatch.BaseRevision == nil || currentRevision <= *dispatch.BaseRevision {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	nextEligible := now.Add(cooldown)
	if owner.ConclusionVersionRegenerationAllowed(obligation.VersionRegenerationCount, obligation.AutomaticTurnCount, obligation.ExplicitRetryCount) {
		requestID := blackboardConclusionAttemptRequestID("version", dispatch.ContinuationID, obligation.SourceTurnID, 1, fmt.Sprintf("%d", currentRevision))
		nowDispatch := ConclusionDispatch{
			ID: e.Dialect.NewID(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindVersionRegeneration,
			ContinuationID: dispatch.ContinuationID, SourceSessionID: dispatch.SourceSessionID,
			DispatchRequestID: requestID, BaseRevision: intPointer(currentRevision),
			SynchronizedRevision: intPointer(currentRevision), DeliveryState: ConclusionDispatchRequested,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := e.supersedeActiveDispatchTx(tx, dispatch, "superseded_by_version_regeneration", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		if _, err := tx.Exec(`INSERT INTO `+e.Dialect.DispatchesTable+`
			(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,synchronized_revision,delivery_state,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
			nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, currentRevision, currentRevision, string(nowDispatch.DeliveryState),
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return BlackboardConclusionReceipt{}, false, fmt.Errorf("store version regeneration Conclusion Dispatch: %w", err)
		}
		obligation.State = BlackboardConclusionReceiptVersionRegenerationDispatchRequested
		obligation.VersionRegenerationCount = 1
		obligation.AutomaticTurnCount++
		obligation.ErrorCode = BlackboardConclusionErrorVersionConflict
		obligation.NextEligibleAt = &nextEligible
		obligation.CanonicalResultJSON = nil
		obligation.CanonicalResultSHA256 = ""
		obligation.UpdatedAt = now
		if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
		view := obligationView(obligation)
		view.ActiveDispatchID = nowDispatch.ID
		view.DispatchKind = nowDispatch.Kind
		view.DispatchRequestID = nowDispatch.DispatchRequestID
		view.ContinuationID = nowDispatch.ContinuationID
		view.SourceSessionID = nowDispatch.SourceSessionID
		view.BaseRevision = nowDispatch.BaseRevision
		view.SynchronizedRevision = nowDispatch.SynchronizedRevision
		if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
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
	if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := e.supersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
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
func (e *Engine) MarkBlackboardConclusionVersionConflictActionRequired(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	return e.MarkBlackboardConclusionApplyActionRequired(dispatchRequestID, BlackboardConclusionErrorVersionConflict, now, cooldown)
}

// MarkBlackboardConclusionApplyActionRequired fails closed when a validated
// result cannot be applied or its pre-regeneration synchronization fails.
func (e *Engine) MarkBlackboardConclusionApplyActionRequired(dispatchRequestID string, code BlackboardConclusionErrorCode, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 || !validBlackboardConclusionErrorCode(code) {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin incompatible "+e.Dialect.Subject+" version: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := e.loadConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired && obligation.ErrorCode == code {
		return blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
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
	if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := e.supersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
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
func (e *Engine) MarkBlackboardConclusionRecoveryActionRequired(dispatchRequestID string, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || !owner.ValidConclusionRecoveryReason(reason) || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := e.loadConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	return e.markConclusionRecoveryActionRequiredTx(tx, obligation, dispatch, reason, now, cooldown)
}

// MarkBlackboardConclusionRecoveryActionRequiredByReceiptID resolves any
// restart-stranded pre-apply obligation, including pending obligations that do
// not yet have a dispatch. Replays preserve the original cooldown and counters.
func (e *Engine) MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(obligationID string, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	obligationID = strings.TrimSpace(obligationID)
	if obligationID == "" || !owner.ValidConclusionRecoveryReason(reason) || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" dispatch recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := e.loadPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	dispatch, err := e.activeConclusionDispatchTx(tx, obligation.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, err
	}
	return e.markConclusionRecoveryActionRequiredTx(tx, obligation, dispatch, reason, now, cooldown)
}

func (e *Engine) markConclusionRecoveryActionRequiredTx(tx *sql.Tx, obligation PendingBlackboardConclusion, dispatch *ConclusionDispatch, reason ConclusionRecoveryReason, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	if obligation.State == BlackboardConclusionReceiptActionRequired {
		return blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	if !owner.ConclusionRecoveryEligible(obligation.State) {
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
	if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	// Supersede ANY active dispatch so a later provider callback resolves to a
	// terminal delivery and is recorded as a late outcome instead of being
	// silently dropped while the obligation is already action_required.
	if dispatch != nil && owner.ConclusionDispatchActive(dispatch.DeliveryState) {
		if err := e.supersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
	}
	view := blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
		"phase": "action_required", "receipt_id": obligation.ID, "request_id": obligationDispatchRequestID(dispatch),
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
func (e *Engine) MarkBlackboardConclusionWorkTurnConflict(dispatchRequestID string, now time.Time, cooldown time.Duration) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || cooldown < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" work turn conflict: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := e.loadActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	if obligation.State == BlackboardConclusionReceiptActionRequired {
		return blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
	}
	eligible := dispatch.DeliveryState == ConclusionDispatchRequested
	if !eligible {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	errorCode, reason := owner.ConclusionWorkTurnConflictOutcome(obligation.ExplicitRetryCount)
	nextEligible := now.Add(cooldown)
	if obligation.NextEligibleAt != nil {
		nextEligible = obligation.NextEligibleAt.UTC()
	}
	obligation.State = BlackboardConclusionReceiptActionRequired
	obligation.ErrorCode = errorCode
	obligation.NextEligibleAt = &nextEligible
	obligation.UpdatedAt = now
	if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := e.supersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
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
func (e *Engine) BlackboardConclusionRecoveryCandidates() ([]BlackboardConclusionReceipt, error) {
	rows, err := e.DB.Query(`SELECT `+e.obligationColumns()+` FROM `+e.Dialect.ObligationsTable+`
		WHERE state IN (?,?,?,?,?,?) ORDER BY created_at,id`,
		string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested),
		string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested),
		string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list "+e.Dialect.Subject+" recovery candidates: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := e.scanPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan "+e.Dialect.Subject+" recovery candidate: %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate "+e.Dialect.Subject+" recovery candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close "+e.Dialect.Subject+" recovery candidates: %w", err)
	}
	views := make([]BlackboardConclusionReceipt, 0, len(obligations))
	for _, obligation := range obligations {
		view, viewErr := e.combinedConclusionView(e.DB, obligation)
		if viewErr != nil {
			return nil, fmt.Errorf("scan "+e.Dialect.Subject+" recovery candidate view: %w", viewErr)
		}
		views = append(views, view)
	}
	return views, nil
}

// ReconcileStrandedBlackboardConclusionRecoveries makes durably claimed
// repair, version sync/regeneration, and explicit retry work
// operator-actionable after daemon restart. It excludes indistinguishable
// initial dispatch claims (their independent recovery policy cannot be
// inferred) unless an explicit retry was already recorded.
func (e *Engine) ReconcileStrandedBlackboardConclusionRecoveries(now time.Time, cooldown time.Duration) ([]BlackboardConclusionReceipt, error) {
	if cooldown < 0 {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	// Task reconciliation only strands obligations that claimed dispatch work;
	// Non-Project Session reconciliation also strands never-dispatched pending
	// obligations, because a stopped Session cannot resume its dispatch loop.
	var rows *sql.Rows
	var err error
	if e.Dialect.ReconcilePendingObligations {
		rows, err = e.DB.Query(`SELECT `+e.obligationColumns()+` FROM `+e.Dialect.ObligationsTable+`
			WHERE state IN (?,?,?,?,?,?) ORDER BY created_at,id`,
			string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested),
			string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested),
			string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	} else {
		rows, err = e.DB.Query(`SELECT `+e.obligationColumns()+` FROM `+e.Dialect.ObligationsTable+`
			WHERE state IN (?,?,?) OR (state=? AND explicit_retry_count>0) OR (state=? AND (repair_count>0 OR version_regeneration_count>0 OR explicit_retry_count>0))
			ORDER BY created_at,id`, string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested), string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptDispatchRequested),
			string(BlackboardConclusionReceiptAwaitingResult))
	}
	if err != nil {
		return nil, fmt.Errorf("list stranded "+e.Dialect.Subject+" recoveries: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := e.scanPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stranded "+e.Dialect.Subject+" recovery: %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate stranded "+e.Dialect.Subject+" recoveries: %w", err)
	}
	_ = rows.Close()
	reconciled := make([]BlackboardConclusionReceipt, 0, len(obligations))
	for _, obligation := range obligations {
		receipt, changed, err := e.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(obligation.ID, ConclusionRecoveryDispatchFailed, now, cooldown)
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
func (e *Engine) RetryBlackboardConclusion(obligationID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	obligationID, idempotencyKey = strings.TrimSpace(obligationID), strings.TrimSpace(idempotencyKey)
	if obligationID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := e.loadPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	receipt, won, _, err := e.retryBlackboardConclusionTx(tx, obligation, idempotencyKey, nil, false, now)
	if err != nil || !won {
		return receipt, won, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	return receipt, true, nil
}

// RetryLatestBlackboardConclusion atomically applies an owner-scoped operator
// retry key to the latest durable conclusion obligation.
func (e *Engine) RetryLatestBlackboardConclusion(ownerID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	receipt, won, _, err := e.retryLatestBlackboardConclusion(ownerID, idempotencyKey, nil, false, now)
	return receipt, won, err
}

// RetryLatestBlackboardConclusionFailClosedOnDispatchFailure atomically
// retries only when no daemon-proven replacement Runtime binding is required.
func (e *Engine) RetryLatestBlackboardConclusionFailClosedOnDispatchFailure(ownerID, idempotencyKey string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	receipt, won, _, err := e.retryLatestBlackboardConclusion(ownerID, idempotencyKey, nil, true, now)
	return receipt, won, err
}

// RetryLatestBlackboardConclusionForRuntime atomically binds a Runtime-recovery
// retry to a daemon-proven live replacement Runtime. Initial is true only when
// the obligation had never created a prior Conclusion Dispatch.
func (e *Engine) RetryLatestBlackboardConclusionForRuntime(ownerID, idempotencyKey string, binding RetryRuntimeBinding, now time.Time) (BlackboardConclusionReceipt, bool, bool, error) {
	binding.ContinuationID = strings.TrimSpace(binding.ContinuationID)
	binding.SessionID = strings.TrimSpace(binding.SessionID)
	if binding.ContinuationID == "" || binding.SessionID == "" || binding.BaseRevision < 0 {
		return BlackboardConclusionReceipt{}, false, false, ErrInvalidBlackboardConclusionReceipt
	}
	return e.retryLatestBlackboardConclusion(ownerID, idempotencyKey, &binding, true, now)
}

func (e *Engine) retryLatestBlackboardConclusion(ownerID, idempotencyKey string, binding *RetryRuntimeBinding, requireRuntimeBinding bool, now time.Time) (BlackboardConclusionReceipt, bool, bool, error) {
	ownerID, idempotencyKey = strings.TrimSpace(ownerID), strings.TrimSpace(idempotencyKey)
	if ownerID == "" || idempotencyKey == "" {
		return BlackboardConclusionReceipt{}, false, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := e.scanPendingBlackboardConclusion(tx.QueryRow(`SELECT `+e.obligationColumns()+`
		FROM `+e.Dialect.ObligationsTable+` WHERE `+e.Dialect.OwnerColumn+`=? ORDER BY created_at DESC,id DESC LIMIT 1`, ownerID))
	if err != nil {
		return BlackboardConclusionReceipt{}, false, false, e.blackboardConclusionLookupError(err)
	}
	if binding != nil {
		var activeContinuationID, continuationStatus, nativeSessionID string
		if err := tx.QueryRow(`SELECT id,status,native_session_id FROM `+e.Dialect.ContinuationsTable+`
			WHERE `+e.Dialect.ContinuationOwnerColumn+`=? AND status IN ('pending','running','paused')
			ORDER BY number DESC LIMIT 1`, ownerID).
			Scan(&activeContinuationID, &continuationStatus, &nativeSessionID); err != nil {
			return BlackboardConclusionReceipt{}, false, false, e.blackboardConclusionLookupError(err)
		}
		if activeContinuationID != binding.ContinuationID || strings.TrimSpace(nativeSessionID) != binding.SessionID {
			return BlackboardConclusionReceipt{}, false, false, ErrInvalidBlackboardConclusionReceipt
		}
		switch continuationStatus {
		case "pending", "running", "paused":
		default:
			return BlackboardConclusionReceipt{}, false, false, ErrInvalidBlackboardConclusionReceipt
		}
	}
	receipt, won, initial, err := e.retryBlackboardConclusionTx(tx, obligation, idempotencyKey, binding, requireRuntimeBinding, now)
	if err != nil || !won {
		return receipt, won, initial, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, false, err
	}
	return receipt, true, initial, nil
}

func (e *Engine) retryBlackboardConclusionTx(tx *sql.Tx, obligation PendingBlackboardConclusion, idempotencyKey string, binding *RetryRuntimeBinding, requireRuntimeBinding bool, now time.Time) (BlackboardConclusionReceipt, bool, bool, error) {
	var priorObligationID string
	err := tx.QueryRow(`SELECT `+e.Dialect.RetryKeysReceiptColumn+` FROM `+e.Dialect.RetryKeysTable+`
		WHERE `+e.Dialect.RetryKeysOwnerColumn+`=? AND idempotency_key=?`, obligation.OwnerID, idempotencyKey).Scan(&priorObligationID)
	if err == nil {
		prior, loadErr := e.loadPendingBlackboardConclusionByID(tx, priorObligationID)
		if loadErr != nil {
			return BlackboardConclusionReceipt{}, false, false, e.blackboardConclusionLookupError(loadErr)
		}
		view, viewErr := e.combinedConclusionView(tx, prior)
		if viewErr != nil {
			return BlackboardConclusionReceipt{}, false, false, viewErr
		}
		return view, false, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, false, fmt.Errorf("read "+e.Dialect.Subject+" retry idempotency history: %w", err)
	}
	if requireRuntimeBinding && owner.ConclusionRecoveryRequiresRuntimeBinding(obligation.RecoveryReason) && binding == nil {
		return BlackboardConclusionReceipt{}, false, false, ErrInvalidBlackboardConclusionReceipt
	}
	if binding != nil && !owner.ConclusionRecoveryRequiresRuntimeBinding(obligation.RecoveryReason) {
		return BlackboardConclusionReceipt{}, false, false, ErrInvalidBlackboardConclusionReceipt
	}
	switch owner.ConclusionRetryDecisionFor(obligation.State, obligation.ErrorCode, obligation.RecoveryReason, obligation.NextEligibleAt, now) {
	case owner.ConclusionRetryInvalidState:
		return BlackboardConclusionReceipt{}, false, false, ErrInvalidBlackboardConclusionReceipt
	case owner.ConclusionRetryNeverSettled:
		return BlackboardConclusionReceipt{}, false, false, ErrBlackboardConclusionWorkTurnNeverSettled
	case owner.ConclusionRetryAcceptanceAmbiguous, owner.ConclusionRetryCooldown:
		return BlackboardConclusionReceipt{}, false, false, ErrBlackboardConclusionRetryCooldown
	}
	dispatch, err := e.activeConclusionDispatchTx(tx, obligation.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, false, err
	}
	if dispatch == nil {
		// No active dispatch (for example the failed repair was superseded).
		// Bind the retry to the LATEST dispatch's immutable binding, which is
		// either the live replacement (after a steer recovery) or the original
		// source binding of the completed Work Turn.
		dispatch, err = e.latestConclusionDispatchTx(tx, obligation.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return BlackboardConclusionReceipt{}, false, false, err
		}
	}
	retryNumber := obligation.ExplicitRetryCount + 1
	if dispatch == nil {
		if binding != nil {
			requestID := blackboardConclusionAttemptRequestID("retry", binding.ContinuationID, obligation.SourceTurnID, retryNumber, idempotencyKey)
			_, applyKey := blackboardConclusionRequestLineage(obligation.SourceContinuationID, obligation.SourceTurnID)
			nowDispatch := ConclusionDispatch{
				ID: e.Dialect.NewID(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindInitial,
				ContinuationID: binding.ContinuationID, SourceSessionID: binding.SessionID,
				DispatchRequestID: requestID, BaseRevision: intPointer(binding.BaseRevision),
				DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
			}
			if _, err := tx.Exec(`INSERT INTO `+e.Dialect.DispatchesTable+`
				(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
				nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, binding.BaseRevision, string(nowDispatch.DeliveryState),
				now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
				return BlackboardConclusionReceipt{}, false, false, fmt.Errorf("store replacement retry Conclusion Dispatch: %w", err)
			}
			obligation.State = BlackboardConclusionReceiptDispatchRequested
			obligation.ExplicitRetryCount++
			obligation.OperatorRetryKey = idempotencyKey
			obligation.ApplyIdempotencyKey = applyKey
			obligation.BaseRevision = intPointer(binding.BaseRevision)
			obligation.AutomaticTurnCount++
			obligation.ErrorCode = ""
			obligation.RecoveryReason = ""
			obligation.NextEligibleAt = nil
			obligation.UpdatedAt = now
			if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
				return BlackboardConclusionReceipt{}, false, false, err
			}
			if _, err := tx.Exec(`INSERT INTO `+e.Dialect.RetryKeysTable+`
				(`+e.Dialect.RetryKeysOwnerColumn+`,`+e.Dialect.RetryKeysReceiptColumn+`,idempotency_key,dispatch_request_id,created_at) VALUES (?,?,?,?,?)`,
				obligation.OwnerID, obligation.ID, idempotencyKey, requestID, now.Format(time.RFC3339Nano)); err != nil {
				return BlackboardConclusionReceipt{}, false, false, fmt.Errorf("store replacement "+e.Dialect.Subject+" retry history: %w", err)
			}
			view := blackboardConclusionReceiptFromObligationDispatch(&nowDispatch, obligation)
			if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
				"phase": "retry_requested", "receipt_id": obligation.ID, "request_id": requestID,
				"explicit_retry_count": obligation.ExplicitRetryCount, "turn_kind": "control",
			}, now); err != nil {
				return BlackboardConclusionReceipt{}, false, false, err
			}
			return view, true, true, nil
		}
		// Never-dispatched obligation: re-arm to pending so the initial claim
		// runs on the live runtime. The retry history records the deterministic
		// initial request lineage.
		initialRequestID, _ := blackboardConclusionRequestLineage(obligation.SourceContinuationID, obligation.SourceTurnID)
		obligation.State = BlackboardConclusionReceiptPending
		obligation.ExplicitRetryCount++
		obligation.OperatorRetryKey = idempotencyKey
		obligation.ErrorCode = ""
		obligation.NextEligibleAt = nil
		obligation.UpdatedAt = now
		if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
			return BlackboardConclusionReceipt{}, false, false, err
		}
		if _, err := tx.Exec(`INSERT INTO `+e.Dialect.RetryKeysTable+`
			(`+e.Dialect.RetryKeysOwnerColumn+`,`+e.Dialect.RetryKeysReceiptColumn+`,idempotency_key,dispatch_request_id,created_at) VALUES (?,?,?,?,?)`,
			obligation.OwnerID, obligation.ID, idempotencyKey, initialRequestID, now.Format(time.RFC3339Nano)); err != nil {
			return BlackboardConclusionReceipt{}, false, false, fmt.Errorf("store pending "+e.Dialect.Subject+" retry history: %w", err)
		}
		if err := e.appendBlackboardConclusionEventTx(tx, obligationView(obligation), map[string]any{
			"phase": "retry_requested", "receipt_id": obligation.ID, "request_id": initialRequestID,
			"explicit_retry_count": obligation.ExplicitRetryCount, "turn_kind": "control",
		}, now); err != nil {
			return BlackboardConclusionReceipt{}, false, false, err
		}
		return obligationView(obligation), true, true, nil
	}
	dispatchContinuationID, dispatchSessionID := dispatch.ContinuationID, dispatch.SourceSessionID
	dispatchBaseRevision := dispatch.BaseRevision
	if binding != nil {
		dispatchContinuationID = binding.ContinuationID
		dispatchSessionID = binding.SessionID
		dispatchBaseRevision = intPointer(binding.BaseRevision)
	}
	requestID := blackboardConclusionAttemptRequestID("retry", dispatchContinuationID, obligation.SourceTurnID, retryNumber, idempotencyKey)
	nowDispatch := ConclusionDispatch{
		ID: e.Dialect.NewID(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRetry,
		ContinuationID: dispatchContinuationID, SourceSessionID: dispatchSessionID,
		DispatchRequestID: requestID, BaseRevision: dispatchBaseRevision,
		DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.supersedeActiveDispatchTx(tx, dispatch, "superseded_by_retry", now); err != nil {
		return BlackboardConclusionReceipt{}, false, false, err
	}
	if _, err := tx.Exec(`INSERT INTO `+e.Dialect.DispatchesTable+`
		(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
		nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return BlackboardConclusionReceipt{}, false, false, fmt.Errorf("store retry Conclusion Dispatch: %w", err)
	}
	obligation.State = BlackboardConclusionReceiptDispatchRequested
	obligation.ExplicitRetryCount++
	obligation.OperatorRetryKey = idempotencyKey
	obligation.ErrorCode = ""
	if binding != nil {
		obligation.BaseRevision = dispatchBaseRevision
		obligation.RecoveryReason = ""
	}
	obligation.NextEligibleAt = nil
	// The operator retry starts a fresh generation: the previous validated
	// result and its bounded rejection detail are no longer current.
	obligation.CanonicalResultJSON = nil
	obligation.CanonicalResultSHA256 = ""
	obligation.ValidationReason = ""
	obligation.ValidationFieldPath = ""
	obligation.ValidationExpected = ""
	obligation.UpdatedAt = now
	if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, false, err
	}
	if _, err := tx.Exec(`INSERT INTO `+e.Dialect.RetryKeysTable+`
		(`+e.Dialect.RetryKeysOwnerColumn+`,`+e.Dialect.RetryKeysReceiptColumn+`,idempotency_key,dispatch_request_id,created_at) VALUES (?,?,?,?,?)`,
		obligation.OwnerID, obligation.ID, idempotencyKey, requestID, now.Format(time.RFC3339Nano)); err != nil {
		return BlackboardConclusionReceipt{}, false, false, fmt.Errorf("store "+e.Dialect.Subject+" retry idempotency history: %w", err)
	}
	view := obligationView(obligation)
	view.ActiveDispatchID = nowDispatch.ID
	view.DispatchKind = nowDispatch.Kind
	view.DispatchRequestID = nowDispatch.DispatchRequestID
	view.ContinuationID = nowDispatch.ContinuationID
	view.SourceSessionID = nowDispatch.SourceSessionID
	view.BaseRevision = nowDispatch.BaseRevision
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{"phase": "retry_requested", "receipt_id": obligation.ID, "request_id": requestID, "explicit_retry_count": obligation.ExplicitRetryCount, "turn_kind": "control"}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, false, err
	}
	return view, true, false, nil
}

// BlackboardConclusionByDispatchRequestID resolves the obligation whose ACTIVE
// dispatch owns the provider request correlation. Only the ACTIVE dispatch may
// validate or apply a result: a callback that resolves to a superseded or late
// dispatch is durably recorded as a late terminal delivery outcome and returns
// ErrBlackboardConclusionDispatchInactive so the coordinator drops it. It can
// never mutate the obligation or Blackboard (ADR 0021).
func (e *Engine) BlackboardConclusionByDispatchRequestID(dispatchRequestID string) (BlackboardConclusionReceipt, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return BlackboardConclusionReceipt{}, ErrInvalidBlackboardConclusionReceipt
	}
	dispatch, err := e.scanConclusionDispatch(e.DB.QueryRow(`SELECT `+conclusionDispatchColumns+`
		FROM `+e.Dialect.DispatchesTable+` WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, e.blackboardConclusionLookupError(err)
	}
	obligation, err := e.loadPendingBlackboardConclusionByID(e.DB, dispatch.ObligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, e.blackboardConclusionLookupError(err)
	}
	if !e.conclusionDispatchIsActive(dispatch.DeliveryState) {
		// A late or duplicate result for an obsolete dispatch. Record the
		// bounded late outcome on this dispatch (idempotent) and reject the
		// callback so it cannot settle or corrupt the current obligation.
		if dispatch.DeliveryState == ConclusionDispatchSuperseded {
			_, _ = e.DB.Exec(`UPDATE `+e.Dialect.DispatchesTable+` SET delivery_state=?,terminal_outcome=?,updated_at=?
				WHERE dispatch_request_id=? AND delivery_state=?`,
				string(ConclusionDispatchLateTerminal), "late_result", time.Now().UTC().Format(time.RFC3339Nano),
				dispatchRequestID, string(ConclusionDispatchSuperseded))
		}
		return BlackboardConclusionReceipt{}, ErrBlackboardConclusionDispatchInactive
	}
	return blackboardConclusionReceiptFromObligationDispatch(&dispatch, obligation), nil
}

// RecordLateConclusionDispatchOutcome durably records a callback that resolved
// to a superseded dispatch: the dispatch becomes late_terminal and can never
// advance the obligation. It is idempotent.
func (e *Engine) RecordLateConclusionDispatchOutcome(dispatchRequestID string) error {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" {
		return ErrInvalidBlackboardConclusionReceipt
	}
	_, err := e.DB.Exec(`UPDATE `+e.Dialect.DispatchesTable+` SET delivery_state=?,terminal_outcome=?,updated_at=?
		WHERE dispatch_request_id=? AND delivery_state=?`,
		string(ConclusionDispatchLateTerminal), "late_result", time.Now().UTC().Format(time.RFC3339Nano),
		dispatchRequestID, string(ConclusionDispatchSuperseded))
	return err
}

func (e *Engine) conclusionDispatchIsActive(state ConclusionDispatchState) bool {
	return owner.ConclusionDispatchActive(state)
}

// MarkBlackboardConclusionValidated persists canonical closed result bytes on
// the obligation before Blackboard application.
func (e *Engine) MarkBlackboardConclusionValidated(dispatchRequestID string, canonicalResult []byte) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || len(canonicalResult) == 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	canonicalResult = append([]byte(nil), canonicalResult...)
	hash := owner.CanonicalConclusionSHA256(canonicalResult)
	return e.advanceObligationStateByDispatchRequest(dispatchRequestID, BlackboardConclusionReceiptAwaitingResult,
		BlackboardConclusionReceiptValidated,
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, now time.Time) error {
			obligation.State = BlackboardConclusionReceiptValidated
			obligation.CanonicalResultJSON = canonicalResult
			obligation.CanonicalResultSHA256 = hash
			obligation.ErrorCode = ""
			obligation.NextEligibleAt = nil
			obligation.UpdatedAt = now
			if _, err := tx.Exec(`UPDATE `+e.Dialect.ObligationsTable+`
				SET state=?,canonical_result_json=?,canonical_result_sha256=?,error_code=NULL,next_eligible_at=NULL,updated_at=? WHERE id=?`,
				string(obligation.State), canonicalResult, hash, now.Format(time.RFC3339Nano), obligation.ID); err != nil {
				return err
			}
			dispatch, err := e.activeConclusionDispatchTx(tx, obligation.ID)
			if err != nil {
				return err
			}
			if dispatch == nil {
				return ErrInvalidBlackboardConclusionReceipt
			}
			dispatch.DeliveryState = ConclusionDispatchValidated
			if _, err := tx.Exec(`UPDATE `+e.Dialect.DispatchesTable+` SET delivery_state=?,updated_at=? WHERE id=?`,
				string(dispatch.DeliveryState), now.Format(time.RFC3339Nano), dispatch.ID); err != nil {
				return err
			}
			view := blackboardConclusionReceiptFromObligationDispatch(dispatch, *obligation)
			return e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
				"phase": "result_validated", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
				"source_turn_id": obligation.SourceTurnID, "control_turn_id": dispatch.ControlTurnID, "result_hash": hash,
			}, now)
		})
}

// MarkBlackboardConclusionApplied completes the obligation with the exact
// Blackboard revision returned by ApplyForContinuation.
func (e *Engine) MarkBlackboardConclusionApplied(dispatchRequestID string, appliedRevision int) (BlackboardConclusionReceipt, bool, error) {
	dispatchRequestID = strings.TrimSpace(dispatchRequestID)
	if dispatchRequestID == "" || appliedRevision < 0 {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	return e.advanceObligationStateByDispatchRequest(dispatchRequestID, BlackboardConclusionReceiptValidated,
		BlackboardConclusionReceiptApplied,
		func(tx *sql.Tx, obligation *PendingBlackboardConclusion, now time.Time) error {
			obligation.State = BlackboardConclusionReceiptApplied
			obligation.AppliedRevision = intPointer(appliedRevision)
			obligation.UpdatedAt = now
			if _, err := tx.Exec(`UPDATE `+e.Dialect.ObligationsTable+` SET state=?,applied_revision=?,updated_at=? WHERE id=?`,
				string(obligation.State), appliedRevision, now.Format(time.RFC3339Nano), obligation.ID); err != nil {
				return err
			}
			dispatch, err := e.activeConclusionDispatchTx(tx, obligation.ID)
			if err != nil {
				return err
			}
			if dispatch == nil {
				return ErrInvalidBlackboardConclusionReceipt
			}
			dispatch.DeliveryState = ConclusionDispatchApplied
			dispatch.TerminalOutcome = "applied"
			if _, err := tx.Exec(`UPDATE `+e.Dialect.DispatchesTable+` SET delivery_state=?,terminal_outcome=?,updated_at=? WHERE id=?`,
				string(dispatch.DeliveryState), dispatch.TerminalOutcome, now.Format(time.RFC3339Nano), dispatch.ID); err != nil {
				return err
			}
			view := blackboardConclusionReceiptFromObligationDispatch(dispatch, *obligation)
			return e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
				"phase": "applied", "receipt_id": obligation.ID, "request_id": dispatchRequestID,
				"source_turn_id": obligation.SourceTurnID, "control_turn_id": dispatch.ControlTurnID, "applied_revision": appliedRevision,
			}, now)
		})
}

// LatestBlackboardConclusion returns the newest durable obligation for a Task
// together with its ACTIVE dispatch.
func (e *Engine) LatestBlackboardConclusion(ownerID string) (*BlackboardConclusionReceipt, error) {
	obligation, err := e.scanPendingBlackboardConclusion(e.DB.QueryRow(`
		SELECT `+e.obligationColumns()+`
		FROM `+e.Dialect.ObligationsTable+` WHERE `+e.Dialect.OwnerColumn+`=? ORDER BY created_at DESC,id DESC LIMIT 1`, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest "+e.Dialect.Subject+" obligation: %w", err)
	}
	view, err := e.combinedConclusionView(e.DB, obligation)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// ValidatedBlackboardConclusions returns durable apply intents for daemon
// startup replay. It is read-only and preserves canonical and idempotency
// lineage exactly as stored.
func (e *Engine) ValidatedBlackboardConclusions() ([]BlackboardConclusionReceipt, error) {
	rows, err := e.DB.Query(`SELECT `+e.obligationColumns()+`
		FROM `+e.Dialect.ObligationsTable+` WHERE state=? ORDER BY created_at,id`, string(BlackboardConclusionReceiptValidated))
	if err != nil {
		return nil, fmt.Errorf("list validated "+e.Dialect.Subject+"s: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := e.scanPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan validated "+e.Dialect.Subject+": %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate validated "+e.Dialect.Subject+"s: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close validated "+e.Dialect.Subject+"s: %w", err)
	}
	views := make([]BlackboardConclusionReceipt, 0, len(obligations))
	for _, obligation := range obligations {
		view, viewErr := e.combinedConclusionView(e.DB, obligation)
		if viewErr != nil {
			return nil, fmt.Errorf("scan validated "+e.Dialect.Subject+" view: %w", viewErr)
		}
		views = append(views, view)
	}
	return views, nil
}

// ConclusionDispatches returns the immutable dispatch history for an
// obligation, newest first. The obligation row itself is not projected.
func (e *Engine) ConclusionDispatches(obligationID string) ([]ConclusionDispatch, error) {
	obligationID = strings.TrimSpace(obligationID)
	if obligationID == "" {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	rows, err := e.DB.Query(`SELECT `+conclusionDispatchColumns+` FROM `+e.Dialect.DispatchesTable+`
		WHERE obligation_id=? ORDER BY rowid DESC`, obligationID)
	if err != nil {
		return nil, fmt.Errorf("list Conclusion Dispatches: %w", err)
	}
	defer rows.Close()
	var dispatches []ConclusionDispatch
	for rows.Next() {
		dispatch, scanErr := e.scanConclusionDispatch(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Conclusion Dispatch: %w", scanErr)
		}
		dispatches = append(dispatches, dispatch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Conclusion Dispatches: %w", err)
	}
	return dispatches, nil
}

// CreateRecoveryConclusionDispatches supersedes every in-flight Conclusion
// Dispatch of a Task that is bound to the old Continuation and creates a NEW
// recovery dispatch bound immutably to the replacement Continuation and
// session. This replaces the old mutable receipt rebinding (#197): historical
// dispatch identity is never rewritten. The obligation state is re-armed to the
// matching pre-send protocol state so the daemon can re-dispatch.
func (e *Engine) CreateRecoveryConclusionDispatches(taskID, oldContinuationID, replacementContinuationID, replacementSessionID string) ([]BlackboardConclusionReceipt, error) {
	taskID, oldContinuationID, replacementContinuationID, replacementSessionID =
		strings.TrimSpace(taskID), strings.TrimSpace(oldContinuationID), strings.TrimSpace(replacementContinuationID), strings.TrimSpace(replacementSessionID)
	if taskID == "" || oldContinuationID == "" || replacementContinuationID == "" || replacementSessionID == "" || oldContinuationID == replacementContinuationID {
		return nil, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin recovery Conclusion Dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT `+e.obligationColumns()+` FROM `+e.Dialect.ObligationsTable+`
		WHERE `+e.Dialect.OwnerColumn+`=? AND state IN (?,?,?,?,?,?) ORDER BY created_at,id`,
		taskID, string(BlackboardConclusionReceiptPending), string(BlackboardConclusionReceiptDispatchRequested),
		string(BlackboardConclusionReceiptRepairDispatchRequested), string(BlackboardConclusionReceiptVersionSyncRequested),
		string(BlackboardConclusionReceiptVersionRegenerationDispatchRequested), string(BlackboardConclusionReceiptAwaitingResult))
	if err != nil {
		return nil, fmt.Errorf("list recovery Conclusion Dispatches: %w", err)
	}
	var obligations []PendingBlackboardConclusion
	for rows.Next() {
		obligation, scanErr := e.scanPendingBlackboardConclusion(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan recovery Conclusion Dispatch obligation: %w", scanErr)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate recovery Conclusion Dispatch obligations: %w", err)
	}
	_ = rows.Close()

	var views []BlackboardConclusionReceipt
	for _, obligation := range obligations {
		dispatch, dispatchErr := e.activeConclusionDispatchTx(tx, obligation.ID)
		if dispatchErr != nil && !errors.Is(dispatchErr, sql.ErrNoRows) {
			return nil, dispatchErr
		}
		boundToOld := dispatch != nil && dispatch.ContinuationID == oldContinuationID
		boundToReplacement := dispatch != nil && dispatch.ContinuationID == replacementContinuationID
		pendingBoundToOld := dispatch == nil && obligation.State == BlackboardConclusionReceiptPending && obligation.SourceContinuationID == oldContinuationID
		if !boundToOld && !pendingBoundToOld {
			if boundToReplacement {
				view, viewErr := e.combinedConclusionView(tx, obligation)
				if viewErr != nil {
					return nil, viewErr
				}
				views = append(views, view)
			}
			continue
		}
		if dispatch != nil {
			if err := e.supersedeActiveDispatchTx(tx, dispatch, "superseded_by_recovery", now); err != nil {
				return nil, err
			}
		}
		number := e.conclusionDispatchSequence(tx, obligation.ID) + 1
		requestID := blackboardConclusionAttemptRequestID("recovery", replacementContinuationID, obligation.SourceTurnID, number, "")
		nowDispatch := ConclusionDispatch{
			ID: e.Dialect.NewID(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRecovery,
			ContinuationID: replacementContinuationID, SourceSessionID: replacementSessionID,
			DispatchRequestID: requestID, BaseRevision: dispatchBaseRevision(dispatch),
			DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
		}
		if _, err := tx.Exec(`INSERT INTO `+e.Dialect.DispatchesTable+`
			(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
			nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return nil, fmt.Errorf("store recovery Conclusion Dispatch: %w", err)
		}
		switch obligation.State {
		case BlackboardConclusionReceiptPending, BlackboardConclusionReceiptAwaitingResult:
			obligation.State = BlackboardConclusionReceiptDispatchRequested
		}
		obligation.UpdatedAt = now
		if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
			return nil, err
		}
		view := obligationView(obligation)
		view.ActiveDispatchID = nowDispatch.ID
		view.DispatchKind = nowDispatch.Kind
		view.DispatchRequestID = nowDispatch.DispatchRequestID
		view.ContinuationID = nowDispatch.ContinuationID
		view.SourceSessionID = nowDispatch.SourceSessionID
		view.BaseRevision = nowDispatch.BaseRevision
		if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
			"phase": "recovery_dispatch_created", "receipt_id": obligation.ID, "request_id": requestID,
			"replacement_continuation_id": replacementContinuationID, "replacement_session_id": replacementSessionID,
			"turn_kind": "control",
		}, now); err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit recovery Conclusion Dispatches: %w", err)
	}
	return views, nil
}

// CreateRecoveryConclusionDispatch supersedes the active dispatch (if any) of
// one obligation and creates a NEW recovery dispatch bound immutably to the
// proven-live replacement continuation + session. It is the single-obligation
// form used by restart recovery when the active dispatch is bound to a dead
// continuation; the old dispatch identity is never rewritten. Won is false when
// the obligation is already bound to the target continuation or is terminal.
func (e *Engine) CreateRecoveryConclusionDispatch(obligationID, continuationID, sessionID string, now time.Time) (BlackboardConclusionReceipt, bool, error) {
	obligationID, continuationID, sessionID = strings.TrimSpace(obligationID), strings.TrimSpace(continuationID), strings.TrimSpace(sessionID)
	if obligationID == "" || continuationID == "" || sessionID == "" {
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now = now.UTC()
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin recovery Conclusion Dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, err := e.loadPendingBlackboardConclusionByID(tx, obligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	dispatch, err := e.activeConclusionDispatchTx(tx, obligation.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, false, err
	}
	if dispatch != nil && dispatch.ContinuationID == continuationID && dispatch.SourceSessionID == sessionID {
		view, viewErr := e.combinedConclusionView(tx, obligation)
		if viewErr != nil {
			return BlackboardConclusionReceipt{}, false, viewErr
		}
		return view, false, nil
	}
	if obligation.State == BlackboardConclusionReceiptApplied || obligation.State == BlackboardConclusionReceiptClean {
		view, viewErr := e.combinedConclusionView(tx, obligation)
		if viewErr != nil {
			return BlackboardConclusionReceipt{}, false, viewErr
		}
		return view, false, nil
	}
	if dispatch != nil {
		if err := e.supersedeActiveDispatchTx(tx, dispatch, "superseded_by_recovery", now); err != nil {
			return BlackboardConclusionReceipt{}, false, err
		}
	}
	number := e.conclusionDispatchSequence(tx, obligation.ID) + 1
	requestID := blackboardConclusionAttemptRequestID("recovery", continuationID, obligation.SourceTurnID, number, "")
	nowDispatch := ConclusionDispatch{
		ID: e.Dialect.NewID(), ObligationID: obligation.ID, Kind: ConclusionDispatchKindRecovery,
		ContinuationID: continuationID, SourceSessionID: sessionID,
		DispatchRequestID: requestID, BaseRevision: dispatchBaseRevision(dispatch),
		DeliveryState: ConclusionDispatchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO `+e.Dialect.DispatchesTable+`
		(id,obligation_id,kind,continuation_id,source_session_id,dispatch_request_id,base_revision,delivery_state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, nowDispatch.ID, nowDispatch.ObligationID, string(nowDispatch.Kind), nowDispatch.ContinuationID,
		nowDispatch.SourceSessionID, nowDispatch.DispatchRequestID, intPointerValue(nowDispatch.BaseRevision), string(nowDispatch.DeliveryState),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("store recovery Conclusion Dispatch: %w", err)
	}
	switch obligation.State {
	case BlackboardConclusionReceiptPending, BlackboardConclusionReceiptAwaitingResult:
		obligation.State = BlackboardConclusionReceiptDispatchRequested
	}
	obligation.UpdatedAt = now
	if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view := obligationView(obligation)
	view.ActiveDispatchID = nowDispatch.ID
	view.DispatchKind = nowDispatch.Kind
	view.DispatchRequestID = nowDispatch.DispatchRequestID
	view.ContinuationID = nowDispatch.ContinuationID
	view.SourceSessionID = nowDispatch.SourceSessionID
	view.BaseRevision = nowDispatch.BaseRevision
	if err := e.appendBlackboardConclusionEventTx(tx, view, map[string]any{
		"phase": "recovery_dispatch_created", "receipt_id": obligation.ID, "request_id": requestID,
		"replacement_continuation_id": continuationID, "replacement_session_id": sessionID,
		"turn_kind": "control",
	}, now); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit recovery Conclusion Dispatch: %w", err)
	}
	return view, true, nil
}

// --- internal helpers ---

func obligationView(obligation PendingBlackboardConclusion) BlackboardConclusionReceipt {
	view := BlackboardConclusionReceipt{
		ID: obligation.ID, OwnerID: obligation.OwnerID,
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

func blackboardConclusionReceiptFromObligationDispatch(dispatch *ConclusionDispatch, obligation PendingBlackboardConclusion) BlackboardConclusionReceipt {
	view := obligationView(obligation)
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

func (e *Engine) combinedConclusionView(queryer interface {
	QueryRow(string, ...any) *sql.Row
}, obligation PendingBlackboardConclusion) (BlackboardConclusionReceipt, error) {
	dispatch, err := e.scanConclusionDispatch(queryer.QueryRow(`SELECT `+conclusionDispatchColumns+`
		FROM `+e.Dialect.DispatchesTable+` WHERE obligation_id=? AND delivery_state IN (?,?,?)
		ORDER BY created_at DESC,id DESC LIMIT 1`, obligation.ID,
		string(ConclusionDispatchRequested), string(ConclusionDispatchAwaitingResult), string(ConclusionDispatchValidated)))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BlackboardConclusionReceipt{}, fmt.Errorf("load active Conclusion Dispatch: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Terminal obligations (applied / action_required) keep their last
		// dispatch as the visible delivery lineage.
		latest, latestErr := e.scanConclusionDispatch(queryer.QueryRow(`SELECT `+conclusionDispatchColumns+`
			FROM `+e.Dialect.DispatchesTable+` WHERE obligation_id=? ORDER BY rowid DESC LIMIT 1`, obligation.ID))
		if latestErr != nil && !errors.Is(latestErr, sql.ErrNoRows) {
			return BlackboardConclusionReceipt{}, fmt.Errorf("load latest Conclusion Dispatch: %w", latestErr)
		}
		if latestErr == nil {
			return blackboardConclusionReceiptFromObligationDispatch(&latest, obligation), nil
		}
		return blackboardConclusionReceiptFromObligationDispatch(nil, obligation), nil
	}
	return blackboardConclusionReceiptFromObligationDispatch(&dispatch, obligation), nil
}

type obligationQueryer interface {
	QueryRow(string, ...any) *sql.Row
}

func (e *Engine) loadPendingBlackboardConclusionByID(queryer obligationQueryer, obligationID string) (PendingBlackboardConclusion, error) {
	return e.scanPendingBlackboardConclusion(queryer.QueryRow(`SELECT `+e.obligationColumns()+`
		FROM `+e.Dialect.ObligationsTable+` WHERE id=?`, obligationID))
}

// loadConclusionByDispatchRequestID resolves the obligation for any dispatch
// request id, active or terminal. Callers that advance the protocol must gate
// on the active dispatch themselves; this loader is used for idempotent replay
// of terminal states (for example applied).
func (e *Engine) loadConclusionByDispatchRequestID(tx *sql.Tx, dispatchRequestID string) (PendingBlackboardConclusion, *ConclusionDispatch, error) {
	dispatch, err := e.scanConclusionDispatch(tx.QueryRow(`SELECT `+conclusionDispatchColumns+`
		FROM `+e.Dialect.DispatchesTable+` WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	obligation, err := e.loadPendingBlackboardConclusionByID(tx, dispatch.ObligationID)
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	return obligation, &dispatch, nil
}

func (e *Engine) loadActiveConclusionByDispatchRequestID(tx *sql.Tx, dispatchRequestID string) (PendingBlackboardConclusion, *ConclusionDispatch, error) {
	dispatch, err := e.scanConclusionDispatch(tx.QueryRow(`SELECT `+conclusionDispatchColumns+`
		FROM `+e.Dialect.DispatchesTable+` WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	if !owner.ConclusionDispatchActive(dispatch.DeliveryState) {
		// A callback for a superseded or late dispatch is obsolete: it must
		// fail closed (ErrOwnerNotFound) and can never advance the obligation.
		return PendingBlackboardConclusion{}, nil, sql.ErrNoRows
	}
	obligation, err := e.loadPendingBlackboardConclusionByID(tx, dispatch.ObligationID)
	if err != nil {
		return PendingBlackboardConclusion{}, nil, err
	}
	return obligation, &dispatch, nil
}

func (e *Engine) conclusionViewByDispatchRequestIDTx(tx *sql.Tx, dispatchRequestID string) (BlackboardConclusionReceipt, error) {
	dispatch, err := e.scanConclusionDispatch(tx.QueryRow(`SELECT `+conclusionDispatchColumns+`
		FROM `+e.Dialect.DispatchesTable+` WHERE dispatch_request_id=?`, dispatchRequestID))
	if err != nil {
		return BlackboardConclusionReceipt{}, err
	}
	obligation, err := e.loadPendingBlackboardConclusionByID(tx, dispatch.ObligationID)
	if err != nil {
		return BlackboardConclusionReceipt{}, err
	}
	return blackboardConclusionReceiptFromObligationDispatch(&dispatch, obligation), nil
}

func (e *Engine) latestConclusionDispatchTx(tx *sql.Tx, obligationID string) (*ConclusionDispatch, error) {
	dispatch, err := e.scanConclusionDispatch(tx.QueryRow(`SELECT `+conclusionDispatchColumns+`
		FROM `+e.Dialect.DispatchesTable+` WHERE obligation_id=? ORDER BY rowid DESC LIMIT 1`, obligationID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dispatch, nil
}

func (e *Engine) activeConclusionDispatchTx(tx *sql.Tx, obligationID string) (*ConclusionDispatch, error) {
	dispatch, err := e.scanConclusionDispatch(tx.QueryRow(`SELECT `+conclusionDispatchColumns+`
		FROM `+e.Dialect.DispatchesTable+` WHERE obligation_id=? AND delivery_state IN (?,?,?)
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

func (e *Engine) supersedeActiveDispatchTx(tx *sql.Tx, dispatch *ConclusionDispatch, outcome string, now time.Time) error {
	dispatch.DeliveryState = ConclusionDispatchSuperseded
	dispatch.TerminalOutcome = outcome
	dispatch.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE `+e.Dialect.DispatchesTable+` SET delivery_state=?,terminal_outcome=?,updated_at=? WHERE id=?`,
		string(dispatch.DeliveryState), outcome, now.Format(time.RFC3339Nano), dispatch.ID); err != nil {
		return fmt.Errorf("supersede Conclusion Dispatch: %w", err)
	}
	return nil
}

func (e *Engine) failActiveDispatchTx(tx *sql.Tx, obligation PendingBlackboardConclusion, dispatch *ConclusionDispatch, code BlackboardConclusionErrorCode, nextEligible time.Time, detail ConclusionValidationDetail, now time.Time) (PendingBlackboardConclusion, error) {
	obligation.State = BlackboardConclusionReceiptActionRequired
	obligation.ErrorCode = code
	obligation.NextEligibleAt = &nextEligible
	obligation.ValidationReason = detail.Reason
	obligation.ValidationFieldPath = detail.FieldPath
	obligation.ValidationExpected = detail.Expected
	obligation.UpdatedAt = now
	if err := e.updateObligationProtocolTx(tx, obligation); err != nil {
		return PendingBlackboardConclusion{}, err
	}
	if dispatch != nil {
		if err := e.supersedeActiveDispatchTx(tx, dispatch, "action_required", now); err != nil {
			return PendingBlackboardConclusion{}, err
		}
	}
	return obligation, nil
}

func (e *Engine) updateObligationProtocolTx(tx *sql.Tx, obligation PendingBlackboardConclusion) error {
	if _, err := tx.Exec(`UPDATE `+e.Dialect.ObligationsTable+`
		SET state=?,canonical_result_json=?,canonical_result_sha256=?,apply_idempotency_key=?,applied_revision=?,base_revision=?,automatic_turn_count=?,repair_count=?,
		version_regeneration_count=?,explicit_retry_count=?,operator_retry_key=?,error_code=?,recovery_reason=?,next_eligible_at=?,
		validation_reason=?,validation_field_path=?,validation_expected=?,updated_at=? WHERE id=?`,
		string(obligation.State), obligation.CanonicalResultJSON, obligation.CanonicalResultSHA256,
		obligation.ApplyIdempotencyKey, intPointerValue(obligation.AppliedRevision), intPointerValue(obligation.BaseRevision), obligation.AutomaticTurnCount, obligation.RepairCount,
		obligation.VersionRegenerationCount, obligation.ExplicitRetryCount, obligation.OperatorRetryKey,
		string(obligation.ErrorCode), string(obligation.RecoveryReason),
		formatTimePtr(obligation.NextEligibleAt), obligation.ValidationReason, obligation.ValidationFieldPath,
		obligation.ValidationExpected, obligation.UpdatedAt.Format(time.RFC3339Nano), obligation.ID); err != nil {
		return fmt.Errorf("update "+e.Dialect.Subject+" obligation: %w", err)
	}
	return nil
}

// advanceObligationStateByDispatchRequest transitions the obligation's protocol
// state, verifying the request id still resolves to the ACTIVE dispatch.
func (e *Engine) advanceObligationStateByDispatchRequest(dispatchRequestID string, from, to BlackboardConclusionReceiptState, advance func(*sql.Tx, *PendingBlackboardConclusion, time.Time) error) (BlackboardConclusionReceipt, bool, error) {
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" %s: %w", to, err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := e.loadConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	if obligation.State != from {
		if obligation.State == to {
			return blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	if err := advance(tx, &obligation, now); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("mark "+e.Dialect.Subject+" %s: %w", to, err)
	}
	view, err := e.conclusionViewByDispatchRequestIDTx(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit "+e.Dialect.Subject+" %s: %w", to, err)
	}
	return view, true, nil
}

// advanceActiveConclusionDispatch transitions the ACTIVE dispatch's delivery
// state together with the obligation protocol state. Only the active dispatch
// may advance.
func (e *Engine) advanceActiveConclusionDispatch(dispatchRequestID string, fromDispatch, toDispatch ConclusionDispatchState, toObligation BlackboardConclusionReceiptState,
	replayMatches func(BlackboardConclusionReceipt) bool,
	advance func(*sql.Tx, *PendingBlackboardConclusion, *ConclusionDispatch, time.Time) error,
) (BlackboardConclusionReceipt, bool, error) {
	tx, err := e.DB.Begin()
	if err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("begin "+e.Dialect.Subject+" %s: %w", toDispatch, err)
	}
	defer func() { _ = tx.Rollback() }()
	obligation, dispatch, err := e.loadActiveConclusionByDispatchRequestID(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, e.blackboardConclusionLookupError(err)
	}
	if dispatch == nil || dispatch.DeliveryState != fromDispatch {
		if dispatch != nil && dispatch.DeliveryState == toDispatch && replayMatches(blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation)) {
			return blackboardConclusionReceiptFromObligationDispatch(dispatch, obligation), false, nil
		}
		return BlackboardConclusionReceipt{}, false, ErrInvalidBlackboardConclusionReceipt
	}
	now := time.Now().UTC()
	if err := advance(tx, &obligation, dispatch, now); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("mark "+e.Dialect.Subject+" %s: %w", toDispatch, err)
	}
	obligation.State = toObligation
	obligation.UpdatedAt = now
	if _, err := tx.Exec(`UPDATE `+e.Dialect.ObligationsTable+` SET state=?,updated_at=? WHERE id=?`,
		string(obligation.State), now.Format(time.RFC3339Nano), obligation.ID); err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	view, err := e.conclusionViewByDispatchRequestIDTx(tx, dispatchRequestID)
	if err != nil {
		return BlackboardConclusionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlackboardConclusionReceipt{}, false, fmt.Errorf("commit "+e.Dialect.Subject+" %s: %w", toDispatch, err)
	}
	return view, true, nil
}

func (e *Engine) scanPendingBlackboardConclusion(row interface{ Scan(...any) error }) (PendingBlackboardConclusion, error) {
	var obligation PendingBlackboardConclusion
	var state, createdAt, updatedAt string
	var nextEligibleAt, errorCode, recoveryReason, validationReason, validationFieldPath, validationExpected, applyKey, resultHash, operatorRetryKey sql.NullString
	var appliedRevision, baseRevision sql.NullInt64
	var canonicalResult []byte
	if err := row.Scan(&obligation.ID, &obligation.OwnerID, &obligation.SourceRequestID, &obligation.SourceRequestCorrelationExact,
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
			return PendingBlackboardConclusion{}, fmt.Errorf("parse "+e.Dialect.Subject+" next_eligible_at: %w", err)
		}
		obligation.NextEligibleAt = &parsed
	}
	var err error
	if obligation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return PendingBlackboardConclusion{}, fmt.Errorf("parse "+e.Dialect.Subject+" created_at: %w", err)
	}
	if obligation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return PendingBlackboardConclusion{}, fmt.Errorf("parse "+e.Dialect.Subject+" updated_at: %w", err)
	}
	return obligation, nil
}

func (e *Engine) scanConclusionDispatch(row interface{ Scan(...any) error }) (ConclusionDispatch, error) {
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
			return ConclusionDispatch{}, fmt.Errorf("parse Conclusion Dispatch send_started_at: %w", err)
		}
		dispatch.SendStartedAt = &parsed
	}
	var err error
	if dispatch.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return ConclusionDispatch{}, fmt.Errorf("parse Conclusion Dispatch created_at: %w", err)
	}
	if dispatch.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return ConclusionDispatch{}, fmt.Errorf("parse Conclusion Dispatch updated_at: %w", err)
	}
	return dispatch, nil
}

func validBlackboardConclusionErrorCode(code BlackboardConclusionErrorCode) bool {
	return owner.ValidBlackboardConclusionErrorCode(code)
}

func (e *Engine) blackboardConclusionLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOwnerNotFound
	}
	return fmt.Errorf("load "+e.Dialect.Subject+" obligation: %w", err)
}

func blackboardConclusionRequestLineage(continuationID, sourceTurnID string) (string, string) {
	return owner.ConclusionRequestLineage(continuationID, sourceTurnID)
}

func blackboardConclusionAttemptRequestID(kind, continuationID, sourceTurnID string, number int, key string) string {
	return owner.ConclusionAttemptRequestID(kind, continuationID, sourceTurnID, number, key)
}

func (e *Engine) appendBlackboardConclusionEventTx(tx *sql.Tx, receipt BlackboardConclusionReceipt, payload map[string]any, now time.Time) error {
	if err := e.Dialect.AppendEvent(tx, receipt.OwnerID, receipt.ContinuationID, e.Dialect.EventKind, payload, now); err != nil {
		return fmt.Errorf("store "+e.Dialect.Subject+" Event: %w", err)
	}
	return nil
}

func (e *Engine) conclusionDispatchSequence(tx *sql.Tx, obligationID string) int {
	var count int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM `+e.Dialect.DispatchesTable+` WHERE obligation_id=?`, obligationID).Scan(&count)
	return count
}

func dispatchBaseRevision(dispatch *ConclusionDispatch) *int {
	if dispatch == nil {
		return nil
	}
	return dispatch.BaseRevision
}

func obligationDispatchRequestID(dispatch *ConclusionDispatch) string {
	if dispatch == nil {
		return ""
	}
	return dispatch.DispatchRequestID
}

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

func intPointer(value int) *int { return &value }

// MapSentinels returns err unchanged unless one of the engine sentinels
// matches; then it returns an error that carries the original message and
// chain while satisfying errors.Is for the owner-package counterpart. Pairs
// alternate from, to.
func MapSentinels(err error, pairs ...error) error {
	if err == nil || len(pairs) < 2 {
		return err
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		if errors.Is(err, pairs[i]) {
			return mappedSentinelError{err: err, to: pairs[i+1]}
		}
	}
	return err
}

type mappedSentinelError struct {
	err error
	to  error
}

func (m mappedSentinelError) Error() string        { return m.err.Error() }
func (m mappedSentinelError) Unwrap() error        { return m.err }
func (m mappedSentinelError) Is(target error) bool { return target == m.to }
