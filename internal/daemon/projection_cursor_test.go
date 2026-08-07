package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"pentest/internal/project"
	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

// #191: Runtime Owner Workspace projections load incrementally after an
// ordered cursor instead of re-downloading the complete Timeline and
// Transcript on every poll.

type projectionCursorBody struct {
	TaskID    string            `json:"task_id"`
	SessionID string            `json:"session_id"`
	Items     []timeline.Item   `json:"items"`
	Entries   []transcript.Entry `json:"entries"`
	Cursor    int               `json:"cursor"`
}

func newProjectionCursorFixture(t *testing.T) (*Server, string, string) {
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
	projectRecord, err := server.projects.Create("Cursor", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "cursor timeline", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, projectRecord.ID, created.ID
}

func appendProjectionEvents(t *testing.T, server *Server, taskID string) {
	t.Helper()
	for _, phase := range []string{"started", "completed"} {
		if _, err := server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": phase, "adapter": "codex",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := server.tasks.AppendEvent(taskID, task.EventKindConversation, task.EventPayload{
		"role": "user", "text": "Inspect the cursor boundary.",
	}); err != nil {
		t.Fatal(err)
	}
}

func getProjection(t *testing.T, server *Server, path string, after int) projectionCursorBody {
	t.Helper()
	url := path
	if after > 0 {
		url += "?after=" + strconv.Itoa(after)
	}
	request := httptest.NewRequest(http.MethodGet, url, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", url, response.Code, response.Body.String())
	}
	var body projectionCursorBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET %s: %v", url, err)
	}
	return body
}

func TestTaskTimelineCursorReturnsOnlyNewItems(t *testing.T) {
	server, projectID, taskID := newProjectionCursorFixture(t)
	appendProjectionEvents(t, server, taskID)
	base := "/api/projects/" + projectID + "/tasks/" + taskID + "/timeline"

	first := getProjection(t, server, base, 0)
	if len(first.Items) == 0 || first.Cursor == 0 {
		t.Fatalf("initial timeline = %#v, want items and a cursor", first)
	}
	appendProjectionEvents(t, server, taskID)

	second := getProjection(t, server, base, first.Cursor)
	if len(second.Items) == 0 || second.Cursor <= first.Cursor {
		t.Fatalf("incremental timeline = %#v, want items after cursor %d", second, first.Cursor)
	}
	for _, item := range second.Items {
		if item.Seq <= first.Cursor {
			t.Fatalf("incremental timeline resent item seq %d at or before cursor %d", item.Seq, first.Cursor)
		}
	}

	empty := getProjection(t, server, base, second.Cursor)
	if len(empty.Items) != 0 || empty.Cursor != second.Cursor {
		t.Fatalf("empty timeline poll = %#v, want no items and stable cursor", empty)
	}
}

func TestTaskTranscriptCursorReturnsOnlyNewEntries(t *testing.T) {
	server, projectID, taskID := newProjectionCursorFixture(t)
	appendProjectionEvents(t, server, taskID)
	base := "/api/projects/" + projectID + "/tasks/" + taskID + "/transcript"

	first := getProjection(t, server, base, 0)
	if len(first.Entries) == 0 || first.Cursor == 0 {
		t.Fatalf("initial transcript = %#v, want entries and a cursor", first)
	}
	// The complete first read includes the synthetic goal row (Seq 0).
	if first.Entries[0].Seq != 0 || first.Entries[0].Role != "user" || first.Entries[0].Text != "cursor timeline" {
		t.Fatalf("initial transcript = %#v, want leading goal row", first.Entries)
	}
	appendProjectionEvents(t, server, taskID)

	second := getProjection(t, server, base, first.Cursor)
	if len(second.Entries) == 0 || second.Cursor <= first.Cursor {
		t.Fatalf("incremental transcript = %#v, want entries after cursor %d", second, first.Cursor)
	}
	for _, entry := range second.Entries {
		if entry.Seq <= first.Cursor {
			t.Fatalf("incremental transcript resent entry seq %d at or before cursor %d", entry.Seq, first.Cursor)
		}
	}

	empty := getProjection(t, server, base, second.Cursor)
	if len(empty.Entries) != 0 || empty.Cursor != second.Cursor {
		t.Fatalf("empty transcript poll = %#v, want no entries and stable cursor", empty)
	}
}

func TestSessionTimelineAndTranscriptShareTheCursorContract(t *testing.T) {
	server, _, _ := newProjectionCursorFixture(t)
	created, err := server.sessions.Create(session.CreateRequest{Input: "Cursor session"})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"started", "completed"} {
		if _, err := server.sessions.AppendEvent(created.ID, session.EventKindLifecycle, session.EventPayload{
			"phase": phase, "adapter": "codex",
		}); err != nil {
			t.Fatal(err)
		}
	}
	timelineBase := "/api/sessions/" + created.ID + "/timeline"
	first := getProjection(t, server, timelineBase, 0)
	if len(first.Items) == 0 || first.Cursor == 0 {
		t.Fatalf("initial Session timeline = %#v", first)
	}
	empty := getProjection(t, server, timelineBase, first.Cursor)
	if len(empty.Items) != 0 || empty.Cursor != first.Cursor {
		t.Fatalf("empty Session timeline poll = %#v", empty)
	}

	transcriptBase := "/api/sessions/" + created.ID + "/transcript"
	if _, err := server.sessions.AppendEvent(created.ID, session.EventKindConversation, session.EventPayload{
		"role": "user", "text": "Session cursor boundary.",
	}); err != nil {
		t.Fatal(err)
	}
	delta := getProjection(t, server, transcriptBase, 0)
	if len(delta.Entries) == 0 || delta.Cursor == 0 {
		t.Fatalf("Session transcript = %#v, want entries and a cursor", delta)
	}
	emptyTranscript := getProjection(t, server, transcriptBase, delta.Cursor)
	if len(emptyTranscript.Entries) != 0 || emptyTranscript.Cursor != delta.Cursor {
		t.Fatalf("empty Session transcript poll = %#v", emptyTranscript)
	}
}

func TestProjectionCursorSurvivesDaemonRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "pentest.db")
	server, err := NewServer(Config{DBPath: dbPath, RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	projectRecord, err := server.projects.Create("Restart cursor", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{ProjectID: projectRecord.ID, Goal: "restart cursor", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	appendProjectionEvents(t, server, created.ID)
	base := "/api/projects/" + projectRecord.ID + "/tasks/" + created.ID + "/timeline"
	first := getProjection(t, server, base, 0)
	appendProjectionEvents(t, server, created.ID)
	before := getProjection(t, server, base, first.Cursor)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewServer(Config{DBPath: dbPath, RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after := getProjection(t, reopened, base, first.Cursor)
	if after.Cursor < before.Cursor || len(after.Items) < len(before.Items) {
		t.Fatalf("cursor delta across restart = %#v, want at least %#v", after, before)
	}
	// Reopen may add terminal lifecycle events (for example the interrupted
	// orphan marker), so the restart delta must be an ordered superset of the
	// pre-restart delta with identical leading items.
	for index := range before.Items {
		if after.Items[index].Seq != before.Items[index].Seq || after.Items[index].Type != before.Items[index].Type || after.Items[index].Content != before.Items[index].Content {
			t.Fatalf("restart delta item %d = %#v, want %#v", index, after.Items[index], before.Items[index])
		}
	}
}
