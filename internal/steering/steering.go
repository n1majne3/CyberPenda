// Package steering persists the owner-neutral Accepted Steering state machine
// shared by Task and Session Runtime owners. One table carries both owner
// kinds; the Runtime Harness drives every state transition and Conversation
// Events remain projections of the request and outcome, never the dispatch
// source of truth.
package steering

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/store"
)

// ErrNotFound reports a missing Accepted Steering record.
var ErrNotFound = errors.New("accepted steering not found")

// ErrDuplicateRequest reports a second durable record for one request identity.
// Callers treat it as an idempotent replay of the existing record.
var ErrDuplicateRequest = errors.New("duplicate accepted steering request")

// ErrInvalidReason reports a reason string outside the closed Steering
// failure vocabulary at the owner boundary.
var ErrInvalidReason = errors.New("invalid accepted steering reason")

// AcceptRequest is the owner-neutral durable payload of one accepted steer.
// Raw provider text never enters this record.
type AcceptRequest struct {
	RequestID                string
	Message                  string
	Mode                     owner.SteeringMode
	ModelProviderID          string
	Model                    string
	RequestedReasoningEffort string
	SessionID                string
}

// Service persists Accepted Steering records. Task and Session owners share
// the same table and the same transition vocabulary.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

const steeringRecordColumns = `id, owner_kind, owner_id, request_id, message, mode, model_provider_id, model,
	requested_reasoning_effort, state, queue_order, conversation_event_id, continuation_id, session_id,
	send_started_at, result_json, error_code, error_message, created_at, updated_at`

