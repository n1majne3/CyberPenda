package daemon

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

// ownerProjection selects the Event kinds one Runtime Owner history read
// needs. It mirrors the store-level Event projections without exposing the
// Task and Session store types to the module.
type ownerProjection string

const (
	ownerProjectionTimeline   ownerProjection = "timeline"
	ownerProjectionTranscript ownerProjection = "transcript"
)

// ownerEvent is the owner-neutral retained Event shape. Task and Session store
// adapters project their Event rows into it so one history module serves both
// Runtime Owner kinds.
type ownerEvent struct {
	ID        string
	Seq       int
	Kind      string
	Payload   map[string]any
	CreatedAt time.Time
}

// ownerHistoryWindowQuery is one bounded keyset read against an owner's
// retained Events. The module always applies the fixed history query limit;
// callers only choose the cursor window.
type ownerHistoryWindowQuery struct {
	BeforeSet bool
	Before    int
	AfterSet  bool
	After     int
}

// ownerHistoryEventWindow is the store answer to one bounded read: ordered
// projection Events plus the cursor state the module needs to reconcile pages.
type ownerHistoryEventWindow struct {
	Events                 []ownerEvent
	Cursor                 int
	HasOlder               bool
	HasNewer               bool
	ScanCursor             int
	PriorContinuation      int
	PriorTranscriptAdapter string
}

// ownerHistoryStore is the seam between the Runtime Owner history module and
// one owner kind's Event store. Task and Session each supply one adapter; the
// module never touches their store types directly.
type ownerHistoryStore interface {
	TimelineWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error)
	TranscriptWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error)
	// EventSeqByRef resolves one stable timeline-item or transcript-entry
	// reference to the Seq of the first retained Event whose ID the reference
	// embeds. ok=false means no retained Event owns the reference.
	EventSeqByRef(ref string) (int, bool, error)
}

// ownerHistory is the deep Runtime Owner history read model: cursor paging,
// window bounds, projection, and complete detail lookup for one Task or
// Session. Handlers keep only route parsing and owner-specific response
// envelopes.
type ownerHistory struct {
	store                ownerHistoryStore
	subject              transcript.Subject
	timelineDetailBase   string
	transcriptDetailBase string
}

// TimelinePage returns the bounded Timeline projection page for one request:
// the newest initial window, a backward `before` page, or a live-tail `after`
// delta.
func (h ownerHistory) TimelinePage(req historyRequest) (historyPage[timeline.Item], error) {
	items, window, err := collectTimelineItems(req, func(scan historyRequest) (timelineEventChunk, error) {
		stored, err := h.store.TimelineWindow(ownerHistoryWindowQuery{
			BeforeSet: scan.beforeSet, Before: scan.before,
			AfterSet: scan.afterSet, After: scan.after,
		})
		if err != nil {
			return timelineEventChunk{}, err
		}
		return timelineEventChunk{
			events:     ownerTimelineEvents(stored.Events),
			cursor:     stored.Cursor,
			hasOlder:   stored.HasOlder,
			hasNewer:   stored.HasNewer,
			scanCursor: stored.ScanCursor,
		}, nil
	})
	if err != nil {
		return historyPage[timeline.Item]{}, err
	}
	if items == nil {
		items = []timeline.Item{}
	}
	page := historyResponseFor(items, req, func(item timeline.Item) int {
		return item.Seq
	}, func(item timeline.Item) (timeline.Item, int) {
		return boundedTimelineItem(item, h.timelineDetailBase)
	})
	reconcileHistoryPageWindow(&page, req, window.hasOlder, window.cursor, window.hasNewer, window.scanCursor)
	return page, nil
}

// TimelineItem returns one complete retained Timeline item by stable item ID
// or legacy numeric Seq. The lookup resolves the reference to its source Event
// and reads one bounded window ending at that Seq; it never rebuilds the full
// owner Event history.
func (h ownerHistory) TimelineItem(ref string) (timeline.Item, bool, error) {
	if ref == "" {
		return timeline.Item{}, false, nil
	}
	seq, ok, err := h.resolveRefSeq(ref)
	if err != nil || !ok {
		return timeline.Item{}, false, err
	}
	window, err := h.store.TimelineWindow(ownerHistoryWindowQuery{BeforeSet: true, Before: seq + 1})
	if err != nil {
		return timeline.Item{}, false, err
	}
	seqHint, _ := strconv.Atoi(ref)
	for _, item := range timeline.Build(ownerTimelineEvents(window.Events)) {
		if item.ID == ref || (seqHint > 0 && item.Seq == seqHint) {
			return item, true, nil
		}
	}
	return timeline.Item{}, false, nil
}

