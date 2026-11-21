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

	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/testsupport/fakeprovider"
)

func TestSessionLaunchUsesProjectFreeOwnerAndPersistentProviderControls(t *testing.T) {
	providerSession := fakeprovider.New(fakeprovider.Config{
		SessionID:         "session-provider-1",
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
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", ModelProviderID: modelProvider.ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"input":"inspect the session target","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`))
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
	if created.ID == "" {
		t.Fatal("created Session has no id")
	}

	var launchRequest ProviderSessionLaunchRequest
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		requests := factory.Requests()
		if len(requests) > 0 {
			launchRequest = requests[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if launchRequest.Owner.ID != created.ID {
		t.Fatalf("provider owner id = %q, want %q", launchRequest.Owner.ID, created.ID)
	}
	if err := launchRequest.Owner.Validate(); err != nil {
		t.Fatalf("Session owner contract: %v", err)
	}
	if launchRequest.Owner.ProjectID != "" || launchRequest.Continuation.OwnerID != created.ID || launchRequest.Owner.Capabilities.ProjectScope {
		t.Fatalf("provider launch crossed Project/Task boundary: owner=%#v continuation=%#v", launchRequest.Owner, launchRequest.Continuation)
	}
	found, err := server.sessions.Get(created.ID)
	if err != nil {
		t.Fatalf("read Session Workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(found.Workdir, "turn-notes.txt"), []byte("runtime-owned"), 0o600); err != nil {
		t.Fatalf("seed colliding Runtime file: %v", err)
	}

	message := httptest.NewRecorder()
	messageRequest := multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/messages",
		`{"message":"continue with the second pass"}`, "turn-notes.txt", "operator evidence")
	server.ServeHTTP(message, messageRequest)
	if message.Code != http.StatusAccepted {
		t.Fatalf("Session message status = %d, body=%s", message.Code, message.Body.String())
	}

	conversation := httptest.NewRecorder()
	server.ServeHTTP(conversation, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/conversation", nil))
	if conversation.Code != http.StatusOK {
		t.Fatalf("conversation status = %d, body=%s", conversation.Code, conversation.Body.String())
	}
	var conversationPayload struct {
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.NewDecoder(conversation.Body).Decode(&conversationPayload); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if len(conversationPayload.Events) != 2 {
		t.Fatalf("conversation events = %#v, want initial and continuation input", conversationPayload.Events)
	}
	waitForProviderRequests(t, providerSession, 1)
	messageRequests := providerSession.LastRequests()
	wantMessagePath := filepath.Join(found.Workdir, "turn-notes-1.txt")
	if !strings.Contains(messageRequests[len(messageRequests)-1].Message, "ATTACHED FILES:\n- "+wantMessagePath) {
		t.Fatalf("Session message Runtime input omitted resolved attachment path: %q", messageRequests[len(messageRequests)-1].Message)
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
	var messageAttachment session.Event
	for _, event := range eventsPayload.Events {
		if event.Kind == session.EventKindAttachment && event.Payload["filename"] == "turn-notes-1.txt" {
			messageAttachment = event
			break
		}
	}
	if messageAttachment.ID == "" || messageAttachment.Payload["continuation_id"] != launchRequest.Continuation.ID {
		t.Fatalf("Session message attachment lacks continuation ownership: %#v", messageAttachment)
	}

	var steerBody bytes.Buffer
	steerWriter := multipart.NewWriter(&steerBody)
	payloadField, err := steerWriter.CreateFormField("payload")
	if err != nil {
		t.Fatalf("create Session steer payload field: %v", err)
	}
	if _, err := payloadField.Write([]byte(`{"message":"interrupt and focus on the login flow"}`)); err != nil {
		t.Fatalf("write Session steer payload: %v", err)
	}
	attachmentField, err := steerWriter.CreateFormFile("attachments", "interrupt-notes.txt")
	if err != nil {
		t.Fatalf("create Session steer attachment: %v", err)
	}
	if _, err := attachmentField.Write([]byte("interrupt evidence")); err != nil {
		t.Fatalf("write Session steer attachment: %v", err)
	}
	if err := steerWriter.Close(); err != nil {
		t.Fatalf("close Session steer multipart body: %v", err)
	}
	steer := httptest.NewRecorder()
	steerRequest := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/steer", &steerBody)
	steerRequest.Header.Set("Content-Type", steerWriter.FormDataContentType())
	server.ServeHTTP(steer, steerRequest)
	if steer.Code != http.StatusAccepted {
		t.Fatalf("Session steer status = %d, body=%s", steer.Code, steer.Body.String())
	}
	waitForProviderRequests(t, providerSession, 2)
	requests := providerSession.LastRequests()
	if !strings.Contains(requests[len(requests)-1].Message, "ATTACHED FILES:\n- "+filepath.Join(found.Workdir, "interrupt-notes.txt")) {
		t.Fatalf("Session steer Runtime message omitted attachment path: %q", requests[len(requests)-1].Message)
	}
	steerRequestID := requests[len(requests)-1].RequestID
	if err := providerSession.Acknowledge(steerRequestID); err != nil {
		t.Fatalf("acknowledge Session replacement: %v", err)
	}
	replacementDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(replacementDeadline) {
		active, activeErr := server.sessions.ActiveContinuation(created.ID)
		if activeErr != nil {
			t.Fatalf("read active Session continuation: %v", activeErr)
		}
		if active != nil && active.Number == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	active, err := server.sessions.ActiveContinuation(created.ID)
	if err != nil || active == nil || active.Number != 2 {
		t.Fatalf("Session replacement continuation = %#v, %v", active, err)
	}
	var events []session.Event
	var sawReplacement, sawAttachment bool
	eventDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(eventDeadline) {
		events, err = server.sessions.Events(created.ID)
		if err != nil {
			t.Fatalf("read Session replacement events: %v", err)
		}
		for _, event := range events {
			if event.Payload["continuation_id"] == active.ID {
				sawReplacement = true
			}
			if event.Kind == session.EventKindAttachment && event.Payload["filename"] == "interrupt-notes.txt" {
				sawAttachment = event.Payload["continuation_id"] == launchRequest.Continuation.ID
			}
		}
		if sawReplacement {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawReplacement {
		t.Fatalf("Session replacement emitted no events on continuation %q: %#v", active.ID, events)
	}
	if !sawAttachment {
		t.Fatalf("Session steer attachment lost its source continuation: %#v", events)
	}

	timeline := httptest.NewRecorder()
	server.ServeHTTP(timeline, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/timeline", nil))
	if timeline.Code != http.StatusOK {
		t.Fatalf("timeline status = %d, body=%s", timeline.Code, timeline.Body.String())
	}
	var timelinePayload struct {
		SessionID string `json:"session_id"`
		Items     []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.NewDecoder(timeline.Body).Decode(&timelinePayload); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if timelinePayload.SessionID != created.ID {
		t.Fatalf("timeline session_id = %q, want %q", timelinePayload.SessionID, created.ID)
	}
	for _, item := range timelinePayload.Items {
		if item.Type == "" {
			t.Fatalf("timeline item missing type: %#v", timelinePayload.Items)
		}
	}
	// The steer attachment must surface as a lifecycle timeline item rather
	// than vanishing; conversation events are never part of the timeline.
	var sawAttachmentInTimeline bool
	for _, item := range timelinePayload.Items {
		if item.Type == "lifecycle" && strings.HasPrefix(item.Content, "Attached interrupt-notes.txt") {
			sawAttachmentInTimeline = true
		}
	}
	if !sawAttachmentInTimeline {
		t.Fatalf("timeline omitted steer attachment item: %#v", timelinePayload.Items)
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("Session stop status = %d, body=%s", stop.Code, stop.Body.String())
	}
}
