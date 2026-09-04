package transcript_test

import (
	"strings"
	"testing"
	"time"

	"pentest/internal/transcript"
)

func blockEvent(id, text string, at time.Time) transcript.Event {
	return transcript.Event{ID: id, Kind: "runtime_output", Payload: map[string]any{"stream": "stdout", "text": text}, CreatedAt: at}
}

// fleetSubject replays the async Agent-tool path from issue #266: the main
// thread spawns two children, its turn settles, and both children's marked
// items keep flowing interleaved with their lifecycle records.
func fleetEvents(at time.Time) []transcript.Event {
	second := time.Second
	return []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: at},
		blockEvent("ev-2", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_spawn_c06","name":"Agent","input":{"description":"Exploit c-06 HugeGraph"}}]}}`, at.Add(1*second)),
		blockEvent("ev-3", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_spawn_c06","content":"Async agent launched successfully."}]}}`, at.Add(2*second)),
		blockEvent("ev-4", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_spawn_c07","name":"Agent","input":{"description":"telnet login"}}]}}`, at.Add(3*second)),
		blockEvent("ev-5", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_spawn_c07","content":"Async agent launched successfully."}]}}`, at.Add(4*second)),
		blockEvent("ev-6", `{"type":"system","subtype":"task_started","task_id":"a3653e970f04f2dc0","tool_use_id":"call_spawn_c06","description":"Exploit c-06 HugeGraph","subagent_type":"general-purpose","task_type":"local_agent"}`, at.Add(5*second)),
		blockEvent("ev-7", `{"type":"system","subtype":"task_started","task_id":"a0bc0943e0fee5b04","tool_use_id":"call_spawn_c07","description":"telnet login","subagent_type":"Explore","task_type":"local_agent"}`, at.Add(6*second)),
		blockEvent("ev-8", `{"type":"assistant","parent_tool_use_id":"call_spawn_c06","subagent_type":"general-purpose","task_description":"Exploit c-06 HugeGraph","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_child_1","name":"Bash","input":{"command":"curl hugegraph"}}]}}`, at.Add(7*second)),
		blockEvent("ev-9", `{"type":"user","parent_tool_use_id":"call_spawn_c07","subagent_type":"Explore","task_description":"telnet login","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_child_7","content":"root shell"}]}}`, at.Add(8*second)),
		blockEvent("ev-10", `{"type":"assistant","parent_tool_use_id":"call_spawn_c06","subagent_type":"general-purpose","task_description":"Exploit c-06 HugeGraph","message":{"role":"assistant","content":[{"type":"text","text":"hugegraph exploited"}]}}`, at.Add(9*second)),
		blockEvent("ev-11", `{"type":"assistant","parent_tool_use_id":"call_spawn_c07","subagent_type":"Explore","task_description":"telnet login","message":{"role":"assistant","content":[{"type":"text","text":"telnet done"}]}}`, at.Add(10*second)),
		blockEvent("ev-12", `{"type":"user","parent_tool_use_id":"call_spawn_c06","subagent_type":"general-purpose","task_description":"Exploit c-06 HugeGraph","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_child_1","content":"8080 open"}]}}`, at.Add(11*second)),
		blockEvent("ev-13", `{"type":"system","subtype":"task_notification","task_id":"a3653e970f04f2dc0","status":"completed","summary":"Exploit c-06 HugeGraph"}`, at.Add(12*second)),
		blockEvent("ev-14", `{"type":"system","subtype":"task_notification","task_id":"a0bc0943e0fee5b04","status":"failed","summary":"telnet login"}`, at.Add(13*second)),
	}
}

func findBlocks(t *testing.T, entries []transcript.Entry) []transcript.Entry {
	t.Helper()
	var blocks []transcript.Entry
	for _, entry := range entries {
		if entry.Kind == "subagent_block" {
			blocks = append(blocks, entry)
		}
	}
	return blocks
}

// The unified conversation rule: every child projects as one collapsed,
// attributed block anchored at its spawning Agent tool-call row, aggregating
// all of that child's items despite interleaving; zero child items render as
// main-thread rows; main-thread rows stay ordinary rows.
func TestBuildWindowProjectsAsyncChildBlocks(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "session-1", Title: "Solve challenges", CreatedAt: at}
	got := transcript.Build(subject, fleetEvents(at))

	for _, entry := range got {
		if entry.Kind != "subagent_block" {
			for _, leak := range []string{"hugegraph exploited", "telnet done", "8080 open", "root shell", "curl hugegraph"} {
				if strings.Contains(entry.Text, leak) || entry.ToolCallID == "call_child_1" || entry.ToolCallID == "call_child_7" {
					t.Fatalf("child work must not render as a main-thread row: %#v", entry)
				}
			}
		}
	}

	blocks := findBlocks(t, got)
	if len(blocks) != 2 {
		t.Fatalf("expected one block per child, got %d: %#v", len(blocks), blocks)
	}

	byChild := map[string]transcript.Entry{}
	for _, block := range blocks {
		byChild[block.Details["agent_id"].(string)] = block
	}
	c06 := byChild["a3653e970f04f2dc0"]
	c07 := byChild["a0bc0943e0fee5b04"]

	if c06.Status != "completed" || c07.Status != "failed" {
		t.Fatalf("block states = %q / %q, want completed / failed", c06.Status, c07.Status)
	}
	if c06.Details["subagent_type"] != "general-purpose" || c06.Details["description"] != "Exploit c-06 HugeGraph" {
		t.Fatalf("c06 header details = %#v", c06.Details)
	}
	if c07.Details["subagent_type"] != "Explore" || c07.Details["description"] != "telnet login" {
		t.Fatalf("c07 header details = %#v", c07.Details)
	}

	// Aggregation despite interleaving: each block carries only its own
	// child's items, in arrival order.
	c06Items := c06.Details["items"].([]any)
	if len(c06Items) != 3 {
		t.Fatalf("c06 items = %#v, want tool_use + text + tool_result", c06Items)
	}
	first := c06Items[0].(map[string]any)
	if first["kind"] != "tool_call" || first["tool_name"] != "Bash" {
		t.Fatalf("c06 first item = %#v, want the child Bash call", first)
	}
	c07Items := c07.Details["items"].([]any)
	if len(c07Items) != 2 {
		t.Fatalf("c07 items = %#v, want tool_result + text", c07Items)
	}

	// Anchoring: each block sits directly after its spawning Agent tool-call
	// row (and that call's paired async-launch ack), never elsewhere.
	requireBlockAfterSpawn(t, got, "call_spawn_c06", "subagent-call_spawn_c06")
	requireBlockAfterSpawn(t, got, "call_spawn_c07", "subagent-call_spawn_c07")

	// Main-thread rows stay ordinary rows.
	var sawSpawn, sawAck bool
	for _, entry := range got {
		if entry.Kind == "tool_call" && entry.ToolCallID == "call_spawn_c06" && entry.ToolName == "Agent" {
			sawSpawn = true
		}
		if entry.Kind == "tool_result" && entry.ToolCallID == "call_spawn_c06" {
			sawAck = true
		}
	}
	if !sawSpawn || !sawAck {
		t.Fatalf("main-thread spawn rows missing: spawn=%v ack=%v in %#v", sawSpawn, sawAck, got)
	}
}

func requireBlockAfterSpawn(t *testing.T, entries []transcript.Entry, spawnCallID, blockID string) {
	t.Helper()
	for index, entry := range entries {
		if entry.ID == blockID {
			// Walk backwards over the spawn call and its paired ack result.
			cursor := index - 1
			if cursor >= 0 && entries[cursor].Kind == "tool_result" && entries[cursor].ToolCallID == spawnCallID {
				cursor--
			}
			if cursor < 0 || entries[cursor].Kind != "tool_call" || entries[cursor].ToolCallID != spawnCallID {
				t.Fatalf("block %s must anchor at the spawning tool-call %s; found preceding %#v", blockID, spawnCallID, entries[cursor])
			}
			return
		}
	}
	t.Fatalf("block %s not found in entries", blockID)
}

// Recognized synchronous child shapes switch from dropped to the same
// collapsed block: Codex subAgentActivity, legacy Claude task records, and Pi
// subagents:record each render one block keyed by the child identity.
func TestBuildWindowProjectsSyncChildShapesAsBlocks(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "task-1", Title: "Do work", CreatedAt: at}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "codex"}, CreatedAt: at},
		blockEvent("ev-2", `{"item":{"id":"item-sa-1","type":"subAgentActivity","agentThreadId":"thread-child-1","agentPath":"security/recon","kind":"started"}}`, at.Add(time.Second)),
		blockEvent("ev-3", `{"type":"system","subtype":"task_started","task_id":"bbr05bd75","summary":"Explore FTP directory"}`, at.Add(2*time.Second)),
		blockEvent("ev-4", `{"type":"custom","customType":"subagents:record","data":{"id":"agent-abc123","type":"Explore","description":"Scan the target","status":"completed"}}`, at.Add(3*time.Second)),
		blockEvent("ev-5", `{"type":"assistant","message":{"content":[{"type":"text","text":"Main thread continues."}]}}`, at.Add(4*time.Second)),
	}

	got := transcript.Build(subject, events)

	blocks := findBlocks(t, got)
	if len(blocks) != 3 {
		t.Fatalf("expected three child blocks (codex, claude, pi), got %#v", blocks)
	}
	byID := map[string]transcript.Entry{}
	for _, block := range blocks {
		byID[block.ID] = block
	}
	if _, ok := byID["subagent-thread-child-1"]; !ok {
		t.Fatalf("codex child block missing: %#v", byID)
	}
	if _, ok := byID["subagent-bbr05bd75"]; !ok {
		t.Fatalf("claude task child block missing: %#v", byID)
	}
	pi := byID["subagent-agent-abc123"]
	if !ok(pi.Details) {
		t.Fatalf("pi child block missing: %#v", byID)
	}
	if pi.Status != "completed" {
		t.Fatalf("pi block status = %q, want completed", pi.Status)
	}

	var sawMain bool
	for _, entry := range got {
		if entry.Kind == "message" && entry.Text == "Main thread continues." {
			sawMain = true
		}
	}
	if !sawMain {
		t.Fatalf("main-thread message missing: %#v", got)
	}
}

