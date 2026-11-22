package transcript_test

import (
	"strings"
	"testing"
	"time"

	"pentest/internal/transcript"
)

func TestBuildDropsPiSessionUserMessageAndKeepsAssistantReply(t *testing.T) {
	createdAt := time.Now().UTC()
	subject := transcript.Subject{ID: "session-1", Title: "QA run", CreatedAt: createdAt}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:pi-1"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"stream": "pi_session",
			"text":   `{"type":"message","id":"a89ef71f","message":{"role":"user","content":[{"type":"text","text":"Reply only with CYBERPENDA-QA-READY.\n\nYou may use a state file to keep a lightweight work trace."}]}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"stream": "pi_session",
			"text":   `{"type":"message","id":"b90ef82e","message":{"role":"assistant","content":[{"type":"text","text":"CYBERPENDA-QA-READY"}]}}`,
		}, CreatedAt: createdAt.Add(2 * time.Second)},
	}

	got := transcript.Build(subject, events)

	requireEntry(t, got, "ev-3-message-0", "message", "assistant", "CYBERPENDA-QA-READY")
	for _, entry := range got {
		if strings.Contains(entry.Text, "You may use a state file to keep a lightweight work trace.") {
			t.Fatalf("pi_session user prompt leaked into transcript: %#v", entry)
		}
		if entry.ID == "ev-2-runtime" {
			t.Fatalf("pi_session user message collapsed into raw runtime fallback: %#v", entry)
		}
	}
}
