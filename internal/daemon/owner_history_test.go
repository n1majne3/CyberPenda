package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/transcript"
)

// fakeOwnerHistoryStore emulates the Task and Session Event window stores for
// the owner history module. It records every window query so tests can prove
// detail lookups read bounded windows instead of full Event history.
type fakeOwnerHistoryStore struct {
	events []ownerEvent
	// transcriptStarted counts lifecycle started Events by source Seq so the
	// fake can compute PriorContinuation like the real stores do.
	queries []ownerHistoryWindowQuery
}

var timelineProjectionKinds = map[string]bool{
	"runtime_output": true, "lifecycle": true, "steering": true,
	"attachment": true, "blackboard_conclusion": true,
}

var transcriptProjectionKinds = map[string]bool{
	"conversation": true, "runtime_output": true, "lifecycle": true, "steering": true,
}

func (s *fakeOwnerHistoryStore) projectionKinds(kind ownerProjection) map[string]bool {
	if kind == ownerProjectionTranscript {
		return transcriptProjectionKinds
	}
	return timelineProjectionKinds
}

func (s *fakeOwnerHistoryStore) window(kind ownerProjection, query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	s.queries = append(s.queries, query)
	kinds := s.projectionKinds(kind)
	projected := make([]ownerEvent, 0, len(s.events))
	for _, event := range s.events {
		if kinds[event.Kind] {
			projected = append(projected, event)
		}
	}
	cursor := 0
	for _, event := range projected {
		if event.Seq > cursor {
			cursor = event.Seq
		}
	}
	selected := make([]ownerEvent, 0, len(projected))
	for _, event := range projected {
		if query.BeforeSet && event.Seq >= query.Before {
			continue
		}
		if query.AfterSet && event.Seq <= query.After {
			continue
		}
		selected = append(selected, event)
	}
	ascending := query.AfterSet
	hasOlder := false
	if !ascending {
		for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
			selected[left], selected[right] = selected[right], selected[left]
		}
		if len(selected) > historyEventQueryLimit {
			hasOlder = true
			selected = selected[:historyEventQueryLimit]
		}
		for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
			selected[left], selected[right] = selected[right], selected[left]
		}
	} else if len(selected) > historyEventQueryLimit {
		selected = selected[:historyEventQueryLimit]
	}
	hasNewer := ascending && len(selected) == historyEventQueryLimit && len(selected) > 0 && selected[len(selected)-1].Seq < cursor
	scanCursor := query.After
	if len(selected) > 0 {
		scanCursor = selected[len(selected)-1].Seq
	}
	window := ownerHistoryEventWindow{
		Events: selected, Cursor: cursor, HasOlder: hasOlder,
		HasNewer: hasNewer, ScanCursor: scanCursor,
	}
	if kind == ownerProjectionTranscript && len(selected) > 0 {
		window.PriorContinuation = s.startedBefore(selected[0].Seq)
		for index := len(s.events) - 1; index >= 0; index-- {
			event := s.events[index]
			if event.Seq < selected[0].Seq && event.Kind == "lifecycle" {
				if adapter, ok := event.Payload["adapter"].(string); ok {
					window.PriorTranscriptAdapter = adapter
				}
				break
			}
		}
	}
	return window, nil
}

func (s *fakeOwnerHistoryStore) startedBefore(seq int) int {
	count := 0
	for _, event := range s.events {
		if event.Seq >= seq {
			break
		}
		if event.Kind == "lifecycle" && event.Payload["phase"] == "started" {
			count++
		}
	}
	return count
}

func (s *fakeOwnerHistoryStore) TimelineWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	return s.window(ownerProjectionTimeline, query)
}

func (s *fakeOwnerHistoryStore) TranscriptWindow(query ownerHistoryWindowQuery) (ownerHistoryEventWindow, error) {
	return s.window(ownerProjectionTranscript, query)
}

