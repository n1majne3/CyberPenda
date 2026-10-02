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
	wantIDs := []string{"entry-1", "entry-2a", "entry-2b", "entry-3", "entry-5", "entry-6"}
	if len(entries) != len(wantIDs) {
		t.Fatalf("JSONL entries = %d, want %d: %s", len(entries), len(wantIDs), stdout.String())
	}
	for index, want := range wantIDs {
		if entries[index]["id"] != want {
			t.Fatalf("entry %d id = %v, want %q", index, entries[index]["id"], want)
		}
	}
	if strings.Contains(stdout.String(), "saw ") || strings.Contains(stdout.String(), `"kind":"message"`) {
		t.Fatalf("LLM conversation lines reached hosted stdout: %s", stdout.String())
	}
	for _, secret := range []string{benchmarkToken, modelKey} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("stdout disclosed exact secret %q: %s", secret, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), "keep-sk-abcdefghijklmnop") {
		t.Fatalf("exact masking changed unrelated content: %s", stdout.String())
	}
	if entries[3]["text"] != "full [REDACTED]" || entries[3]["truncated"] != nil || entries[3]["detail"] != nil {
		t.Fatalf("detail entry was not emitted complete: %#v", entries[3])
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
					streamEntry("entry-7", 7, "tool_call", "assistant", "seven", createdAt),
					streamEntry("entry-8", 8, "tool_call", "assistant", "eight", createdAt))
			case "before=7":
				// BuildWindow includes the synthetic Task Goal on every
				// backward page. More retained Event history still exists.
				writeStreamPage(t, response, 8, true,
					streamEntry("goal", 0, "tool_call", "user", "goal", createdAt),
					streamEntry("entry-4", 4, "tool_call", "assistant", "four", createdAt),
					streamEntry("entry-5", 5, "tool_call", "assistant", "five", createdAt),
					streamEntry("entry-6", 6, "tool_call", "assistant", "six", createdAt))
			case "before=4":
				writeStreamPage(t, response, 8, false,
					streamEntry("goal", 0, "tool_call", "user", "goal", createdAt),
					streamEntry("entry-1", 1, "tool_call", "assistant", "one", createdAt),
					streamEntry("entry-2", 2, "tool_call", "assistant", "two", createdAt),
					streamEntry("entry-3", 3, "tool_call", "assistant", "three", createdAt))
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
				writeStreamPage(t, response, 6, false, streamEntry("entry-6", 6, "tool_call", "assistant", "visible", "2026-08-12T00:00:00Z"))
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

func TestHTTPAppWaitKeepsALiveRuntimeAfterStdoutAndTaskFailures(t *testing.T) {
	t.Run("stdout after running", func(t *testing.T) {
		var writes int
		var diagnostics bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/transcript") {
				if _, set := request.URL.Query()["after"]; !set {
					writeStreamPage(t, response, 0, false)
					return
				}
				writeStreamPage(t, response, 1, false, streamEntry("entry-1", 1, "tool_call", "assistant", "live", "2026-08-12T00:00:00Z"))
				return
			}
			writeStreamJSON(t, response, map[string]any{"status": "running"})
		})
		writer := &countingFailWriter{failAfter: 1, writes: &writes}
		done := make(chan error, 1)
		go func() {
			done <- newTranscriptHTTPAppWithDiagnostics(handler, &diagnostics).Wait(ctx, hostedcontroller.HostedEvaluationReference{
				ProjectID: "project-1", TaskID: "task-1",
			}, writer, nil)
		}()
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			if writes >= 1 && strings.Contains(diagnostics.String(), "stdout") {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		select {
		case err := <-done:
			t.Fatalf("Wait returned while the Runtime was live: %v", err)
		default:
		}
		cancel()
		err := <-done
		if err != nil {
			t.Fatalf("Wait after platform end = %v", err)
		}
		if !strings.Contains(diagnostics.String(), "stdout") {
			t.Fatalf("diagnostics = %q, want stdout operational error", diagnostics.String())
		}
	})
	t.Run("failed status after running", func(t *testing.T) {
		var sawFailed int
		var diagnostics bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/transcript") {
				writeStreamPage(t, response, 0, false)
				return
			}
			if sawFailed == 0 {
				writeStreamJSON(t, response, map[string]any{"status": "running"})
				sawFailed = 1
				return
			}
			sawFailed++
			writeStreamJSON(t, response, map[string]any{"status": "failed"})
		})
		done := make(chan error, 1)
		go func() {
			done <- newTranscriptHTTPAppWithDiagnostics(handler, &diagnostics).Wait(ctx, hostedcontroller.HostedEvaluationReference{
				ProjectID: "project-1", TaskID: "task-1",
			}, io.Discard, nil)
		}()
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) && sawFailed < 2 {
			time.Sleep(2 * time.Millisecond)
		}
		select {
		case err := <-done:
			t.Fatalf("Wait returned after a live Runtime failed: %v", err)
		default:
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Wait after platform end = %v", err)
		}
	})
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
				writeStreamPage(t, response, 1, false, streamEntry("entry-1", 1, "tool_call", "assistant", "hello", "2026-08-12T00:00:00Z"))
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