// TranscriptPage returns the bounded Transcript projection page for one
// request, with the parser context that was active before the window so
// Continuation numbering stays absolute.
func (h ownerHistory) TranscriptPage(req historyRequest) (historyPage[transcript.Entry], error) {
	window, err := h.store.TranscriptWindow(ownerHistoryWindowQuery{
		BeforeSet: req.beforeSet, Before: req.before,
		AfterSet: req.afterSet, After: req.after,
	})
	if err != nil {
		return historyPage[transcript.Entry]{}, err
	}
	entries := transcript.BuildWindow(h.subject, ownerTranscriptEvents(window.Events), transcript.WindowContext{
		Continuation: window.PriorContinuation,
		Adapter:      window.PriorTranscriptAdapter,
	})
	if entries == nil {
		entries = []transcript.Entry{}
	}
	page := historyResponseFor(entries, req, func(entry transcript.Entry) int {
		return entry.Seq
	}, func(entry transcript.Entry) (transcript.Entry, int) {
		return boundedTranscriptEntry(entry, h.transcriptDetailBase)
	})
	reconcileHistoryPageWindow(&page, req, window.HasOlder, window.Cursor, window.HasNewer, window.ScanCursor)
	return page, nil
}

// TranscriptEntry returns one complete retained Transcript entry by ID,
// including the synthetic seq-0 goal row and the full payload that the
// history window preview truncated. Like TimelineItem it reads one bounded
// window ending at the source Event instead of rebuilding full history.
func (h ownerHistory) TranscriptEntry(ref string) (transcript.Entry, bool, error) {
	if ref == "" {
		return transcript.Entry{}, false, nil
	}
	if strings.TrimSpace(h.subject.Title) != "" && ref == h.subject.ID+"-goal" {
		goal := transcript.BuildWindow(h.subject, nil, transcript.WindowContext{})
		if len(goal) == 1 {
			return goal[0], true, nil
		}
	}
	seq, ok, err := h.resolveRefSeq(ref)
	if err != nil || !ok {
		return transcript.Entry{}, false, err
	}
	window, err := h.store.TranscriptWindow(ownerHistoryWindowQuery{BeforeSet: true, Before: seq + 1})
	if err != nil {
		return transcript.Entry{}, false, err
	}
	for _, entry := range transcript.BuildWindow(h.subject, ownerTranscriptEvents(window.Events), transcript.WindowContext{
		Continuation: window.PriorContinuation,
		Adapter:      window.PriorTranscriptAdapter,
	}) {
		if entry.ID == ref {
			return entry, true, nil
		}
	}
	return transcript.Entry{}, false, nil
}

// resolveRefSeq maps a numeric legacy reference to its Seq and any other
// reference to the source Event Seq resolved by the store adapter.
func (h ownerHistory) resolveRefSeq(ref string) (int, bool, error) {
	if seq, err := strconv.Atoi(ref); err == nil && seq > 0 {
		return seq, true, nil
	}
	return h.store.EventSeqByRef(ref)
}

// reconcileHistoryPageWindow folds the store window's cursor state into the
// projected page: older Event rows beyond the projection edge still count as
// older history, and a live-tail scan that stopped before the projection tail
// keeps its scan cursor so the next poll cannot miss rows.
func reconcileHistoryPageWindow[T any](page *historyPage[T], req historyRequest, windowHasOlder bool, windowCursor int, windowHasNewer bool, windowScanCursor int) {
	page.hasOlder = page.hasOlder || windowHasOlder
	if req.afterSet && windowHasNewer {
		page.cursor = windowScanCursor
	} else {
		page.cursor = windowCursor
	}
}

func ownerTimelineEvents(events []ownerEvent) []timeline.Event {
	converted := make([]timeline.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, timeline.Event{
			ID: event.ID, Seq: event.Seq, Kind: event.Kind,
			Payload: event.Payload, CreatedAt: event.CreatedAt,
		})
	}
	return converted
}

func ownerTranscriptEvents(events []ownerEvent) []transcript.Event {
	converted := make([]transcript.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, transcript.Event{
			ID: event.ID, Seq: event.Seq, Kind: event.Kind,
			Payload: event.Payload, CreatedAt: event.CreatedAt,
		})
	}
	return converted
}

// taskOwnerHistoryStore adapts the Task Event store to the owner history
// seam.
type taskOwnerHistoryStore struct {
	tasks  *task.Service
	taskID string
}

func (s taskOwnerHistoryStore) TimelineWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	return s.window(task.EventProjectionTimeline, query)
}

func (s taskOwnerHistoryStore) TranscriptWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	return s.window(task.EventProjectionTranscript, query)
}

