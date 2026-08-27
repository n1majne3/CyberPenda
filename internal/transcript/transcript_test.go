package transcript_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pentest/internal/transcript"
)

func TestParserForAdapterUsesRuntimePluginMetadata(t *testing.T) {
	cases := map[string]string{
		"claude_code": "claude_stream_json",
		"codex":       "codex_json",
		"pi":          "pi_json_session",
		"hermes":      "hermes_acp",
		"fake":        "plain_runtime_output",
		"missing":     "plain_runtime_output",
	}
	for adapter, want := range cases {
		t.Run(adapter, func(t *testing.T) {
			if got := transcript.ParserForAdapter(adapter, nil); got != want {
				t.Fatalf("parser = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildIncludesGoalContinuationsSteeringAndFallback(t *testing.T) {
	createdAt := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "task-1", Title: "Recon Juice Shop", CreatedAt: createdAt}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "pi"}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": "plain line"}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "steering", Payload: map[string]any{"directive": "Focus admin"}, CreatedAt: createdAt.Add(3 * time.Second)},
	}

	got := transcript.Build(subject, events)

	requireEntry(t, got, "task-1-goal", "message", "user", "Recon Juice Shop")
	requireEntry(t, got, "ev-1-continuation", "continuation", "system", "Continuation #1 started with pi")
	fallback := requireEntry(t, got, "ev-2-runtime", "runtime_output", "runtime", "plain line")
	if fallback.Stream != "stdout" || fallback.Status != "collapsed" {
		t.Fatalf("expected collapsed stdout fallback, got %#v", fallback)
	}
	requireEntry(t, got, "ev-3-steering", "message", "user", "Focus admin")
}

func TestBuildParsesPiSessionStreamRegardlessOfAdapterName(t *testing.T) {
	// Persistent Pi provider sessions launch through a ProviderSessionRunAdapter
	// whose Name() is "provider-session:<id>", which does not resolve to any
	// runtime plugin parser. The session-file tailer still emits Pi's real
	// output as runtime_output events carrying stream "pi_session", so the
	// transcript must select the Pi parser from the stream, not the adapter.
	subject := transcript.Subject{ID: "task-1", Title: "Recon", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:abc123"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"stream": "pi_session",
			"text":   `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"Enumerated the login endpoint"}]}}`,
		}},
	}

	got := transcript.Build(subject, events)

	requireEntry(t, got, "ev-2-message-0", "message", "assistant", "Enumerated the login endpoint")
	for _, entry := range got {
		if entry.ID == "ev-2-runtime" {
			t.Fatalf("pi_session line collapsed to runtime fallback instead of parsing: %#v", entry)
		}
	}
}

func TestBuildParsesCodexAppServerItemsFromProviderSession(t *testing.T) {
	createdAt := time.Now().UTC()
	subject := transcript.Subject{ID: "session-1", Title: "VPN health check", CreatedAt: createdAt}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:thread-1"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex",
			"stream":   "codex_app_server",
			"text":     `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"commandExecution","id":"item-cmd","command":"curl -sS http://10.0.100.58","aggregatedOutput":"curl: (52) Empty reply from server\n","status":"failed"}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex",
			"stream":   "codex_app_server",
			"text":     `{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","id":"item-msg","text":"VPN检测未通过"}}`,
		}, CreatedAt: createdAt.Add(2 * time.Second)},
	}

	got := transcript.Build(subject, events)
	toolCall := firstEntryOfKind(t, got, transcript.KindToolCall)
	if toolCall.Role != transcript.RoleAssistant {
		t.Fatalf("tool call = %#v", toolCall)
	}
	toolResult := firstEntryOfKind(t, got, transcript.KindToolResult)
	if toolResult.Role != transcript.RoleTool || toolResult.Text != "curl: (52) Empty reply from server\n" {
		t.Fatalf("tool result = %#v", toolResult)
	}
	requireEntry(t, got, "ev-3-message", "message", "assistant", "VPN检测未通过")
	for _, entry := range got {
		if entry.Kind == "runtime_output" {
			t.Fatalf("codex item collapsed to runtime fallback: %#v", entry)
		}
	}
}

