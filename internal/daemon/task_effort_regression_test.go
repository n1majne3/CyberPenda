package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskSteeringValidatesEffortBeforePersistence(t *testing.T) {
	for _, route := range []string{"steer/queue", "steer"} {
		for _, tc := range []struct {
			name, selection string
			status, added   int
			effort          string
		}{
			{"invalid effort only", `"reasoning_effort":"not-valid"`, 400, 0, "medium"},
			{"invalid with model", `"model":"gpt-test","reasoning_effort":"not-valid"`, 400, 0, "medium"},
			{"valid effort only", `"reasoning_effort":" high "`, 200, 1, "high"},
			{"empty effort", `"reasoning_effort":""`, 200, 0, "medium"},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				s, found, _ := newFinishTaskFixture(t, nil)
				before, err := s.tasks.Events(found.ID)
				if err != nil {
					t.Fatal(err)
				}
				versions, err := s.tasks.RuntimeConfigVersions(found.ID)
				if err != nil {
					t.Fatal(err)
				}
				w := httptest.NewRecorder()
				s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/projects/"+found.ProjectID+"/tasks/"+found.ID+"/"+route, strings.NewReader(`{"directive":"New turn",`+tc.selection+`}`)))
				if w.Code != tc.status {
					t.Fatalf("status=%d, want %d: %s", w.Code, tc.status, w.Body.String())
				}
				after, err := s.tasks.Events(found.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantEvents := len(before)
				if tc.status == http.StatusOK {
					wantEvents++
				}
				if len(after) != wantEvents {
					t.Fatalf("events=%d, want %d", len(after), wantEvents)
				}
				afterVersions, err := s.tasks.RuntimeConfigVersions(found.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(afterVersions) != len(versions)+tc.added {
					t.Fatalf("versions=%d, want %d", len(afterVersions), len(versions)+tc.added)
				}
				selection, err := s.currentTurnSelection(found)
				if err != nil {
					t.Fatal(err)
				}
				if selection.RequestedReasoningEffort != tc.effort {
					t.Fatalf("effort=%q, want %q", selection.RequestedReasoningEffort, tc.effort)
				}
			})
		}
	}
}
