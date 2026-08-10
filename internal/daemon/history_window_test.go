package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

// #202: Runtime Owner Timeline and Transcript reads return a bounded recent
// Runtime Owner History Window, older content pages back with an owner-local
// `before` cursor, an oversized item becomes a bounded preview with an explicit
// detail path, and an explicit `after=0` is a live-tail delta that never resends
// synthetic seq-0 content.

// historyWindowBody is the shared Timeline/Transcript response shape.
type historyWindowBody struct {
	TaskID    string             `json:"task_id"`
	SessionID string             `json:"session_id"`
	Items     []timeline.Item    `json:"items"`
	Entries   []transcript.Entry `json:"entries"`
	Cursor    int                `json:"cursor"`
	HasOlder  bool               `json:"has_older"`
}

func newHistoryWindowFixture(t *testing.T) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectRecord, err := server.projects.Create("History", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Type: task.TypePentest, Goal: "history window", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, projectRecord.ID, created.ID
}

func getHistoryURL(t *testing.T, server *Server, url string) historyWindowBody {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, url, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", url, response.Code, response.Body.String())
	}
	var body historyWindowBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET %s: %v", url, err)
	}
	return body
}

func getHistoryDetail(t *testing.T, server *Server, url string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, url, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", url, response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET %s: %v", url, err)
	}
	return body
}

