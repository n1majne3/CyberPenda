package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/testsupport/fakeprovider"
)

func TestSessionStopRecordsSingleStoppedLifecycleEvent(t *testing.T) {
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID:         "session-stop-1",
		ManualAcknowledge: true,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true,
			InterruptThenReplace: true, PermissionResponse: true,
		},
	})
	factory := &recordingProviderSessionFactory{
		session: providerSession,
		adapter: &persistentTestAdapter{},
	}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	modelProvider := createSnapshotTestModelProvider(t, server, "gpt-session")
	profile, err := server.profiles.Create("Session Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		DefaultRunner: "host", ModelProviderID: modelProvider.ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"input":"run check","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}

	waitForSessionRunning(t, server, created.ID)

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("Session stop status = %d, body=%s", stop.Code, stop.Body.String())
	}

	events, err := server.sessions.Events(created.ID)
	if err != nil {
		t.Fatalf("list Session events: %v", err)
	}
	stoppedEvents := 0
	for _, ev := range events {
		if ev.Kind == session.EventKindLifecycle && ev.Payload["phase"] == "stopped" {
			stoppedEvents++
		}
	}
	if stoppedEvents != 1 {
		t.Fatalf("stopped lifecycle event count = %d, want 1 (events=%#v)", stoppedEvents, events)
	}

	timeline := httptest.NewRecorder()
	server.ServeHTTP(timeline, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/timeline", nil))
	if timeline.Code != http.StatusOK {
		t.Fatalf("Session timeline status = %d, body=%s", timeline.Code, timeline.Body.String())
	}
	var payload struct {
		Items []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.NewDecoder(timeline.Body).Decode(&payload); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	stoppedTimelineItems := 0
	for _, item := range payload.Items {
		if item.Type == "lifecycle" && item.Content == "Lifecycle: stopped" {
			stoppedTimelineItems++
		}
	}
	if stoppedTimelineItems != 1 {
		t.Fatalf("timeline stopped item count = %d, want 1 (items=%#v)", stoppedTimelineItems, payload.Items)
	}

	transcriptResp := httptest.NewRecorder()
	server.ServeHTTP(transcriptResp, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/transcript", nil))
	if transcriptResp.Code != http.StatusOK {
		t.Fatalf("Session transcript status = %d, body=%s", transcriptResp.Code, transcriptResp.Body.String())
	}
	var transcriptPayload struct {
		Entries []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(transcriptResp.Body).Decode(&transcriptPayload); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	stoppedTranscriptEntries := 0
	for _, entry := range transcriptPayload.Entries {
		if entry.Kind == "continuation" && entry.Text == "Continuation #1 stopped" {
			stoppedTranscriptEntries++
		}
	}
	if stoppedTranscriptEntries != 1 {
		t.Fatalf("transcript stopped entry count = %d, want 1 (entries=%#v)", stoppedTranscriptEntries, transcriptPayload.Entries)
	}
}

func TestSessionStopRecordsStoppedLifecycleEventForOrphanedContinuation(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	created, err := server.sessions.Create(session.CreateRequest{Input: "orphan check"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	cont, err := server.sessions.CreateContinuation(created.ID, "pi-profile", "pi", session.RunnerHost)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(cont.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatalf("mark continuation running: %v", err)
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("Session stop status = %d, body=%s", stop.Code, stop.Body.String())
	}

	events, err := server.sessions.Events(created.ID)
	if err != nil {
		t.Fatalf("list Session events: %v", err)
	}
	stoppedEvents := 0
	for _, ev := range events {
		if ev.Kind == session.EventKindLifecycle && ev.Payload["phase"] == "stopped" {
			stoppedEvents++
		}
	}
	if stoppedEvents != 1 {
		t.Fatalf("orphaned continuation stopped lifecycle event count = %d, want 1 (events=%#v)", stoppedEvents, events)
	}
}

func waitForSessionRunning(t *testing.T, server *Server, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastStatus string
	var lastActive bool
	for time.Now().Before(deadline) {
		lastActive = server.sessionHarness != nil && server.sessionHarness.IsActive(sessionID)
		if active, err := server.sessions.ActiveContinuation(sessionID); err == nil && active != nil {
			lastStatus = string(active.Status)
			if lastActive && active.Status == session.RuntimeStatusRunning {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Session %s did not reach running state (harnessActive=%v continuationStatus=%q)", sessionID, lastActive, lastStatus)
}
