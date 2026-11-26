package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/session"
)

func TestSessionStopPreservesSettledContinuation(t *testing.T) {
	for _, status := range []session.RuntimeStatus{session.RuntimeStatusFailed, session.RuntimeStatusCompleted, session.RuntimeStatusStopped} {
		t.Run(string(status), func(t *testing.T) {
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
				RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			created, err := server.sessions.Create(session.CreateRequest{Input: "Keep terminal history"})
			if err != nil {
				t.Fatal(err)
			}
			continuation, err := server.sessions.CreateContinuation(created.ID, "pi-profile", "pi", session.RunnerHost)
			if err != nil {
				t.Fatal(err)
			}
			for _, next := range []session.RuntimeStatus{session.RuntimeStatusRunning, status} {
				if _, err := server.sessions.UpdateContinuationStatus(continuation.ID, next); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := server.sessions.AppendEvent(created.ID, session.EventKindLifecycle, session.EventPayload{
				"phase": string(status), "continuation_id": continuation.ID,
			}); err != nil {
				t.Fatal(err)
			}
			before, err := server.sessions.Events(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("Stop status = %d, body=%s", response.Code, response.Body.String())
			}
			var result struct {
				LatestContinuation struct {
					ID     string                `json:"id"`
					Status session.RuntimeStatus `json:"status"`
				} `json:"latest_continuation"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.LatestContinuation.ID != continuation.ID || result.LatestContinuation.Status != status {
				t.Fatalf("Stop rewrote settled continuation: %+v", result.LatestContinuation)
			}
			after, err := server.sessions.Events(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("Stop rewrote terminal history: before=%+v after=%+v", before, after)
			}
		})
	}
}
