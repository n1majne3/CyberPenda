package transcript_test

import (
	"strings"
	"testing"
	"time"

	"pentest/internal/runtimeoutput"
	"pentest/internal/transcript"
)

func TestBuildDropsClaudeThinkingTokenNoise(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": `{"type":"system","subtype":"thinking_tokens","estimated_tokens":13,"uuid":"abc"}`}},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Ready."}]}}`}},
	}

	got := transcript.Build(subject, events)

	for _, entry := range got {
		if entry.Kind == "runtime_output" && strings.Contains(entry.Text, "thinking_tokens") {
			t.Fatalf("expected thinking token noise to be dropped, got %#v", entry)
		}
	}
	requireEntry(t, got, "ev-3-message-0", "message", "assistant", "Ready.")
}

func TestIsIgnorableRuntimeLineDetectsThinkingTokens(t *testing.T) {
	line := `{"type":"system","subtype":"thinking_tokens","estimated_tokens":13}`
	if !transcript.IsIgnorableRuntimeLine(line) {
		t.Fatal("expected thinking token line to be ignorable")
	}
	if transcript.IsIgnorableRuntimeLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`) {
		t.Fatal("expected assistant message to remain visible")
	}
}

func TestIsIgnorableRuntimeLineKeepsTaskProgress(t *testing.T) {
	line := `{"type":"system","subtype":"task_progress","task_id":"t1","description":"Exploit: privacy-policy","workflow_progress":[{"label":"privacy-policy"}]}`
	if transcript.IsIgnorableRuntimeLine(line) {
		t.Fatal("expected task_progress subagent activity to be kept for the timeline")
	}
}

func TestIsIgnorableRuntimeLineKeepsTaskStartedAndFailed(t *testing.T) {
	started := `{"type":"system","subtype":"task_started","task_id":"bbr05bd75","summary":"Explore FTP directory"}`
	if transcript.IsIgnorableRuntimeLine(started) {
		t.Fatal("expected task_started subagent activity to be kept")
	}
	failed := `{"type":"system","subtype":"task_failed","task_id":"bbr05bd75","status":"failed","summary":"Explore FTP directory"}`
	if transcript.IsIgnorableRuntimeLine(failed) {
		t.Fatal("expected task_failed subagent activity to be kept")
	}
}

func TestBuildDropsTaskProgressNoise(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": `{"type":"system","subtype":"task_progress","task_id":"t1","description":"Exploit: privacy-policy","workflow_progress":[{"label":"privacy-policy","phaseTitle":"Exploit"}]}`}},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Found the policy hash."}]}}`}},
	}

	got := transcript.Build(subject, events)

	// task_progress is kept for the timeline's Subagent Activity projection,
	// but the transcript message flow must not surface the raw record as a
	// message or tool entry.
	for _, entry := range got {
		if (entry.Kind == "message" || entry.Kind == "tool_call") && strings.Contains(entry.Text, "task_progress") {
			t.Fatalf("expected task_progress to stay out of the message flow, got %#v", entry)
		}
	}
	requireEntry(t, got, "ev-3-message-0", "message", "assistant", "Found the policy hash.")
}

func TestIsIgnorableRuntimeLineDetectsClaudeInitAndResult(t *testing.T) {
	init := `{"type":"system","subtype":"init","cwd":"/task/workdir","session_id":"abc"}`
	if !transcript.IsIgnorableRuntimeLine(init) {
		t.Fatal("expected system init line to be ignorable")
	}
	result := `{"type":"result","subtype":"success","is_error":false}`
	if !transcript.IsIgnorableRuntimeLine(result) {
		t.Fatal("expected result line to be ignorable")
	}
}

func TestIsIgnorableRuntimeLineKeepsThinkingOnlyAssistant(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"planning next step"}]}}`
	if transcript.IsIgnorableRuntimeLine(line) {
		t.Fatal("thinking-only assistant lines are durable transcript content and must not be ignored")
	}
}