func (s taskOwnerHistoryStore) window(projection task.EventProjection, query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	stored, err := s.tasks.HistoryEventWindow(s.taskID, task.EventWindowQuery{
		Projection: projection,
		BeforeSet:  query.BeforeSet, Before: query.Before,
		AfterSet: query.AfterSet, After: query.After,
		Limit: historyEventQueryLimit,
	})
	if err != nil {
		return ownerHistoryEventWindow{}, err
	}
	return ownerHistoryEventWindow{
		Events:                 taskOwnerEvents(stored.Events),
		Cursor:                 stored.Cursor,
		HasOlder:               stored.HasOlder,
		HasNewer:               stored.HasNewer,
		ScanCursor:             stored.ScanCursor,
		PriorContinuation:      stored.PriorContinuation,
		PriorTranscriptAdapter: stored.PriorTranscriptAdapter,
	}, nil
}

func (s taskOwnerHistoryStore) EventSeqByRef(ref string) (int, bool, error) {
	return s.tasks.HistoryEventSeqByRef(s.taskID, ref)
}

func taskOwnerEvents(events []task.Event) []ownerEvent {
	converted := make([]ownerEvent, 0, len(events))
	for _, event := range events {
		converted = append(converted, ownerEvent{
			ID: event.ID, Seq: event.Seq, Kind: string(event.Kind),
			Payload: event.Payload, CreatedAt: event.CreatedAt,
		})
	}
	return converted
}

// sessionOwnerHistoryStore adapts the Session Event store to the owner
// history seam.
type sessionOwnerHistoryStore struct {
	sessions  *session.Service
	sessionID string
}

func (s sessionOwnerHistoryStore) TimelineWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	return s.window(session.EventProjectionTimeline, query)
}

func (s sessionOwnerHistoryStore) TranscriptWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	return s.window(session.EventProjectionTranscript, query)
}

func (s sessionOwnerHistoryStore) window(projection session.EventProjection, query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	stored, err := s.sessions.HistoryEventWindow(s.sessionID, session.EventWindowQuery{
		Projection: projection,
		BeforeSet:  query.BeforeSet, Before: query.Before,
		AfterSet: query.AfterSet, After: query.After,
		Limit: historyEventQueryLimit,
	})
	if err != nil {
		return ownerHistoryEventWindow{}, err
	}
	return ownerHistoryEventWindow{
		Events:                 sessionOwnerEvents(stored.Events),
		Cursor:                 stored.Cursor,
		HasOlder:               stored.HasOlder,
		HasNewer:               stored.HasNewer,
		ScanCursor:             stored.ScanCursor,
		PriorContinuation:      stored.PriorContinuation,
		PriorTranscriptAdapter: stored.PriorTranscriptAdapter,
	}, nil
}

func (s sessionOwnerHistoryStore) EventSeqByRef(ref string) (int, bool, error) {
	return s.sessions.HistoryEventSeqByRef(s.sessionID, ref)
}

func sessionOwnerEvents(events []session.Event) []ownerEvent {
	converted := make([]ownerEvent, 0, len(events))
	for _, event := range events {
		converted = append(converted, ownerEvent{
			ID: event.ID, Seq: event.Seq, Kind: string(event.Kind),
			Payload: event.Payload, CreatedAt: event.CreatedAt,
		})
	}
	return converted
}

// taskOwnerHistory builds the owner history read model for one Task.
func (server *Server) taskOwnerHistory(found task.Task) ownerHistory {
	return ownerHistory{
		store: taskOwnerHistoryStore{tasks: server.tasks, taskID: found.ID},
		subject: transcript.Subject{
			ID: found.ID, Title: found.Goal, CreatedAt: found.CreatedAt,
		},
		timelineDetailBase:   fmt.Sprintf("/api/projects/%s/tasks/%s/timeline/items", found.ProjectID, found.ID),
		transcriptDetailBase: fmt.Sprintf("/api/projects/%s/tasks/%s/transcript/entries", found.ProjectID, found.ID),
	}
}

// sessionOwnerHistory builds the owner history read model for one Session.
// The Session has no synthetic goal row because its initial input is already
// a conversation Event.
func (server *Server) sessionOwnerHistory(found session.Session) ownerHistory {
	return ownerHistory{
		store: sessionOwnerHistoryStore{sessions: server.sessions, sessionID: found.ID},
		subject: transcript.Subject{
			ID: found.ID, CreatedAt: found.CreatedAt,
		},
		timelineDetailBase:   "/api/sessions/" + found.ID + "/timeline/items",
		transcriptDetailBase: "/api/sessions/" + found.ID + "/transcript/entries",
	}
}
