package daemon_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/daemon"
)

func TestSessionHTTPLifecycleAndOwnerIsolation(t *testing.T) {
	runtimeRoot := t.TempDir()
	sessionRoot := filepath.Join(t.TempDir(), "managed-sessions")
	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot:          runtimeRoot,
		SessionRoot:          sessionRoot,
		DisableBuiltinSkills: true,
	})

	created := postSession(t, server, `{"input":"\n  Check the admin panel\nwith more context"}`)
	if created.Title != "Check the admin panel" || created.Lifecycle != "open" {
		t.Fatalf("created Session = %#v", created)
	}
	if created.ID == "" {
		t.Fatal("expected Session id")
	}
	workdir := filepath.Join(sessionRoot, created.ID)
	if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		t.Fatalf("Session Workdir stat = %v, info=%v", err, info)
	}

	list := getSessions(t, server, "/api/sessions")
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("open Sessions = %#v", list)
	}
	if archived := getSessions(t, server, "/api/sessions?lifecycle=archived"); len(archived) != 0 {
		t.Fatalf("archived Sessions before archive = %#v", archived)
	}

	patch := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/sessions/"+created.ID, bytes.NewBufferString(`{"title":"Admin review"}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(patch, request)
	if patch.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body=%s", patch.Code, patch.Body.String())
	}
	var renamed sessionJSON
	decodeJSON(t, patch, &renamed)
	if renamed.ID != created.ID || renamed.Title != "Admin review" {
		t.Fatalf("renamed Session = %#v", renamed)
	}

	postSessionAction(t, server, created.ID, "archive", http.StatusOK)
	if open := getSessions(t, server, "/api/sessions"); len(open) != 0 {
		t.Fatalf("open Sessions after archive = %#v", open)
	}
	archived := getSessions(t, server, "/api/sessions/archived")
	if len(archived) != 1 || archived[0].ID != created.ID || archived[0].Title != "Admin review" {
		t.Fatalf("archived Sessions = %#v", archived)
	}

	deleteOpen := httptest.NewRecorder()
	server.ServeHTTP(deleteOpen, httptest.NewRequest(http.MethodDelete, "/api/sessions/"+created.ID, nil))
	if deleteOpen.Code != http.StatusNoContent {
		t.Fatalf("delete archived Session status = %d, body=%s", deleteOpen.Code, deleteOpen.Body.String())
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("deleted Session Workdir still exists, stat error=%v", err)
	}
	getDeleted := httptest.NewRecorder()
	server.ServeHTTP(getDeleted, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID, nil))
	if getDeleted.Code != http.StatusNotFound {
		t.Fatalf("deleted Session GET status = %d, body=%s", getDeleted.Code, getDeleted.Body.String())
	}

	// Session identity is not a Project child. Project-shaped lookups must not
	// turn a Session into a Task or disclose a matching owner.
	foreign := httptest.NewRecorder()
	server.ServeHTTP(foreign, httptest.NewRequest(http.MethodGet, "/api/projects/project-a/sessions/"+created.ID, nil))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("Project/Session mismatch status = %d, body=%s", foreign.Code, foreign.Body.String())
	}
}

func TestCreateSessionMultipartCopiesSafeAttachmentReferencesToEvents(t *testing.T) {
	sessionRoot := filepath.Join(t.TempDir(), "managed-sessions")
	server := newDaemonWithConfig(t, daemon.Config{
		Version: "test-version", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		SessionRoot: sessionRoot, DisableBuiltinSkills: true,
	})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("payload", `{"input":"Review the attached files"}`); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	for _, name := range []string{"../notes.txt", "notes.txt"} {
		part, err := writer.CreateFormFile("attachments", name)
		if err != nil {
			t.Fatalf("create attachment part: %v", err)
		}
		if _, err := part.Write([]byte(name)); err != nil {
			t.Fatalf("write attachment part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("multipart create status = %d, body=%s", response.Code, response.Body.String())
	}
	var created sessionJSON
	decodeJSON(t, response, &created)
	workdir := filepath.Join(sessionRoot, created.ID)
	assertSessionFile(t, filepath.Join(workdir, "notes.txt"), "../notes.txt")
	assertSessionFile(t, filepath.Join(workdir, "notes-1.txt"), "notes.txt")

	events := getSessionEvents(t, server, created.ID)
	if len(events) != 3 || events[1].Kind != "attachment" || events[2].Kind != "attachment" {
		t.Fatalf("Session Events = %#v", events)
	}
	for _, event := range events[1:] {
		if event.Payload["relative_path"] == nil || event.Payload["sha256"] == nil {
			t.Fatalf("attachment Event lacks safe metadata: %#v", event.Payload)
		}
	}
}

func TestSessionHTTPRequiresTheDaemonAuthenticationBoundary(t *testing.T) {
	server := newDaemonWithConfig(t, daemon.Config{
		Version: "test-version", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		SessionRoot: filepath.Join(t.TempDir(), "managed-sessions"),
		ListenAddr:  "127.0.0.1:8787", AuthToken: "operator-token", DisableBuiltinSkills: true,
	})

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized Session list status = %d, body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	request.Header.Set("Authorization", "Bearer operator-token")
	server.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized Session list status = %d, body=%s", authorized.Code, authorized.Body.String())
	}
}

type sessionJSON struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Lifecycle      string `json:"lifecycle"`
	LastActivityAt string `json:"last_activity_at"`
}

type sessionEventJSON struct {
	Seq     int            `json:"seq"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

func postSession(t *testing.T, server *daemon.Server, body string) sessionJSON {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", response.Code, response.Body.String())
	}
	var result sessionJSON
	decodeJSON(t, response, &result)
	return result
}

func getSessions(t *testing.T, server *daemon.Server, route string) []sessionJSON {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list Sessions status = %d, body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Sessions []sessionJSON `json:"sessions"`
	}
	decodeJSON(t, response, &result)
	return result.Sessions
}

func getSessionEvents(t *testing.T, server *daemon.Server, id string) []sessionEventJSON {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/events", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list Session Events status = %d, body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Events []sessionEventJSON `json:"events"`
	}
	decodeJSON(t, response, &result)
	return result.Events
}

func postSessionAction(t *testing.T, server *daemon.Server, id, action string, wantStatus int) {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/"+action, nil))
	if response.Code != wantStatus {
		t.Fatalf("Session %s status = %d, body=%s", action, response.Code, response.Body.String())
	}
}

func decodeJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
}

func assertSessionFile(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read Session attachment %s: %v", name, err)
	}
	if string(got) != want {
		t.Errorf("Session attachment %s = %q, want %q", name, got, want)
	}
}
