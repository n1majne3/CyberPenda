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
		// Reasoning is durable transcript content: thinking-only assistant
		// frames are stored and projected, never dropped.
		{`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan"}]}}`, false},
	}
	for _, tc := range cases {
		if got := runtimeoutput.ShouldIgnoreForStorage(tc.line); got != tc.ignore {
			t.Fatalf("ShouldIgnoreForStorage(%q) = %v, want %v", tc.line, got, tc.ignore)
		}
	}
}

func TestShouldIgnoreForTimelineAllowsThinkingOnlyAssistant(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan recon"}]}}`
	if runtimeoutput.ShouldIgnoreForStorage(line) {
		t.Fatal("storage must keep thinking-only assistant frames")
	}
	if runtimeoutput.ShouldIgnoreForTimeline(line) {
		t.Fatal("timeline projection should keep thinking-only assistant")
	}
}

func TestParseRecordProjectsReasoningKindForThinkingBlocks(t *testing.T) {
	record := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "thinking", "thinking": "plan the recon"}},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %#v", turns)
	}
	if turns[0].Kind != runtimeoutput.KindReasoning {
		t.Fatalf("kind = %q, want %q", turns[0].Kind, runtimeoutput.KindReasoning)
	}
	if string(turns[0].Kind) != "reasoning" {
		t.Fatalf("wire kind = %q, want reasoning", turns[0].Kind)
	}
	if turns[0].Text != "plan the recon" {
		t.Fatalf("text = %q", turns[0].Text)
	}
}

func TestParseRecordPiReasoningBlock(t *testing.T) {
	record := map[string]any{
		"type": "message",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "reasoning", "id": "msg-1-think-0", "reasoning": "enumerate endpoints first"},
				map[string]any{"type": "text", "text": "Starting scan."},
			},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %#v", turns)
	}
	if turns[0].Kind != runtimeoutput.KindReasoning || turns[0].Text != "enumerate endpoints first" {
		t.Fatalf("reasoning turn = %#v", turns[0])
	}
	if turns[0].ProviderItemID != "msg-1-think-0" {
		t.Fatalf("provider item id = %q, want msg-1-think-0", turns[0].ProviderItemID)
	}
	if turns[1].Kind != runtimeoutput.KindText || turns[1].Text != "Starting scan." {
		t.Fatalf("text turn = %#v", turns[1])
	}
	if disabled := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{}, time.Time{}); len(disabled) != 1 || disabled[0].Kind != runtimeoutput.KindText {
		t.Fatalf("IncludeThinking=false must hide reasoning, got %#v", disabled)
	}
}

func TestParseRecordThinkingDeltaCarriesProviderItemID(t *testing.T) {
	record := map[string]any{
		"type":    "content_block_delta",
		"index":   float64(0),
		"item_id": "t1-thinking-0",
		"delta":   map[string]any{"type": "thinking_delta", "thinking": "partial thought"},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %#v", turns)
	}
	if turns[0].Kind != runtimeoutput.KindReasoning || turns[0].Text != "partial thought" {
		t.Fatalf("delta turn = %#v", turns[0])
	}
	if turns[0].ProviderItemID != "t1-thinking-0" || turns[0].LifecyclePhase != "streaming" {
		t.Fatalf("delta identity/lifecycle = %#v", turns[0])
	}
	if disabled := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{}, time.Time{}); len(disabled) != 0 {
		t.Fatalf("IncludeThinking=false must hide thinking deltas, got %#v", disabled)
	}
}

func TestStorageKeepsOnlyReasoningClaudeCLIStreamEvents(t *testing.T) {
	thinking := `{"type":"stream_event","uuid":"message-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"checking"}}}`
	text := `{"type":"stream_event","uuid":"message-1","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"visible later"}}}`
	start := `{"type":"stream_event","uuid":"message-1","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}`
	if runtimeoutput.ShouldIgnoreForStorage(thinking) {
		t.Fatal("thinking delta must remain durable")
	}
	if !runtimeoutput.ShouldIgnoreForStorage(text) || !runtimeoutput.ShouldIgnoreForStorage(start) {
		t.Fatal("non-reasoning partial events must not become raw transcript fallback")
	}
}