func TestBuildReconcilesCodexStartedCompletedAndReasoningItems(t *testing.T) {
	createdAt := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", Title: "Inspect", CreatedAt: createdAt}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:thread-1"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/started", "stream": "codex_app_server",
			"text": `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","status":"inProgress"}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/started", "stream": "codex_app_server",
			"text": `{"item":{"type":"reasoning","id":"item-reasoning","summary":[]}}`,
		}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-4", Seq: 4, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/completed", "stream": "codex_app_server",
			"text": `{"item":{"type":"reasoning","id":"item-reasoning","summary":["Checked the target."]}}`,
		}, CreatedAt: createdAt.Add(3 * time.Second)},
		{ID: "ev-5", Seq: 5, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/completed", "stream": "codex_app_server",
			"text": `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","aggregatedOutput":"200 OK","status":"completed"}}`,
		}, CreatedAt: createdAt.Add(4 * time.Second)},
	}

	got := transcript.Build(subject, events)
	var calls, results, thinking []transcript.Entry
	for _, entry := range got {
		switch entry.Kind {
		case transcript.KindToolCall:
			calls = append(calls, entry)
		case transcript.KindToolResult:
			results = append(results, entry)
		case transcript.KindThinking:
			thinking = append(thinking, entry)
		}
	}
	if len(calls) != 1 || len(results) != 1 {
		t.Fatalf("tool lifecycle calls=%#v results=%#v", calls, results)
	}
	if len(thinking) != 1 || thinking[0].Text != "Checked the target." || thinking[0].Role == transcript.RoleUser {
		t.Fatalf("thinking entries = %#v", thinking)
	}
}

func TestBuildPreservesStartedCommandPositionWhenCompletionArrivesAfterAnotherEntry(t *testing.T) {
	createdAt := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:thread-1"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/started", "stream": "codex_app_server",
			"text": `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","status":"inProgress"}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/completed", "stream": "codex_app_server",
			"text": `{"item":{"type":"agentMessage","id":"item-message","text":"Still working."}}`,
		}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-4", Seq: 4, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/completed", "stream": "codex_app_server",
			"text": `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","aggregatedOutput":"200 OK","status":"completed"}}`,
		}, CreatedAt: createdAt.Add(3 * time.Second)},
	}

	got := transcript.Build(transcript.Subject{ID: "session-1", CreatedAt: createdAt}, events)
	call := firstEntryOfKind(t, got, transcript.KindToolCall)
	if call.Seq != 2 || !call.CreatedAt.Equal(createdAt.Add(time.Second)) {
		t.Fatalf("completed command moved started call to seq/time %d/%s, want 2/%s", call.Seq, call.CreatedAt, createdAt.Add(time.Second))
	}
	for index := 1; index < len(got); index++ {
		if got[index].Seq < got[index-1].Seq {
			t.Fatalf("transcript seqs are not chronological: %#v", got)
		}
	}
}

func TestBuildWindowUsesStableCodexToolCallIdentityAcrossPages(t *testing.T) {
	createdAt := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", CreatedAt: createdAt}
	started := transcript.BuildWindow(subject, []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:thread-1"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/started", "stream": "codex_app_server",
			"text": `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","status":"inProgress"}}`,
		}},
	}, transcript.WindowContext{})
	completed := transcript.BuildWindow(subject, []transcript.Event{{
		ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"provider": "codex", "provider_event": "item/completed", "stream": "codex_app_server",
			"text": `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","aggregatedOutput":"200 OK","status":"completed"}}`,
		},
	}}, transcript.WindowContext{Continuation: 1, Adapter: "provider-session:thread-1"})

	startedCall := firstEntryOfKind(t, started, transcript.KindToolCall)
	completedCall := firstEntryOfKind(t, completed, transcript.KindToolCall)
	if startedCall.ID != completedCall.ID {
		t.Fatalf("tool call IDs differ across windows: %q != %q", startedCall.ID, completedCall.ID)
	}
	_ = firstEntryOfKind(t, completed, transcript.KindToolResult)
}

