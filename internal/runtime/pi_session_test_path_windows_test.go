//go:build windows

package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

func TestPiSessionTailDoesNotReopenWindowsHardLinkAlias(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	sessionFile := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_session-with-long-name.jsonl")
	writeSessionLine(t, sessionFile, sessionHeaderLine(t, "sess-hard-link", ""))
	writeSessionLine(t, sessionFile, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"once through aliases"}]}}`)

	hardLink := filepath.Join(sessionDir, "2026-06-19T12-11-46-221Z_hard-link.jsonl")
	if err := os.Link(sessionFile, hardLink); err != nil {
		t.Fatalf("create Windows hard-link alias: %v", err)
	}

	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	waitForCount(t, getEmits, 2, 2*time.Second)
	time.Sleep(400 * time.Millisecond)
	if got := len(getEmits()); got != 2 {
		t.Fatalf("one physical session file was emitted through aliases: got %d records, want 2", got)
	}
}
