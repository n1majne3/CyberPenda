package task

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"pentest/internal/childhistory"
	"pentest/internal/transcript"
)

// AppendEvent appends a structured event to the task timeline. Seq is assigned
// monotonically per task. The task must exist.
func (s *Service) AppendEvent(taskID string, kind EventKind, payload EventPayload) (Event, error) {
	return s.appendEvent(taskID, "", kind, payload)
}

// AppendContinuationEvent appends a Runtime Event bound to one Continuation.
// The Continuation must belong to the Task.
func (s *Service) AppendContinuationEvent(taskID, continuationID string, kind EventKind, payload EventPayload) (Event, error) {
	return s.appendEvent(taskID, continuationID, kind, payload)
}

// AppendEventTx appends a structured task event inside a caller-owned
// transaction. Seq is assigned monotonically per task within the transaction
// so the event can be committed atomically with another owner-neutral record
// (for example a durable Accepted Steering request).
func (s *Service) AppendEventTx(tx *sql.Tx, taskID string, kind EventKind, payload EventPayload) (Event, error) {
	return appendTaskEventTx(tx, taskID, kind, payload, time.Now().UTC())
}

// appendTaskEventTx stores one structured task event inside the caller-owned
// transaction. Seq is assigned monotonically per task within the transaction.
func appendTaskEventTx(tx *sql.Tx, taskID string, kind EventKind, payload EventPayload, now time.Time) (Event, error) {
	return appendTaskContinuationEventTx(tx, taskID, "", kind, payload, now)
}

