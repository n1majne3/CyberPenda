package runtimeoutput_test

import (
	"testing"
	"time"

	"pentest/internal/runtimeoutput"
)

func TestShouldIgnoreForStorageDropsThinkingTokensAndTaskProgress(t *testing.T) {
	cases := []struct {
		line   string
		ignore bool
	}{
		{`{"type":"system","subtype":"thinking_tokens","estimated_tokens":13}`, true},
		{`{"type":"system","subtype":"task_progress","description":"Exploit"}`, true},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"Visible."}]}}`, false},
		{`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan"}]}}`, true},
	}
	for _, tc := range cases {
		if got := runtimeoutput.ShouldIgnoreForStorage(tc.line); got != tc.ignore {
			t.Fatalf("ShouldIgnoreForStorage(%q) = %v, want %v", tc.line, got, tc.ignore)
		}
	}
}

func TestShouldIgnoreForTimelineAllowsThinkingOnlyAssistant(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan recon"}]}}`
	if !runtimeoutput.ShouldIgnoreForStorage(line) {
		t.Fatal("storage should ignore thinking-only assistant")
	}
	if runtimeoutput.ShouldIgnoreForTimeline(line) {
		t.Fatal("timeline projection should keep thinking-only assistant")
	}
}

func TestParseRecordClaudeAssistantBlocks(t *testing.T) {
	record := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "I will inspect the app."},
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "curl", "input": map[string]any{"url": "http://127.0.0.1:3000"}},
			},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d: %#v", len(turns), turns)
	}
	if turns[0].Kind != runtimeoutput.KindText || turns[0].Text != "I will inspect the app." {
		t.Fatalf("unexpected text turn: %#v", turns[0])
	}
	if turns[1].Kind != runtimeoutput.KindToolUse || turns[1].Tool != "curl" {
		t.Fatalf("unexpected tool turn: %#v", turns[1])
	}
}

func TestParseRecordOmitsThinkingWhenDisabled(t *testing.T) {
	record := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "hidden"},
				map[string]any{"type": "text", "text": "visible"},
			},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{}, time.Time{})
	if len(turns) != 1 || turns[0].Text != "visible" {
		t.Fatalf("expected only visible text, got %#v", turns)
	}
}

func TestParseLinePlainTextFallback(t *testing.T) {
	at := time.Now().UTC()
	turns, fallback := runtimeoutput.ParseLine("plain runtime line", at, runtimeoutput.ParseOptions{IncludeThinking: true})
	if !fallback || len(turns) != 1 || turns[0].Kind != runtimeoutput.KindText || turns[0].Text != "plain runtime line" {
		t.Fatalf("unexpected plain fallback: %#v fallback=%v", turns, fallback)
	}
}

func TestCoalesceMergesAdjacentThinking(t *testing.T) {
	turns := []runtimeoutput.Turn{
		{Kind: runtimeoutput.KindThinking, Text: "part one"},
		{Kind: runtimeoutput.KindThinking, Text: " part two"},
		{Kind: runtimeoutput.KindToolUse, Tool: "Bash"},
	}
	got := runtimeoutput.CoalesceStreaming(turns)
	if len(got) != 2 {
		t.Fatalf("expected 2 coalesced turns, got %d: %#v", len(got), got)
	}
	if got[0].Text != "part one part two" {
		t.Fatalf("unexpected merged thinking: %#v", got[0])
	}
}