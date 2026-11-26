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
	redacted := slices.Clone(lines[:4])
	redacted[1] = strings.Replace(redacted[1], "BASELINE_PI_READY", "[redacted]", 1)
	for _, tc := range []struct {
		name           string
		retained, want []string
	}{
		{"durable stop", lines[:4], lines[4:]},
		{"crash before final record persisted", lines[:3], lines[3:]},
		{"durable output redaction preserves native identity", redacted, lines[4:]},
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

func TestPiSessionTailPreservesAnonymousRecordMultiplicity(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "anonymous.jsonl")
	header := `{"type":"session","id":"native-anonymous"}`
	line := `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"same answer"}]}}`
	for _, record := range []string{header, line, line, line} {
		writeSessionLine(t, path, record)
	}
	adapter := runtime.NewPiSessionTailAdapter(funcInnerAdapter{run: func(context.Context, func(task.EventKind, task.EventPayload)) error {
		return nil
	}}, directory, header, line)
	var got []string
	if err := adapter.Run(context.Background(), "resume", func(_ task.EventKind, payload task.EventPayload) {
		got = append(got, payload["text"].(string))
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{line, line}) {
		t.Fatalf("anonymous output = %q, want two new occurrences", got)
	}
}
