package runtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
	"pentest/internal/transcript"
)

// fakeInnerAdapter is a no-op adapter used to isolate the tail decorator.
type fakeInnerAdapter struct{}

func (fakeInnerAdapter) Name() string { return "pi" }
func (fakeInnerAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	<-ctx.Done()
	return ctx.Err()
}

// funcInnerAdapter runs an arbitrary function so a test can simulate a runtime
// that exits on its own (normal completion) while the run context stays live.
type funcInnerAdapter struct {
	name string
	run  func(ctx context.Context, emit func(task.EventKind, task.EventPayload)) error
}

func (f funcInnerAdapter) Name() string { return f.name }
func (f funcInnerAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	return f.run(ctx, emit)
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

func TestPiSessionTailProjectsReasoningBlock(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")
	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	sessionFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_abc.jsonl")
	writeSessionLine(t, sessionFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"reasoning","id":"pi-reasoning-1","reasoning":"checking the target"}]}}`)
	waitForCount(t, getEmits, 1, 2*time.Second)

	got := getEmits()
	text, _ := got[0].payload["text"].(string)
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		t.Fatalf("tail output is not provider JSON: %v", err)
	}
	entries := transcript.ParseRecord(record, transcript.Entry{ID: "event-1", Seq: 1, Continuation: 1})
	if len(entries) != 1 || entries[0].Kind != transcript.KindReasoning || entries[0].Text != "checking the target" {
		t.Fatalf("Pi reasoning projection = %#v", entries)
	}
}

func TestPiSessionTailShapeRedactsReasoning(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")
	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	sessionFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_abc.jsonl")
	writeSessionLine(t, sessionFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"reasoning","reasoning":"use bearer secret-pi-token-123456"}]}}`)
	waitForCount(t, getEmits, 1, 2*time.Second)
	text, _ := getEmits()[0].payload["text"].(string)
	if strings.Contains(text, "secret-pi-token-123456") || !strings.Contains(text, "bearer [REDACTED]") {
		t.Fatalf("Pi reasoning was not shape-redacted: %q", text)
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

// TestPiSessionTailDrainsAndStopsWhenInnerReturns proves the tail goroutine
// stops (no leak) and drains the remaining session lines when the inner runtime
// exits normally, even though the harness leaves the run context live on
// completion. Run must not return until the final drain has been emitted.
func TestPiSessionTailDrainsAndStopsWhenInnerReturns(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")
	sessionFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_abc.jsonl")

	inner := funcInnerAdapter{name: "pi", run: func(context.Context, func(task.EventKind, task.EventPayload)) error {
		// Write a line just before exiting and never touch the context, mirroring
		// a persistent Pi session that completes on its own.
		writeSessionLine(t, sessionFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"final"}]}}`)
		return nil
	}}
	adapter := runtime.NewPiSessionTailAdapter(inner, sessionDir)
	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})

	done := make(chan struct{})
	go func() {
		_ = adapter.Run(context.Background(), "goal", emitCalls)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not return after inner runtime exited with a live context")
	}

	// Because Run drains before returning, the final line is deterministically
	// emitted by the time Run returns.
	got := getEmits()
	if len(got) == 0 {
		t.Fatal("expected the final session line to be drained before Run returned")
	}
	for _, e := range got {
		if stream, _ := e.payload["stream"].(string); stream != "pi_session" {
			t.Fatalf("expected stream pi_session, got %q", stream)
		}
	}
}

// TestPiSessionTailFollowsSubagentSessionFiles proves the tailer keeps reading
// the parent session file after a subagent spawns a newer session file, so the
// parent's settle records (subagents:record) are not stranded while the child
// file is newest.
func TestPiSessionTailFollowsSubagentSessionFiles(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")

	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	// Parent session appears first.
	parentFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_parent.jsonl")
	writeSessionLine(t, parentFile, `{"type":"session","version":3,"id":"sess-parent","cwd":"/task/workdir"}`)
	writeSessionLine(t, parentFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"parent working"}]}}`)
	waitForCount(t, getEmits, 2, 2*time.Second)

	// A subagent spawns and writes a NEWER session file.
	childFile := filepath.Join(sessionDir, "2026-06-19T12-12-30-500Z_child.jsonl")
	writeSessionLine(t, childFile, `{"type":"session","version":3,"id":"sess-child","parentSession":"`+parentFile+`"}`)
	waitForCount(t, getEmits, 3, 2*time.Second)

	// While the child file is newest, the parent settles the subagent. That
	// record must still be observed.
	before := len(getEmits())
	writeSessionLine(t, parentFile, `{"type":"custom","customType":"subagents:record","data":{"id":"agent-1","type":"Explore","description":"Scan","status":"completed"}}`)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		emits := getEmits()
		for _, e := range emits[before:] {
			if text, _ := e.payload["text"].(string); strings.Contains(text, "subagents:record") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("parent subagents:record was stranded once the child file became newest; emits=%d", len(getEmits()))
}

// TestPiSessionTailDoesNotDuplicateOnStableNewest proves a line already read
// from the newest file is not re-emitted on later polls (offset bookkeeping is
// per-file and stable).
func TestPiSessionTailDoesNotDuplicateOnStableNewest(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")
	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	sessionFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_abc.jsonl")
	writeSessionLine(t, sessionFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"once"}]}}`)
	waitForCount(t, getEmits, 1, 2*time.Second)

	// Let several poll intervals pass with no new line; the count must stay 1.
	time.Sleep(400 * time.Millisecond)
	if got := len(getEmits()); got != 1 {
		t.Fatalf("expected exactly 1 emit with no new lines, got %d", got)
	}
}

// TestPiSessionTailSkipsDeeplyNestedSessionFiles proves the tailer does not
// open session files for nested (grandchild) subagents. The subagents
// extension only emits settle records for top-level agents, so following
// deeper files would only grow open file handles without adding attribution.
func TestPiSessionTailSkipsDeeplyNestedSessionFiles(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")
	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	parentFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_parent.jsonl")
	writeSessionLine(t, parentFile, `{"type":"session","version":3,"id":"sess-parent","cwd":"/task/workdir"}`)
	waitForCount(t, getEmits, 1, 2*time.Second)

	childFile := filepath.Join(sessionDir, "2026-06-19T12-12-30-500Z_child.jsonl")
	writeSessionLine(t, childFile, `{"type":"session","version":3,"id":"sess-child","parentSession":"`+parentFile+`"}`)
	waitForCount(t, getEmits, 2, 2*time.Second)

	// A grandchild (nested) subagent file appears. Its transcript must not be
	// tailed.
	grandchildFile := filepath.Join(sessionDir, "2026-06-19T12-13-40-600Z_grandchild.jsonl")
	before := len(getEmits())
	writeSessionLine(t, grandchildFile, `{"type":"session","version":3,"id":"sess-grandchild","parentSession":"`+childFile+`"}`)
	writeSessionLine(t, grandchildFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"nested work"}]}}`)

	// Allow several poll intervals; the grandchild lines must never appear.
	time.Sleep(400 * time.Millisecond)
	for _, e := range getEmits()[before:] {
		if text, _ := e.payload["text"].(string); strings.Contains(text, "sess-grandchild") || strings.Contains(text, "nested work") {
			t.Fatalf("nested subagent session file should not be tailed, got %q", text)
		}
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