func appendTaskContinuationEventTx(tx *sql.Tx, taskID, continuationID string, kind EventKind, payload EventPayload, now time.Time) (Event, error) {
	if payload == nil {
		payload = EventPayload{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id = ?`, taskID).Scan(&maxSeq); err != nil {
		return Event{}, fmt.Errorf("read max seq: %w", err)
	}
	event := Event{
		ID: newID(), TaskID: taskID, ContinuationID: continuationID, Kind: kind, Payload: payload, Seq: int(maxSeq.Int64) + 1, CreatedAt: now,
	}
	if _, err := tx.Exec(
		`INSERT INTO task_events (id, task_id, continuation_id, seq, kind, payload_json, created_at) VALUES (?, ?, NULLIF(?,''), ?, ?, ?, ?)`,
		event.ID, event.TaskID, event.ContinuationID, event.Seq, string(event.Kind), string(payloadJSON), event.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return Event{}, fmt.Errorf("store event: %w", err)
	}
	if err := childhistory.Record(tx, childhistory.Owner{Kind: "task", ID: event.TaskID}, transcript.Event{ID: event.ID, Seq: event.Seq, Kind: string(event.Kind), Payload: event.Payload, CreatedAt: event.CreatedAt}); err != nil {
		return Event{}, fmt.Errorf("index child history: %w", err)
	}
	return event, nil
}

func (s *Service) appendEvent(taskID, continuationID string, kind EventKind, payload EventPayload) (Event, error) {
	if _, err := s.Get(taskID); err != nil {
		return Event{}, err
	}
	if continuationID != "" {
		var ownerTaskID string
		if err := s.db.QueryRow(`SELECT task_id FROM task_continuations WHERE id=?`, continuationID).Scan(&ownerTaskID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Event{}, ErrNotFound
			}
			return Event{}, fmt.Errorf("load event Continuation: %w", err)
		}
		if ownerTaskID != taskID {
			return Event{}, ErrNotFound
		}
	}

	if payload == nil {
		payload = EventPayload{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}

	now := time.Now().UTC()
	event := Event{
		ID:             newID(),
		TaskID:         taskID,
		ContinuationID: continuationID,
		Kind:           kind,
		Payload:        payload,
		CreatedAt:      now,
	}

	// Compute next seq within a transaction so concurrent appends stay ordered.
	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id = ?`, taskID).Scan(&maxSeq); err != nil {
		return Event{}, fmt.Errorf("read max seq: %w", err)
	}
	event.Seq = int(maxSeq.Int64) + 1

	if _, err := tx.Exec(
		`INSERT INTO task_events (id, task_id, continuation_id, seq, kind, payload_json, created_at) VALUES (?, ?, NULLIF(?,''), ?, ?, ?, ?)`,
		event.ID, event.TaskID, event.ContinuationID, event.Seq, string(event.Kind), string(payloadJSON), event.CreatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return Event{}, fmt.Errorf("store event: %w", err)
	}
	if err := childhistory.Record(tx, childhistory.Owner{Kind: "task", ID: event.TaskID}, transcript.Event{ID: event.ID, Seq: event.Seq, Kind: string(event.Kind), Payload: event.Payload, CreatedAt: event.CreatedAt}); err != nil {
		return Event{}, fmt.Errorf("index child history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit event: %w", err)
	}
	return event, nil
}

// Events returns the task timeline ordered by sequence.
func (s *Service) Events(taskID string) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, task_id, continuation_id, attempt_node_id, seq, kind, payload_json, created_at FROM task_events WHERE task_id = ? ORDER BY seq ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// EventProjection selects the event kinds needed by one Runtime Owner history
// projection. This keeps SQL work bounded without loading unrelated history.
type EventProjection string

const (
	EventProjectionTimeline   EventProjection = "timeline"
	EventProjectionTranscript EventProjection = "transcript"
)

// EventWindowQuery is one bounded keyset read. Before and After are exclusive;
// zero means the initial recent window.
type EventWindowQuery struct {
	Projection EventProjection
	BeforeSet  bool
	Before     int
	AfterSet   bool
	After      int
	Limit      int
}

// EventWindow is an ordered bounded event slice plus projection cursor state.
type EventWindow struct {
	Events                 []Event
	Cursor                 int
	HasOlder               bool
	HasNewer               bool
	ScanCursor             int
	PriorContinuation      int
	PriorTranscriptAdapter string
}

// HistoryEventWindow reads only event kinds used by the selected projection.
// Query work is bounded by Limit plus one row, independent of full Task history.
func (s *Service) HistoryEventWindow(taskID string, query EventWindowQuery) (EventWindow, error) {
	if _, err := s.Get(taskID); err != nil {
		return EventWindow{}, err
	}
	if query.Limit < 1 || query.Before < 0 || query.After < 0 || (query.BeforeSet && query.AfterSet) {
		return EventWindow{}, fmt.Errorf("invalid Task Event window query")
	}
	kinds := "('runtime_output','lifecycle','steering','attachment','blackboard_conclusion')"
	if query.Projection == EventProjectionTranscript {
		kinds = "('conversation','runtime_output','lifecycle','steering')"
	} else if query.Projection != EventProjectionTimeline {
		return EventWindow{}, fmt.Errorf("invalid Task Event projection")
	}
	var cursor int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM task_events WHERE task_id=? AND kind IN `+kinds, taskID).Scan(&cursor); err != nil {
		return EventWindow{}, fmt.Errorf("read Task Event projection cursor: %w", err)
	}
	order := "DESC"
	predicate := ""
	args := []any{taskID}
	if query.BeforeSet {
		predicate = " AND seq < ?"
		args = append(args, query.Before)
	} else if query.AfterSet {
		predicate = " AND seq > ?"
		args = append(args, query.After)
		order = "ASC"
	}
	args = append(args, query.Limit+1)
	rows, err := s.db.Query(
		`SELECT id,task_id,continuation_id,attempt_node_id,seq,kind,payload_json,created_at
		 FROM task_events WHERE task_id=? AND kind IN `+kinds+predicate+` ORDER BY seq `+order+` LIMIT ?`, args...,
	)
	if err != nil {
		return EventWindow{}, fmt.Errorf("list Task Event window: %w", err)
	}
	defer rows.Close()
	events, err := scanEvents(rows)
	if err != nil {
		return EventWindow{}, err
	}
	hasOlder := false
	if order == "DESC" {
		if len(events) > query.Limit {
			hasOlder = true
			events = events[:query.Limit]
		}
		for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
			events[left], events[right] = events[right], events[left]
		}
	} else if len(events) > query.Limit {
		events = events[:query.Limit]
	}
	hasNewer := order == "ASC" && len(events) == query.Limit && scanCursorLessThanProjectionTail(events, cursor)
	scanCursor := query.After
	if len(events) > 0 {
		scanCursor = events[len(events)-1].Seq
	}
	window := EventWindow{Events: events, Cursor: cursor, HasOlder: hasOlder, HasNewer: hasNewer, ScanCursor: scanCursor}
	if query.Projection == EventProjectionTranscript && len(events) > 0 {
		if err := s.readTranscriptContextBefore(taskID, events[0].Seq, &window); err != nil {
			return EventWindow{}, err
		}
	}
	return window, nil
}

// HistoryEventSeqByRef resolves one Runtime Owner history detail reference
// (a stable timeline item ID or transcript entry ID) to the Seq of the first
// retained Event whose ID the reference embeds. Projection-derived IDs append
// suffixes to their source Event ID, so an exact match or an Event-ID prefix
// followed by a dash both resolve. ok=false means no Event owns the reference.
func (s *Service) HistoryEventSeqByRef(taskID string, ref string) (int, bool, error) {
	if _, err := s.Get(taskID); err != nil {
		return 0, false, err
	}
	var seq int
	err := s.db.QueryRow(
		`SELECT seq FROM task_events WHERE task_id=? AND (id=? OR instr(?, id || '-')=1) ORDER BY seq ASC LIMIT 1`,
		taskID, ref, ref,
	).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("resolve Task Event reference: %w", err)
	}
	return seq, true, nil
}

func (s *Service) readTranscriptContextBefore(taskID string, seq int, window *EventWindow) error {
	var continuationID string
	err := s.db.QueryRow(`
		SELECT COALESCE(continuation_id,''),COALESCE(json_extract(payload_json,'$.adapter'),'') FROM task_events
		WHERE task_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'
		ORDER BY seq DESC LIMIT 1`, taskID, seq,
	).Scan(&continuationID, &window.PriorTranscriptAdapter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Task Transcript context: %w", err)
	}
	if continuationID != "" {
		var provider string
		err = s.db.QueryRow(`SELECT number,runtime_provider FROM task_continuations WHERE id=? AND task_id=?`, continuationID, taskID).
			Scan(&window.PriorContinuation, &provider)
		if err == nil {
			if provider != "" {
				window.PriorTranscriptAdapter = provider
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read Task Transcript Continuation pin: %w", err)
		}
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM task_events
		WHERE task_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'`, taskID, seq,
	).Scan(&window.PriorContinuation); err != nil {
		return fmt.Errorf("read legacy Task Transcript Continuation context: %w", err)
	}
	return nil
}

func scanCursorLessThanProjectionTail(events []Event, cursor int) bool {
	return len(events) > 0 && events[len(events)-1].Seq < cursor
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var event Event
		var continuationID sql.NullString
		var attemptNodeID sql.NullString
		var kind string
		var payloadJSON string
		var createdAt string
		if err := rows.Scan(&event.ID, &event.TaskID, &continuationID, &attemptNodeID, &event.Seq, &kind, &payloadJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.ContinuationID = continuationID.String
		event.AttemptNodeID = attemptNodeID.String
		event.Kind = EventKind(kind)
		if err := json.Unmarshal([]byte(payloadJSON), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode event payload: %w", err)
		}
		var parseErr error
		if event.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt); parseErr != nil {
			return nil, fmt.Errorf("parse created_at: %w", parseErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return events, nil
}
