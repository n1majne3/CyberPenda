package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/testsupport/fakeprovider"
)

func TestRejectedSessionQueueLeavesNoSteering(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"locked profile", `{"directive":"rejected","runtime_profile_id":"locked"}`, http.StatusBadRequest},
		{"empty", `{"directive":" "}`, http.StatusBadRequest},
		{"failed preparation", `{"directive":"no captured configuration"}`, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewServer(Config{DBPath: ":memory:", RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			found, err := s.sessions.Create(session.CreateRequest{Input: "Initial"})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+found.ID+"/steer/queue", strings.NewReader(tc.body)))
			if w.Code != tc.status {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			assertSessionQueueSteeringCount(t, s, found.ID, 0)
		})
	}
}

func TestAcceptedSessionQueueRecordsSteering(t *testing.T) {
	provider := fakeprovider.New(fakeprovider.Config{SessionID: "queue-provider", Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true}})
	s, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true, ProviderSessionFactory: &recordingProviderSessionFactory{session: provider, adapter: &persistentTestAdapter{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mp := createSnapshotTestModelProvider(t, s, "queue-model")
	p, err := s.profiles.Create("Queue", runtimeprofile.ProviderCodex, runtimeprofile.Fields{DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: mp.ID, ModelOverride: "queue-model"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"input":"Initial","runtime_profile_id":"`+p.ID+`","runner":"host","host_activated":true}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create=%d: %s", w.Code, w.Body.String())
	}
	var found session.Session
	if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := s.sessions.LatestContinuation(found.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c != nil && c.Number == 1 && c.Status == session.RuntimeStatusRunning && s.sessionHarness.IsActive(found.ID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Session did not start: %#v", c)
		}
		time.Sleep(time.Millisecond)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+found.ID+"/steer/queue", strings.NewReader(`{"directive":"Accepted directive"}`)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("queue=%d: %s", w.Code, w.Body.String())
	}
	assertSessionQueueSteeringCount(t, s, found.ID, 1)
	events, err := s.sessions.Events(found.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == session.EventKindConversation && event.Payload["text"] == "Accepted directive" {
			return
		}
	}
	t.Fatal("accepted conversation is missing")
}

func assertSessionQueueSteeringCount(t *testing.T, s *Server, id string, want int) {
	t.Helper()
	events, err := s.sessions.Events(id)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == session.EventKindSteering && event.Payload["phase"] == "steering_requested" {
			count++
		}
	}
	if count != want {
		t.Fatalf("steering_requested=%d, want %d", count, want)
	}
}
