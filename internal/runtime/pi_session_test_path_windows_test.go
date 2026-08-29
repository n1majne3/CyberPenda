//go:build windows

package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

// equivalentSessionPath returns a different native Windows spelling when the
// filesystem exposes one. GitHub's Windows runner gives TempDir an 8.3 path,
// so GetLongPathNameW supplies the real long spelling used by this regression.
func equivalentSessionPath(t *testing.T, path string) string {
	t.Helper()
	utf16, err := windows.UTF16FromString(path)
	if err != nil {
		t.Fatalf("encode Windows session path: %v", err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetLongPathName(&utf16[0], &buf[0], uint32(len(buf)))
		if err != nil {
			t.Fatalf("expand Windows session path %q: %v", path, err)
		}
		if int(n) < len(buf) {
			long := windows.UTF16ToString(buf[:n])
			if long != path {
				return long
			}
			// A case-only alias still proves identity does not depend on the
			// path string when 8.3 names are disabled on the filesystem.
			return strings.ToUpper(path)
		}
		buf = make([]uint16, n+1)
	}
}

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
