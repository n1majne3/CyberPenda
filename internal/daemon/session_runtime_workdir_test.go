package daemon

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/testsupport/fakeprovider"
)

func TestCreateSandboxSessionMountsExternalManagedWorkdirThroughPublicHTTP(t *testing.T) {
	volumeRoot := filepath.Join(t.TempDir(), "data")
	sessionRoot := filepath.Join(t.TempDir(), "external-sessions")
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID:    "session-external-workdir",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot: filepath.Join(volumeRoot, "runs"), SessionRoot: sessionRoot,
		TaskVolume: "cyberpenda-data", TaskVolumeRoot: volumeRoot,
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "sandbox", BinaryPath: "codex", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session", SandboxImage: "sandbox:test",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	request := multipartSessionRequest(t, http.MethodPost, "/api/sessions",
		`{"input":"inspect attachment","runtime_profile_id":"`+profile.ID+`","runner":"sandbox"}`,
		"sample.bin", "payload")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create sandbox Session status = %d, body=%s", response.Code, response.Body.String())
	}
	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider launch requests = %d, want 1", len(requests))
	}
	args := requests[0].Facts.SandboxCreateArgs
	if len(args) == 0 {
		t.Fatalf("Session sandbox create args are empty")
	}
	joined := strings.Join(args, " ")
	found, err := server.sessions.Get(requests[0].Owner.ID)
	if err != nil {
		t.Fatalf("read created Session: %v", err)
	}
	wantMount := "type=bind,src=" + found.Workdir + ",dst=/task/workdir"
	if !strings.Contains(joined, wantMount) {
		t.Fatalf("Session sandbox does not mount its managed Workdir at /task/workdir: %q", joined)
	}
	if !strings.Contains(requests[0].LaunchGoal, "ATTACHED FILES:") || !strings.Contains(requests[0].LaunchGoal, "/task/workdir/sample.bin") {
		t.Fatalf("Session sandbox Runtime goal does not identify its uploaded file: %q", requests[0].LaunchGoal)
	}
}

