package runtimeoutput_test

import (
	"testing"
	"time"

	"pentest/internal/runtimeoutput"
)

// Some model providers return reasoning_content already decorated with ANSI
// SGR color codes (pi stores them verbatim in its session log). Reasoning
// turns must carry plain text.
func TestParseRecordStripsANSIFromThinkingBlocks(t *testing.T) {
	record := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{
				"type":     "thinking",
				"thinking": "\x1b[38;5;109mThinking:\x1b[39m \x1b[38;5;244mcheck the log\x1b[39m",
			}},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %#v", turns)
	}
	if turns[0].Text != "Thinking: check the log" {
		t.Fatalf("text = %q, want ANSI-free reasoning", turns[0].Text)
	}
}

func TestParseRecordStripsANSIFromReasoningDelta(t *testing.T) {
	turns := runtimeoutput.ParseRecord(map[string]any{
		"delta": map[string]any{"reasoning_content": "\x1b[38;5;244mVPN connected?\x1b[39m"},
	}, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %#v", turns)
	}
	if turns[0].Text != "VPN connected?" {
		t.Fatalf("text = %q, want ANSI-free reasoning delta", turns[0].Text)
	}
}

func TestParseRecordStripsANSIFromCodexReasoning(t *testing.T) {
	turns := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"type":    "reasoning",
		"id":      "item-1",
		"content": "\x1b[1mplan recon\x1b[0m",
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{IncludeReasoningSummaries: true}, time.Time{})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %#v", turns)
	}
	if turns[0].Text != "plan recon" {
		t.Fatalf("text = %q, want ANSI-free codex reasoning", turns[0].Text)
	}
}

func TestParseRecordStripsANSIFromHermesThoughtChunk(t *testing.T) {
	turns := runtimeoutput.ParseRecord(map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"text":          "\x1b[32mthinking green\x1b[0m",
	}, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %#v", turns)
	}
	if turns[0].Text != "thinking green" {
		t.Fatalf("text = %q, want ANSI-free thought chunk", turns[0].Text)
	}
}

// Plain text turns keep their content untouched; only reasoning is decorated
// by provider display formatting.
func TestParseRecordKeepsANSILessTextUntouched(t *testing.T) {
	record := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "plain output"}},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{}, time.Time{})
	if len(turns) != 1 || turns[0].Text != "plain output" {
		t.Fatalf("text turn = %#v", turns)
	}
}
