package runtimeoutput_test

import (
	"testing"
	"time"

	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

type semanticStep struct {
	kind   string
	text   string
	tool   string
	output string
}

func transcriptSteps(entries []transcript.Entry) []semanticStep {
	out := make([]semanticStep, 0, len(entries))
	for _, entry := range entries {
		switch entry.Kind {
		case transcript.KindMessage:
			if entry.Role == transcript.RoleAssistant && entry.Text != "" {
				out = append(out, semanticStep{kind: "text", text: entry.Text})
			}
		case transcript.KindToolCall:
			out = append(out, semanticStep{kind: "tool_use", tool: entry.ToolName})
		case transcript.KindToolResult:
			out = append(out, semanticStep{kind: "tool_result", tool: entry.ToolName, output: entry.Text})
		}
	}
	return out
}

func timelineSteps(items []timeline.Item) []semanticStep {
	out := make([]semanticStep, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "thinking":
			continue
		case "text":
			out = append(out, semanticStep{kind: "text", text: item.Content})
		case "tool_use":
			out = append(out, semanticStep{kind: "tool_use", tool: item.Tool})
		case "tool_result":
			out = append(out, semanticStep{kind: "tool_result", tool: item.Tool, output: item.Output})
		case "error":
			out = append(out, semanticStep{kind: "error", text: item.Content})
		}
	}
	return out
}

func assertSemanticParity(t *testing.T, gotTranscript, gotTimeline []semanticStep) {
	t.Helper()
	if len(gotTranscript) != len(gotTimeline) {
		t.Fatalf("step count mismatch: transcript=%#v timeline=%#v", gotTranscript, gotTimeline)
	}
	for i := range gotTranscript {
		if gotTranscript[i] != gotTimeline[i] {
			t.Fatalf("step[%d] mismatch: transcript=%#v timeline=%#v", i, gotTranscript[i], gotTimeline[i])
		}
	}
}

func TestTranscriptTimelineParityClaudeAssistantFlow(t *testing.T) {
	createdAt := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	subject := task.Task{ID: "task-1", Goal: "Recon", CreatedAt: createdAt}
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "claude_code"}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"system","subtype":"init","session_id":"abc"}`}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan recon"}]}}`}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-4", Seq: 4, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"curl example.com"}}]}}`}, CreatedAt: createdAt.Add(3 * time.Second)},
		{ID: "ev-5", Seq: 5, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"200 OK"}]}}`}, CreatedAt: createdAt.Add(4 * time.Second)},
		{ID: "ev-6", Seq: 6, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Done inspecting."}]}}`}, CreatedAt: createdAt.Add(5 * time.Second)},
	}

	transcriptEntries := transcript.Build(subject, events)
	timelineItems := timeline.Build(events)

	assertSemanticParity(t, transcriptSteps(transcriptEntries), timelineSteps(timelineItems))
}

func TestTranscriptTimelineParityOpenAIToolFlow(t *testing.T) {
	createdAt := time.Now().UTC()
	subject := task.Task{ID: "task-1", Goal: "Do work", CreatedAt: createdAt}
	events := []task.Event{
		{ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "pi"}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_call","id":"call-1","name":"curl","arguments":{"url":"http://127.0.0.1:3000"}}`}},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_result","tool_call_id":"call-1","output":"OK"}`}},
	}

	transcriptEntries := transcript.Build(subject, events)
	timelineItems := timeline.Build(events)

	assertSemanticParity(t, transcriptSteps(transcriptEntries), timelineSteps(timelineItems))
}

func TestTranscriptTimelineParityDropsSharedNoise(t *testing.T) {
	events := []task.Event{
		{ID: "ev-0", Seq: 0, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "claude_code"}},
		{ID: "ev-1", Seq: 1, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"system","subtype":"thinking_tokens","estimated_tokens":13}`}},
		{ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"system","subtype":"task_progress","description":"Exploit"}`}},
		{ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"assistant","message":{"content":[{"type":"text","text":"Visible."}]}}`}},
	}

	subject := task.Task{ID: "task-1", Goal: "Do work", CreatedAt: time.Now().UTC()}
	transcriptEntries := transcript.Build(subject, events)
	timelineItems := timeline.Build(events)

	assertSemanticParity(t, transcriptSteps(transcriptEntries), timelineSteps(timelineItems))
	if len(timelineSteps(timelineItems)) != 1 || timelineSteps(timelineItems)[0].text != "Visible." {
		t.Fatalf("expected one visible text step, got %#v", timelineSteps(timelineItems))
	}
}