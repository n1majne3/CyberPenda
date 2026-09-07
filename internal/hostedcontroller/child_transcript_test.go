package hostedcontroller_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/hostedcontroller"
)

func TestHostedTranscriptReadsChildPagesAtSourceBoundary(t *testing.T) {
	const base = "/api/projects/project-1/tasks/task-1"
	const history = base + "/transcript/children/subagent-a"
	childReads := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base + "/transcript":
			if r.URL.Query().Has("after") {
				writeStreamPage(t, w, 8, false)
				return
			}
			block := streamEntry("subagent-a", 8, "subagent_block", "runtime", "Child A", "2026-09-07T00:00:00Z")
			block["details"] = map[string]any{"history": history}
			writeStreamPage(t, w, 8, false, block)
		case history:
			childReads++
			if r.URL.Query().Get("through") != "8" {
				t.Errorf("child read lost source boundary: %s", r.URL)
			}
			if r.URL.Query().Get("after") == "0" {
				writeStreamJSON(t, w, map[string]any{"entries": []any{streamEntry("child-1", 2, "message", "assistant", "first secret", "2026-09-07T00:00:00Z")}, "cursor": 20, "has_newer": true})
			} else {
				item := streamEntry("child-2", 7, "tool_result", "tool", "preview", "2026-09-07T00:00:00Z")
				item["truncated"] = true
				item["detail"] = history + "/items/21"
				writeStreamJSON(t, w, map[string]any{"entries": []any{item}, "cursor": 21, "has_newer": false})
			}
		case history + "/items/21":
			writeStreamJSON(t, w, streamEntry("child-2", 7, "tool_result", "tool", "complete child result secret", "2026-09-07T00:00:00Z"))
		case base:
			writeStreamJSON(t, w, map[string]any{"status": "failed"})
		default:
			http.NotFound(w, r)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", PollPeriod: time.Millisecond, Client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}})
	var out bytes.Buffer
	err := app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &out, []string{"secret"})
	if err == nil || !strings.Contains(err.Error(), "hosted Runtime failed") {
		t.Fatalf("Wait = %v", err)
	}
	if childReads != 2 || !strings.Contains(out.String(), "first [REDACTED]") || !strings.Contains(out.String(), "complete child result [REDACTED]") {
		t.Fatalf("child reads=%d output=%s", childReads, out.String())
	}
	if strings.Contains(out.String(), "secret") {
		t.Fatal("child output leaked credential")
	}
}

func TestHostedChildSummaryOnlyBackwardPages(t *testing.T) {
	const base = "/api/projects/project-1/tasks/task-1"
	const history = base + "/transcript/children/subagent-a"
	var backwards []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base + "/transcript":
			if r.URL.Query().Has("after") {
				writeStreamPage(t, w, 1200, false)
				return
			}
			seq, before, older := 1200, 801, true
			if value := r.URL.Query().Get("before"); value != "" {
				backwards = append(backwards, value)
				if value == "801" {
					seq, before = 800, 401
				} else {
					seq, before, older = 400, 1, false
				}
			}
			block := streamEntry("subagent-a", seq, "subagent_block", "runtime", "Child A", "2026-09-07T00:00:00Z")
			block["details"] = map[string]any{"history": history}
			if seq == 800 {
				block["details"].(map[string]any)["legacy_items"] = []any{streamEntry("legacy", 600, "message", "assistant", "legacy child work", "2026-09-07T00:00:00Z")}
			}
			writeStreamJSON(t, w, map[string]any{"entries": []any{block}, "cursor": 1200, "before": before, "has_older": older})
		case history:
			if r.URL.Query().Get("through") != "1200" {
				t.Errorf("lost latest child snapshot: %s", r.URL)
			}
			writeStreamJSON(t, w, map[string]any{"entries": []any{streamEntry("child-item", 2, "message", "assistant", "retained child work", "2026-09-07T00:00:00Z")}, "cursor": 1, "has_newer": false})
		case base:
			writeStreamJSON(t, w, map[string]any{"status": "failed"})
		default:
			http.NotFound(w, r)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", PollPeriod: time.Millisecond, Client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}})
	var out bytes.Buffer
	err := app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &out, nil)
	if err == nil || !strings.Contains(err.Error(), "hosted Runtime failed") {
		t.Fatalf("Wait=%v", err)
	}
	if strings.Join(backwards, ",") != "801,401" || strings.Count(out.String(), "retained child work") != 1 || strings.Count(out.String(), "legacy child work") != 1 {
		t.Fatalf("pages=%v stdout=%s", backwards, out.String())
	}
}

func TestHostedTruncatedMixedChildHistory(t *testing.T) {
	for _, mixedSeq := range []int{800, 1200} {
		t.Run(fmt.Sprint(mixedSeq), func(t *testing.T) {
			legacyText := strings.Repeat("legacy payload ", 30000)
			detailReads := 0
			const base = "/api/projects/project-1/tasks/task-1"
			const history = base + "/transcript/children/subagent-a"
			var backwards []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case base + "/transcript":
					if r.URL.Query().Has("after") {
						writeStreamPage(t, w, 1200, false)
						return
					}
					seq, before, older := 1200, 801, true
					if value := r.URL.Query().Get("before"); value != "" {
						backwards = append(backwards, value)
						if value == "801" {
							seq, before = 800, 401
						} else {
							seq, before, older = 400, 1, false
						}
					}
					block := streamEntry("subagent-a", seq, "subagent_block", "runtime", "Child A", "2026-09-07T00:00:00Z")
					block["details"] = map[string]any{"history": history}
					if seq == mixedSeq {
						block["truncated"] = true
						block["detail"] = base + "/transcript/entries/mixed"
						block["details"].(map[string]any)["legacy_items"] = []any{streamEntry("legacy", 600, "message", "assistant", "legacy child work", "2026-09-07T00:00:00Z")}
					}
					writeStreamJSON(t, w, map[string]any{"entries": []any{block}, "cursor": 1200, "before": before, "has_older": older})
				case base + "/transcript/entries/mixed":
					detailReads++
					block := streamEntry("subagent-a", mixedSeq, "subagent_block", "runtime", "Child A", "2026-09-07T00:00:00Z")
					block["details"] = map[string]any{"history": history, "legacy_items": []any{streamEntry("legacy", 600, "tool_result", "tool", legacyText, "2026-09-07T00:00:00Z")}}
					writeStreamJSON(t, w, block)
				case history:
					if r.URL.Query().Get("through") != "1200" {
						t.Errorf("lost latest child snapshot: %s", r.URL)
					}
					writeStreamJSON(t, w, map[string]any{"entries": []any{streamEntry("child-item", 2, "message", "assistant", "retained child work", "2026-09-07T00:00:00Z")}, "cursor": 1, "has_newer": false})
				case base:
					writeStreamJSON(t, w, map[string]any{"status": "failed"})
				default:
					http.NotFound(w, r)
				}
			})
			app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{BaseURL: "http://hosted.test", PollPeriod: time.Millisecond, Client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w.Result(), nil
			})}})
			var out bytes.Buffer
			err := app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &out, nil)
			if err == nil || !strings.Contains(err.Error(), "hosted Runtime failed") {
				t.Fatalf("Wait=%v", err)
			}
			if strings.Join(backwards, ",") != "801,401" || strings.Count(out.String(), "retained child work") != 1 || strings.Count(out.String(), legacyText) != 1 || detailReads != 1 {
				t.Fatalf("pages=%v stdout=%s", backwards, out.String())
			}
		})
	}
}