// appendTimelineEvents appends lifecycle events that each project to exactly
// one non-coalesced timeline item (phase is distinct per event, and lifecycle
// items flush streaming turns around them).
func appendTimelineEvents(t *testing.T, server *Server, taskID string, count int) {
	t.Helper()
	for index := 1; index <= count; index++ {
		if _, err := server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": fmt.Sprintf("phase-%d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// appendBigToolResults appends runtime output records with a large tool result
// payload; each projects to one oversized tool_result timeline item.
func appendBigToolResults(t *testing.T, server *Server, taskID string, count, size int) {
	t.Helper()
	for index := 1; index <= count; index++ {
		record, err := json.Marshal(map[string]any{
			"type": "tool_result", "tool_name": "bash",
			"output": fmt.Sprintf("output %d\n%s", index, strings.Repeat("x", size)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.tasks.AppendEvent(taskID, task.EventKindRuntimeOutput, task.EventPayload{
			"text": string(record),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// appendConversationMessages appends conversation events that each project to
// one transcript message entry; they do not appear in the timeline.
func appendConversationMessages(t *testing.T, server *Server, taskID string, count, size int) {
	t.Helper()
	for index := 1; index <= count; index++ {
		text := fmt.Sprintf("Message %d", index)
		if size > 0 {
			text += "\n" + strings.Repeat("x", size)
		}
		if _, err := server.tasks.AppendEvent(taskID, task.EventKindConversation, task.EventPayload{
			"role": "assistant", "text": text,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func serializedSize(t *testing.T, value any) int {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func TestTaskHistoryWindowIsBoundedByItemCount(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	appendTimelineEvents(t, server, taskID, 300)
	timelineBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/timeline"

	timelineWindow := getHistoryURL(t, server, timelineBase)
	if len(timelineWindow.Items) != historyWindowMaxItems {
		t.Fatalf("initial timeline window = %d items, want %d", len(timelineWindow.Items), historyWindowMaxItems)
	}
	if !timelineWindow.HasOlder {
		t.Fatal("initial timeline window has_older=false, want true for 300 items")
	}
	if timelineWindow.Cursor != 300 {
		t.Fatalf("timeline cursor = %d, want full projection max 300", timelineWindow.Cursor)
	}
	// The window is the newest items in chronological order: seqs 101..300.
	if first, last := timelineWindow.Items[0].Seq, timelineWindow.Items[len(timelineWindow.Items)-1].Seq; first != 101 || last != 300 {
		t.Fatalf("timeline window spans seqs %d..%d, want 101..300", first, last)
	}
	for index := 1; index < len(timelineWindow.Items); index++ {
		if timelineWindow.Items[index].Seq <= timelineWindow.Items[index-1].Seq {
			t.Fatalf("timeline window out of order at %d", index)
		}
	}

	transcriptTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectID, Type: task.TypePentest, Goal: "transcript count", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationMessages(t, server, transcriptTask.ID, 300, 0)
	transcriptWindow := getHistoryURL(t, server, "/api/projects/"+projectID+"/tasks/"+transcriptTask.ID+"/transcript")
	if len(transcriptWindow.Entries) != historyWindowMaxItems {
		t.Fatalf("initial transcript window = %d entries, want %d", len(transcriptWindow.Entries), historyWindowMaxItems)
	}
	if !transcriptWindow.HasOlder {
		t.Fatal("initial transcript window has_older=false, want true")
	}
	if transcriptWindow.Cursor != 300 {
		t.Fatalf("transcript cursor = %d, want full projection max 300", transcriptWindow.Cursor)
	}
}

func TestTaskTimelineWindowScansPastInvisibleRecentEventChunk(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	if _, err := server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{"phase": "visible-old-event"}); err != nil {
		t.Fatal(err)
	}
	ignored := `{"type":"system","subtype":"task_progress","description":"internal"}`
	for index := 0; index < historyEventQueryLimit+1; index++ {
		if _, err := server.tasks.AppendEvent(taskID, task.EventKindRuntimeOutput, task.EventPayload{"text": ignored}); err != nil {
			t.Fatal(err)
		}
	}

	window := getHistoryURL(t, server, "/api/projects/"+projectID+"/tasks/"+taskID+"/timeline")
	if len(window.Items) != 1 || !strings.Contains(window.Items[0].Content, "visible-old-event") {
		t.Fatalf("Timeline window = %#v, want the visible item behind an invisible database chunk", window)
	}
	if window.HasOlder {
		t.Fatalf("Timeline window has_older=true after reaching the oldest visible item: %#v", window)
	}
}

func TestTaskTranscriptResumeWindowKeepsContinuationContextAndHidesProviderUserText(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	first, err := server.tasks.CreateContinuation(taskID, "profile-1", "claude_code", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(continuationID string, kind task.EventKind, payload task.EventPayload) {
		t.Helper()
		if _, err := server.tasks.AppendContinuationEvent(taskID, continuationID, kind, payload); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(first.ID, task.EventKindLifecycle, task.EventPayload{"phase": "started", "adapter": "claude_code"})
	appendEvent(first.ID, task.EventKindRuntimeOutput, task.EventPayload{
		"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"First Continuation"}]}}`,
	})
	appendEvent(first.ID, task.EventKindLifecycle, task.EventPayload{"phase": "failed"})
	second, err := server.tasks.CreateReplacementContinuation(first)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(second.ID, task.EventKindLifecycle, task.EventPayload{"phase": "started", "adapter": "claude_code"})
	appendEvent(second.ID, task.EventKindRuntimeOutput, task.EventPayload{
		"text": `{"type":"user","message":{"content":[{"type":"text","text":"internal resumed Skill prompt"}]}}`,
	})
	appendEvent(second.ID, task.EventKindRuntimeOutput, task.EventPayload{
		"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Resumed work"}]}}`,
	})

	base := "/api/projects/" + projectID + "/tasks/" + taskID + "/transcript"
	boundary := getHistoryURL(t, server, base+"?after=3")
	if len(boundary.Entries) != 2 {
		t.Fatalf("resume boundary entries = %#v, want one Continuation row and one assistant message", boundary.Entries)
	}
	if got := boundary.Entries[0]; got.Kind != transcript.KindContinuation || got.Continuation != 2 || got.Text != "Continuation #2 started with claude_code" {
		t.Fatalf("resume boundary = %#v, want absolute Continuation #2", got)
	}
	if got := boundary.Entries[1]; got.Kind != transcript.KindMessage || got.Role != transcript.RoleAssistant || got.Continuation != 2 || got.Text != "Resumed work" {
		t.Fatalf("resumed assistant entry = %#v, want parsed Continuation #2 message", got)
	}

	inside := getHistoryURL(t, server, base+"?after=4")
	if len(inside.Entries) != 1 {
		t.Fatalf("inside-resume entries = %#v, want only the assistant message", inside.Entries)
	}
	if got := inside.Entries[0]; got.Kind != transcript.KindMessage || got.Role != transcript.RoleAssistant || got.Continuation != 2 || got.Text != "Resumed work" {
		t.Fatalf("inside-resume assistant entry = %#v, want parser context from before the page", got)
	}
}

func TestTaskHistoryWindowIsBoundedBySerializedBytes(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	// Ten ~40KiB tool results plus ten ~40KiB conversation entries: the byte
	// bound must cut both windows well before the item-count bound.
	appendBigToolResults(t, server, taskID, 10, 40<<10)
	appendConversationMessages(t, server, taskID, 10, 40<<10)
	timelineBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/timeline"
	transcriptBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/transcript"

	for _, tc := range []struct {
		name string
		url  string
		rows func(historyWindowBody) []any
	}{
		{"timeline", timelineBase, func(body historyWindowBody) []any {
			rows := make([]any, len(body.Items))
			for index := range body.Items {
				rows[index] = body.Items[index]
			}
			return rows
		}},
		{"transcript", transcriptBase, func(body historyWindowBody) []any {
			rows := make([]any, len(body.Entries))
			for index := range body.Entries {
				rows[index] = body.Entries[index]
			}
			return rows
		}},
	} {
		page := getHistoryURL(t, server, tc.url)
		if len(tc.rows(page)) >= 10 {
			t.Fatalf("%s window = %d rows, want byte bound to cut below 10", tc.name, len(tc.rows(page)))
		}
		total := 0
		for _, row := range tc.rows(page) {
			total += serializedSize(t, row)
		}
		if total > historyWindowMaxBytes {
			t.Fatalf("%s window serialized size = %d bytes, want <= %d", tc.name, total, historyWindowMaxBytes)
		}
		if !page.HasOlder {
			t.Fatalf("%s window has_older=false, want true", tc.name)
		}
	}
}

func TestTaskHistoryBackwardPagingHasNoGapsOrDuplicates(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	appendTimelineEvents(t, server, taskID, 250)
	appendConversationMessages(t, server, taskID, 250, 0)
	timelineBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/timeline"
	transcriptBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/transcript"

	for _, tc := range []struct {
		name   string
		url    string
		seqsOf func(historyWindowBody) []int
	}{
		{"timeline", timelineBase, func(body historyWindowBody) []int {
			seqs := make([]int, len(body.Items))
			for index, item := range body.Items {
				seqs[index] = item.Seq
			}
			return seqs
		}},
		{"transcript", transcriptBase, func(body historyWindowBody) []int {
			seqs := make([]int, len(body.Entries))
			for index, entry := range body.Entries {
				seqs[index] = entry.Seq
			}
			return seqs
		}},
	} {
		var seen []int
		page := getHistoryURL(t, server, tc.url)
		if !page.HasOlder {
			t.Fatalf("%s: initial window has_older=false, want older pages", tc.name)
		}
		seen = append(seen, tc.seqsOf(page)...)
		// Walk backward until the boundary: each page is strictly older than
		// the previous one and the final page reaches the cursor-zero boundary.
		before := tc.seqsOf(page)[0]
		for {
			older := getHistoryURL(t, server, tc.url+"?before="+strconv.Itoa(before))
			seqs := tc.seqsOf(older)
			if len(seqs) == 0 {
				t.Fatalf("%s: backward page before=%d returned nothing with has_older=%v", tc.name, before, older.HasOlder)
			}
			// No gaps: the newest seq on this page is exactly before-1.
			if seqs[len(seqs)-1] != before-1 {
				t.Fatalf("%s: page before=%d ends at seq %d, want %d", tc.name, before, seqs[len(seqs)-1], before-1)
			}
			// No duplicates across pages.
			for _, seq := range seqs {
				for _, prior := range seen {
					if prior == seq {
						t.Fatalf("%s: seq %d returned twice across pages", tc.name, seq)
					}
				}
			}
			seen = append(seen, seqs...)
			before = seqs[0]
			if !older.HasOlder {
				break
			}
		}
		sort.Ints(seen)
		// The timeline's first Seq is 1; the transcript carries the synthetic
		// seq-0 goal row plus one continuation row per lifecycle event and one
		// message row per conversation event. Either way the walk must be
		// contiguous without gaps or duplicates.
		want := 250
		if tc.name == "transcript" {
			want = 501
		}
		if len(seen) != want {
			t.Fatalf("%s: page walk returned %d seqs %v, want %d", tc.name, len(seen), seen, want)
		}
		for index := 1; index < len(seen); index++ {
			if seen[index] != seen[index-1]+1 {
				t.Fatalf("%s: page walk seqs %v, want contiguous 0..%d", tc.name, seen, want-1)
			}
		}
	}
}

func TestHistoryAfterZeroIsAnExplicitDelta(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	timelineBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/timeline"
	transcriptBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/transcript"

	// A fresh owner has only the synthetic goal row (Seq 0) in the transcript.
	initial := getHistoryURL(t, server, transcriptBase)
	if len(initial.Entries) != 1 || initial.Entries[0].Seq != 0 || initial.Cursor != 0 {
		t.Fatalf("fresh transcript = %#v, want only the seq-0 goal row with cursor 0", initial)
	}
	// An idle poll after cursor zero sends an explicit after=0 and must not
	// receive the synthetic row again.
	idle := getHistoryURL(t, server, transcriptBase+"?after=0")
	if len(idle.Entries) != 0 || idle.Cursor != 0 {
		t.Fatalf("after=0 transcript = %#v, want no entries and cursor 0", idle)
	}

	emptyTimeline := getHistoryURL(t, server, timelineBase)
	if len(emptyTimeline.Items) != 0 || emptyTimeline.Cursor != 0 {
		t.Fatalf("fresh timeline = %#v, want no items and cursor 0", emptyTimeline)
	}
	emptyDelta := getHistoryURL(t, server, timelineBase+"?after=0")
	if len(emptyDelta.Items) != 0 || emptyDelta.Cursor != 0 {
		t.Fatalf("after=0 timeline = %#v, want no items and cursor 0", emptyDelta)
	}

	// Later events still arrive through after=0.
	appendConversationMessages(t, server, taskID, 2, 0)
	delta := getHistoryURL(t, server, transcriptBase+"?after=0")
	if len(delta.Entries) != 2 || delta.Cursor != 2 {
		t.Fatalf("after=0 transcript after new events = %#v, want 2 entries and cursor 2", delta)
	}
}

func TestHistoryOversizedItemPreviewAndDetail(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	big := strings.Repeat("z", 600<<10)
	record, err := json.Marshal(map[string]any{
		"type": "tool_result", "tool_name": "bash", "output": big,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.AppendEvent(taskID, task.EventKindRuntimeOutput, task.EventPayload{
		"text": string(record),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.AppendEvent(taskID, task.EventKindConversation, task.EventPayload{
		"role": "assistant", "text": big,
	}); err != nil {
		t.Fatal(err)
	}
	timelineBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/timeline"
	transcriptBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/transcript"

	timelinePage := getHistoryURL(t, server, timelineBase)
	if len(timelinePage.Items) != 1 {
		t.Fatalf("timeline window = %d items, want 1 oversized item", len(timelinePage.Items))
	}
	item := timelinePage.Items[0]
	if !item.Truncated {
		t.Fatal("oversized timeline item missing truncation marker")
	}
	if len(item.Output) > historyPreviewChars+64 {
		t.Fatalf("oversized timeline preview = %d chars, want bounded preview", len(item.Output))
	}
	if !strings.Contains(item.Output, "… (truncated)") {
		t.Fatal("oversized timeline preview missing truncation marker in output")
	}
	if item.Detail == "" {
		t.Fatal("oversized timeline item missing detail reference")
	}
	full := getHistoryDetail(t, server, item.Detail)
	if got := full["output"].(string); got != big {
		t.Fatalf("timeline detail output = %d chars, want the complete retained %d chars", len(got), len(big))
	}
	if full["truncated"] == true {
		t.Fatal("timeline detail response still marked truncated")
	}

	transcriptPage := getHistoryURL(t, server, transcriptBase)
	// Goal row, the raw runtime_output fallback entry, and the conversation
	// message: both payload rows are oversized previews in the window.
	if len(transcriptPage.Entries) != 3 {
		t.Fatalf("transcript window = %d entries, want goal row plus two oversized rows", len(transcriptPage.Entries))
	}
	var oversized transcript.Entry
	for _, entry := range transcriptPage.Entries {
		if entry.Seq == 2 {
			oversized = entry
		}
	}
	if oversized.ID == "" || !oversized.Truncated {
		t.Fatalf("oversized transcript entry = %#v, want truncation marker", oversized)
	}
	if len(oversized.Text) > historyPreviewChars+64 {
		t.Fatalf("oversized transcript preview = %d chars, want bounded preview", len(oversized.Text))
	}
	fullEntry := getHistoryDetail(t, server, oversized.Detail)
	if got := fullEntry["text"].(string); got != big {
		t.Fatalf("transcript detail text = %d chars, want the complete retained %d chars", len(got), len(big))
	}
}

func TestHistoryConcurrentLiveUpdatesDoNotLeakAcrossPages(t *testing.T) {
	server, projectID, taskID := newHistoryWindowFixture(t)
	appendTimelineEvents(t, server, taskID, 250)
	timelineBase := "/api/projects/" + projectID + "/tasks/" + taskID + "/timeline"

	window := getHistoryURL(t, server, timelineBase)
	if len(window.Items) != historyWindowMaxItems || !window.HasOlder {
		t.Fatalf("window = %d items has_older=%v, want a full bounded window", len(window.Items), window.HasOlder)
	}
	// Live events arrive while the operator is reading older history.
	appendTimelineEvents(t, server, taskID, 5)

	// A backward page from the oldest window Seq must not include the newer
	// live events, and must have no gaps against the window.
	older := getHistoryURL(t, server, timelineBase+"?before="+strconv.Itoa(window.Items[0].Seq))
	if older.Items[len(older.Items)-1].Seq != window.Items[0].Seq-1 {
		t.Fatalf("backward page ends at %d, want %d", older.Items[len(older.Items)-1].Seq, window.Items[0].Seq-1)
	}
	for _, item := range older.Items {
		if item.Seq > window.Items[0].Seq {
			t.Fatalf("backward page contains live seq %d newer than the page boundary", item.Seq)
		}
	}
	if older.HasOlder {
		t.Fatal("backward page has_older=true, want the boundary at seq 1")
	}

	// The live-tail poll returns exactly the new events, no duplicates.
	delta := getHistoryURL(t, server, timelineBase+"?after="+strconv.Itoa(window.Cursor))
	if len(delta.Items) != 5 || delta.Items[0].Seq != 251 || delta.Items[4].Seq != 255 {
		t.Fatalf("live delta = %#v, want exactly seqs 251..255", delta.Items)
	}

	// The full walk covers every Seq exactly once.
	seen := map[int]bool{}
	for _, page := range [][]timeline.Item{older.Items, window.Items, delta.Items} {
		for _, item := range page {
			if seen[item.Seq] {
				t.Fatalf("seq %d returned twice", item.Seq)
			}
			seen[item.Seq] = true
		}
	}
	for seq := 1; seq <= 255; seq++ {
		if !seen[seq] {
			t.Fatalf("seq %d missing from the paged walk", seq)
		}
	}
}

func TestHistoryCursorsAreOwnerLocal(t *testing.T) {
	server, _, taskID := newHistoryWindowFixture(t)
	appendTimelineEvents(t, server, taskID, 250)

	otherProject, err := server.projects.Create("Other", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: otherProject.ID, Type: task.TypePentest, Goal: "small", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendTimelineEvents(t, server, otherTask.ID, 3)
	appendConversationMessages(t, server, otherTask.ID, 3, 0)

	// The small owner's window must not be suppressed by the large owner's
	// high cursor, and must never contain the large owner's items.
	smallTimeline := getHistoryURL(t, server, "/api/projects/"+otherProject.ID+"/tasks/"+otherTask.ID+"/timeline")
	if len(smallTimeline.Items) != 3 || smallTimeline.HasOlder || smallTimeline.Cursor != 3 {
		t.Fatalf("small owner timeline = %#v, want 3 items, cursor 3, no older pages", smallTimeline)
	}
	for index, item := range smallTimeline.Items {
		if item.Seq != index+1 {
			t.Fatalf("small owner timeline item %d = seq %d, want %d", index, item.Seq, index+1)
		}
	}
	smallTranscript := getHistoryURL(t, server, "/api/projects/"+otherProject.ID+"/tasks/"+otherTask.ID+"/transcript")
	// Goal row + three lifecycle continuation rows + three conversation rows.
	if len(smallTranscript.Entries) != 7 || smallTranscript.HasOlder || smallTranscript.Cursor != 6 {
		t.Fatalf("small owner transcript = %#v, want 7 entries, cursor 6, no older pages", smallTranscript)
	}
	for _, entry := range smallTranscript.Entries {
		if strings.Contains(entry.Text, "Message 250") {
			t.Fatalf("small owner transcript contains large owner content: %#v", entry)
		}
	}
}

func TestSessionHistoryWindowAndPaging(t *testing.T) {
	server, _, _ := newHistoryWindowFixture(t)
	created, err := server.sessions.Create(session.CreateRequest{Input: "History session"})
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 250; index++ {
		if _, err := server.sessions.AppendEvent(created.ID, session.EventKindLifecycle, session.EventPayload{
			"phase": fmt.Sprintf("phase-%d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index <= 250; index++ {
		if _, err := server.sessions.AppendEvent(created.ID, session.EventKindConversation, session.EventPayload{
			"role": "assistant", "text": fmt.Sprintf("Session message %d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	timelineBase := "/api/sessions/" + created.ID + "/timeline"
	transcriptBase := "/api/sessions/" + created.ID + "/transcript"

	timelineWindow := getHistoryURL(t, server, timelineBase)
	if len(timelineWindow.Items) != historyWindowMaxItems || !timelineWindow.HasOlder || timelineWindow.Cursor != 251 {
		t.Fatalf("session timeline window = %#v, want 200 items, has_older, source Event cursor 251", timelineWindow)
	}
	if timelineWindow.Items[0].Seq != 52 || timelineWindow.Items[len(timelineWindow.Items)-1].Seq != 251 {
		t.Fatalf("session timeline window spans source Event seqs %d..%d, want 52..251", timelineWindow.Items[0].Seq, timelineWindow.Items[len(timelineWindow.Items)-1].Seq)
	}

	transcriptWindow := getHistoryURL(t, server, transcriptBase)
	if len(transcriptWindow.Entries) != historyWindowMaxItems || !transcriptWindow.HasOlder {
		t.Fatalf("session transcript window = %d entries has_older=%v, want bounded window", len(transcriptWindow.Entries), transcriptWindow.HasOlder)
	}
	if transcriptWindow.Entries[0].Seq != 302 || transcriptWindow.Entries[len(transcriptWindow.Entries)-1].Seq != 501 {
		t.Fatalf("session transcript window spans %d..%d, want 302..501", transcriptWindow.Entries[0].Seq, transcriptWindow.Entries[len(transcriptWindow.Entries)-1].Seq)
	}

	older := getHistoryURL(t, server, transcriptBase+"?before=251")
	if len(older.Entries) != historyWindowMaxItems || !older.HasOlder {
		t.Fatalf("session transcript backward page = %d entries has_older=%v, want a bounded page with more history", len(older.Entries), older.HasOlder)
	}
	if older.Entries[0].Seq != 51 || older.Entries[len(older.Entries)-1].Seq != 250 {
		t.Fatalf("session transcript backward page spans %d..%d, want 51..250", older.Entries[0].Seq, older.Entries[len(older.Entries)-1].Seq)
	}

	boundary := getHistoryURL(t, server, transcriptBase+"?before=51")
	if len(boundary.Entries) != 50 || boundary.HasOlder {
		t.Fatalf("session transcript boundary page = %d entries has_older=%v, want 50 and the cursor-zero boundary", len(boundary.Entries), boundary.HasOlder)
	}
	if boundary.Entries[0].Seq != 1 || boundary.Entries[49].Seq != 50 {
		t.Fatalf("session transcript boundary page spans %d..%d, want 1..50", boundary.Entries[0].Seq, boundary.Entries[49].Seq)
	}

	// Session owners follow the same cursor-zero contract. The Session input
	// is itself a conversation event, so the full projection is 501 rows.
	delta := getHistoryURL(t, server, transcriptBase+"?after=0")
	if len(delta.Entries) != 501 || delta.Cursor != 501 {
		t.Fatalf("session after=0 transcript = %d entries cursor %d, want all 501", len(delta.Entries), delta.Cursor)
	}
}

func TestSessionTranscriptResumeWindowKeepsParserContext(t *testing.T) {
	server, _, _ := newHistoryWindowFixture(t)
	created, err := server.sessions.Create(session.CreateRequest{Input: "Resume session"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := server.sessions.CreateContinuation(created.ID, "profile-1", "claude_code", session.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(continuationID string, kind session.EventKind, payload session.EventPayload) {
		t.Helper()
		payload["continuation_id"] = continuationID
		if _, err := server.sessions.AppendEvent(created.ID, kind, payload); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(first.ID, session.EventKindLifecycle, session.EventPayload{"phase": "started", "adapter": "claude_code"})
	appendEvent(first.ID, session.EventKindRuntimeOutput, session.EventPayload{
		"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"First Session Continuation"}]}}`,
	})
	appendEvent(first.ID, session.EventKindLifecycle, session.EventPayload{"phase": "failed"})
	second, err := server.sessions.CreateReplacementContinuation(first, nil)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(second.ID, session.EventKindLifecycle, session.EventPayload{"phase": "started", "adapter": "claude_code"})
	appendEvent(second.ID, session.EventKindRuntimeOutput, session.EventPayload{
		"text": `{"type":"user","message":{"content":[{"type":"text","text":"internal Session prompt"}]}}`,
	})
	appendEvent(second.ID, session.EventKindRuntimeOutput, session.EventPayload{
		"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Resumed Session work"}]}}`,
	})

	base := "/api/sessions/" + created.ID + "/transcript"
	boundary := getHistoryURL(t, server, base+"?after=4")
	if len(boundary.Entries) != 2 {
		t.Fatalf("Session resume boundary entries = %#v, want one Continuation row and one assistant message", boundary.Entries)
	}
	if got := boundary.Entries[0]; got.Kind != transcript.KindContinuation || got.Continuation != 2 || got.Text != "Continuation #2 started with claude_code" {
		t.Fatalf("Session resume boundary = %#v, want absolute Continuation #2", got)
	}
	if got := boundary.Entries[1]; got.Kind != transcript.KindMessage || got.Role != transcript.RoleAssistant || got.Continuation != 2 || got.Text != "Resumed Session work" {
		t.Fatalf("resumed Session assistant entry = %#v, want parsed Continuation #2 message", got)
	}

	inside := getHistoryURL(t, server, base+"?after=5")
	if len(inside.Entries) != 1 {
		t.Fatalf("inside Session resume entries = %#v, want only the assistant message", inside.Entries)
	}
	if got := inside.Entries[0]; got.Kind != transcript.KindMessage || got.Role != transcript.RoleAssistant || got.Continuation != 2 || got.Text != "Resumed Session work" {
		t.Fatalf("inside Session resume assistant entry = %#v, want parser context from before the page", got)
	}
}

func TestSessionHistoryOversizedItemPreviewAndDetail(t *testing.T) {
	server, _, _ := newHistoryWindowFixture(t)
	created, err := server.sessions.Create(session.CreateRequest{Input: "Big session"})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("y", 600<<10)
	if _, err := server.sessions.AppendEvent(created.ID, session.EventKindConversation, session.EventPayload{
		"role": "assistant", "text": big,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.sessions.AppendEvent(created.ID, session.EventKindRuntimeOutput, session.EventPayload{
		"text": big,
	}); err != nil {
		t.Fatal(err)
	}
	transcriptBase := "/api/sessions/" + created.ID + "/transcript"
	timelineBase := "/api/sessions/" + created.ID + "/timeline"

	transcriptPage := getHistoryURL(t, server, transcriptBase)
	// The Session's initial input is itself a conversation event (Seq 1), so
	// the oversized rows carry the big text; pick any truncated preview.
	var preview transcript.Entry
	for _, entry := range transcriptPage.Entries {
		if entry.Truncated && entry.Detail != "" {
			preview = entry
		}
	}
	if preview.ID == "" {
		t.Fatalf("session oversized transcript entries = %#v, want a truncated preview", transcriptPage.Entries)
	}
	if full := getHistoryDetail(t, server, preview.Detail); full["text"] != big {
		t.Fatal("session transcript detail did not return the complete retained text")
	}

	timelinePage := getHistoryURL(t, server, timelineBase)
	if len(timelinePage.Items) != 1 || !timelinePage.Items[0].Truncated {
		t.Fatalf("session oversized timeline = %#v, want one truncated preview", timelinePage.Items)
	}
	if full := getHistoryDetail(t, server, timelinePage.Items[0].Detail); full["content"] != big {
		t.Fatal("session timeline detail did not return the complete retained text")
	}
}