func firstEntryOfKind(t *testing.T, entries []transcript.Entry, kind string) transcript.Entry {
	t.Helper()
	for _, entry := range entries {
		if entry.Kind == kind {
			return entry
		}
	}
	t.Fatalf("entry kind %q missing from %#v", kind, entries)
	return transcript.Entry{}
}

func TestBuildProjectsNativeSteerControlsWithoutDuplicatingConversationMessage(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Inspect target", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "conv-1", Seq: 1, Kind: "conversation", Payload: map[string]any{
			"role": "user", "text": "focus on admin", "request_id": "req-1", "delivery": "native_steer",
		}},
		{ID: "steer-1", Seq: 2, Kind: "steering", Payload: map[string]any{
			"request_id": "req-1", "session_id": "session-1", "mode": "interrupt_then_replace", "outcome": "requested",
		}},
		{ID: "steer-2", Seq: 3, Kind: "steering", Payload: map[string]any{
			"request_id": "req-1", "session_id": "session-1", "mode": "interrupt_then_replace", "outcome": "acknowledged",
		}},
		{ID: "steer-3", Seq: 4, Kind: "steering", Payload: map[string]any{
			"request_id": "req-1", "session_id": "session-1", "mode": "interrupt_then_replace", "outcome": "settled",
		}},
		{ID: "steer-4", Seq: 5, Kind: "steering", Payload: map[string]any{
			"request_id": "req-1", "session_id": "session-1", "mode": "interrupt_then_replace", "outcome": "started",
		}},
		{ID: "steer-5", Seq: 6, Kind: "steering", Payload: map[string]any{
			"request_id": "req-1", "session_id": "session-1", "mode": "interrupt_then_replace", "outcome": "applied", "phase": "steering_applied",
		}},
	}

	got := transcript.Build(subject, events)
	var userMessages []transcript.Entry
	var controls []transcript.Entry
	for _, entry := range got {
		if entry.Role == transcript.RoleUser && entry.Text == "focus on admin" {
			userMessages = append(userMessages, entry)
		}
		if entry.Kind == transcript.KindContinuation && entry.Role == transcript.RoleSystem && entry.Seq >= 2 {
			controls = append(controls, entry)
		}
	}
	if len(userMessages) != 1 {
		t.Fatalf("native steer user messages = %#v, want one", userMessages)
	}
	if len(controls) != 5 {
		t.Fatalf("native steer control entries = %#v, want five", controls)
	}
	if controls[0].Text != "Native steer requested" || controls[1].Text != "Provider acknowledged native steer" || controls[2].Text != "Previous provider turn settled" || controls[3].Text != "Replacement provider turn started" || controls[4].Text != "Native steer applied" {
		t.Fatalf("native steer control order = %#v", controls)
	}
}

func TestBuildProjectsRedactedProviderPermissionLifecycle(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Inspect target", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "perm-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{
			"phase": "provider_permission_requested", "permission_request_id": "permission-1", "provider": "claude_code",
		}},
		{ID: "perm-2", Seq: 2, Kind: "lifecycle", Payload: map[string]any{
			"phase": "provider_permission_response_applied", "permission_request_id": "permission-1", "outcome": "applied",
		}},
	}

	got := transcript.Build(subject, events)
	requested := requireEntry(t, got, "perm-1-continuation", "continuation", "system", "Provider permission requested")
	if requested.Details["permission_request_id"] != "permission-1" || requested.Details["tool_input"] != nil {
		t.Fatalf("permission request projection = %#v", requested)
	}
	requireEntry(t, got, "perm-2-continuation", "continuation", "system", "Provider permission response applied")
}

