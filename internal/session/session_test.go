package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/store"
)

type recordingSessionTerminalMarker struct{ ids []string }

func (marker *recordingSessionTerminalMarker) MarkContinuationTerminal(_ context.Context, id string) error {
	marker.ids = append(marker.ids, id)
	return nil
}

func TestCreateSessionDerivesTitleAndOwnsInitialConversation(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	input := "\n  Review the exposed admin route  \nwith a second line"
	created, err := service.Create(CreateRequest{Input: input})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if !strings.HasPrefix(created.ID, "session-") {
		t.Fatalf("session id = %q, want an explicit Session identity", created.ID)
	}
	if created.Title != "Review the exposed admin route" {
		t.Fatalf("session title = %q", created.Title)
	}
	if created.Lifecycle != LifecycleOpen {
		t.Fatalf("session lifecycle = %q, want %q", created.Lifecycle, LifecycleOpen)
	}
	wantWorkdir := filepath.Join(dataRoot, "sessions", created.ID)
	if created.Workdir != wantWorkdir {
		t.Fatalf("session workdir = %q, want %q", created.Workdir, wantWorkdir)
	}
	info, err := os.Stat(wantWorkdir)
	if err != nil {
		t.Fatalf("stat session workdir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("session workdir is not a directory")
	}

	events, err := service.Events(created.ID)
	if err != nil {
		t.Fatalf("list session events: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].Kind != EventKindConversation {
		t.Fatalf("initial session events = %#v", events)
	}
	var payload struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	encoded, err := json.Marshal(events[0].Payload)
	if err != nil {
		t.Fatalf("encode initial event: %v", err)
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode initial event: %v", err)
	}
	if payload.Role != "user" || payload.Text != input {
		t.Fatalf("initial conversation payload = %#v", payload)
	}
}

func TestCreateSessionPersistsAssistedBlackboardConclusionRunControl(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	created, err := service.Create(CreateRequest{
		Input:                    "Inspect the standalone target",
		BlackboardConclusionMode: BlackboardConclusionModeAssisted,
	})
	if err != nil {
		t.Fatalf("create assisted Session: %v", err)
	}
	if created.RunControls.BlackboardConclusionMode != BlackboardConclusionModeAssisted {
		t.Fatalf("created run controls = %#v", created.RunControls)
	}

	reloaded, err := service.Get(created.ID)
	if err != nil {
		t.Fatalf("reload assisted Session: %v", err)
	}
	if reloaded.RunControls.BlackboardConclusionMode != BlackboardConclusionModeAssisted {
		t.Fatalf("reloaded run controls = %#v", reloaded.RunControls)
	}
}

func TestListSessionsAppliesAnOptionalActivityLimit(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	for _, input := range []string{"one", "two", "three"} {
		if _, err := service.Create(CreateRequest{Input: input}); err != nil {
			t.Fatalf("create %s: %v", input, err)
		}
	}

	limited, err := service.ListLimited(LifecycleOpen, 2)
	if err != nil {
		t.Fatalf("list limited Sessions: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited Sessions = %d, want 2", len(limited))
	}
	if _, err := service.ListLimited(LifecycleOpen, -1); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("negative limit error = %v, want ErrInvalidLimit", err)
	}
}

func TestCreateSessionSafelyCopiesAttachmentsAndCleansPartialWrites(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))

	created, err := service.Create(CreateRequest{
		Input: "Review these files",
		Attachments: []Attachment{
			inMemoryAttachment("../notes.txt", "first"),
			inMemoryAttachment("notes.txt", "second"),
			inMemoryAttachment("NOTES.TXT", "third"),
		},
	})
	if err != nil {
		t.Fatalf("create Session with attachments: %v", err)
	}
	assertFileContents(t, filepath.Join(created.Workdir, "notes.txt"), "first")
	assertFileContents(t, filepath.Join(created.Workdir, "notes-1.txt"), "second")
	assertFileContents(t, filepath.Join(created.Workdir, "NOTES-2.TXT"), "third")
	events, err := service.Events(created.ID)
	if err != nil {
		t.Fatalf("list attachment Events: %v", err)
	}
	if len(events) != 4 || events[1].Kind != EventKindAttachment || events[2].Kind != EventKindAttachment || events[3].Kind != EventKindAttachment {
		t.Fatalf("attachment Events = %#v", events)
	}
	for _, event := range events[1:] {
		if _, ok := event.Payload["relative_path"].(string); !ok {
			t.Fatalf("attachment Event has no relative path: %#v", event.Payload)
		}
	}

	_, err = service.Create(CreateRequest{
		Input: "This must not leave a partial Workdir",
		Attachments: []Attachment{
			inMemoryAttachment("ok.txt", "persisted only on success"),
			{Name: "broken.txt", Size: 1, Open: func() (io.ReadCloser, error) {
				return nil, errors.New("source unavailable")
			}},
		},
	})
	if err == nil {
		t.Fatal("expected failed attachment source")
	}
	open, err := service.List(LifecycleOpen)
	if err != nil {
		t.Fatalf("list Sessions after failed create: %v", err)
	}
	if len(open) != 1 || open[0].ID != created.ID {
		t.Fatalf("failed create left a durable Session: %#v", open)
	}
	entries, err := os.ReadDir(filepath.Join(dataRoot, "sessions"))
	if err != nil {
		t.Fatalf("read Session root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != created.ID {
		t.Fatalf("failed create left Workdir entries: %#v", entries)
	}
}

