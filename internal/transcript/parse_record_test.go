package transcript_test

import (
	"testing"
	"time"

	"pentest/internal/transcript"
)

func TestParseRecordPiSessionLines(t *testing.T) {
	base := time.Date(2026, 6, 19, 12, 11, 46, 0, time.UTC)

	cases := []struct {
		name   string
		record map[string]any
		check  func(t *testing.T, entries []transcript.Entry)
	}{
		{
			name: "assistant message with text and tool call",
			record: map[string]any{
				"type":      "message",
				"timestamp": "2026-06-19T12:12:01.077Z",
				"message": map[string]any{
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "text", "text": "Let me open the app."},
						map[string]any{"type": "toolCall", "id": "call_00_abc", "name": "bash", "arguments": map[string]any{"command": "agent-browser open http://x"}},
					},
				},
			},
			check: func(t *testing.T, entries []transcript.Entry) {
				msg := findEntryByKindRole(t, entries, "message", "assistant")
				if msg.Text != "Let me open the app." {
					t.Fatalf("assistant text = %q", msg.Text)
				}
				call := findEntryByKind(t, entries, "tool_call")
				if call.ToolCallID != "call_00_abc" || call.ToolName != "bash" {
					t.Fatalf("tool call = %#v", call)
				}
			},
		},
		{
			name: "tool result message",
			record: map[string]any{
				"type": "message",
				"message": map[string]any{
					"role":       "toolResult",
					"toolCallId": "call_00_abc",
					"toolName":   "bash",
					"content":    []any{map[string]any{"type": "text", "text": "✓ Done"}},
				},
			},
			check: func(t *testing.T, entries []transcript.Entry) {
				res := findEntryByKind(t, entries, "tool_result")
				if res.Text != "✓ Done" || res.ToolCallID != "call_00_abc" {
					t.Fatalf("tool result = %#v", res)
				}
			},
		},
		{
			name:   "session metadata line yields no entries",
			record: map[string]any{"type": "session", "version": 3, "cwd": "/task/workdir"},
			check: func(t *testing.T, entries []transcript.Entry) {
				if len(entries) != 0 {
					t.Fatalf("expected no entries for session line, got %#v", entries)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := transcript.ParseRecord(tc.record, transcript.Entry{CreatedAt: base})
			tc.check(t, entries)
		})
	}
}

func TestBuildParsesProviderSessionAdapterFromPayloadProvider(t *testing.T) {
	createdAt := time.Now().UTC()
	subject := transcript.Subject{ID: "session-1", Title: "", CreatedAt: createdAt}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:abc123"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider": "claude_code",
			"stream":   "assistant",
			"text":     `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
	}

	got := transcript.Build(subject, events)

	requireEntry(t, got, "ev-2-tool-call-0", "tool_call", "assistant", "")
	// Empty Title must not synthesize a goal row.
	for _, entry := range got {
		if entry.Kind == "message" && entry.Role == "user" {
			t.Fatalf("unexpected synthetic goal row for empty title: %#v", entry)
		}
	}
}