func TestParseRecordClaudeCLIStreamEventReasoningDelta(t *testing.T) {
	record := map[string]any{
		"type": "stream_event",
		"uuid": "claude-message-1",
		"event": map[string]any{
			"type":  "content_block_delta",
			"index": float64(0),
			"delta": map[string]any{"type": "thinking_delta", "thinking": "checking auth"},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 || turns[0].Kind != runtimeoutput.KindReasoning || turns[0].Text != "checking auth" {
		t.Fatalf("Claude CLI stream event = %#v", turns)
	}
	if turns[0].ProviderItemID != "claude-message-1-reasoning-0" || turns[0].LifecyclePhase != "streaming" {
		t.Fatalf("Claude CLI stream identity/lifecycle = %#v", turns[0])
	}
}

func TestReconcileClaudeCLIIncrementalReasoningDeltas(t *testing.T) {
	parse := func(text string) []runtimeoutput.Turn {
		return runtimeoutput.ParseRecord(map[string]any{
			"type": "stream_event",
			"uuid": "claude-message-1",
			"event": map[string]any{
				"type":  "content_block_delta",
				"index": float64(0),
				"delta": map[string]any{"type": "thinking_delta", "thinking": text},
			},
		}, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	}
	turns := runtimeoutput.ReconcileLifecycle(append(parse("checking "), parse("auth")...))
	if len(turns) != 1 || turns[0].Text != "checking auth" {
		t.Fatalf("incremental reasoning reconciliation = %#v", turns)
	}
}

func TestParseRecordClaudeCLICompletedReasoningUsesSameStableItem(t *testing.T) {
	record := map[string]any{
		"type": "assistant",
		"uuid": "claude-message-1",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "thinking", "thinking": "checking auth"}},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 || turns[0].ProviderItemID != "claude-message-1-reasoning-0" {
		t.Fatalf("completed reasoning identity = %#v", turns)
	}
}

func TestParseRecordHermesThoughtChunkProjectsReasoning(t *testing.T) {
	record := map[string]any{
		"method": "session/update",
		"params": map[string]any{
			"sessionId": "hermes-session",
			"update": map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"text":          "considering the auth flow",
				"item_id":       "hermes-thought-turn-1",
			},
		},
	}
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeThinking: true}, time.Time{})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %#v", turns)
	}
	if turns[0].Kind != runtimeoutput.KindReasoning || turns[0].Text != "considering the auth flow" {
		t.Fatalf("thought turn = %#v", turns[0])
	}
	if turns[0].ProviderItemID != "hermes-thought-turn-1" {
		t.Fatalf("provider item id = %q", turns[0].ProviderItemID)
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

	started := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":    "commandExecution",
			"id":      "item-cmd",
			"command": "curl -sS http://10.0.100.58",
			"status":  "inProgress",
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/started"}, runtimeoutput.ParseOptions{}, at)
	if len(started) != 1 || started[0].Kind != runtimeoutput.KindToolUse || started[0].ProviderItemID != "item-cmd" || started[0].LifecyclePhase != "started" {
		t.Fatalf("started command = %#v", started)
	}
	completed := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":             "commandExecution",
			"id":               "item-cmd",
			"command":          "curl -sS http://10.0.100.58",
			"aggregatedOutput": "ok",
			"status":           "completed",
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{}, at.Add(time.Second))
	reconciled := runtimeoutput.ReconcileLifecycle(append(started, completed...))
	if len(reconciled) != 2 || reconciled[0].Kind != runtimeoutput.KindToolUse || reconciled[1].Kind != runtimeoutput.KindToolResult {
		t.Fatalf("reconciled command lifecycle = %#v", reconciled)
	}

	reasoningStarted := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":    "reasoning",
			"id":      "item-reasoning",
			"summary": []any{},
			"content": []any{"raw chain of thought"},
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/started"}, runtimeoutput.ParseOptions{IncludeReasoningSummaries: true}, at)
	reasoningCompleted := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":    "reasoning",
			"id":      "item-reasoning",
			"summary": []any{"Checked the target.", "Prepared the command."},
			"content": []any{"raw chain of thought"},
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{IncludeReasoningSummaries: true}, at.Add(time.Second))
	if len(reasoningStarted) != 0 {
		t.Fatalf("started reasoning should stay hidden until completion: %#v", reasoningStarted)
	}
	// Raw reasoning content is durable transcript content. It is preferred over
	// the provider summary because third-party model summaries are not the real
	// thinking; the summary remains the fallback when no content exists.
	reasoning := runtimeoutput.ReconcileLifecycle(reasoningCompleted)
	if len(reasoning) != 1 || reasoning[0].Kind != runtimeoutput.KindReasoning || reasoning[0].Text != "raw chain of thought" {
		t.Fatalf("reasoning lifecycle = %#v", reasoning)
	}
	if reasoning[0].ProviderItemID != "item-reasoning" {
		t.Fatalf("provider item id = %q", reasoning[0].ProviderItemID)
	}
	summaryOnly := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":    "reasoning",
			"id":      "item-reasoning-2",
			"summary": []any{"Checked the target.", "Prepared the command."},
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{IncludeReasoningSummaries: true}, at.Add(time.Second))
	if len(summaryOnly) != 1 || summaryOnly[0].Kind != runtimeoutput.KindReasoning || summaryOnly[0].Text != "Checked the target.\nPrepared the command." {
		t.Fatalf("summary-fallback reasoning = %#v", summaryOnly)
	}
	if empty := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{"type": "reasoning", "id": "empty", "summary": []any{}},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{IncludeReasoningSummaries: true}, at); len(empty) != 0 {
		t.Fatalf("empty completed reasoning invented text: %#v", empty)
	}
	if hidden := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{"type": "reasoning", "id": "hidden", "summary": []any{"summary"}},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{}, at); len(hidden) != 0 {
		t.Fatalf("reasoning ignored IncludeReasoningSummaries: %#v", hidden)
	}
}

func TestParseLinePlainTextFallback(t *testing.T) {
	at := time.Now().UTC()
	turns, fallback := runtimeoutput.ParseLine("plain runtime line", at, runtimeoutput.ParseOptions{IncludeThinking: true})
	if !fallback || len(turns) != 1 || turns[0].Kind != runtimeoutput.KindText || turns[0].Text != "plain runtime line" {
		t.Fatalf("unexpected plain fallback: %#v fallback=%v", turns, fallback)
	}
}

func TestCoalesceMergesAdjacentReasoning(t *testing.T) {
	turns := []runtimeoutput.Turn{
		{Kind: runtimeoutput.KindReasoning, Text: "part one"},
		{Kind: runtimeoutput.KindReasoning, Text: " part two"},
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
