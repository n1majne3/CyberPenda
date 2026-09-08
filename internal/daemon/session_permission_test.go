package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/session"
)

func TestSessionPermissionResponseValidationAndReplay(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	created, err := server.sessions.Create(session.CreateRequest{Input: "inspect", BlackboardMode: session.BlackboardModeDisabled})
	if err != nil {
		t.Fatal(err)
	}
	provider := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{SessionID: "permission-provider"})
	if err := server.BindSessionProviderSession(created.ID, provider); err != nil {
		t.Fatal(err)
	}
	// Completed delivery can be replayed even without a live Continuation.
	_, err = server.sessions.AppendEvent(created.ID, session.EventKindLifecycle, session.EventPayload{
		"phase": "provider_permission_response_applied", "mode": "permission_response",
		"permission_request_id": "perm-1", "request_id": "reply-1",
		"permission_decision": "allow", "outcome": "applied",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"invalid JSON", "{", "invalid JSON body", http.StatusBadRequest},
		{"empty decision", `{}`, "permission decision must be allow or deny", http.StatusBadRequest},
		{"invalid decision", `{"decision":"maybe"}`, "permission decision must be allow or deny", http.StatusBadRequest},
		{"replay", `{"request_id":"reply-1","decision":"approved"}`, `"outcome":"applied"`, http.StatusAccepted},
		{"conflicting replay", `{"request_id":"reply-1","decision":"deny"}`, "different decision", http.StatusConflict},
		{"no pending request", `{"request_id":"reply-2","decision":"allow"}`, "no longer pending", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/permissions/perm-1/respond", bytes.NewBufferString(tc.body)))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%s; want %d containing %q", response.Code, response.Body.String(), tc.status, tc.want)
			}
			if requests := provider.LastRequests(); len(requests) != 0 {
				t.Fatalf("validation or replay dispatched Provider requests: %#v", requests)
			}
		})
	}
}
