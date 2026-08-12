package hostedcontroller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentest/internal/hostedcontroller"
)

func TestHTTPAppWaitStreamsCompleteMaskedTranscriptAndFinalDrain(t *testing.T) {
	const benchmarkToken = "benchmark-token-value"
	const modelKey = "model-key-value"
	createdAt := "2026-08-12T00:00:00Z"
	var transcriptRequests []string
	detailSource := map[string]any{
		"id": "entry-3", "seq": 3, "continuation": 1, "kind": "tool_result", "role": "tool",
		"text": "full " + benchmarkToken,
		"details": map[string]any{
			"credential":              modelKey,
			benchmarkToken + "-field": []any{"keep-sk-abcdefghijklmnop", benchmarkToken + ":" + modelKey},
		},
		"created_at": createdAt,
	}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/api/projects/project-1/tasks/task-1/transcript/entries/entry-3":
			writeStreamJSON(t, response, detailSource)
		case request.URL.Path == "/api/projects/project-1/tasks/task-1/transcript":
			transcriptRequests = append(transcriptRequests, request.URL.RawQuery)
			switch request.URL.RawQuery {
			case "":
				preview := streamEntry("entry-3", 3, "tool_result", "tool", "preview", createdAt)
				preview["truncated"] = true
				preview["detail"] = "/api/projects/project-1/tasks/task-1/transcript/entries/entry-3"
				writeStreamPage(t, response, 4, true, preview,
					streamEntry("entry-4", 4, "message", "assistant", "saw "+benchmarkToken, createdAt))
			case "before=3":
				// A concurrent Event advanced this page's cursor to 5. The
				// initial snapshot boundary remains 4.
				writeStreamPage(t, response, 5, false,
					streamEntry("goal", 0, "message", "user", "goal", createdAt),
					streamEntry("entry-1", 1, "continuation", "system", "started", createdAt),
					streamEntry("entry-2a", 2, "tool_call", "assistant", "call", createdAt),
					streamEntry("entry-2b", 2, "tool_result", "tool", "result", createdAt))
			case "after=4":
				writeStreamPage(t, response, 5, false,
					streamEntry("entry-5", 5, "runtime_output", "runtime", "new while paging", createdAt))
			case "after=5":
				writeStreamPage(t, response, 5, false)
			case "after=6":
				writeStreamPage(t, response, 6, false)
			default:
				t.Fatalf("unexpected transcript query %q", request.URL.RawQuery)
			}
		case request.URL.Path == "/api/projects/project-1/tasks/task-1":
			writeStreamJSON(t, response, map[string]any{"status": "failed"})
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	})
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.RawQuery == "after=5" && strings.HasSuffix(request.URL.Path, "/transcript") {
			seen := 0
			for _, query := range transcriptRequests {
				if query == "after=5" {
					seen++
				}
			}
			if seen > 0 {
				recorder := httptest.NewRecorder()
				writeStreamPage(t, recorder, 6, false,
					streamEntry("entry-6", 6, "continuation", "system", "failed "+modelKey, createdAt))
				return recorder.Result(), nil
			}
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", Client: &http.Client{Transport: transport}, PollPeriod: time.Millisecond,
	})
	var stdout bytes.Buffer
	err := app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, []string{benchmarkToken, modelKey})
	if err == nil || !strings.Contains(err.Error(), "hosted Runtime failed") {
		t.Fatalf("Wait error = %v, want failure after final drain", err)
	}
	entries := decodeStreamJSONL(t, stdout.Bytes())
	wantIDs := []string{"goal", "entry-1", "entry-2a", "entry-2b", "entry-3", "entry-4", "entry-5", "entry-6"}
	if len(entries) != len(wantIDs) {
		t.Fatalf("JSONL entries = %d, want %d: %s", len(entries), len(wantIDs), stdout.String())
	}
	for index, want := range wantIDs {
		if entries[index]["id"] != want {
			t.Fatalf("entry %d id = %v, want %q", index, entries[index]["id"], want)
		}
	}
	for _, secret := range []string{benchmarkToken, modelKey} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("stdout disclosed exact secret %q", secret)
		}
	}
	if !strings.Contains(stdout.String(), "keep-sk-abcdefghijklmnop") {
		t.Fatalf("exact masking changed unrelated content: %s", stdout.String())
	}
	if entries[4]["text"] != "full [REDACTED]" || entries[4]["truncated"] != nil || entries[4]["detail"] != nil {
		t.Fatalf("detail entry was not emitted complete: %#v", entries[4])
	}
	if detailSource["text"] != "full "+benchmarkToken {
		t.Fatalf("masking mutated retained source: %#v", detailSource)
	}
	if got := strings.Join(transcriptRequests, ","); got != ",before=3,after=4,after=5,after=6" {
		t.Fatalf("transcript requests = %q", got)
	}
}

