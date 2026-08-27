package timeline_test

import (
	"strings"
	"testing"
	"time"

	"pentest/internal/timeline"
)

// event builds a timeline.Event in the compact form used by both owner kinds.
func event(kind, text string, at time.Time) timeline.Event {
	return timeline.Event{Kind: kind, Payload: map[string]any{"text": text}, CreatedAt: at}
}

func TestBuildParsesThinkingToolUseTextAndResult(t *testing.T) {
	createdAt := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	events := []timeline.Event{
		{Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: createdAt},
		event("runtime_output", `{"type":"system","subtype":"init","session_id":"abc"}`, createdAt.Add(time.Second)),
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan recon"}]}}`, createdAt.Add(2*time.Second)),
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"curl example.com"}}]}}`, createdAt.Add(3*time.Second)),
		event("runtime_output", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"200 OK"}]}}`, createdAt.Add(4*time.Second)),
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"text","text":"Done inspecting."}]}}`, createdAt.Add(5*time.Second)),
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

func TestBuildReconcilesCodexItemLifecycle(t *testing.T) {
	createdAt := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	events := []timeline.Event{
		{ID: "ev-1", Seq: 1, Kind: "runtime_output", Payload: map[string]any{
			"provider_event": "item/started",
			"text":           `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","status":"inProgress"}}`,
		}, CreatedAt: createdAt},
		{ID: "ev-2", Seq: 2, Kind: "runtime_output", Payload: map[string]any{
			"provider_event": "item/started",
			"text":           `{"item":{"type":"reasoning","id":"item-reasoning","summary":[]}}`,
		}, CreatedAt: createdAt.Add(time.Second)},
		{ID: "ev-3", Seq: 3, Kind: "runtime_output", Payload: map[string]any{
			"provider_event": "item/completed",
			"text":           `{"item":{"type":"reasoning","id":"item-reasoning","summary":["Checked the target."]}}`,
		}, CreatedAt: createdAt.Add(2 * time.Second)},
		{ID: "ev-4", Seq: 4, Kind: "runtime_output", Payload: map[string]any{
			"provider_event": "item/completed",
			"text":           `{"item":{"type":"commandExecution","id":"item-cmd","command":"curl example.com","aggregatedOutput":"200 OK","status":"completed"}}`,
		}, CreatedAt: createdAt.Add(3 * time.Second)},
	}

	got := timeline.Build(events)
	var toolUses, toolResults, thinking int
	for _, item := range got {
		switch item.Type {
		case "tool_use":
			toolUses++
		case "tool_result":
			toolResults++
		case "thinking":
			thinking++
			if item.Content != "Checked the target." {
				t.Fatalf("thinking = %#v", item)
			}
		}
	}
	if toolUses != 1 || toolResults != 1 || thinking != 1 {
		t.Fatalf("Timeline lifecycle counts: tool_use=%d tool_result=%d thinking=%d items=%#v", toolUses, toolResults, thinking, got)
	}
}

