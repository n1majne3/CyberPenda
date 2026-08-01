package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/store"
)

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