func multipartSessionRequest(t *testing.T, method, target, payload, filename, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("payload", payload); err != nil {
		t.Fatalf("write multipart payload: %v", err)
	}
	part, err := writer.CreateFormFile("attachments", filename)
	if err != nil {
		t.Fatalf("create attachment field: %v", err)
	}
	if _, err := part.Write([]byte(contents)); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	request := httptest.NewRequest(method, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestCreateSessionProjectsUploadedAttachmentPathIntoInitialRuntimeTurn(t *testing.T) {
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID:    "session-attachment-path",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{
		session:          providerSession,
		adapter:          runtime.NewProviderSessionRunAdapter(providerSession, make(chan struct{})),
		bindContinuation: true,
	}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	modelProvider := createSnapshotTestModelProvider(t, server, "gpt-session")
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: modelProvider.ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	payload, err := writer.CreateFormField("payload")
	if err != nil {
		t.Fatalf("create payload field: %v", err)
	}
	if _, err := payload.Write([]byte(`{"input":"inspect the upload","runtime_profile_id":"` + profile.ID + `","runner":"host","host_activated":true}`)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	file, err := writer.CreateFormFile("attachments", "sample.bin")
	if err != nil {
		t.Fatalf("create attachment field: %v", err)
	}
	if _, err := file.Write([]byte("payload")); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", response.Code, response.Body.String())
	}

	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider launch requests = %d, want 1", len(requests))
	}
	found, err := server.sessions.Get(requests[0].Owner.ID)
	if err != nil {
		t.Fatalf("read created Session: %v", err)
	}
	wantPath := filepath.Join(found.Workdir, "sample.bin")
	if !strings.Contains(requests[0].LaunchGoal, "ATTACHED FILES:\n- "+wantPath) {
		t.Fatalf("initial Runtime goal = %q, want attachment path %q", requests[0].LaunchGoal, wantPath)
	}
	waitForProviderRequests(t, providerSession, 1)
	if got := providerSession.LastRequests()[0].Message; !strings.Contains(got, "ATTACHED FILES:\n- "+wantPath) {
		t.Fatalf("provider initial Turn = %q, want attachment path %q", got, wantPath)
	}
}

func TestResumedSessionDoesNotAdvertiseDeletedHistoricalAttachments(t *testing.T) {
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID:    "session-stale-attachment-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	server.ServeHTTP(create, multipartSessionRequest(t, http.MethodPost, "/api/sessions",
		`{"input":"inspect stale evidence","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
		"stale.txt", "temporary evidence"))
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	found, err := server.sessions.Get(created.ID)
	if err != nil {
		t.Fatalf("read Session Workdir: %v", err)
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("stop Session status = %d, body=%s", stop.Code, stop.Body.String())
	}
	if err := os.Remove(filepath.Join(found.Workdir, "stale.txt")); err != nil {
		t.Fatalf("remove historical attachment: %v", err)
	}
	factory.mu.Lock()
	factory.session = fakeprovider.New(fakeprovider.Config{
		SessionID:    "session-stale-attachment-2",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory.adapter = &persistentTestAdapter{}
	factory.mu.Unlock()

	resume := httptest.NewRecorder()
	resumeRequest := multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/messages",
		`{"message":"resume with fresh file","host_activated":true}`, "fresh.txt", "fresh evidence")
	server.ServeHTTP(resume, resumeRequest)
	if resume.Code != http.StatusAccepted {
		t.Fatalf("resume Session status = %d, body=%s", resume.Code, resume.Body.String())
	}
	requests := factory.Requests()
	if len(requests) != 2 {
		events, _ := server.sessions.Events(created.ID)
		t.Fatalf("provider launch requests = %d, want 2; response=%s events=%#v", len(requests), resume.Body.String(), events)
	}
	if strings.Contains(requests[1].LaunchGoal, "stale.txt") || !strings.Contains(requests[1].LaunchGoal, "ATTACHED FILES:\n- "+filepath.Join(found.Workdir, "fresh.txt")) {
		t.Fatalf("resumed Runtime goal does not contain only available attachments: %q", requests[1].LaunchGoal)
	}
	eventsResponse := httptest.NewRecorder()
	server.ServeHTTP(eventsResponse, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/events", nil))
	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("Session Events status = %d, body=%s", eventsResponse.Code, eventsResponse.Body.String())
	}
	var eventsPayload struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&eventsPayload); err != nil {
		t.Fatalf("decode Session Events: %v", err)
	}
	for _, event := range eventsPayload.Events {
		if event.Kind == session.EventKindAttachment && event.Payload["filename"] == "fresh.txt" {
			if event.Payload["continuation_id"] != requests[1].Continuation.ID {
				t.Fatalf("resumed attachment continuation = %#v, want %q", event.Payload["continuation_id"], requests[1].Continuation.ID)
			}
			return
		}
	}
	t.Fatal("resumed Session has no fresh attachment Event")
}

func TestRejectedResumeAttachmentDoesNotCreateAContinuationOrEvent(t *testing.T) {
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID:    "session-invalid-resume-attachment",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(
		`{"input":"inspect","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
	))
	createRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, createRequest)
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	if created.LatestContinuation == nil {
		t.Fatal("created Session has no continuation")
	}

	// Stop must run after startup owns the Session and records its state.
	deadline := time.Now().Add(2 * time.Second)
	for {
		latest, err := server.sessions.LatestContinuation(created.ID)
		if err == nil && latest != nil && latest.Status == session.RuntimeStatusRunning && server.sessionHarness.IsActive(created.ID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Session startup did not settle: continuation=%#v err=%v", latest, err)
		}
		time.Sleep(time.Millisecond)
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("stop Session status = %d, body=%s", stop.Code, stop.Body.String())
	}
	beforeEvents := httptest.NewRecorder()
	server.ServeHTTP(beforeEvents, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/events", nil))
	var before struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(beforeEvents.Body).Decode(&before); err != nil {
		t.Fatalf("decode Events before rejected resume: %v", err)
	}

	rejected := httptest.NewRecorder()
	server.ServeHTTP(rejected, multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/messages",
		`{"message":"resume with invalid attachment","host_activated":true}`, "..", "invalid"))
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("rejected resume status = %d, body=%s", rejected.Code, rejected.Body.String())
	}

	read := httptest.NewRecorder()
	server.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID, nil))
	var found session.Session
	if err := json.NewDecoder(read.Body).Decode(&found); err != nil {
		t.Fatalf("decode Session after rejected resume: %v", err)
	}
	if found.LatestContinuation == nil || found.LatestContinuation.ID != created.LatestContinuation.ID {
		t.Fatalf("rejected resume created continuation: before=%#v after=%#v", created.LatestContinuation, found.LatestContinuation)
	}
	afterEvents := httptest.NewRecorder()
	server.ServeHTTP(afterEvents, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/events", nil))
	var after struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(afterEvents.Body).Decode(&after); err != nil {
		t.Fatalf("decode Events after rejected resume: %v", err)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("rejected resume appended Events: before=%d after=%d", len(before.Events), len(after.Events))
	}
}
