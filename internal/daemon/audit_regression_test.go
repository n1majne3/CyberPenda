package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/store"
	"pentest/internal/task"
)

func TestListenAddressAuthentication(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		status int
	}{
		{":8787", http.StatusUnauthorized},
		{"[::]:8787", http.StatusUnauthorized},
		{"0.0.0.0:8787", http.StatusUnauthorized},
		{"127.0.0.1:8787", http.StatusOK},
		{"localhost:8787", http.StatusOK},
		{"[::1]:8787", http.StatusOK},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			s, err := NewServer(Config{DBPath: ":memory:", ListenAddr: tc.addr, RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
			if w.Code != tc.status {
				t.Fatalf("tokenless API status = %d, want %d", w.Code, tc.status)
			}
			if tc.status == http.StatusUnauthorized {
				r := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
				r.Header.Set("Authorization", "Bearer "+s.GeneratedOperatorToken())
				w = httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("authenticated API status = %d", w.Code)
				}
			}
		})
	}
}

func TestRegistryFailureClosesDatabase(t *testing.T) {
	for _, kind := range []string{"plugin", "extension"} {
		t.Run(kind, func(t *testing.T) {
			config := Config{DBPath: ":memory:", RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true}
			missing := filepath.Join(t.TempDir(), "missing")
			if kind == "plugin" {
				config.RuntimePluginDirs = []string{missing}
			} else {
				config.RuntimeExtensionDirs = []string{missing}
			}
			var opened *store.DB
			s, err := newServer(config, func(path string) (*store.DB, error) {
				var err error
				opened, err = store.Open(path)
				return opened, err
			})
			if s != nil {
				t.Cleanup(func() { _ = s.Close() })
			}
			if opened == nil {
				t.Fatal("database was not opened")
			}
			t.Cleanup(func() { _ = opened.Close() })
			if err == nil {
				t.Fatal("invalid registry must fail initialization")
			}
			if err := opened.Ping(); err == nil {
				t.Fatal("database remains open after registry initialization failed")
			}
		})
	}
}

func TestRejectedTaskSelectionLeavesNoSteering(t *testing.T) {
	for _, route := range []string{"steer/queue", "steer"} {
		for _, selection := range []string{`"model_provider_id":"missing-provider"`, `"runtime_profile_id":"locked-profile"`} {
			t.Run(route+"/"+selection, func(t *testing.T) {
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
				s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/projects/"+found.ProjectID+"/tasks/"+found.ID+"/"+route, strings.NewReader(`{"directive":"rejected directive",`+selection+`}`)))
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d: %s", w.Code, w.Body.String())
				}
				after, err := s.tasks.Events(found.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(after) != len(before) {
					t.Fatalf("rejected selection added %d events", len(after)-len(before))
				}
				afterVersions, err := s.tasks.RuntimeConfigVersions(found.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(afterVersions) != len(versions) {
					t.Fatal("rejected selection changed Runtime Configuration Snapshot")
				}
			})
		}
	}
}

func TestNativeResumeReturnsSteeringReadError(t *testing.T) {
	s, found, _ := newFinishTaskFixture(t, nil)
	if _, err := s.db.Exec(`UPDATE tasks SET run_controls_json='{"blackboard_mode":"disabled"}' WHERE id=?`, found.ID); err != nil {
		t.Fatal(err)
	}
	found, err := s.tasks.Get(found.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.tasks.CreateContinuation(found.ID, found.RuntimeProfileID, "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.tasks.UpdateContinuationRuntimeMetadata(c.ID, "", "native-resume", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE task_events RENAME COLUMN payload_json TO unavailable_payload`); err != nil {
		t.Fatal(err)
	}
	_, _, plan, err := s.prepareNativeResumeContinuation(found, "resume")
	if err == nil || !strings.Contains(err.Error(), "list events:") {
		t.Fatalf("resume error = %v; want steering read error", err)
	}
	if plan.Adapter != nil {
		t.Fatal("failed resume returned a launch adapter")
	}
}

func TestTaskSteeringSelectionStorageOrder(t *testing.T) {
	for _, route := range []string{"steer/queue", "steer"} {
		for _, fault := range []string{"", "task_events", "task_runtime_config_versions"} {
			for _, selection := range []string{"", `,"model":"gpt-test"`} {
				t.Run(route+"/"+fault+"/"+selection, func(t *testing.T) {
					s, found, _ := newFinishTaskFixture(t, nil)
					if fault != "" {
						if _, err := s.db.Exec(`CREATE TRIGGER fail_steering BEFORE INSERT ON ` + fault + ` BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`); err != nil {
							t.Fatal(err)
						}
					}
					before, err := s.tasks.RuntimeConfigVersions(found.ID)
					if err != nil {
						t.Fatal(err)
					}
					w := httptest.NewRecorder()
					s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/projects/"+found.ProjectID+"/tasks/"+found.ID+"/"+route, strings.NewReader(`{"directive":"test directive"`+selection+`}`)))
					want := http.StatusOK
					if fault == "task_events" || fault == "task_runtime_config_versions" && selection != "" {
						want = http.StatusInternalServerError
					}
					if w.Code != want {
						t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
					}
					events, err := s.tasks.Events(found.ID)
					if err != nil {
						t.Fatal(err)
					}
					versions, err := s.tasks.RuntimeConfigVersions(found.ID)
					if err != nil {
						t.Fatal(err)
					}
					wantEvents, wantVersions := 0, len(before)
					if want == http.StatusOK {
						wantEvents = 1
						if selection != "" {
							wantVersions++
						}
					}
					if fault == "task_runtime_config_versions" && selection != "" {
						wantEvents = 1
					}
					if len(events) != wantEvents || len(versions) != wantVersions {
						t.Fatalf("events=%d versions=%d, want events=%d versions=%d", len(events), len(versions), wantEvents, wantVersions)
					}
					if want == http.StatusOK && selection != "" {
						resolved, err := s.currentTurnSelection(found)
						if err != nil {
							t.Fatal(err)
						}
						if resolved.ModelProviderID == "" || resolved.Model != "gpt-test" || resolved.RequestedReasoningEffort != "medium" {
							t.Fatalf("incomplete selection after steer: %#v", resolved)
						}
					}
				})
			}
		}
	}
}