func TestBuildParsesOpenAIToolCallAndResult(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "pi"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"tool_call","id":"call-1","name":"curl","arguments":{"url":"http://127.0.0.1:3000"}}`}},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"tool_result","tool_call_id":"call-1","output":"200 OK"}`}},
	}

	got := transcript.Build(subject, events)

	call := requireEntry(t, got, "ev-2-tool-call", "tool_call", "assistant", "")
	if call.ToolCallID != "call-1" || call.ToolName != "curl" || call.Status != "collapsed" {
		t.Fatalf("unexpected tool call: %#v", call)
	}
	if got := call.Details["arguments"]; got == nil {
		t.Fatalf("expected tool call arguments in details, got %#v", call.Details)
	}

	result := requireEntry(t, got, "ev-3-tool-result", "tool_result", "tool", "200 OK")
	if result.ToolCallID != "call-1" || result.Status != "collapsed" {
		t.Fatalf("unexpected tool result: %#v", result)
	}
}

func TestBuildParsesClaudeAssistantTextAndToolUse(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"I will inspect the app."},{"type":"tool_use","id":"toolu_1","name":"curl","input":{"url":"http://127.0.0.1:3000"}}]}}`}},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{"text": `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"OK"}]}}`}},
	}

	got := transcript.Build(subject, events)

	requireEntry(t, got, "ev-2-message-0", "message", "assistant", "I will inspect the app.")
	call := requireEntry(t, got, "ev-2-tool-call-1", "tool_call", "assistant", "")
	if call.ToolCallID != "toolu_1" || call.ToolName != "curl" {
		t.Fatalf("unexpected Claude tool call: %#v", call)
	}
	result := requireEntry(t, got, "ev-3-tool-result-0", "tool_result", "tool", "OK")
	if result.ToolCallID != "toolu_1" {
		t.Fatalf("unexpected Claude tool result: %#v", result)
	}
}

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

func TestIsIgnorableRuntimeLineDetectsTaskProgress(t *testing.T) {
	line := `{"type":"system","subtype":"task_progress","description":"Exploit: privacy-policy","workflow_progress":[{"label":"privacy-policy"}]}`
	if !transcript.IsIgnorableRuntimeLine(line) {
		t.Fatal("expected task_progress line to be ignorable")
	}
}

func TestIsIgnorableRuntimeLineDetectsTaskStartedAndFailed(t *testing.T) {
	started := `{"type":"system","subtype":"task_started","task_id":"bbr05bd75","summary":"Explore FTP directory"}`
	if !transcript.IsIgnorableRuntimeLine(started) {
		t.Fatal("expected task_started line to be ignorable")
	}
	failed := `{"type":"system","subtype":"task_failed","task_id":"bbr05bd75","status":"failed","summary":"Explore FTP directory"}`
	if !transcript.IsIgnorableRuntimeLine(failed) {
		t.Fatal("expected task_failed line to be ignorable")
	}
}

func TestBuildDropsTaskProgressNoise(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": `{"type":"system","subtype":"task_progress","description":"Exploit: privacy-policy","workflow_progress":[{"label":"privacy-policy","phaseTitle":"Exploit"}]}`}},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Found the policy hash."}]}}`}},
	}

	got := transcript.Build(subject, events)

	for _, entry := range got {
		if entry.Kind == "runtime_output" && strings.Contains(entry.Text, "task_progress") {
			t.Fatalf("expected task_progress noise to be dropped, got %#v", entry)
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

func TestIsIgnorableRuntimeLineDetectsThinkingOnlyAssistant(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"planning next step"}]}}`
	if !transcript.IsIgnorableRuntimeLine(line) {
		t.Fatal("expected thinking-only assistant line to be ignorable")
	}
}

func TestBuildDropsClaudeInitResultAndThinkingOnlyChunks(t *testing.T) {
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
}

func TestBuildFallsBackForUnknownJSONRuntimeOutput(t *testing.T) {
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: time.Now().UTC()}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "pi"}},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{"stream": "stderr", "text": `{"type":"new.provider.event","text":"keep raw"}`}},
	}

	got := transcript.Build(subject, events)

	fallback := requireEntry(t, got, "ev-2-runtime", "runtime_output", "runtime", `{"type":"new.provider.event","text":"keep raw"}`)
	if fallback.Stream != "stderr" || fallback.Status != "collapsed" {
		t.Fatalf("expected collapsed stderr fallback, got %#v", fallback)
	}
}

func TestBuildParsesHermesACPRuntimeOutputFromPersistentSessionAdapter(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "task-hermes", Title: "Inspect target", CreatedAt: createdAt}
	events := []transcript.Event{
		{
			ID: "ev-1", Seq: 1, Kind: "runtime_output",
			Payload: map[string]any{
				"provider": "hermes",
				"stream":   "hermes_acp",
				"text":     `{"sessionId":"hermes-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Inspecting the app."}}}`,
			},
			CreatedAt: createdAt.Add(time.Second),
		},
	}
	got := transcript.Build(subject, events)
	msg := findEntryByKindRole(t, got, "message", "assistant")
	if msg.Text != "Inspecting the app." {
		t.Fatalf("assistant text = %q in %#v", msg.Text, got)
	}
}

func TestBuildProjectsHermesACPToolCallRawInput(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 13, 45, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", Title: "reverse the package", CreatedAt: createdAt}
	got := transcript.Build(subject, []transcript.Event{{
		ID: "ev-1", Seq: 1, Kind: "runtime_output",
		Payload: map[string]any{
			"provider": "hermes",
			"stream":   "hermes_acp",
			"text":     `{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"tool_describe","kind":"other","rawInput":{"name":"mcp__pentest__blackboard_change"},"locations":[]}}`,
		},
		CreatedAt: createdAt,
	}})
	call := findEntryByKind(t, got, "tool_call")
	if call.ToolName != "tool_describe" {
		t.Fatalf("tool name = %q, want tool_describe", call.ToolName)
	}
	input, _ := call.Details["input"].(map[string]any)
	if input["name"] != "mcp__pentest__blackboard_change" {
		t.Fatalf("tool_describe details.input = %#v, want name mcp__pentest__blackboard_change", call.Details)
	}
}

func TestBuildProjectsHermesACPWriteLocationsAsInput(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 13, 45, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", Title: "reverse the package", CreatedAt: createdAt}
	got := transcript.Build(subject, []transcript.Event{{
		ID: "ev-1", Seq: 1, Kind: "runtime_output",
		Payload: map[string]any{
			"provider": "hermes",
			"stream":   "hermes_acp",
			"text":     `{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-2","title":"write: /task/workdir/tools/split_fat.py","kind":"edit","locations":[{"path":"/task/workdir/tools/split_fat.py"}]}}`,
		},
		CreatedAt: createdAt,
	}})
	call := findEntryByKind(t, got, "tool_call")
	input, _ := call.Details["input"].(map[string]any)
	if input["path"] != "/task/workdir/tools/split_fat.py" {
		t.Fatalf("write details.input = %#v, want path /task/workdir/tools/split_fat.py", call.Details)
	}
}