func TestBuildGivesMultipleItemsFromOneEventStableDistinctIDs(t *testing.T) {
	createdAt := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	items := timeline.Build([]timeline.Event{{
		ID:        "event-7",
		Seq:       7,
		Kind:      "runtime_output",
		Payload:   map[string]any{"text": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan"},{"type":"tool_use","id":"call-1","name":"curl","input":{"url":"https://example.com"}}]}}`},
		CreatedAt: createdAt,
	}})
	if len(items) != 2 {
		t.Fatalf("Timeline items = %#v, want two items from one provider Event", items)
	}
	if items[0].Seq != 7 || items[1].Seq != 7 {
		t.Fatalf("Timeline item seqs = %d/%d, want source Event seq 7", items[0].Seq, items[1].Seq)
	}
	if items[0].ID == "" || items[1].ID == "" || items[0].ID == items[1].ID {
		t.Fatalf("Timeline item IDs = %q/%q, want stable distinct IDs", items[0].ID, items[1].ID)
	}
}

func TestBuildCoalescesAdjacentThinkingFragments(t *testing.T) {
	createdAt := time.Now().UTC()
	events := []timeline.Event{
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"part one"}]}}`, createdAt),
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":" part two"}]}}`, createdAt.Add(time.Second)),
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
	events := []timeline.Event{
		event("runtime_output", `{"type":"system","subtype":"thinking_tokens","estimated_tokens":13}`, time.Now()),
		event("runtime_output", `{"type":"system","subtype":"task_progress","description":"Exploit"}`, time.Now()),
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"text","text":"Visible."}]}}`, time.Now()),
	}

	got := timeline.Build(events)

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "Visible." {
		t.Fatalf("expected only visible assistant text, got %#v", got)
	}
}

func TestBuildParsesOpenAIToolCallFormat(t *testing.T) {
	events := []timeline.Event{
		event("runtime_output", `{"type":"tool_call","id":"call-1","name":"curl","arguments":{"url":"http://127.0.0.1:3000"}}`, time.Now()),
		event("runtime_output", `{"type":"tool_result","tool_call_id":"call-1","output":"OK"}`, time.Now()),
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "tool_use", "curl", "")
	requireItem(t, got, 1, "tool_result", "", "OK")
}

func TestBuildIncludesSteeringAndNativeResumeLifecycle(t *testing.T) {
	events := []timeline.Event{
		{Kind: "steering", Payload: map[string]any{"phase": "steering_requested", "directive": "focus admin"}},
		{Kind: "lifecycle", Payload: map[string]any{"phase": "interrupting"}},
		{Kind: "lifecycle", Payload: map[string]any{"phase": "resuming_native"}},
		{Kind: "steering", Payload: map[string]any{"phase": "steering_applied", "directive": "focus admin"}},
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "steering", "", "Steering: steering_requested - focus admin")
	requireItem(t, got, 1, "lifecycle", "", "Lifecycle: interrupting")
	requireItem(t, got, 2, "lifecycle", "", "Lifecycle: resuming_native")
	requireItem(t, got, 3, "steering", "", "Steering: steering_applied - focus admin")
}

func TestBuildNamesNativeSteerControlOutcomes(t *testing.T) {
	events := []timeline.Event{
		{Kind: "steering", Payload: map[string]any{"request_id": "req-1", "outcome": "requested", "mode": "interrupt_then_replace"}},
		{Kind: "steering", Payload: map[string]any{"request_id": "req-1", "outcome": "settled", "mode": "interrupt_then_replace"}},
		{Kind: "steering", Payload: map[string]any{"request_id": "req-1", "outcome": "failed", "error_code": "timeout"}},
	}

	got := timeline.Build(events)
	requireItem(t, got, 0, "steering", "", "Steering: requested (interrupt_then_replace)")
	requireItem(t, got, 1, "steering", "", "Steering: settled (interrupt_then_replace)")
	requireItem(t, got, 2, "steering", "", "Steering: failed (timeout)")
}

func TestBuildKeepsControlEventsBetweenRuntimeOutput(t *testing.T) {
	events := []timeline.Event{
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"text","text":"before"}]}}`, time.Now()),
		{Kind: "steering", Payload: map[string]any{"phase": "steering_requested", "directive": "focus admin"}},
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"text","text":"after"}]}}`, time.Now()),
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "text", "", "before")
	requireItem(t, got, 1, "steering", "", "Steering: steering_requested - focus admin")
	requireItem(t, got, 2, "text", "", "after")
}

func TestBuildProjectsAssistedConclusionPhasesWithoutStructuredResult(t *testing.T) {
	events := []timeline.Event{
		{Kind: "blackboard_conclusion", Payload: map[string]any{
			"phase": "pending_detected", "source_turn_id": "work-1",
			"source_work_watermark": 47, "semantic_persistence_watermark": 23,
		}},
		{Kind: "blackboard_conclusion", Payload: map[string]any{"phase": "dispatch_requested", "source_turn_id": "work-1", "request_id": "conclude-secret"}},
		{Kind: "blackboard_conclusion", Payload: map[string]any{"phase": "awaiting_result", "control_turn_id": "control-1"}},
		{Kind: "blackboard_conclusion", Payload: map[string]any{"phase": "result_validated", "result_hash": "secret-hash"}},
		{Kind: "blackboard_conclusion", Payload: map[string]any{"phase": "applied", "applied_revision": 4}},
		{Kind: "blackboard_conclusion", Payload: map[string]any{"phase": "repair_requested", "request_id": "repair-secret", "error_code": "semantic_conclusion_invalid_result"}},
		{Kind: "blackboard_conclusion", Payload: map[string]any{"phase": "action_required", "request_id": "failed-secret", "error_code": "conclude_tool_use_forbidden"}},
		{Kind: "blackboard_conclusion", Payload: map[string]any{"phase": "retry_requested", "request_id": "retry-secret"}},
	}

	got := timeline.Build(events)
	requireItem(t, got, 0, "harness", "", "Blackboard conclusion pending for work Turn work-1")
	requireItem(t, got, 1, "harness", "", "Blackboard Conclude Turn dispatch requested")
	requireItem(t, got, 2, "harness", "", "Blackboard Conclude Turn started")
	requireItem(t, got, 3, "harness", "", "Blackboard conclusion result validated")
	requireItem(t, got, 4, "harness", "", "Blackboard conclusion applied at revision 4")
	requireItem(t, got, 5, "harness", "", "Blackboard conclusion repair requested")
	requireItem(t, got, 6, "harness", "", "Blackboard conclusion requires action (conclude_tool_use_forbidden)")
	requireItem(t, got, 7, "harness", "", "Blackboard conclusion retry requested")
	for _, item := range got {
		if strings.Contains(item.Content, "secret") || strings.Contains(item.Content, "control-1") ||
			strings.Contains(item.Content, "47") || strings.Contains(item.Content, "23") {
			t.Fatalf("Harness item leaked correlation/result detail: %#v", item)
		}
	}
}

func TestBuildMapsSessionAttachmentsToLifecycleItems(t *testing.T) {
	events := []timeline.Event{
		{Kind: "attachment", Payload: map[string]any{"filename": "scan.txt", "size": 512}},
		event("runtime_output", `{"type":"assistant","message":{"content":[{"type":"text","text":"Analyzed."}]}}`, time.Now()),
	}

	got := timeline.Build(events)

	requireItem(t, got, 0, "lifecycle", "", "Attached scan.txt (512 bytes)")
	requireItem(t, got, 1, "text", "", "Analyzed.")
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
