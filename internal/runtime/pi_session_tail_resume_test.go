package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

func TestPiSessionTailResumeReplaysOnlyUnretainedRecords(t *testing.T) {
	fixture, err := os.ReadFile("testdata/pi-resume-retained.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(fixture)), "\n")
	for _, tc := range []struct {
		name           string
		retained, want []string
	}{
		{"durable stop", lines[:4], lines[4:]},
		{"crash before final record persisted", lines[:3], lines[3:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "retained.jsonl")
			for _, line := range lines[:4] {
				writeSessionLine(t, path, line)
			}
			inner := funcInnerAdapter{run: func(context.Context, func(task.EventKind, task.EventPayload)) error {
				for _, line := range append(slices.Clone(lines[4:]), lines[len(lines)-1]) {
					writeSessionLine(t, path, line)
				}
				return nil
			}}
			adapter := runtime.NewPiSessionTailAdapter(inner, directory, tc.retained...)
			var got []string
			if err := adapter.Run(context.Background(), "resume", func(_ task.EventKind, payload task.EventPayload) {
				got = append(got, payload["text"].(string))
			}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("resumed Pi output = %q, want only unretained records %q", got, tc.want)
			}
		})
	}
}
