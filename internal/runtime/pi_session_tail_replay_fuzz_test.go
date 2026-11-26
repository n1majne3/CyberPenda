package runtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

func FuzzPiSessionResumeReplay(f *testing.F) {
	fixture, err := os.ReadFile("testdata/pi-resume-retained.jsonl")
	if err != nil {
		f.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(fixture)), "\n")
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{5, 5, 4, 1, 3, 0, 2, 4})
	f.Add([]byte{1, 3, 2})
	f.Fuzz(func(t *testing.T, arrival []byte) {
		if len(arrival) > 128 {
			arrival = arrival[:128]
		}
		directory := t.TempDir()
		path := filepath.Join(directory, "replay.jsonl")
		writeSessionLine(t, path, lines[0])
		for _, index := range arrival {
			writeSessionLine(t, path, lines[int(index)%len(lines)])
		}
		retained := slices.Clone(lines[:4])
		var output []string
		replay := func() {
			t.Helper()
			adapter := runtime.NewPiSessionTailAdapter(funcInnerAdapter{run: func(context.Context, func(task.EventKind, task.EventPayload)) error {
				return nil
			}}, directory, retained...)
			var batch []string
			if err := adapter.Run(context.Background(), "resume", func(_ task.EventKind, payload task.EventPayload) {
				batch = append(batch, payload["text"].(string))
			}); err != nil {
				t.Fatal(err)
			}
			retained = append(retained, batch...)
			output = append(output, batch...)
		}
		replay()
		for _, line := range lines {
			writeSessionLine(t, path, line)
		}
		replay()
		if len(output) != 2 {
			t.Fatalf("reconnect replay emitted %d records, want exactly two new native messages", len(output))
		}
		want := map[string]string{"8ad02701": "user", "af6d826a": "assistant"}
		for _, line := range output {
			var record struct {
				ID      string `json:"id"`
				Message struct {
					Role string `json:"role"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			if role, exists := want[record.ID]; !exists || record.Message.Role != role {
				t.Fatalf("duplicate, historical, or misattributed native message %q: %s", record.ID, line)
			}
			delete(want, record.ID)
		}
	})
}