func TestHTTPAppWaitStreamsEveryEntryOnceAcrossThreeBackwardPages(t *testing.T) {
	createdAt := "2026-08-12T00:00:00Z"
	var transcriptRequests []string
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/transcript") {
			transcriptRequests = append(transcriptRequests, request.URL.RawQuery)
			switch request.URL.RawQuery {
			case "":
				writeStreamPage(t, response, 8, true,
					streamEntry("entry-7", 7, "message", "assistant", "seven", createdAt),
					streamEntry("entry-8", 8, "message", "assistant", "eight", createdAt))
			case "before=7":
				// BuildWindow includes the synthetic Task Goal on every
				// backward page. More retained Event history still exists.
				writeStreamPage(t, response, 8, true,
					streamEntry("goal", 0, "message", "user", "goal", createdAt),
					streamEntry("entry-4", 4, "message", "assistant", "four", createdAt),
					streamEntry("entry-5", 5, "message", "assistant", "five", createdAt),
					streamEntry("entry-6", 6, "message", "assistant", "six", createdAt))
			case "before=4":
				writeStreamPage(t, response, 8, false,
					streamEntry("goal", 0, "message", "user", "goal", createdAt),
					streamEntry("entry-1", 1, "message", "assistant", "one", createdAt),
					streamEntry("entry-2", 2, "message", "assistant", "two", createdAt),
					streamEntry("entry-3", 3, "message", "assistant", "three", createdAt))
			case "after=8":
				writeStreamPage(t, response, 8, false)
			default:
				t.Fatalf("unexpected transcript query %q", request.URL.RawQuery)
			}
			return
		}
		writeStreamJSON(t, response, map[string]any{"status": "failed"})
	})

	var stdout bytes.Buffer
	err := newTranscriptHTTPApp(handler).Wait(context.Background(), hostedcontroller.HostedEvaluationReference{
		ProjectID: "project-1", TaskID: "task-1",
	}, &stdout, nil)
	if err == nil || !strings.Contains(err.Error(), "hosted Runtime failed") {
		t.Fatalf("Wait error = %v, want Runtime failure after Transcript drain", err)
	}
	entries := decodeStreamJSONL(t, stdout.Bytes())
	wantIDs := []string{"goal", "entry-1", "entry-2", "entry-3", "entry-4", "entry-5", "entry-6", "entry-7", "entry-8"}
	if len(entries) != len(wantIDs) {
		t.Fatalf("JSONL entries = %d, want %d: %s", len(entries), len(wantIDs), stdout.String())
	}
	for index, want := range wantIDs {
		if entries[index]["id"] != want {
			t.Fatalf("entry %d id = %v, want %q", index, entries[index]["id"], want)
		}
	}
	if got := strings.Join(transcriptRequests, ","); got != ",before=7,before=4,after=8,after=8" {
		t.Fatalf("transcript requests = %q", got)
	}
}

