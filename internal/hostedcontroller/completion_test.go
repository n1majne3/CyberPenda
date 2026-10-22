package hostedcontroller_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/hostedcontroller"
	"pentest/internal/tsecbenchclient"
)

type completionLog struct{ messages chan string }

func (l completionLog) Write(p []byte) (int, error) {
	select {
	case l.messages <- string(p):
	default:
	}
	return len(p), nil
}

func TestWatchdogRequiresPlatformCompletionAndSettledCleanup(t *testing.T) {
	const ledger = `{"challenges":{"one":{}},"dispatches":{"d001":{"code":"one","state":"done","terminal_at":1,"agent_id":"native-1","runtime_settled":true}}}`
	const platform = `[{"unique_code":"one","flag_count":2,"correct_flag_count":2,"is_completed":true,"container_status":"stopped"}]`
	for _, tc := range []struct {
		name, ledger, platform, turn string
		quiet                        bool
	}{
		{"complete", ledger, platform, "idle", true},
		{"partial chain", ledger, strings.ReplaceAll(strings.ReplaceAll(platform, `"correct_flag_count":2`, `"correct_flag_count":1`), `"is_completed":true`, `"is_completed":false`), "idle", false},
		{"empty platform", ledger, `[]`, "idle", false},
		{"platform unavailable", ledger, `error`, "idle", false},
		{"instance still active", ledger, strings.ReplaceAll(platform, `"stopped"`, `"available"`), "idle", false},
		{"cleanup pending", strings.ReplaceAll(ledger, `"done"`, `"dispatched"`), platform, "idle", false},
		{"cancel not settled", strings.ReplaceAll(ledger, `"runtime_settled":true`, `"runtime_settled":false`), platform, "idle", false},
		{"parent busy", ledger, platform, "busy", false},
		{"missing ledger", `{}`, platform, "idle", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			graph := filepath.Join(root, "task-1", "workdir", "graph")
			if err := os.MkdirAll(graph, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(graph, "ledger.json"), []byte(tc.ledger), 0600); err != nil {
				t.Fatal(err)
			}
			const base = "/api/projects/project-1/tasks/task-1"
			steered := make(chan struct{}, 4)
			client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				switch r.URL.Path {
				case "/openapi/v1/challenges":
					if r.Method != http.MethodGet {
						t.Errorf("unexpected platform mutation: %s", r.Method)
					}
					if tc.platform == "error" {
						w.WriteHeader(503)
					} else {
						_, _ = io.WriteString(w, tc.platform)
					}
				case base:
					writeStreamJSON(t, w, map[string]any{"status": "running", "runtime_activity": map[string]any{"liveness": "live", "turn_activity": tc.turn}})
				case base + "/transcript":
					writeStreamPage(t, w, 0, false)
				case base + "/steer":
					steered <- struct{}{}
					w.WriteHeader(202)
				default:
					http.NotFound(w, r)
				}
				return w.Result(), nil
			})}
			platformClient, err := tsecbenchclient.New(tsecbenchclient.Config{BaseURL: "http://platform.test", Token: "test", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			logs := completionLog{messages: make(chan string, 16)}
			app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
				BaseURL: "http://daemon.test", Client: client, PollPeriod: time.Millisecond,
				SilenceReviveSec: 1, SilenceReviveCooldownSec: 1,
				CompletionPlatform: platformClient, RuntimeRoot: root, Diagnostics: logs,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			done := make(chan error, 1)
			go func() {
				done <- app.Wait(ctx, hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, io.Discard, nil)
			}()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			for {
				select {
				case <-steered:
					if tc.quiet {
						t.Fatal("completed evaluation was revived")
					}
					return
				case message := <-logs.messages:
					if strings.Contains(message, "all challenges complete; cleanup settled") {
						if !tc.quiet {
							t.Fatalf("incomplete evidence suppressed recovery: %s", message)
						}
						return
					}
				case <-ctx.Done():
					t.Fatal("watchdog did not reach an observable decision")
				}
			}
		})
	}
}