func (s *fakeOwnerHistoryStore) EventSeqByRef(ref string) (int, bool, error) {
	for _, event := range s.events {
		if event.ID == ref || strings.HasPrefix(ref, event.ID+"-") {
			return event.Seq, true, nil
		}
	}
	return 0, false, nil
}

func (s *fakeOwnerHistoryStore) resetQueries() {
	s.queries = nil
}

func newOwnerHistoryTestSubject() transcript.Subject {
	return transcript.Subject{ID: "owner-1", Title: "owner goal", CreatedAt: time.Unix(0, 0).UTC()}
}

func newTestOwnerHistory(store *fakeOwnerHistoryStore) ownerHistory {
	return ownerHistory{
		store:                store,
		subject:              newOwnerHistoryTestSubject(),
		timelineDetailBase:   "/api/test/timeline/items",
		transcriptDetailBase: "/api/test/transcript/entries",
	}
}

func appendOwnerLifecycleEvents(store *fakeOwnerHistoryStore, count, startSeq int) {
	for index := 1; index <= count; index++ {
		store.events = append(store.events, ownerEvent{
			ID: fmt.Sprintf("evt-%06d", startSeq+index), Seq: startSeq + index, Kind: "lifecycle",
			Payload:   map[string]any{"phase": fmt.Sprintf("phase-%d", index)},
			CreatedAt: time.Unix(int64(startSeq+index), 0).UTC(),
		})
	}
}