func inMemoryAttachment(name, contents string) Attachment {
	return Attachment{
		Name: name,
		Size: int64(len(contents)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(contents))), nil
		},
	}
}

func assertFileContents(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func TestSessionLifecyclePreservesIdentityAndDeletesOnlyAfterArchive(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))

	created, err := service.Create(CreateRequest{Input: "Keep this conversation"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	if err := service.Delete(created.ID); !errors.Is(err, ErrOpenSession) {
		t.Fatalf("delete open Session error = %v, want %v", err, ErrOpenSession)
	}

	rename, err := service.Rename(created.ID, "Renamed conversation")
	if err != nil {
		t.Fatalf("rename Session: %v", err)
	}
	if rename.ID != created.ID || rename.Title != "Renamed conversation" {
		t.Fatalf("renamed Session = %#v", rename)
	}
	archived, err := service.Archive(created.ID)
	if err != nil {
		t.Fatalf("archive Session: %v", err)
	}
	if archived.ID != created.ID || archived.Lifecycle != LifecycleArchived {
		t.Fatalf("archived Session = %#v", archived)
	}
	if _, err := service.Archive(created.ID); !errors.Is(err, ErrAlreadyArchived) {
		t.Fatalf("archive archived Session error = %v, want %v", err, ErrAlreadyArchived)
	}
	restored, err := service.Restore(created.ID)
	if err != nil {
		t.Fatalf("restore Session: %v", err)
	}
	if restored.ID != created.ID || restored.Title != "Renamed conversation" || restored.Lifecycle != LifecycleOpen {
		t.Fatalf("restored Session = %#v", restored)
	}
	events, err := service.Events(created.ID)
	if err != nil {
		t.Fatalf("read retained Session Events: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("retained Session Events = %d, want conversation + rename + archive + restore", len(events))
	}
	if _, err := service.Archive(created.ID); err != nil {
		t.Fatalf("archive before permanent delete: %v", err)
	}
	if err := service.Delete(created.ID); err != nil {
		t.Fatalf("delete archived Session: %v", err)
	}
	if _, err := service.Get(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted Session error = %v, want %v", err, ErrNotFound)
	}
	if _, err := os.Stat(created.Workdir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted Session Workdir stat error = %v, want not-exist", err)
	}
}

func TestCommittedSessionDeletionLeavesRetryableWorkdirCleanup(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	created, err := service.Create(CreateRequest{Input: "delete me"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Archive(created.ID); err != nil {
		t.Fatal(err)
	}
	cleanupFailure := errors.New("filesystem busy")
	service.removeAll = func(string) error { return cleanupFailure }
	if err := service.Delete(created.ID); err != nil {
		t.Fatalf("committed logical deletion reported failure: %v", err)
	}
	if _, err := service.Get(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted Session still exists: %v", err)
	}
	quarantine := filepath.Join(dataRoot, "sessions", ".deleting-"+created.ID)
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("retry marker missing: %v", err)
	}
	service.removeAll = os.RemoveAll
	if err := service.CleanupDeletedWorkdirs(); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if _, err := os.Stat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry marker remains: %v", err)
	}
}

func TestCleanupRestoresAQuarantinedWorkdirWhenDeletionDidNotCommit(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	created, err := service.Create(CreateRequest{Input: "survive restart"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Archive(created.ID); err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(dataRoot, "sessions", ".deleting-"+created.ID)
	if err := os.Rename(created.Workdir, quarantine); err != nil {
		t.Fatal(err)
	}
	if err := service.CleanupDeletedWorkdirs(); err != nil {
		t.Fatalf("startup recovery: %v", err)
	}
	if _, err := service.Get(created.ID); err != nil {
		t.Fatalf("archived Session lost: %v", err)
	}
	if _, err := os.Stat(created.Workdir); err != nil {
		t.Fatalf("archived Workdir was not restored: %v", err)
	}
}

func TestTerminalSessionContinuationClosesItsCapability(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	marker := &recordingSessionTerminalMarker{}
	service.SetContinuationTerminalMarker(marker)
	created, err := service.Create(CreateRequest{Input: "finish"})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := service.CreateContinuation(created.ID, "profile-1", "fake", RunnerSandbox, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateContinuationStatus(continuation.ID, RuntimeStatusCompleted); err != nil {
		t.Fatal(err)
	}
	if len(marker.ids) != 1 || marker.ids[0] != continuation.ID {
		t.Fatalf("terminal capability notifications = %#v", marker.ids)
	}
}

func TestSessionContinuationsRetainRuntimeSelectionAndSeparateConversationFromTimeline(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	created, err := service.Create(CreateRequest{Input: "Inspect the attached application"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	config, err := service.RecordRuntimeConfig(created.ID, "profile-codex", map[string]any{
		"provider": "codex", "model": "gpt-5", "reasoning_effort": "high",
	})
	if err != nil {
		t.Fatalf("record runtime config: %v", err)
	}
	continuation, err := service.CreateContinuation(created.ID, config.RuntimeProfileID, "codex", RunnerSandbox, config.Config)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if continuation.Number != 1 || continuation.Status != RuntimeStatusPending {
		t.Fatalf("continuation = %#v", continuation)
	}
	if _, err := service.UpdateContinuationStatus(continuation.ID, RuntimeStatusRunning); err != nil {
		t.Fatalf("mark continuation running: %v", err)
	}
	if _, err := service.AppendConversationEvent(created.ID, continuation.ID, "assistant", "I will inspect the application now."); err != nil {
		t.Fatalf("append runtime conversation: %v", err)
	}
	if _, err := service.AppendEvent(created.ID, EventKindRuntimeOutput, EventPayload{"stream": "stdout", "text": "bounded output"}); err != nil {
		t.Fatalf("append runtime timeline event: %v", err)
	}
	if _, err := service.UpdateContinuationStatus(continuation.ID, RuntimeStatusCompleted); err != nil {
		t.Fatalf("complete continuation: %v", err)
	}

	conversation, err := service.Conversation(created.ID)
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if len(conversation) != 2 || conversation[0].Kind != EventKindConversation || conversation[1].Payload["role"] != "assistant" {
		t.Fatalf("conversation = %#v", conversation)
	}
	timeline, err := service.Timeline(created.ID)
	if err != nil {
		t.Fatalf("load timeline: %v", err)
	}
	for _, event := range timeline {
		if event.Kind == EventKindConversation {
			t.Fatalf("timeline duplicated conversation event: %#v", event)
		}
	}
	versions, err := service.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatalf("load runtime config history: %v", err)
	}
	if len(versions) != 1 || versions[0].Config["reasoning_effort"] != "high" {
		t.Fatalf("runtime config versions = %#v", versions)
	}
}

func TestSessionHistoryEventWindowReadsOnlyTheRequestedProjectionWindow(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := store.Open(filepath.Join(dataRoot, "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	service := NewService(db, filepath.Join(dataRoot, "sessions"))
	created, err := service.Create(CreateRequest{Input: "history"})
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 5; index++ {
		if _, err := service.AppendEvent(created.ID, EventKindLifecycle, EventPayload{"phase": index}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.AppendEvent(created.ID, EventKindConversation, EventPayload{"role": "assistant", "text": "irrelevant"}); err != nil {
		t.Fatal(err)
	}

	window, err := service.HistoryEventWindow(created.ID, EventWindowQuery{
		Projection: EventProjectionTimeline,
		Limit:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Events) != 3 || window.Events[0].Seq != 4 || window.Events[2].Seq != 6 {
		t.Fatalf("initial Session Timeline Event window = %#v, want seqs 4..6", window.Events)
	}
	if window.Cursor != 6 || !window.HasOlder {
		t.Fatalf("initial Session Timeline Event window cursor=%d has_older=%t, want 6/true", window.Cursor, window.HasOlder)
	}
}

func TestSessionOwnerContractRemainsProjectFree(t *testing.T) {
	contract := (Session{ID: "session-1", Workdir: "/tmp/session-1"}).OwnerContract()
	if err := contract.Validate(); err != nil {
		t.Fatalf("validate Session owner contract: %v", err)
	}
	if contract.ProjectID != "" || contract.TaskID != "" || !contract.IsSession() || contract.Capabilities.ProjectScope || contract.Capabilities.ProjectArtifacts {
		t.Fatalf("Session owner contract leaked Project capabilities: %#v", contract)
	}
}
