package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	sessiondomain "pentest/internal/session"
	"pentest/internal/task"
)

func TestLimitedSessionNavigationPromotesAnOlderBusySession(t *testing.T) {
	server, err := NewServer(Config{
		Version:              "test-version",
		DBPath:               filepath.Join(t.TempDir(), "pentest.db"),
		SessionRoot:          filepath.Join(t.TempDir(), "managed-sessions"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	var oldest sessiondomain.Session
	for index := range 6 {
		created, createErr := server.sessions.Create(sessiondomain.CreateRequest{Input: "session"})
		if createErr != nil {
			t.Fatalf("create Session %d: %v", index, createErr)
		}
		if index == 0 {
			oldest = created
		}
	}

	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "busy-session-runtime",
		ActiveTurnID: "busy-session-turn",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true,
			InterruptTurn:     true,
		},
		ManualAcknowledge: true,
	})
	if err := server.BindSessionProviderSession(oldest.ID, provider); err != nil {
		t.Fatalf("bind busy Session runtime: %v", err)
	}
	turnDone := make(chan error, 1)
	go func() {
		_, sendErr := provider.InterruptTurn(context.Background(), runtime.ProviderSessionRequest{
			RequestID: "busy-navigation-turn",
			Message:   "keep working",
		}, func(task.EventKind, task.EventPayload) {})
		turnDone <- sendErr
	}()
	deadline := time.Now().Add(time.Second)
	for !provider.ControlBusy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !provider.ControlBusy() {
		t.Fatal("provider Session did not become busy")
	}
	t.Cleanup(func() {
		_ = provider.Acknowledge("busy-navigation-turn")
		<-turnDone
	})

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions?limit=5", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list Sessions status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Sessions []sessiondomain.Session `json:"sessions"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode Sessions: %v", err)
	}
	if len(payload.Sessions) != 5 {
		t.Fatalf("limited Sessions = %d, want 5", len(payload.Sessions))
	}
	if payload.Sessions[0].ID != oldest.ID || payload.Sessions[0].RuntimeActivity.TurnActivity != runtimeTurnBusy {
		t.Fatalf("first Session = %#v, want older busy Session %q", payload.Sessions[0], oldest.ID)
	}
}