func TestOwnerHistoryTimelinePageBoundedWindowAndCursors(t *testing.T) {
	store := &fakeOwnerHistoryStore{}
	appendOwnerLifecycleEvents(store, 300, 0)
	history := newTestOwnerHistory(store)

	page, err := history.TimelinePage(parseHistoryRequestFromCursors(false, 0, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.items) != historyWindowMaxItems || !page.hasOlder || page.cursor != 300 {
		t.Fatalf("initial timeline page = %d items has_older=%v cursor=%d, want bounded newest window", len(page.items), page.hasOlder, page.cursor)
	}
	if first, last := page.items[0].Seq, page.items[len(page.items)-1].Seq; first != 101 || last != 300 {
		t.Fatalf("initial window spans seqs %d..%d, want 101..300", first, last)
	}

	older, err := history.TimelinePage(parseHistoryRequestFromCursors(true, 101, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(older.items) != 100 || older.hasOlder {
		t.Fatalf("backward page before=101 = %d items has_older=%v, want the 100 oldest without older pages", len(older.items), older.hasOlder)
	}
	if older.items[0].Seq != 1 || older.items[len(older.items)-1].Seq != 100 {
		t.Fatalf("backward page spans %d..%d, want 1..100", older.items[0].Seq, older.items[len(older.items)-1].Seq)
	}

	delta, err := history.TimelinePage(parseHistoryRequestFromCursors(false, 0, true, 295))
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.items) != 5 || delta.items[0].Seq != 296 || delta.items[4].Seq != 300 || delta.cursor != 300 {
		t.Fatalf("live-tail delta = %#v, want exactly seqs 296..300", delta.items)
	}
}

func TestOwnerHistoryTranscriptPageKeepsWindowContext(t *testing.T) {
	store := &fakeOwnerHistoryStore{}
	store.events = []ownerEvent{
		{ID: "evt-start-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: time.Unix(1, 0).UTC()},
		{ID: "evt-msg-1", Seq: 2, Kind: "conversation", Payload: map[string]any{"role": "assistant", "text": "First Continuation"}, CreatedAt: time.Unix(2, 0).UTC()},
		{ID: "evt-start-2", Seq: 3, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: time.Unix(3, 0).UTC()},
		{ID: "evt-msg-2", Seq: 4, Kind: "conversation", Payload: map[string]any{"role": "assistant", "text": "Resumed work"}, CreatedAt: time.Unix(4, 0).UTC()},
	}
	history := newTestOwnerHistory(store)

	page, err := history.TranscriptPage(parseHistoryRequestFromCursors(false, 0, true, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.items) != 2 {
		t.Fatalf("delta entries = %#v, want the Continuation row and the message", page.items)
	}
	if got := page.items[0]; got.Kind != transcript.KindContinuation || got.Continuation != 2 {
		t.Fatalf("delta Continuation row = %#v, want absolute Continuation 2 from window context", got)
	}
	if got := page.items[1]; got.Kind != transcript.KindMessage || got.Continuation != 2 || got.Text != "Resumed work" {
		t.Fatalf("delta message = %#v, want parser context from before the page", got)
	}
}

func TestOwnerHistoryDetailLookupReadsBoundedWindowsOnly(t *testing.T) {
	store := &fakeOwnerHistoryStore{}
	appendOwnerLifecycleEvents(store, 300, 0)
	history := newTestOwnerHistory(store)
	store.resetQueries()

	item, found, err := history.TimelineItem("evt-000003")
	if err != nil || !found {
		t.Fatalf("lookup by stable event ID = (%v, %v, %v), want the seq-3 item", item, found, err)
	}
	if item.Seq != 3 {
		t.Fatalf("stable-ID lookup returned seq %d, want 3", item.Seq)
	}
	if len(store.queries) != 1 {
		t.Fatalf("stable-ID lookup issued %d window queries, want 1", len(store.queries))
	}
	query := store.queries[0]
	if !query.BeforeSet || query.Before != 4 || query.AfterSet {
		t.Fatalf("stable-ID lookup query = %+v, want one bounded backward window ending at the source seq", query)
	}

	store.resetQueries()
	numeric, found, err := history.TimelineItem("3")
	if err != nil || !found || numeric.Seq != 3 {
		t.Fatalf("numeric lookup = (%v, %v, %v), want the seq-3 item", numeric, found, err)
	}
	if len(store.queries) != 1 || !store.queries[0].BeforeSet || store.queries[0].Before != 4 {
		t.Fatalf("numeric lookup queries = %+v, want one bounded backward window", store.queries)
	}

	if _, found, err := history.TimelineItem("evt-does-not-exist"); err != nil || found {
		t.Fatalf("unknown ref lookup = (%v, %v), want not found without error", found, err)
	}
	if _, found, err := history.TimelineItem(""); err != nil || found {
		t.Fatalf("empty ref lookup = (%v, %v), want not found without error", found, err)
	}
}

func TestOwnerHistoryTranscriptDetailResolvesDerivedIDsAndGoalRow(t *testing.T) {
	store := &fakeOwnerHistoryStore{}
	record, err := json.Marshal(map[string]any{
		"type": "tool_result", "tool_name": "bash", "output": "tool output",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.events = []ownerEvent{
		{ID: "evt-start", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: time.Unix(1, 0).UTC()},
		{ID: "evt-tool", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"text": string(record)}, CreatedAt: time.Unix(2, 0).UTC()},
		{ID: "evt-msg", Seq: 3, Kind: "conversation", Payload: map[string]any{"role": "assistant", "text": "message body"}, CreatedAt: time.Unix(3, 0).UTC()},
	}
	history := newTestOwnerHistory(store)

	page, err := history.TranscriptPage(parseHistoryRequestFromCursors(false, 0, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	var derived, message transcript.Entry
	for _, entry := range page.items {
		if entry.Kind == transcript.KindToolResult {
			derived = entry
		}
		if entry.Kind == transcript.KindMessage && entry.Text == "message body" {
			message = entry
		}
	}
	if derived.ID == "" {
		t.Fatalf("page = %#v, want a parsed tool_result entry with a derived ID", page.items)
	}
	full, found, err := history.TranscriptEntry(derived.ID)
	if err != nil || !found {
		t.Fatalf("derived-ID lookup = (%v, %v, %v)", full, found, err)
	}
	if full.Text != "tool output" || full.ID != derived.ID {
		t.Fatalf("derived-ID lookup = %#v, want the complete retained entry", full)
	}

	if message.ID == "" {
		t.Fatalf("page = %#v, want the conversation message entry", page.items)
	}
	byEvent, found, err := history.TranscriptEntry(message.ID)
	if err != nil || !found || byEvent.Text != "message body" {
		t.Fatalf("event-ID lookup = (%#v, %v, %v)", byEvent, found, err)
	}

	goal, found, err := history.TranscriptEntry("owner-1-goal")
	if err != nil || !found {
		t.Fatalf("goal-row lookup = (%v, %v, %v)", goal, found, err)
	}
	if goal.Seq != 0 || goal.Text != "owner goal" {
		t.Fatalf("goal-row lookup = %#v, want the synthetic seq-0 goal row", goal)
	}
}

func TestOwnerHistoryOversizedPreviewDetailRoundTrip(t *testing.T) {
	store := &fakeOwnerHistoryStore{}
	big := strings.Repeat("z", 600<<10)
	record, err := json.Marshal(map[string]any{
		"type": "tool_result", "tool_name": "bash", "output": big,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.events = []ownerEvent{
		{ID: "evt-big", Seq: 1, Kind: "runtime_output", Payload: map[string]any{"text": string(record)}, CreatedAt: time.Unix(1, 0).UTC()},
	}
	history := newTestOwnerHistory(store)

	page, err := history.TimelinePage(parseHistoryRequestFromCursors(false, 0, false, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.items) != 1 || !page.items[0].Truncated || page.items[0].Detail == "" {
		t.Fatalf("oversized page = %#v, want one bounded preview with a detail reference", page.items)
	}
	full, found, err := history.TimelineItem(page.items[0].ID)
	if err != nil || !found {
		t.Fatalf("oversized detail lookup = (%v, %v, %v)", full, found, err)
	}
	if full.Output != big || full.Truncated {
		t.Fatalf("oversized detail = %d chars truncated=%v, want the complete retained output", len(full.Output), full.Truncated)
	}
}

// parseHistoryRequestFromCursors builds one history request without an
// HTTP request, for module-level tests.
func parseHistoryRequestFromCursors(beforeSet bool, before int, afterSet bool, after int) historyRequest {
	return historyRequest{beforeSet: beforeSet, before: before, afterSet: afterSet, after: after}
}

func TestOwnerHistoryTimelineItemMatchesLegacyNumericSeq(t *testing.T) {
	store := &fakeOwnerHistoryStore{}
	appendOwnerLifecycleEvents(store, 5, 0)
	history := newTestOwnerHistory(store)

	item, found, err := history.TimelineItem("2")
	if err != nil || !found || item.Seq != 2 || item.ID != "evt-000002" {
		t.Fatalf("numeric detail = (%#v, %v, %v), want the seq-2 item", item, found, err)
	}
}

// TestOwnerHistoryTaskSessionParity drives one Task and one Session through
// the same retained Event stream and proves both Runtime Owners emit the same
// history shapes: bounded windows of the same projected rows, owner-local
// cursors, and detail lookups that return the same complete content. The
// Session's initial input Event shifts every source Seq by exactly one.
func TestOwnerHistoryTaskSessionParity(t *testing.T) {
	server, projectID, _ := newHistoryWindowFixture(t)
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectID, Type: task.TypePentest, Goal: "parity goal", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionCreated, err := server.sessions.Create(session.CreateRequest{Input: "session input"})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("p", 600<<10)
	bigRecord, err := json.Marshal(map[string]any{
		"type": "tool_result", "tool_name": "bash", "output": big,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct {
		id          string
		appendEvent func(kind string, payload map[string]any)
	}{
		{created.ID, func(kind string, payload map[string]any) {
			if _, err := server.tasks.AppendEvent(created.ID, task.EventKind(kind), payload); err != nil {
				t.Fatal(err)
			}
		}},
		{sessionCreated.ID, func(kind string, payload map[string]any) {
			if _, err := server.sessions.AppendEvent(sessionCreated.ID, session.EventKind(kind), payload); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		for index := 1; index <= 250; index++ {
			owner.appendEvent("lifecycle", map[string]any{"phase": fmt.Sprintf("phase-%d", index)})
		}
		for index := 1; index <= 50; index++ {
			owner.appendEvent("conversation", map[string]any{"role": "assistant", "text": fmt.Sprintf("Message %d", index)})
		}
		owner.appendEvent("runtime_output", map[string]any{"text": string(bigRecord)})
	}
	taskTimelineBase := "/api/projects/" + projectID + "/tasks/" + created.ID + "/timeline"
	taskTranscriptBase := "/api/projects/" + projectID + "/tasks/" + created.ID + "/transcript"
	sessionTimelineBase := "/api/sessions/" + sessionCreated.ID + "/timeline"
	sessionTranscriptBase := "/api/sessions/" + sessionCreated.ID + "/transcript"

	taskTimeline := getHistoryURL(t, server, taskTimelineBase)
	sessionTimeline := getHistoryURL(t, server, sessionTimelineBase)
	if len(taskTimeline.Items) != historyWindowMaxItems || len(sessionTimeline.Items) != historyWindowMaxItems {
		t.Fatalf("timeline windows = %d/%d items, want both bounded at %d", len(taskTimeline.Items), len(sessionTimeline.Items), historyWindowMaxItems)
	}
	if !taskTimeline.HasOlder || !sessionTimeline.HasOlder {
		t.Fatal("timeline windows must both report has_older")
	}
	if sessionTimeline.Cursor != taskTimeline.Cursor+1 {
		t.Fatalf("timeline cursors = %d/%d, want the session cursor one past the task cursor", taskTimeline.Cursor, sessionTimeline.Cursor)
	}
	for index := range taskTimeline.Items {
		taskItem, sessionItem := taskTimeline.Items[index], sessionTimeline.Items[index]
		if sessionItem.Seq != taskItem.Seq+1 {
			t.Fatalf("timeline item %d seqs = %d/%d, want a constant +1 offset", index, taskItem.Seq, sessionItem.Seq)
		}
		if taskItem.Type != sessionItem.Type || taskItem.Content != sessionItem.Content {
			t.Fatalf("timeline item %d = %#v vs %#v, want identical projections", index, taskItem, sessionItem)
		}
	}

	taskTranscript := getHistoryURL(t, server, taskTranscriptBase)
	sessionTranscript := getHistoryURL(t, server, sessionTranscriptBase)
	if len(taskTranscript.Entries) != historyWindowMaxItems || len(sessionTranscript.Entries) != historyWindowMaxItems {
		t.Fatalf("transcript windows = %d/%d entries, want both bounded at %d", len(taskTranscript.Entries), len(sessionTranscript.Entries), historyWindowMaxItems)
	}
	for index := range taskTranscript.Entries {
		taskEntry, sessionEntry := taskTranscript.Entries[index], sessionTranscript.Entries[index]
		if sessionEntry.Seq != taskEntry.Seq+1 {
			t.Fatalf("transcript entry %d seqs = %d/%d, want a constant +1 offset", index, taskEntry.Seq, sessionEntry.Seq)
		}
		if taskEntry.Kind != sessionEntry.Kind || taskEntry.Role != sessionEntry.Role || taskEntry.Text != sessionEntry.Text {
			t.Fatalf("transcript entry %d = %#v vs %#v, want identical projections", index, taskEntry, sessionEntry)
		}
	}

	taskDetailItem := taskTimeline.Items[len(taskTimeline.Items)-1]
	sessionDetailItem := sessionTimeline.Items[len(sessionTimeline.Items)-1]
	if !taskDetailItem.Truncated || !sessionDetailItem.Truncated {
		t.Fatal("the newest oversized tool result must be a truncated preview for both owners")
	}
	taskFull := getHistoryDetail(t, server, taskDetailItem.Detail)
	sessionFull := getHistoryDetail(t, server, sessionDetailItem.Detail)
	if taskFull["output"] != big || sessionFull["output"] != big {
		t.Fatal("detail lookups must return the same complete retained output for both owners")
	}
}