// Accept durably records one Accepted Steering request together with its
// conversation projection in one transaction. The projection callback appends
// the owner-local conversation event inside the same transaction and returns
// the event ID so the record can reference it. The queue order is assigned
// atomically per owner so concurrent accepts keep FIFO order.
func (s *Service) Accept(ctx context.Context, kind owner.Kind, ownerID string, input AcceptRequest, projection func(tx *sql.Tx) (string, error)) (*owner.SteeringRecord, error) {
	if kind != owner.KindTask && kind != owner.KindSession {
		return nil, fmt.Errorf("invalid Accepted Steering owner kind %q", kind)
	}
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(input.RequestID) == "" || strings.TrimSpace(input.Message) == "" {
		return nil, fmt.Errorf("Accepted Steering requires owner, request identity, and message")
	}
	if input.Mode != owner.SteeringModeInTurnSteer && input.Mode != owner.SteeringModeInterruptThenReplace {
		return nil, fmt.Errorf("invalid Accepted Steering mode %q", input.Mode)
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin Accepted Steering: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var conversationEventID string
	if projection != nil {
		conversationEventID, err = projection(tx)
		if err != nil {
			return nil, err
		}
	}
	id := newID()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO accepted_steering (id, owner_kind, owner_id, request_id, message, mode, model_provider_id, model,
			requested_reasoning_effort, state, queue_order, conversation_event_id, continuation_id, session_id,
			send_started_at, result_json, error_code, error_message, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(MAX(queue_order), 0) + 1, ?, '', ?, '', '{}', '', '', ?, ?
			FROM accepted_steering WHERE owner_kind = ? AND owner_id = ?`,
		id, string(kind), ownerID, strings.TrimSpace(input.RequestID), strings.TrimSpace(input.Message),
		string(input.Mode), strings.TrimSpace(input.ModelProviderID), strings.TrimSpace(input.Model),
		strings.TrimSpace(input.RequestedReasoningEffort), string(owner.SteeringPending),
		conversationEventID, strings.TrimSpace(input.SessionID),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		string(kind), ownerID,
	); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateRequest, err)
		}
		return nil, fmt.Errorf("store Accepted Steering: %w", err)
	}
	record, err := scanSteeringRecord(tx.QueryRowContext(ctx, `SELECT `+steeringRecordColumns+` FROM accepted_steering WHERE id=?`, id))
	if err != nil {
		return nil, fmt.Errorf("read Accepted Steering: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Accepted Steering: %w", err)
	}
	return record, nil
}

// ByRequestID returns the durable record for one owner and request identity.
func (s *Service) ByRequestID(kind owner.Kind, ownerID, requestID string) (*owner.SteeringRecord, error) {
	record, err := scanSteeringRecord(s.db.QueryRow(`SELECT `+steeringRecordColumns+`
		FROM accepted_steering WHERE owner_kind=? AND owner_id=? AND request_id=?`,
		string(kind), ownerID, strings.TrimSpace(requestID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load Accepted Steering: %w", err)
	}
	return record, nil
}

// OldestPending returns the oldest pending Accepted Steering record for one
// owner, or nil when the owner queue is empty. Dispatch order is strictly the
// durable queue order, independent of goroutine scheduling.
func (s *Service) OldestPending(kind owner.Kind, ownerID string) (*owner.SteeringRecord, error) {
	record, err := scanSteeringRecord(s.db.QueryRow(`SELECT `+steeringRecordColumns+`
		FROM accepted_steering WHERE owner_kind=? AND owner_id=? AND state=? ORDER BY queue_order ASC LIMIT 1`,
		string(kind), ownerID, string(owner.SteeringPending)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load pending Accepted Steering: %w", err)
	}
	return record, nil
}

// NonTerminal returns every Accepted Steering record that has not reached a
// terminal outcome, across all owners, for startup reconciliation.
func (s *Service) NonTerminal() ([]owner.SteeringRecord, error) {
	rows, err := s.db.Query(`SELECT `+steeringRecordColumns+`
		FROM accepted_steering WHERE state IN (?, ?) ORDER BY owner_kind, owner_id, queue_order ASC`,
		string(owner.SteeringPending), string(owner.SteeringDispatchStarted))
	if err != nil {
		return nil, fmt.Errorf("list non-terminal Accepted Steering: %w", err)
	}
	defer rows.Close()
	records := make([]owner.SteeringRecord, 0)
	for rows.Next() {
		record, scanErr := scanSteeringRecordRows(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Accepted Steering: %w", scanErr)
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

// MarkDispatchStarted records the durable send-start fence: pending →
// dispatch_started. It also records the resolved Continuation the request is
// delivered against. A record already past pending is returned unchanged so
// recovery can never fence the same request twice.
func (s *Service) MarkDispatchStarted(ctx context.Context, id, continuationID string, now time.Time) (*owner.SteeringRecord, error) {
	record, err := s.transition(ctx, id, func(tx *sql.Tx, current *owner.SteeringRecord) error {
		if current.State != owner.SteeringPending {
			return errAlreadyTransitioned
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE accepted_steering
			SET state=?, continuation_id=?, send_started_at=?, updated_at=? WHERE id=?`,
			string(owner.SteeringDispatchStarted), strings.TrimSpace(continuationID), stamp, stamp, id); err != nil {
			return err
		}
		current.State = owner.SteeringDispatchStarted
		current.ContinuationID = strings.TrimSpace(continuationID)
		current.SendStartedAt = timePtr(now.UTC())
		current.UpdatedAt = now.UTC()
		return nil
	})
	return record, err
}