func TestBuildProjectsHermesACPTerminalContentAsCommand(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 13, 45, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", Title: "reverse the package", CreatedAt: createdAt}
	got := transcript.Build(subject, []transcript.Event{{
		ID: "ev-1", Seq: 1, Kind: "runtime_output",
		Payload: map[string]any{
			"provider": "hermes",
			"stream":   "hermes_acp",
			"text":     `{"sessionId":"hermes-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-3","title":"terminal: ls -la /task/workdir","kind":"execute","content":[{"type":"content","content":{"type":"text","text":"$ ls -la /task/workdir"}}]}}`,
		},
		CreatedAt: createdAt,
	}})
	call := findEntryByKind(t, got, "tool_call")
	input, _ := call.Details["input"].(map[string]any)
	if input["command"] != "ls -la /task/workdir" {
		t.Fatalf("terminal details.input = %#v, want command ls -la /task/workdir", call.Details)
	}
}

func TestBuildCoalescesAdjacentHermesACPMessageChunks(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 13, 8, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", Title: "just say hi", CreatedAt: createdAt}
	chunk := func(id string, seq int, text string) transcript.Event {
		return transcript.Event{
			ID: id, Seq: seq, Kind: "runtime_output",
			Payload: map[string]any{
				"provider": "hermes",
				"stream":   "hermes_acp",
				"text":     `{"sessionId":"hermes-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + mustJSONString(t, text) + `}}}`,
			},
			CreatedAt: createdAt.Add(time.Duration(seq) * time.Second),
		}
	}
	got := transcript.Build(subject, []transcript.Event{
		{ID: "ev-start", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "provider-session:abc"}, CreatedAt: createdAt},
		chunk("ev-5", 5, "Hi"),
		chunk("ev-6", 6, "!"),
		chunk("ev-7", 7, " "),
		chunk("ev-8", 8, "👋"),
	})
	var assistants []transcript.Entry
	for _, entry := range got {
		if entry.Kind == "message" && entry.Role == "assistant" {
			assistants = append(assistants, entry)
		}
	}
	if len(assistants) != 1 {
		t.Fatalf("assistant messages = %d, want 1 coalesced sentence: %#v", len(assistants), assistants)
	}
	if assistants[0].Text != "Hi! 👋" {
		t.Fatalf("assistant text = %q, want %q", assistants[0].Text, "Hi! 👋")
	}
	if assistants[0].Seq != 8 {
		t.Fatalf("coalesced seq = %d, want last chunk seq 8", assistants[0].Seq)
	}
	if assistants[0].Stream != "hermes_acp" {
		t.Fatalf("coalesced stream = %q, want hermes_acp", assistants[0].Stream)
	}
}

func TestBuildKeepsAdjacentConversationAssistantMessagesSeparate(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 13, 20, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "task-1", Title: "history window", CreatedAt: createdAt}
	got := transcript.Build(subject, []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "conversation", Payload: map[string]any{"role": "assistant", "text": "Message 1"}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-2", Seq: 2, Kind: "conversation", Payload: map[string]any{"role": "assistant", "text": "Message 2"}, CreatedAt: createdAt.Add(2 * time.Second)},
	})
	var assistants []transcript.Entry
	for _, entry := range got {
		if entry.Kind == "message" && entry.Role == "assistant" {
			assistants = append(assistants, entry)
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("conversation assistant messages = %#v, want two separate rows", assistants)
	}
	if assistants[0].Text != "Message 1" || assistants[1].Text != "Message 2" {
		t.Fatalf("conversation assistant texts = %#v", assistants)
	}
}

func mustJSONString(t *testing.T, text string) string {
	t.Helper()
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

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
			name: "hermes ACP agent message chunk",
			record: map[string]any{
				"method": "session/update",
				"params": map[string]any{
					"sessionId": "hermes-session",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "Hermes is working."},
					},
				},
			},
			check: func(t *testing.T, entries []transcript.Entry) {
				msg := findEntryByKindRole(t, entries, "message", "assistant")
				if msg.Text != "Hermes is working." {
					t.Fatalf("assistant text = %q", msg.Text)
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

func findEntryByKind(t *testing.T, entries []transcript.Entry, kind string) transcript.Entry {
	t.Helper()
	for _, e := range entries {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("missing entry of kind %q in %#v", kind, entries)
	return transcript.Entry{}
}

func findEntryByKindRole(t *testing.T, entries []transcript.Entry, kind, role string) transcript.Entry {
	t.Helper()
	for _, e := range entries {
		if e.Kind == kind && e.Role == role {
			return e
		}
	}
	t.Fatalf("missing entry kind=%q role=%q in %#v", kind, role, entries)
	return transcript.Entry{}
}

func requireEntry(t *testing.T, entries []transcript.Entry, id, kind, role, text string) transcript.Entry {
	t.Helper()
	for _, entry := range entries {
		if entry.ID != id {
			continue
		}
		if entry.Kind != kind || entry.Role != role || entry.Text != text {
			t.Fatalf("entry %s = kind=%q role=%q text=%q, want kind=%q role=%q text=%q", id, entry.Kind, entry.Role, entry.Text, kind, role, text)
		}
		return entry
	}
	t.Fatalf("missing entry %s in %#v", id, entries)
	return transcript.Entry{}
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
