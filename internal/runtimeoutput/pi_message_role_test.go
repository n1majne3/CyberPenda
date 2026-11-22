package runtimeoutput_test

import (
	"testing"
	"time"

	"pentest/internal/runtimeoutput"
)

func TestParseRecordMessageHonorsNestedMessageRole(t *testing.T) {
	tests := []struct {
		name     string
		record   map[string]any
		wantRole string
		wantText string
	}{
		{
			name: "pi user message with text block",
			record: map[string]any{
				"type": "message",
				"message": map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type": "text",
							"text": "Reply only with CYBERPENDA-QA-READY.\n\nYou may use a state file to keep a lightweight work trace.",
						},
					},
				},
			},
			wantRole: "user",
			wantText: "Reply only with CYBERPENDA-QA-READY.\n\nYou may use a state file to keep a lightweight work trace.",
		},
		{
			name: "pi user message with string content",
			record: map[string]any{
				"type": "message",
				"message": map[string]any{
					"role":    "user",
					"content": "Follow up without tools.",
				},
			},
			wantRole: "user",
			wantText: "Follow up without tools.",
		},
		{
			name: "pi assistant message keeps assistant role",
			record: map[string]any{
				"type": "message",
				"message": map[string]any{
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "text", "text": "CYBERPENDA-QA-READY"},
					},
				},
			},
			wantRole: "assistant",
			wantText: "CYBERPENDA-QA-READY",
		},
		{
			name: "pi system message keeps system role",
			record: map[string]any{
				"type": "message",
				"message": map[string]any{
					"role":    "system",
					"content": "Session initialized.",
				},
			},
			wantRole: "system",
			wantText: "Session initialized.",
		},
		{
			name: "top-level role overrides generic message default",
			record: map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Top-level user prompt",
			},
			wantRole: "user",
			wantText: "Top-level user prompt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			turns := runtimeoutput.ParseRecord(tc.record, runtimeoutput.ParseOptions{}, time.Time{})
			if len(turns) != 1 {
				t.Fatalf("expected 1 turn, got %#v", turns)
			}
			if turns[0].Kind != runtimeoutput.KindText {
				t.Fatalf("kind = %q, want %q", turns[0].Kind, runtimeoutput.KindText)
			}
			if turns[0].Role != tc.wantRole {
				t.Fatalf("role = %q, want %q", turns[0].Role, tc.wantRole)
			}
			if turns[0].Text != tc.wantText {
				t.Fatalf("text = %q, want %q", turns[0].Text, tc.wantText)
			}
		})
	}
}
