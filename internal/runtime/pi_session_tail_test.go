package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

// fakeInnerAdapter is a no-op adapter used to isolate the tail decorator.
type fakeInnerAdapter struct{}

func (fakeInnerAdapter) Name() string { return "pi" }
func (fakeInnerAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	<-ctx.Done()
	return ctx.Err()
}

func collectEmits(emit func(task.EventKind, task.EventPayload)) (emitFunc func(task.EventKind, task.EventPayload), get func() []recordedEmit, mu *sync.Mutex) {
	var recorded []recordedEmit
	var m sync.Mutex
	emitFunc = func(kind task.EventKind, payload task.EventPayload) {
		m.Lock()
		defer m.Unlock()
		recorded = append(recorded, recordedEmit{kind: kind, payload: payload})
		emit(kind, payload)
	}
	get = func() []recordedEmit {
		m.Lock()
		defer m.Unlock()
		out := make([]recordedEmit, len(recorded))
		copy(out, recorded)
		return out
	}
	return emitFunc, get, &m
}

type recordedEmit struct {
	kind    task.EventKind
	payload task.EventPayload
}

func writeSessionLine(t *testing.T, path, line string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open session file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write session line: %v", err)
	}
}

// TestPiSessionTailEmitsAppendedLines proves the tail decorator reads pi
// session jsonl lines appended after launch and re-emits each as a
// runtime_output event carrying the raw JSON, so the existing transcript
// parser converts it like stdout output.
func TestPiSessionTailEmitsAppendedLines(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")

	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})

	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	// The session dir + file do not exist yet; create them then append lines.
	sessionFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_abc.jsonl")
	writeSessionLine(t, sessionFile, `{"type":"session","version":3}`)
	writeSessionLine(t, sessionFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`)

	waitForCount(t, getEmits, 2, 2*time.Second)
	got := getEmits()
	if len(got) < 2 {
		t.Fatalf("expected at least 2 emits, got %d", len(got))
	}
	for _, e := range got {
		if e.kind != task.EventKindRuntimeOutput {
			t.Fatalf("expected runtime_output kind, got %q", e.kind)
		}
		if stream, _ := e.payload["stream"].(string); stream != "pi_session" {
			t.Fatalf("expected stream pi_session, got %q", stream)
		}
	}
}

func TestPiSessionTailRecordsNativeSessionFromSessionHeader(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")

	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	recorder, ok := adapter.(interface {
		SetMetadataRecorder(func(runtime.NativeSessionMetadata) error)
	})
	if !ok {
		t.Fatal("expected pi tail adapter to support metadata recording")
	}
	var recorded runtime.NativeSessionMetadata
	var mu sync.Mutex
	recorder.SetMetadataRecorder(func(metadata runtime.NativeSessionMetadata) error {
		mu.Lock()
		defer mu.Unlock()
		recorded = metadata
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitCalls, _, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	sessionFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_abc.jsonl")
	writeSessionLine(t, sessionFile, `{"type":"session","version":3,"id":"sess-pi","cwd":"/task/workdir"}`)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := recorded.NativeSessionID
		mu.Unlock()
		if got == "sess-pi" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("expected captured pi session id, got %#v", recorded)
}

// TestPiSessionTailStopsOnContextCancel proves the tail goroutine exits when
// the run context is cancelled (i.e. when the task is stopped).
func TestPiSessionTailStopsOnContextCancel(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")

	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())

	emitCalls, _, _ := collectEmits(func(task.EventKind, task.EventPayload) {})

	done := make(chan struct{})
	go func() {
		_ = adapter.Run(ctx, "goal", emitCalls)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// adapter returned on cancel, as required
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not return after context cancel")
	}
}

func waitForCount(t *testing.T, get func() []recordedEmit, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(get()) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d emits, got %d", want, len(get()))
}