func ok(details map[string]any) bool { return details != nil }

// Forward-only: a session stored before the fix (unmarked multiplexed items,
// no task lifecycle records) replays exactly as the legacy projection — the
// same rows, same ids, and no Subagent Activity blocks.
func TestBuildWindowReplaysPreFixSessionUnchanged(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	subject := transcript.Subject{ID: "task-1", Title: "Solve c-06", CreatedAt: at}
	events := []transcript.Event{
		{ID: "ev-1", Seq: 1, Kind: "lifecycle", Payload: map[string]any{"phase": "started", "adapter": "claude_code"}, CreatedAt: at},
		blockEvent("ev-2", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_01a067a54dba7273b1e0e0d4","name":"Agent","input":{"description":"Exploit c-06 HugeGraph"}}]}}`, at.Add(time.Second)),
		blockEvent("ev-3", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_01a067a54dba7273b1e0e0d4","content":"Async agent launched successfully."}]}}`, at.Add(2*time.Second)),
		// Turn-less unmarked child records, exactly as the pre-fix bridge stored them.
		blockEvent("ev-4", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_Eg1oCkgwoKqYB0UgfxW5k948","content":"arthas config dumped"}]}}`, at.Add(3*time.Second)),
		blockEvent("ev-5", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"arthas exposed"}]}}`, at.Add(4*time.Second)),
	}

	got := transcript.Build(subject, events)

	if len(findBlocks(t, got)) != 0 {
		t.Fatalf("pre-fix session must not gain blocks: %#v", got)
	}
	expected := []struct {
		id   string
		kind string
		role string
	}{
		{"task-1-goal", "message", "user"},
		{"ev-1-continuation", "continuation", "system"},
		{"ev-2-tool-call-0", "tool_call", "assistant"},
		{"ev-3-tool-result-0", "tool_result", "tool"},
		{"ev-4-tool-result-0", "tool_result", "tool"},
		{"ev-5-message-0", "message", "assistant"},
	}
	if len(got) != len(expected) {
		t.Fatalf("entry count = %d, want %d: %#v", len(got), len(expected), got)
	}
	for index, want := range expected {
		if got[index].ID != want.id || got[index].Kind != want.kind || got[index].Role != want.role {
			t.Fatalf("entry[%d] = %s/%s/%s, want %s/%s/%s", index, got[index].ID, got[index].Kind, got[index].Role, want.id, want.kind, want.role)
		}
	}
}

// Each retained window builds independently, so one child must resolve to the
// same block identity in every window that sees any of its records. Items key
// by the spawning tool-call id; lifecycle records join through the spawn
// linkage the bridge stamps on every follow-up record.
func TestBuildWindowBlockIdentityStableAcrossWindows(t *testing.T) {
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	taskStarted := blockEvent("ev-1", `{"type":"system","subtype":"task_started","task_id":"a37dc4e90ed17c84b","tool_use_id":"call_spawn_b02","description":"Exploit b-02","subagent_type":"general-purpose","task_type":"local_agent"}`, at)
	item := blockEvent("ev-2", `{"type":"assistant","parent_tool_use_id":"call_spawn_b02","subagent_type":"general-purpose","task_description":"Exploit b-02","message":{"role":"assistant","content":[{"type":"text","text":"child work"}]}}`, at.Add(time.Second))
	settled := blockEvent("ev-3", `{"type":"system","subtype":"task_updated","task_id":"a37dc4e90ed17c84b","tool_use_id":"call_spawn_b02","patch":{"status":"failed"}}`, at.Add(2*time.Second))
	spawnCall := blockEvent("ev-0", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_spawn_b02","name":"Agent","input":{"description":"Exploit b-02"}}]}}`, at.Add(-1*time.Second))

	subject := transcript.Subject{ID: "task-1", Title: "work", CreatedAt: at}
	context := transcript.WindowContext{Continuation: 1, Adapter: "claude_code"}
	// Window A (older page): spawn call, task_started, and the child item.
	windowA := transcript.BuildWindow(subject, []transcript.Event{spawnCall, taskStarted, item}, context)
	// Window B (delta): only the settled lifecycle record.
	windowB := transcript.BuildWindow(subject, []transcript.Event{settled}, context)

	var idA, idB string
	for _, entry := range windowA {
		if entry.Kind == "subagent_block" {
			idA = entry.ID
		}
	}
	for _, entry := range windowB {
		if entry.Kind == "subagent_block" {
			idB = entry.ID
		}
	}
	if idA == "" || idA != idB {
		t.Fatalf("block identity differs across windows: A=%q B=%q", idA, idB)
	}
	if !strings.HasPrefix(idA, "subagent-call_spawn_b02") {
		t.Fatalf("block id = %q, want the spawn-keyed identity", idA)
	}
	// Window B's block carries the durable task id and settled state.
	for _, entry := range windowB {
		if entry.Kind == "subagent_block" {
			if entry.Status != "failed" {
				t.Fatalf("window B status = %q, want failed", entry.Status)
			}
			if entry.Details["agent_id"] != "a37dc4e90ed17c84b" {
				t.Fatalf("window B agent_id = %v", entry.Details["agent_id"])
			}
		}
	}
}
