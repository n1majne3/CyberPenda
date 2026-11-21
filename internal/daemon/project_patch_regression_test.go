package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pentest/internal/project"
)

func TestProjectPatchPreservesOmittedDescription(t *testing.T) {
	s, err := NewServer(Config{DBPath: ":memory:", RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, tc := range []struct {
		name, body, description string
		status                  int
	}{
		{"rename", `{"name":"After"}`, "Keep this description", http.StatusOK},
		{"scope", `{"scope":{"domains":["example.test"]}}`, "Keep this description", http.StatusOK},
		{"defaults", `{"defaults":{}}`, "Keep this description", http.StatusOK},
		{"empty patch", `{}`, "Keep this description", http.StatusOK},
		{"explicit clear", `{"description":""}`, "", http.StatusOK},
		{"replace", `{"description":"Changed"}`, "Changed", http.StatusOK},
		{"invalid name", `{"name":""}`, "Keep this description", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := s.projects.Create("Before", "Keep this description", project.Scope{}, project.Defaults{})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/projects/"+p.ID, strings.NewReader(tc.body)))
			if w.Code != tc.status {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			after, err := s.projects.Get(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Description != tc.description {
				t.Fatalf("description=%q, want %q", after.Description, tc.description)
			}
		})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/projects/missing", strings.NewReader(`{}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing Project status=%d", w.Code)
	}
}