// MarkApplied records the terminal delivered outcome with the redacted
// provider result as delivery evidence.
func (s *Service) MarkApplied(ctx context.Context, id string, result map[string]any) (*owner.SteeringRecord, error) {
	evidence, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode Accepted Steering result: %w", err)
	}
	record, err := s.transition(ctx, id, func(tx *sql.Tx, current *owner.SteeringRecord) error {
		if current.State != owner.SteeringDispatchStarted {
			return errAlreadyTransitioned
		}
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE accepted_steering
			SET state=?, result_json=?, updated_at=? WHERE id=?`,
			string(owner.SteeringApplied), string(evidence), stamp, id); err != nil {
			return err
		}
		current.State = owner.SteeringApplied
		current.Result = result
		current.UpdatedAt = time.Now().UTC()
		return nil
	})
	return record, err
}

// MarkFailed records a terminal failure with a closed vocabulary reason.
func (s *Service) MarkFailed(ctx context.Context, id string, reason owner.SteeringFailureReason, message string) (*owner.SteeringRecord, error) {
	return s.markSettled(ctx, id, owner.SteeringFailed, reason, message)
}

// MarkActionRequired records the terminal operator-actionable outcome. The
// reason must be a member of the closed vocabulary; arbitrary reason strings
// are rejected at this owner boundary.
func (s *Service) MarkActionRequired(ctx context.Context, id string, reason owner.SteeringFailureReason, message string) (*owner.SteeringRecord, error) {
	return s.markSettled(ctx, id, owner.SteeringActionRequired, reason, message)
}

func (s *Service) markSettled(ctx context.Context, id string, state owner.SteeringState, reason owner.SteeringFailureReason, message string) (*owner.SteeringRecord, error) {
	if !owner.ValidSteeringFailureReason(reason) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidReason, reason)
	}
	record, err := s.transition(ctx, id, func(tx *sql.Tx, current *owner.SteeringRecord) error {
		if current.State.Terminal() {
			return errAlreadyTransitioned
		}
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE accepted_steering
			SET state=?, error_code=?, error_message=?, updated_at=? WHERE id=?`,
			string(state), string(reason), strings.TrimSpace(message), stamp, id); err != nil {
			return err
		}
		current.State = state
		current.ErrorCode = reason
		current.ErrorMessage = strings.TrimSpace(message)
		current.UpdatedAt = time.Now().UTC()
		return nil
	})
	return record, err
}

// SettleOwner settles every non-terminal Accepted Steering record for one
// owner. Pending (pre-fence) requests become failed with the given closed
// reason; dispatch_started (post-fence) requests with no durable outcome
// become action_required because delivery is ambiguous and must never be
// replayed. Records already terminal stay untouched.
func (s *Service) SettleOwner(ctx context.Context, kind owner.Kind, ownerID string, reason owner.SteeringFailureReason, message string) ([]owner.SteeringRecord, error) {
	if !owner.ValidSteeringFailureReason(reason) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidReason, reason)
	}
	records, err := s.ownerNonTerminal(kind, ownerID)
	if err != nil {
		return nil, err
	}
	settled := make([]owner.SteeringRecord, 0, len(records))
	for _, record := range records {
		if record.State.Terminal() {
			continue
		}
		if record.State == owner.SteeringDispatchStarted {
			updated, markErr := s.MarkActionRequired(ctx, record.ID, owner.SteeringReasonDeliveryAmbiguous, message)
			if markErr != nil {
				return nil, markErr
			}
			settled = append(settled, *updated)
			continue
		}
		updated, markErr := s.MarkFailed(ctx, record.ID, reason, message)
		if markErr != nil {
			return nil, markErr
		}
		settled = append(settled, *updated)
	}
	return settled, nil
}

