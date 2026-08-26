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

func TestParseRecordCodexAppServerItems(t *testing.T) {
	at := time.Date(2026, 8, 26, 5, 2, 55, 0, time.UTC)
	message := runtimeoutput.ParseRecord(map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"item": map[string]any{
			"type": "agentMessage",
			"id":   "item-msg",
			"text": "VPN检测未通过，请检查靶场VPN网络配置。",
		},
	}, runtimeoutput.ParseOptions{}, at)
	if len(message) != 1 || message[0].Kind != runtimeoutput.KindText || message[0].Role != "assistant" {
		t.Fatalf("agentMessage turns = %#v", message)
	}
	if message[0].Text != "VPN检测未通过，请检查靶场VPN网络配置。" {
		t.Fatalf("agentMessage text = %q", message[0].Text)
	}

	command := runtimeoutput.ParseRecord(map[string]any{
		"item": map[string]any{
			"type":             "commandExecution",
			"id":               "item-cmd",
			"command":          "curl -sS http://10.0.100.58",
			"aggregatedOutput": "curl: (52) Empty reply from server\n",
			"status":           "failed",
			"exitCode":         52,
		},
	}, runtimeoutput.ParseOptions{}, at)
	if len(command) != 2 {
		t.Fatalf("commandExecution turns = %#v", command)
	}
	if command[0].Kind != runtimeoutput.KindToolUse || command[0].Tool != "command_execution" || command[0].ToolCallID != "item-cmd" {
		t.Fatalf("tool use = %#v", command[0])
	}
	if command[0].Input["command"] != "curl -sS http://10.0.100.58" {
		t.Fatalf("tool input = %#v", command[0].Input)
	}
	if command[1].Kind != runtimeoutput.KindToolResult || command[1].Output != "curl: (52) Empty reply from server\n" {
		t.Fatalf("tool result = %#v", command[1])
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

func TestParseRecordHermesACPSessionUpdates(t *testing.T) {
	at := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		record map[string]any
		kind   runtimeoutput.Kind
		text   string
		tool   string
		callID string
	}{
		{
			name: "jsonrpc agent message chunk",
			record: map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "hermes-session",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "Inspecting the app."},
					},
				},
			},
			kind: runtimeoutput.KindText,
			text: "Inspecting the app.",
		},
		{
			name: "params-only tool call",
			record: map[string]any{
				"sessionId": "hermes-session",
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    "call-1",
					"title":         "bash",
					"rawInput":      map[string]any{"command": "ls"},
				},
			},
			kind:   runtimeoutput.KindToolUse,
			tool:   "bash",
			callID: "call-1",
		},
		{
			name: "tool describe rawInput",
			record: map[string]any{
				"sessionId": "hermes-session",
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    "tc-1",
					"title":         "tool_describe",
					"kind":          "other",
					"rawInput":      map[string]any{"name": "mcp__pentest__blackboard_change"},
				},
			},
			kind:   runtimeoutput.KindToolUse,
			tool:   "tool_describe",
			callID: "tc-1",
		},
		{
			name: "update-only tool result",
			record: map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    "call-1",
				"title":         "bash",
				"status":        "completed",
				"content":       "ok",
			},
			kind:   runtimeoutput.KindToolResult,
			tool:   "bash",
			callID: "call-1",
			text:   "ok",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turns := runtimeoutput.ParseRecord(tc.record, runtimeoutput.ParseOptions{}, at)
			if len(turns) != 1 {
				t.Fatalf("turns = %#v", turns)
			}
			got := turns[0]
			if got.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", got.Kind, tc.kind)
			}
			if tc.text != "" && got.Text != tc.text && got.Output != tc.text {
				t.Fatalf("text/output = %q/%q, want %q", got.Text, got.Output, tc.text)
			}
			if tc.tool != "" && got.Tool != tc.tool {
				t.Fatalf("tool = %q, want %q", got.Tool, tc.tool)
			}
			if tc.callID != "" && got.ToolCallID != tc.callID {
				t.Fatalf("call id = %q, want %q", got.ToolCallID, tc.callID)
			}
		})
	}

	if turns := runtimeoutput.ParseRecord(map[string]any{
		"method": "session/update",
		"params": map[string]any{"sessionId": "hermes-session", "update": map[string]any{"sessionUpdate": "turn_ended", "stopReason": "end_turn"}},
	}, runtimeoutput.ParseOptions{}, at); len(turns) != 0 {
		t.Fatalf("turn_ended should not become transcript text, got %#v", turns)
	}
}