type countingFailWriter struct {
	failAfter int
	writes    *int
}

func (w *countingFailWriter) Write([]byte) (int, error) {
	*w.writes++
	if *w.writes >= w.failAfter {
		return 0, errors.New("write failed")
	}
	return 1, nil
}

func newTranscriptHTTPApp(handler http.Handler) *hostedcontroller.HTTPApp {
	return newTranscriptHTTPAppWithDiagnostics(handler, nil)
}

func newTranscriptHTTPAppWithDiagnostics(handler http.Handler, diagnostics io.Writer) *hostedcontroller.HTTPApp {
	return hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", Client: transcriptHTTPClient(handler), PollPeriod: time.Millisecond,
		Diagnostics: diagnostics,
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

// TestHostedTranscriptOmitsLLMConversationKinds locks the stdout contract:
// message and reasoning entries never reach hosted stdout, a truncated message
// never triggers a detail fetch, and non-conversation kinds still stream.
func TestHostedTranscriptOmitsLLMConversationKinds(t *testing.T) {
	const base = "/api/projects/project-1/tasks/task-1"
	detailReads := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/transcript/entries/llm-big"):
			detailReads++
			writeStreamJSON(t, w, streamEntry("llm-big", 3, "message", "assistant", "huge model answer", "2026-09-28T00:00:00Z"))
		case r.URL.Path == base+"/transcript":
			if r.URL.Query().Has("after") {
				writeStreamPage(t, w, 6, false)
				return
			}
			msg := streamEntry("llm-big", 3, "message", "assistant", "preview", "2026-09-28T00:00:00Z")
			msg["truncated"] = true
			msg["detail"] = base + "/transcript/entries/llm-big"
			writeStreamPage(t, w, 6, false,
				streamEntry("u-1", 1, "message", "user", "operator prompt", "2026-09-28T00:00:00Z"),
				streamEntry("r-1", 2, "reasoning", "assistant", "thinking", "2026-09-28T00:00:00Z"),
				msg,
				streamEntry("t-1", 4, "tool_call", "assistant", "call", "2026-09-28T00:00:00Z"),
				streamEntry("t-2", 5, "tool_result", "tool", "result", "2026-09-28T00:00:00Z"))
		case r.URL.Path == base:
			writeStreamJSON(t, w, map[string]any{"status": "failed"})
		default:
			http.NotFound(w, r)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", Client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Result(), nil
		})}, PollPeriod: time.Millisecond,
	})
	var stdout bytes.Buffer
	if err := app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, nil); err == nil || !strings.Contains(err.Error(), "hosted Runtime failed") {
		t.Fatalf("Wait = %v", err)
	}
	entries := decodeStreamJSONL(t, stdout.Bytes())
	wantIDs := []string{"t-1", "t-2"}
	if len(entries) != len(wantIDs) {
		t.Fatalf("entries = %d, want %d: %s", len(entries), len(wantIDs), stdout.String())
	}
	for i, want := range wantIDs {
		if entries[i]["id"] != want {
			t.Fatalf("entry %d = %v, want %q", i, entries[i]["id"], want)
		}
	}
	if detailReads != 0 {
		t.Fatalf("truncated message triggered %d detail fetches", detailReads)
	}
	if strings.Contains(stdout.String(), "operator prompt") || strings.Contains(stdout.String(), "thinking") || strings.Contains(stdout.String(), "huge model answer") {
		t.Fatalf("LLM conversation text reached stdout: %s", stdout.String())
	}
}

// TestHTTPAppWaitRevivesSilentOrchestrator pins the hosted silence watchdog
// (provider-resilience B-layer): when a running Task produces no Transcript
// progress for SilenceReviveSec, Wait sends exactly one revive steering per
// cooldown window and stops after the revive cap. Both 24147 and 24160 died
// this way — the orchestrator turn ended (degenerate output / provider
// outage beyond the retry budget) and nothing ever started a new turn.
func TestHTTPAppWaitRevivesSilentOrchestrator(t *testing.T) {
	const base = "/api/projects/project-1/tasks/task-1"
	var steers []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == base+"/steer" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			steers = append(steers, string(body))
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == base+"/transcript":
			if r.URL.Query().Has("after") {
				writeStreamPage(t, w, 1, false)
				return
			}
			// one initial entry, then the stream goes silent forever
			writeStreamPage(t, w, 1, false,
				streamEntry("entry-1", 1, "tool_result", "tool", "boot", "2026-10-01T03:00:00Z"))
		case r.URL.Path == base:
			writeStreamJSON(t, w, map[string]any{"status": "running"})
		default:
			http.NotFound(w, r)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", PollPeriod: 20 * time.Millisecond,
		SilenceReviveSec: 1, SilenceReviveMax: 2, SilenceReviveCooldownSec: 1,
		Client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Result(), nil
		})},
	})
	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(steers) < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Wait returned while running: %v", err)
	default:
	}
	if len(steers) < 2 {
		t.Fatalf("silence revives = %d, want ≥2 (cooldown respected, cap not hit early): %v", len(steers), steers)
	}
	if len(steers) > 3 {
		t.Fatalf("revive spam: %d steers in window, cooldown not respected", len(steers))
	}
	if !strings.Contains(steers[0], "watchdog") || !strings.Contains(steers[0], "ctf-orchestrator") {
		t.Fatalf("revive payload missing watchdog identity/skill reference: %s", steers[0])
	}
	// Run 24370 evidence: in_turn_steer mode failed on a parked turn four
	// times out of five; only interrupt_then_replace actually revived the
	// orchestrator. The watchdog must request the replacement mode.
	if !strings.Contains(steers[0], `"force_replace":true`) {
		t.Fatalf("revive payload must force interrupt_then_replace: %s", steers[0])
	}
}