func TestHTTPAppWaitCommitsEmptyTranscriptCursorProgress(t *testing.T) {
	var cursors []string
	ctx, cancel := context.WithCancel(context.Background())
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/transcript") {
			after, set := request.URL.Query()["after"]
			if !set {
				writeStreamPage(t, response, 0, false)
				return
			}
			cursors = append(cursors, after[0])
			switch after[0] {
			case "0":
				writeStreamPage(t, response, 5, false)
			case "5":
				writeStreamPage(t, response, 6, false, streamEntry("entry-6", 6, "message", "assistant", "visible", "2026-08-12T00:00:00Z"))
			case "6":
				writeStreamPage(t, response, 6, false)
			}
			return
		}
		writeStreamJSON(t, response, map[string]any{"status": "running"})
		cancel()
	})
	var stdout bytes.Buffer
	if err := newTranscriptHTTPApp(handler).Wait(ctx, hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cursors, ","); got != "0,5,6" {
		t.Fatalf("after cursors = %q, want 0,5,6", got)
	}
	entries := decodeStreamJSONL(t, stdout.Bytes())
	if len(entries) != 1 || entries[0]["id"] != "entry-6" {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestHTTPAppWaitDoesNotEmitPreviewWhenTranscriptDetailFails(t *testing.T) {
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/transcript") {
			entry := streamEntry("large", 1, "tool_result", "tool", "preview", "2026-08-12T00:00:00Z")
			entry["truncated"] = true
			entry["detail"] = "/api/projects/project-1/tasks/task-1/transcript/entries/large"
			writeStreamPage(t, response, 1, false, entry)
			return
		}
		http.Error(response, "detail unavailable", http.StatusInternalServerError)
	})
	var stdout bytes.Buffer
	err := newTranscriptHTTPApp(handler).Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, nil)
	if err == nil || !strings.Contains(err.Error(), "detail") || stdout.Len() != 0 {
		t.Fatalf("error=%v stdout=%q", err, stdout.String())
	}
}

func TestHTTPAppWaitReturnsTranscriptAndStdoutFailures(t *testing.T) {
	t.Run("Transcript API", func(t *testing.T) {
		app := newTranscriptHTTPApp(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			http.Error(response, "broken", http.StatusInternalServerError)
		}))
		err := app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, io.Discard, nil)
		if err == nil || !strings.Contains(err.Error(), "Transcript") {
			t.Fatalf("Wait error = %v", err)
		}
	})
	t.Run("stdout", func(t *testing.T) {
		handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/transcript") {
				writeStreamPage(t, response, 1, false, streamEntry("entry-1", 1, "message", "assistant", "hello", "2026-08-12T00:00:00Z"))
			}
		})
		err := newTranscriptHTTPApp(handler).Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, streamFailWriter{}, nil)
		if err == nil || !strings.Contains(err.Error(), "stdout") {
			t.Fatalf("Wait error = %v", err)
		}
	})
}

func TestHTTPAppWaitRejectsInvalidTranscriptDetailReference(t *testing.T) {
	for _, detail := range []string{
		"http://attacker.test/api/projects/project-1/tasks/task-1/transcript/entries/large",
		"/api/projects/other/tasks/task-1/transcript/entries/large",
	} {
		t.Run(detail, func(t *testing.T) {
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/transcript") {
					entry := streamEntry("large", 1, "tool_result", "tool", "preview", "2026-08-12T00:00:00Z")
					entry["truncated"] = true
					entry["detail"] = detail
					writeStreamPage(t, response, 1, false, entry)
					return
				}
				t.Fatal("invalid detail reference was followed")
			})
			var stdout bytes.Buffer
			err := newTranscriptHTTPApp(handler).Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, nil)
			if err == nil || !strings.Contains(err.Error(), "invalid detail reference") || stdout.Len() != 0 {
				t.Fatalf("error=%v stdout=%q", err, stdout.String())
			}
		})
	}
}

type streamFailWriter struct{}

func (streamFailWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func newTranscriptHTTPApp(handler http.Handler) *hostedcontroller.HTTPApp {
	return hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", Client: transcriptHTTPClient(handler), PollPeriod: time.Millisecond,
	})
}

func transcriptHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})}
}

func streamEntry(id string, seq int, kind, role, text, createdAt string) map[string]any {
	return map[string]any{
		"id": id, "seq": seq, "continuation": 1, "kind": kind, "role": role,
		"text": text, "created_at": createdAt,
	}
}

func writeStreamPage(t *testing.T, writer io.Writer, cursor int, hasOlder bool, entries ...map[string]any) {
	t.Helper()
	writeStreamJSON(t, writer, map[string]any{
		"task_id": "task-1", "entries": entries, "cursor": cursor, "has_older": hasOlder,
	})
}

func writeStreamJSON(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func decodeStreamJSONL(t *testing.T, output []byte) []map[string]any {
	t.Helper()
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return nil
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	entries := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("JSONL line %d is invalid: %v: %q", index+1, err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}
