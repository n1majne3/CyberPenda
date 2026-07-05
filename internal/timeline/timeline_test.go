package timeline_test

import (
	"testing"
	"time"

	"pentest/internal/task"
	"pentest/internal/timeline"
)

func TestBuildParsesThinkingToolUseTextAndResult(t *testing.T) {
	createdAt := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "claude_code"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"system","subtype":"init","session_id":"abc"}`}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan recon"}]}}`}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-4", Seq: 4, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"curl example.com"}}]}}`}, CreatedAt: createdAt.Add(3 * time.Second)},
		{ID: "ev-5", Seq: 5, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"200 OK"}]}}`}, CreatedAt: createdAt.Add(4 * time.Second)},
		{ID: "ev-6", Seq: 6, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Done inspecting."}]}}`}, CreatedAt: createdAt.Add(5 * time.Second)},
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "lifecycle", "", "Lifecycle: started")
	requireItem(t, got, 1, "thinking", "", "plan recon")
	requireItem(t, got, 2, "tool_use", "Bash", "")
	if got[2].Input["command"] != "curl example.com" {
		t.Fatalf("unexpected tool input: %#v", got[2].Input)
	}
	requireItem(t, got, 3, "tool_result", "", "200 OK")
	requireItem(t, got, 4, "text", "", "Done inspecting.")
}

func TestBuildCoalescesAdjacentThinkingFragments(t *testing.T) {
	createdAt := time.Now().UTC()
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"part one"}]}}`}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":" part two"}]}}`}, CreatedAt: createdAt.Add(time.Second)},
	}

	got := timeline.Build(events)

	if len(got) != 1 {
		t.Fatalf("expected 1 coalesced thinking item, got %d: %#v", len(got), got)
	}
	if got[0].Type != "thinking" || got[0].Content != "part one part two" {
		t.Fatalf("unexpected coalesced thinking: %#v", got[0])
	}
}

func TestBuildDropsTaskProgressAndThinkingTokens(t *testing.T) {
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"system","subtype":"thinking_tokens","estimated_tokens":13}`}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"system","subtype":"task_progress","description":"Exploit"}`}},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Visible."}]}}`}},
	}

	got := timeline.Build(events)

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "Visible." {
		t.Fatalf("expected only visible assistant text, got %#v", got)
	}
}

func TestBuildParsesOpenAIToolCallFormat(t *testing.T) {
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_call","id":"call-1","name":"curl","arguments":{"url":"http://127.0.0.1:3000"}}`}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_result","tool_call_id":"call-1","output":"OK"}`}},
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "tool_use", "curl", "")
	requireItem(t, got, 1, "tool_result", "", "OK")
}

func TestBuildIncludesSteeringAndNativeResumeLifecycle(t *testing.T) {
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindSteering, Payload: task.EventPayload{"phase": "steering_requested", "directive": "focus admin"}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "interrupting"}},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "resuming_native"}},
		{ID: "ev-4", Seq: 4, Kind: task.EventKindSteering, Payload: task.EventPayload{"phase": "steering_applied", "directive": "focus admin"}},
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "steering", "", "Steering: steering_requested - focus admin")
	requireItem(t, got, 1, "lifecycle", "", "Lifecycle: interrupting")
	requireItem(t, got, 2, "lifecycle", "", "Lifecycle: resuming_native")
	requireItem(t, got, 3, "steering", "", "Steering: steering_applied - focus admin")
}

func TestBuildKeepsControlEventsBetweenRuntimeOutput(t *testing.T) {
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"before"}]}}`}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindSteering, Payload: task.EventPayload{"phase": "steering_requested", "directive": "focus admin"}},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"after"}]}}`}},
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "text", "", "before")
	requireItem(t, got, 1, "steering", "", "Steering: steering_requested - focus admin")
	requireItem(t, got, 2, "text", "", "after")
}

func requireItem(t *testing.T, items []timeline.Item, index int, typ, tool, content string) {
	t.Helper()
	if index >= len(items) {
		t.Fatalf("expected item at index %d, got %d items", index, len(items))
	}
	item := items[index]
	if item.Type != typ {
		t.Fatalf("items[%d].Type = %q, want %q", index, item.Type, typ)
	}
	if tool != "" && item.Tool != tool {
		t.Fatalf("items[%d].Tool = %q, want %q", index, item.Tool, tool)
	}
	if content != "" {
		switch typ {
		case "tool_result":
			if item.Output != content {
				t.Fatalf("items[%d].Output = %q, want %q", index, item.Output, content)
			}
		default:
			if item.Content != content {
				t.Fatalf("items[%d].Content = %q, want %q", index, item.Content, content)
			}
		}
	}
}