// TestHTTPAppWaitAutoRespondsPendingPermissionDialog pins the hosted
// permission auto-responder: a pending permission dialog parks the provider
// turn until an extension_ui_response arrives, and run 24370 stayed parked
// for 5h45m because no operator exists in hosted evaluation. Wait answers
// each pending dialog once with the configured decision.
func TestHTTPAppWaitAutoRespondsPendingPermissionDialog(t *testing.T) {
	const base = "/api/projects/project-1/tasks/task-1"
	var responds []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == base+"/permissions/uuid-2/respond" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			responds = append(responds, r.URL.Path+" "+string(body))
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == base+"/transcript":
			if r.URL.Query().Has("after") {
				writeStreamPage(t, w, 2, false)
				return
			}
			dialog := streamEntry("entry-1", 1, "continuation", "system", "Provider permission requested", "2026-10-02T00:06:11Z")
			dialog["details"] = map[string]any{
				"mode": "permission_response", "outcome": "requested",
				"permission_request_id": "uuid-2", "permission_title": "Trust this project?",
				"permission_method": "confirm", "provider": "pi",
			}
			writeStreamPage(t, w, 2, false, dialog)
		case r.URL.Path == base:
			writeStreamJSON(t, w, map[string]any{"status": "running"})
		default:
			http.NotFound(w, r)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", PollPeriod: 20 * time.Millisecond,
		Client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Result(), nil
		})},
	})
	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(responds) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Wait returned while running: %v", err)
	default:
	}
	if len(responds) == 0 {
		t.Fatal("pending permission dialog was never answered")
	}
	if !strings.Contains(responds[0], `"decision":"allow"`) || !strings.Contains(responds[0], "uuid-2") {
		t.Fatalf("permission response payload = %s", responds[0])
	}
	// One answer per dialog id: re-delivered pending events must not
	// re-respond.
	time.Sleep(300 * time.Millisecond)
	if len(responds) != 1 {
		t.Fatalf("permission dialog answered %d times, want exactly 1: %v", len(responds), responds)
	}
}

// With auto-respond off, a pending dialog must stay unanswered and surface
// in the operational log instead.
func TestHTTPAppWaitPermissionAutoRespondOff(t *testing.T) {
	const base = "/api/projects/project-1/tasks/task-1"
	var responds int
	var diagnostics bytes.Buffer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == base+"/permissions/uuid-2/respond" && r.Method == http.MethodPost:
			responds++
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == base+"/transcript":
			if r.URL.Query().Has("after") {
				writeStreamPage(t, w, 2, false)
				return
			}
			dialog := streamEntry("entry-1", 1, "continuation", "system", "Provider permission requested", "2026-10-02T00:06:11Z")
			dialog["details"] = map[string]any{
				"mode": "permission_response", "outcome": "requested",
				"permission_request_id": "uuid-2", "permission_title": "Trust this project?",
			}
			writeStreamPage(t, w, 2, false, dialog)
		case r.URL.Path == base:
			writeStreamJSON(t, w, map[string]any{"status": "running"})
		default:
			http.NotFound(w, r)
		}
	})
	app := hostedcontroller.NewHTTPApp(hostedcontroller.HTTPAppConfig{
		BaseURL: "http://hosted.test", PollPeriod: 20 * time.Millisecond, Diagnostics: &diagnostics,
		PermissionAutoRespond: "off",
		Client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Result(), nil
		})},
	})
	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- app.Wait(context.Background(), hostedcontroller.HostedEvaluationReference{ProjectID: "project-1", TaskID: "task-1"}, &stdout, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(diagnostics.String(), "permission") {
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Wait returned while running: %v", err)
	default:
	}
	if responds != 0 {
		t.Fatalf("auto-respond off still answered %d times", responds)
	}
	if !strings.Contains(diagnostics.String(), "Trust this project?") {
		t.Fatalf("unanswered dialog not surfaced in operational log: %s", diagnostics.String())
	}
}