func (s *Service) ownerNonTerminal(kind owner.Kind, ownerID string) ([]owner.SteeringRecord, error) {
	rows, err := s.db.Query(`SELECT `+steeringRecordColumns+`
		FROM accepted_steering WHERE owner_kind=? AND owner_id=? AND state IN (?, ?) ORDER BY queue_order ASC`,
		string(kind), ownerID, string(owner.SteeringPending), string(owner.SteeringDispatchStarted))
	if err != nil {
		return nil, fmt.Errorf("list owner Accepted Steering: %w", err)
	}
	defer rows.Close()
	records := make([]owner.SteeringRecord, 0)
	for rows.Next() {
		record, scanErr := scanSteeringRecordRows(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Accepted Steering: %w", scanErr)
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

// errAlreadyTransitioned signals an idempotent replay of an already-transitioned
// record; the current durable state is returned unchanged.
var errAlreadyTransitioned = errors.New("accepted steering already transitioned")

type steeringTransition func(tx *sql.Tx, current *owner.SteeringRecord) error

func (s *Service) transition(ctx context.Context, id string, apply steeringTransition) (*owner.SteeringRecord, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin Accepted Steering transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := scanSteeringRecord(tx.QueryRowContext(ctx, `SELECT `+steeringRecordColumns+` FROM accepted_steering WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load Accepted Steering transition: %w", err)
	}
	if err := apply(tx, record); err != nil {
		if errors.Is(err, errAlreadyTransitioned) {
			return record, nil
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Accepted Steering transition: %w", err)
	}
	return record, nil
}

// steeringScan is the raw row shape of the accepted_steering table.
type steeringScan struct {
	id, ownerKind, ownerID, requestID, message, mode                       string
	modelProviderID, model, requestedReasoningEffort, state                string
	queueOrder                                                             int
	conversationEventID, continuationID, sessionID                         string
	sendStartedAt, resultJSON, errorCode, errorMessage, createdAt, updatedAt string
}

func (scan *steeringScan) decode() (*owner.SteeringRecord, error) {
	record := &owner.SteeringRecord{
		ID:                       scan.id,
		OwnerKind:                owner.Kind(scan.ownerKind),
		OwnerID:                  scan.ownerID,
		RequestID:                scan.requestID,
		Message:                  scan.message,
		Mode:                     owner.SteeringMode(scan.mode),
		ModelProviderID:          scan.modelProviderID,
		Model:                    scan.model,
		RequestedReasoningEffort: scan.requestedReasoningEffort,
		State:                    owner.SteeringState(scan.state),
		QueueOrder:               scan.queueOrder,
		ConversationEventID:      scan.conversationEventID,
		ContinuationID:           scan.continuationID,
		SessionID:                scan.sessionID,
		ErrorCode:                owner.SteeringFailureReason(scan.errorCode),
		ErrorMessage:             scan.errorMessage,
	}
	var err error
	if scan.sendStartedAt != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, scan.sendStartedAt); parseErr == nil {
			record.SendStartedAt = &parsed
		}
	}
	if scan.resultJSON != "" && scan.resultJSON != "{}" {
		if err = json.Unmarshal([]byte(scan.resultJSON), &record.Result); err != nil {
			return nil, fmt.Errorf("decode Accepted Steering result: %w", err)
		}
	}
	if record.CreatedAt, err = time.Parse(time.RFC3339Nano, scan.createdAt); err != nil {
		return nil, fmt.Errorf("parse Accepted Steering created_at: %w", err)
	}
	if record.UpdatedAt, err = time.Parse(time.RFC3339Nano, scan.updatedAt); err != nil {
		return nil, fmt.Errorf("parse Accepted Steering updated_at: %w", err)
	}
	return record, nil
}

func (scan *steeringScan) targets() []any {
	return []any{
		&scan.id, &scan.ownerKind, &scan.ownerID, &scan.requestID, &scan.message, &scan.mode,
		&scan.modelProviderID, &scan.model, &scan.requestedReasoningEffort, &scan.state,
		&scan.queueOrder, &scan.conversationEventID, &scan.continuationID, &scan.sessionID,
		&scan.sendStartedAt, &scan.resultJSON, &scan.errorCode, &scan.errorMessage, &scan.createdAt, &scan.updatedAt,
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSteeringRecord(row rowScanner) (*owner.SteeringRecord, error) {
	scan := &steeringScan{}
	if err := row.Scan(scan.targets()...); err != nil {
		return nil, err
	}
	return scan.decode()
}

func scanSteeringRecordRows(rows *sql.Rows) (*owner.SteeringRecord, error) {
	scan := &steeringScan{}
	if err := rows.Scan(scan.targets()...); err != nil {
		return nil, err
	}
	return scan.decode()
}

func timePtr(value time.Time) *time.Time { return &value }

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(raw[:])
}