func TestBuildProjectsReasoningEntriesAndDropsClaudeInitResult(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"system","subtype":"init","session_id":"abc"}`}},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"only thoughts"}]}}`}},
		{ID: "ev-4", Seq: 4, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"curl example.com"}}]}}`}},
		{ID: "ev-5", Seq: 5, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"result","subtype":"success","is_error":false}`}},
		{ID: "ev-6", Seq: 6, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}`}},
	}

	got := transcript.Build(subject, events)

	for _, entry := range got {
		if entry.Kind == "runtime_output" {
			t.Fatalf("expected no runtime_output fallbacks, got %#v", entry)
		}
	}
	call := requireEntry(t, got, "ev-4-tool-call-0", "tool_call", "assistant", "")
	if call.ToolName != "Bash" {
		t.Fatalf("unexpected tool call: %#v", call)
	}
	requireEntry(t, got, "ev-6-message-0", "message", "assistant", "Done.")
	thinking := requireEntry(t, got, "ev-3-reasoning-0", "reasoning", "assistant", "only thoughts")
	if thinking.Status != transcript.StatusCollapsed {
		t.Fatalf("reasoning entry should be collapsed: %#v", thinking)
	}
}

func TestBuildStreamsCodexReasoningDeltasIntoOneGrowingEntry(t *testing.T) {
	createdAt := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", Title: "Inspect", CreatedAt: createdAt}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:thread-1"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/reasoning/summaryTextDelta", "stream": "codex_app_server",
			"text": `{"item":{"type":"reasoning","id":"item-reasoning","summary":["Checked"]}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/reasoning/summaryTextDelta", "stream": "codex_app_server",
			"text": `{"item":{"type":"reasoning","id":"item-reasoning","summary":["Checked the target."]}}`,
		}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-4", Seq: 4, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/started", "stream": "codex_app_server",
			"text": `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","status":"inProgress"}}`,
		}, CreatedAt: createdAt.Add(3 * time.Second)},
		{ID: "ev-5", Seq: 5, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/completed", "stream": "codex_app_server",
			"text": `{"item":{"type":"reasoning","id":"item-reasoning","summary":["Checked the target.","Prepared the command."]}}`,
		}, CreatedAt: createdAt.Add(4 * time.Second)},
	}

	got := transcript.Build(subject, events)

	stableID := "continuation-1-" + runtimeoutput.StableProviderItemID("item-reasoning", "reasoning")
	var reasoningEntries []transcript.Entry
	reasoningIndex := -1
	toolIndex := -1
	for index, entry := range got {
		if entry.Kind == transcript.KindReasoning {
			reasoningEntries = append(reasoningEntries, entry)
			reasoningIndex = index
		}
		if entry.Kind == transcript.KindToolCall && entry.ToolName == "command_execution" {
			toolIndex = index
		}
	}
	if len(reasoningEntries) != 1 {
		t.Fatalf("streamed reasoning must be one growing entry, got %d: %#v", len(reasoningEntries), reasoningEntries)
	}
	entry := reasoningEntries[0]
	if entry.ID != stableID {
		t.Fatalf("reasoning id = %q, want %q", entry.ID, stableID)
	}
	if entry.Seq != 2 {
		t.Fatalf("reasoning seq = %d, want 2 (first projection position)", entry.Seq)
	}
	if entry.Text != "Checked the target.\nPrepared the command." || entry.Status != transcript.StatusCollapsed {
		t.Fatalf("completed reasoning = text=%q status=%q", entry.Text, entry.Status)
	}
	if reasoningIndex == -1 || toolIndex == -1 || reasoningIndex > toolIndex {
		t.Fatalf("reasoning must interleave before its tool call: reasoning=%d tool=%d", reasoningIndex, toolIndex)
	}
}

func TestBuildJoinsClaudeCLIIncrementalThinkingDeltas(t *testing.T) {
	createdAt := time.Now().UTC()
	got := transcript.Build(transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: createdAt}, []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"stream_event","uuid":"claude-message-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"checking "}}}`}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"stream_event","uuid":"claude-message-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"auth"}}}`}, CreatedAt: createdAt.Add(2 * time.Second)},
	})
	var reasoning []transcript.Entry
	for _, entry := range got {
		if entry.Kind == transcript.KindReasoning {
			reasoning = append(reasoning, entry)
		}
	}
	if len(reasoning) != 1 || reasoning[0].Text != "checking auth" || !reasoning[0].Incremental {
		t.Fatalf("incremental Claude CLI reasoning = %#v", reasoning)
	}
}

func TestBuildStreamsClaudeThinkingDeltasIntoOneGrowingEntry(t *testing.T) {
	createdAt := time.Now().UTC()
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: createdAt}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider": "claude", "provider_event": "claude/runtime_output", "stream": "stream_event",
			"text": `{"type":"content_block_delta","index":0,"item_id":"t1-thinking-0","delta":{"type":"thinking_delta","thinking":"partial"}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"provider": "claude", "provider_event": "claude/runtime_output", "stream": "stream_event",
			"text": `{"type":"content_block_delta","index":0,"item_id":"t1-thinking-0","delta":{"type":"thinking_delta","thinking":"partial thought"}}`,
		}, CreatedAt: createdAt.Add(2 * time.Second)},
	}

	got := transcript.Build(subject, events)

	stableID := "continuation-1-" + runtimeoutput.StableProviderItemID("t1-thinking-0", "reasoning")
	var reasoningEntries []transcript.Entry
	for _, entry := range got {
		if entry.Kind == transcript.KindReasoning {
			reasoningEntries = append(reasoningEntries, entry)
		}
	}
	if len(reasoningEntries) != 1 {
		t.Fatalf("streamed thinking must be one growing entry, got %d: %#v", len(reasoningEntries), reasoningEntries)
	}
	entry := reasoningEntries[0]
	if entry.ID != stableID || entry.Seq != 2 || entry.Text != "partial thought" || entry.Status != "streaming" {
		t.Fatalf("growing entry = id=%q seq=%d text=%q status=%q", entry.ID, entry.Seq, entry.Text, entry.Status)
	}
}

func TestBuildCollapsesStreamingReasoningAtContinuationSettlement(t *testing.T) {
	createdAt := time.Now().UTC()
	got := transcript.Build(transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: createdAt}, []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"stream_event","uuid":"claude-message-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"checking auth"}}}`}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "lifecycle", Payload: map[string]any{"phase": "completed"}, CreatedAt: createdAt.Add(2 * time.Second)},
	})
	for _, entry := range got {
		if entry.Kind == transcript.KindReasoning && entry.Status != transcript.StatusCollapsed {
			t.Fatalf("settled reasoning remained live: %#v", entry)
		}
	}
}
