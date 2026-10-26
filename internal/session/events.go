package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/childhistory"
	"pentest/internal/transcript"
)

// Events returns the ordered owner-local Session timeline.
func (s *Service) Events(id string) ([]Event, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,session_id,seq,kind,payload_json,created_at FROM session_events WHERE session_id=? ORDER BY seq ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("list Session Events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// EventsByKind reads only the Session Events needed by one projection.
// An empty kind list returns no Events; it never expands into a full history read.
func (s *Service) EventsByKind(id string, kinds ...EventKind) ([]Event, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	if len(kinds) == 0 {
		return []Event{}, nil
	}
	placeholders := make([]string, len(kinds))
	args := []any{id}
	for i, kind := range kinds {
		placeholders[i] = "?"
		args = append(args, kind)
	}
	rows, err := s.db.Query(`SELECT id,session_id,seq,kind,payload_json,created_at FROM session_events WHERE session_id=? AND kind IN (`+strings.Join(placeholders, ",")+`) ORDER BY seq ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list Session Events by kind: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// PermissionEvents returns only ordered permission requests and applied responses.
// Runtime output and unrelated lifecycle payloads do not need to be decoded.
func (s *Service) PermissionEvents(id string) ([]Event, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,session_id,seq,kind,payload_json,created_at
 FROM session_events WHERE session_id=? AND kind IN ('permission','lifecycle')
 AND json_extract(payload_json,'$.permission_request_id') IS NOT NULL
 AND json_extract(payload_json,'$.phase') IN ('provider_permission_requested','provider_permission_response_applied')
 ORDER BY seq ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("list Session permission events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// EventProjection selects the Session Event kinds needed by one Runtime Owner
// history projection.
type EventProjection string

const (
	EventProjectionTimeline   EventProjection = "timeline"
	EventProjectionTranscript EventProjection = "transcript"
)

type EventWindowQuery struct {
	Projection EventProjection
	BeforeSet  bool
	Before     int
	AfterSet   bool
	After      int
	Limit      int
}

type EventWindow struct {
	Events                 []Event
	Cursor                 int
	HasOlder               bool
	HasNewer               bool
	ScanCursor             int
	PriorContinuation      int
	PriorTranscriptAdapter string
}

// HistoryEventWindow reads a fixed-size keyset window independently of the
// Session's full Event history.
func (s *Service) HistoryEventWindow(id string, query EventWindowQuery) (EventWindow, error) {
	if _, err := s.Get(id); err != nil {
		return EventWindow{}, err
	}
	if query.Limit < 1 || query.Before < 0 || query.After < 0 || (query.BeforeSet && query.AfterSet) {
		return EventWindow{}, fmt.Errorf("invalid Session Event window query")
	}
	kinds := "('runtime_output','lifecycle','steering','attachment','blackboard_conclusion')"
	if query.Projection == EventProjectionTranscript {
		kinds = "('conversation','runtime_output','lifecycle','steering')"
	} else if query.Projection != EventProjectionTimeline {
		return EventWindow{}, fmt.Errorf("invalid Session Event projection")
	}
	var cursor int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM session_events WHERE session_id=? AND kind IN `+kinds, id).Scan(&cursor); err != nil {
		return EventWindow{}, fmt.Errorf("read Session Event projection cursor: %w", err)
	}
	order := "DESC"
	predicate := ""
	args := []any{id}
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
		`SELECT id,session_id,seq,kind,payload_json,created_at FROM session_events
		 WHERE session_id=? AND kind IN `+kinds+predicate+` ORDER BY seq `+order+` LIMIT ?`, args...,
	)
	if err != nil {
		return EventWindow{}, fmt.Errorf("list Session Event window: %w", err)
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
	hasNewer := order == "ASC" && len(events) == query.Limit && len(events) > 0 && events[len(events)-1].Seq < cursor
	scanCursor := query.After
	if len(events) > 0 {
		scanCursor = events[len(events)-1].Seq
	}
	window := EventWindow{Events: events, Cursor: cursor, HasOlder: hasOlder, HasNewer: hasNewer, ScanCursor: scanCursor}
	if query.Projection == EventProjectionTranscript && len(events) > 0 {
		if err := s.readTranscriptContextBefore(id, events[0].Seq, &window); err != nil {
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
func (s *Service) HistoryEventSeqByRef(id string, ref string) (int, bool, error) {
	if _, err := s.Get(id); err != nil {
		return 0, false, err
	}
	candidates := historyReferenceCandidates(ref)
	args := make([]any, 0, len(candidates)+1)
	args = append(args, id)
	for _, candidate := range candidates {
		args = append(args, candidate)
	}
	var seq sql.NullInt64
	err := s.db.QueryRow(
		historyEventSeqByRefQuery(len(candidates)),
		args...,
	).Scan(&seq)
	if err != nil {
		return 0, false, fmt.Errorf("resolve Session Event reference: %w", err)
	}
	if !seq.Valid {
		return 0, false, nil
	}
	return int(seq.Int64), true, nil
}

func historyEventSeqByRefQuery(candidateCount int) string {
	return `SELECT MIN(seq) FROM session_events WHERE +session_id=? AND id IN (` +
		strings.TrimSuffix(strings.Repeat("?,", candidateCount), ",") + `)`
}

func historyReferenceCandidates(ref string) []string {
	candidates := []string{ref}
	for index := range ref {
		if ref[index] == '-' {
			candidates = append(candidates, ref[:index])
		}
	}
	return candidates
}

func (s *Service) readTranscriptContextBefore(sessionID string, seq int, window *EventWindow) error {
	var continuationID string
	err := s.db.QueryRow(`
		SELECT COALESCE(json_extract(payload_json,'$.continuation_id'),''),COALESCE(json_extract(payload_json,'$.adapter'),'') FROM session_events
		WHERE session_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'
		ORDER BY seq DESC LIMIT 1`, sessionID, seq,
	).Scan(&continuationID, &window.PriorTranscriptAdapter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Session Transcript context: %w", err)
	}
	if continuationID != "" {
		var provider string
		err = s.db.QueryRow(`SELECT number,runtime_provider FROM session_continuations WHERE id=? AND session_id=?`, continuationID, sessionID).
			Scan(&window.PriorContinuation, &provider)
		if err == nil {
			if provider != "" {
				window.PriorTranscriptAdapter = provider
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read Session Transcript Continuation pin: %w", err)
		}
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM session_events
		WHERE session_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'`, sessionID, seq,
	).Scan(&window.PriorContinuation); err != nil {
		return fmt.Errorf("read legacy Session Transcript Continuation context: %w", err)
	}
	return nil
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		var kind, payload, created string
		if err := rows.Scan(&event.ID, &event.SessionID, &event.Seq, &kind, &payload, &created); err != nil {
			return nil, fmt.Errorf("scan Session Event: %w", err)
		}
		event.Kind = EventKind(kind)
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode Session Event: %w", err)
		}
		var err error
		if event.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse Session Event time: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Session Events: %w", err)
	}
	return events, nil
}

// AppendEvent records one owner-local timeline event and advances Session
// activity. Conversation events are deliberately stored in the same durable
// stream but are projected separately by Conversation and Timeline.
func (s *Service) AppendEvent(id string, kind EventKind, payload EventPayload) (Event, error) {
	if err := s.FlushRuntimeOutputs(id); err != nil {
		return Event{}, err
	}
	found, err := s.Get(id)
	if err != nil {
		return Event{}, err
	}
	if found.Lifecycle != LifecycleOpen {
		return Event{}, ErrSessionNotOpen
	}
	if continuationID, _ := payload["continuation_id"].(string); strings.TrimSpace(continuationID) != "" {
		continuation, continuationErr := s.Continuation(continuationID)
		if continuationErr != nil || continuation.SessionID != id {
			return Event{}, ErrContinuationNotFound
		}
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, fmt.Errorf("begin Session Event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	event, err := appendEventTx(tx, id, kind, payload, now)
	if err != nil {
		return Event{}, fmt.Errorf("store Session Event: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=?,last_activity_at=? WHERE id=?`, formatTime(now), formatTime(now), id); err != nil {
		return Event{}, fmt.Errorf("update Session activity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit Session Event: %w", err)
	}
	return event, nil
}

// AppendEventTx appends a structured session event inside a caller-owned
// transaction so it can be committed atomically with another owner-neutral
// record (for example a durable Accepted Steering request). Call
// FlushRuntimeOutputs before opening the caller-owned transaction.
func (s *Service) AppendEventTx(tx *sql.Tx, sessionID string, kind EventKind, payload EventPayload) (Event, error) {
	now := time.Now().UTC()
	event, err := appendEventTx(tx, sessionID, kind, payload, now)
	if err != nil {
		return Event{}, err
	}
	if err := touchSessionActivityTx(tx, sessionID, now); err != nil {
		return Event{}, err
	}
	return event, nil
}

func touchSessionActivityTx(tx *sql.Tx, sessionID string, now time.Time) error {
	result, err := tx.Exec(`UPDATE sessions SET updated_at=?,last_activity_at=? WHERE id=? AND lifecycle=?`,
		formatTime(now), formatTime(now), sessionID, string(LifecycleOpen))
	if err != nil {
		return fmt.Errorf("update Session activity: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count Session activity update: %w", err)
	}
	if changed != 1 {
		return ErrSessionNotOpen
	}
	return nil
}

func appendEventTx(tx *sql.Tx, sessionID string, kind EventKind, payload EventPayload, now time.Time) (Event, error) {
	var max sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM session_events WHERE session_id=?`, sessionID).Scan(&max); err != nil {
		return Event{}, fmt.Errorf("read Session Event sequence: %w", err)
	}
	return appendEventAtSequenceTx(tx, sessionID, kind, payload, now, int(max.Int64)+1)
}

func appendEventAtSequenceTx(tx *sql.Tx, sessionID string, kind EventKind, payload EventPayload, now time.Time, seq int) (Event, error) {
	if payload == nil {
		payload = EventPayload{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode Event payload: %w", err)
	}
	id, err := newID()
	if err != nil {
		return Event{}, fmt.Errorf("generate Session Event id: %w", err)
	}
	event := Event{ID: id, SessionID: sessionID, Seq: seq, Kind: kind, Payload: payload, CreatedAt: now.UTC()}
	if _, err := tx.Exec(`INSERT INTO session_events (id,session_id,seq,kind,payload_json,created_at) VALUES (?,?,?,?,?,?)`, event.ID, event.SessionID, event.Seq, string(event.Kind), string(encoded), formatTime(event.CreatedAt)); err != nil {
		return Event{}, err
	}
	if err := childhistory.Record(tx, childhistory.Owner{Kind: "session", ID: event.SessionID}, transcript.Event{ID: event.ID, Seq: event.Seq, Kind: string(event.Kind), Payload: event.Payload, CreatedAt: event.CreatedAt}); err != nil {
		return Event{}, fmt.Errorf("index child history: %w", err)
	}
	return event, nil
}
