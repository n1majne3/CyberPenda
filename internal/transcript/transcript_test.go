package transcript_test

import (
	"testing"
	"time"

	"pentest/internal/task"
	"pentest/internal/transcript"
)

func TestParserForAdapterUsesRuntimePluginMetadata(t *testing.T) {
	cases := map[string]string{
		"claude_code": "claude_stream_json",
		"codex":       "codex_json",
		"pi":          "pi_json_session",
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
	subject := task.Task{ID: "task-1", Goal: "Recon Juice Shop", CreatedAt: createdAt}
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "pi"}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"stream": "stdout", "text": "plain line"}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindSteering, Payload: task.EventPayload{"directive": "Focus admin"}, CreatedAt: createdAt.Add(3 * time.Second)},
	}

	got := transcript.Build(subject, events)

	requireEntry(t, got, "task-task-1-goal", "message", "user", "Recon Juice Shop")
	requireEntry(t, got, "ev-1-continuation", "continuation", "system", "Continuation #1 started with pi")
	fallback := requireEntry(t, got, "ev-2-runtime", "runtime_output", "runtime", "plain line")
	if fallback.Stream != "stdout" || fallback.Status != "collapsed" {
		t.Fatalf("expected collapsed stdout fallback, got %#v", fallback)
	}
	requireEntry(t, got, "ev-3-steering", "message", "user", "Focus admin")
}

func TestBuildParsesOpenAIToolCallAndResult(t *testing.T) {
	subject := task.Task{ID: "task-1", Goal: "Do work", CreatedAt: time.Now().UTC()}
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "pi"}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_call","id":"call-1","name":"curl","arguments":{"url":"http://127.0.0.1:3000"}}`}},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_result","tool_call_id":"call-1","output":"200 OK"}`}},
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
	subject := task.Task{ID: "task-1", Goal: "Do work", CreatedAt: time.Now().UTC()}
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "claude_code"}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"I will inspect the app."},{"type":"tool_use","id":"toolu_1","name":"curl","input":{"url":"http://127.0.0.1:3000"}}]}}`}},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"OK"}]}}`}},
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

func TestBuildFallsBackForUnknownJSONRuntimeOutput(t *testing.T) {
	subject := task.Task{ID: "task-1", Goal: "Do work", CreatedAt: time.Now().UTC()}
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "pi"}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"stream": "stderr", "text": `{"type":"new.provider.event","text":"keep raw"}`}},
	}

	got := transcript.Build(subject, events)

	fallback := requireEntry(t, got, "ev-2-runtime", "runtime_output", "runtime", `{"type":"new.provider.event","text":"keep raw"}`)
	if fallback.Stream != "stderr" || fallback.Status != "collapsed" {
		t.Fatalf("expected collapsed stderr fallback, got %#v", fallback)
	}
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
