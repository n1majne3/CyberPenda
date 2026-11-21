package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

func TestPiSessionTailJoinsLineWrittenInTwoParts(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "--task-workdir--")

	adapter := runtime.NewPiSessionTailAdapter(fakeInnerAdapter{}, sessionDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emitCalls, getEmits, _ := collectEmits(func(task.EventKind, task.EventPayload) {})
	go func() { _ = adapter.Run(ctx, "goal", emitCalls) }()

	first := filepath.Join(sessionDir, "a.jsonl")
	second := filepath.Join(sessionDir, "b.jsonl")
	writeSessionLine(t, first, sessionHeaderLine(t, "first", ""))
	f, err := os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open session file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"type":"message","id":`); err != nil {
		t.Fatalf("write first part: %v", err)
	}
	writeSessionLine(t, second, sessionHeaderLine(t, "second", ""))
	waitForCount(t, getEmits, 2, 2*time.Second)
	if _, err := f.WriteString(`"m1"}` + "\n"); err != nil {
		t.Fatalf("write second part: %v", err)
	}
	waitForCount(t, getEmits, 3, 2*time.Second)
	cancel()

	var texts []string
	for _, e := range getEmits() {
		texts = append(texts, e.payload["text"].(string))
	}
	want := `{"type":"message","id":"m1"}`
	if !slices.Contains(texts, want) {
		t.Fatalf("emitted texts = %q, want the complete line %q", texts, want)
	}
	for _, text := range texts {
		if text == `{"type":"message","id":` || text == `"m1"}` {
			t.Fatalf("emitted a part of a line: %q", texts)
		}
	}
}
