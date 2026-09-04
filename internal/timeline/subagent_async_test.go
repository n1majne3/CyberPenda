package timeline_test

import (
	"testing"
	"time"

	"pentest/internal/timeline"
)

// agentFleetEvents replays the async Agent-tool path from issue #266: the
// main thread launches children through the Agent tool (spawn tool_use plus
// async-launch ack), settles its own turn, and the children's items keep
// flowing as agentId-marked records interleaved with the task lifecycle
// system records.
func agentFleetEvents(at time.Time) []timeline.Event {
	return []timeline.Event{
		event("runtime_output", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_spawn_c06","name":"Agent","input":{"description":"Exploit c-06 HugeGraph","prompt":"solve c-06"}}]}}`, at),
		event("runtime_output", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_spawn_c06","content":"Async agent launched successfully."}]}}`, at.Add(time.Second)),
		event("runtime_output", `{"type":"system","subtype":"task_started","task_id":"bbr05bd75","tool_use_id":"call_spawn_c06","description":"Exploit c-06 HugeGraph","subagent_type":"general-purpose","task_type":"local_agent"}`, at.Add(2*time.Second)),
		event("runtime_output", `{"type":"assistant","parent_tool_use_id":"call_spawn_c06","subagent_type":"general-purpose","task_description":"Exploit c-06 HugeGraph","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_child_1","name":"Bash","input":{"command":"curl hugegraph"}}]}}`, at.Add(3*time.Second)),
		event("runtime_output", `{"type":"user","parent_tool_use_id":"call_spawn_c06","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_child_1","content":"8080 open"}]}}`, at.Add(4*time.Second)),
		event("runtime_output", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Waiting for subagents."}]}}`, at.Add(5*time.Second)),
		event("runtime_output", `{"type":"system","subtype":"task_notification","task_id":"bbr05bd75","status":"completed","summary":"Exploit c-06 HugeGraph"}`, at.Add(6*time.Second)),
	}
}

// The async fleet replay yields one Subagent Activity entry per child with the
// state advancing in place, keeps the main-thread Agent call and its
// async-launch ack as ordinary rows, and never projects the child's own items
// as unattributed main-thread tool rows.
func TestBuildProjectsAsyncAgentFleetSubagentActivity(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	got := timeline.Build(agentFleetEvents(at))

	var activity []timeline.Item
	for _, item := range got {
		if item.Type == "subagent_activity" {
			activity = append(activity, item)
			continue
		}
		if item.Type == "tool_use" && item.Tool == "Bash" {
			t.Fatalf("child tool row must not surface as a main-thread tool_use: %#v", item)
		}
		if item.Type == "tool_result" && item.Output == "8080 open" {
			t.Fatalf("child tool result must not surface as a main-thread tool_result: %#v", item)
		}
	}
	if len(activity) != 1 {
		t.Fatalf("expected exactly one subagent_activity entry, got %#v", activity)
	}
	if activity[0].Status != "completed" {
		t.Fatalf("child state = %q, want completed after task_notification", activity[0].Status)
	}
	if activity[0].Tool != "claude_code" {
		t.Fatalf("provider = %q, want claude_code", activity[0].Tool)
	}

	// Main-thread rows survive: the Agent spawn call and its ack result.
	var sawSpawn, sawAck, sawWaiting bool
	for _, item := range got {
		if item.Type == "tool_use" && item.Tool == "Agent" {
			sawSpawn = true
		}
		if item.Type == "tool_result" && item.Output == "Async agent launched successfully." {
			sawAck = true
		}
		if item.Type == "text" && item.Content == "Waiting for subagents." {
			sawWaiting = true
		}
	}
	if !sawSpawn || !sawAck || !sawWaiting {
		t.Fatalf("main-thread rows missing: spawn=%v ack=%v waiting=%v in %#v", sawSpawn, sawAck, sawWaiting, got)
	}
}

// Two interleaved async children settle independently: each keeps exactly one
// advancing entry keyed by its own identity.
func TestBuildSeparatesInterleavedAsyncChildren(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	events := agentFleetEvents(at)
	events = append(events,
		event("runtime_output", `{"type":"system","subtype":"task_started","task_id":"cxx99120","tool_use_id":"call_spawn_c07","description":"telnet login","subagent_type":"general-purpose","task_type":"local_agent"}`, at.Add(7*time.Second)),
		event("runtime_output", `{"type":"assistant","parent_tool_use_id":"call_spawn_c07","message":{"role":"assistant","content":[{"type":"text","text":"logged in as root"}]}}`, at.Add(8*time.Second)),
		event("runtime_output", `{"type":"assistant","agentId":"bbr05bd75","message":{"role":"assistant","content":[{"type":"text","text":"hugegraph exploited"}]}}`, at.Add(9*time.Second)),
		event("runtime_output", `{"type":"system","subtype":"task_notification","task_id":"cxx99120","status":"failed","summary":"telnet login"}`, at.Add(10*time.Second)),
	)

	got := timeline.Build(events)

	byChild := map[string]string{}
	for _, item := range got {
		if item.Type != "subagent_activity" {
			continue
		}
		spawn, _ := item.Input["spawn_tool_use_id"].(string)
		byChild[spawn] = item.Status
	}
	if len(byChild) != 2 {
		t.Fatalf("expected two distinct child entries, got %#v", byChild)
	}
	if byChild["call_spawn_c06"] != "completed" {
		t.Fatalf("c06 child state = %q, want completed", byChild["call_spawn_c06"])
	}
	if byChild["call_spawn_c07"] != "failed" {
		t.Fatalf("c07 child state = %q, want failed", byChild["call_spawn_c07"])
	}
	for _, item := range got {
		if (item.Type == "text" || item.Type == "tool_use" || item.Type == "tool_result") &&
			(item.Content == "logged in as root" || item.Content == "hugegraph exploited") {
			t.Fatalf("child-attributed item must not surface in the main timeline flow: %#v", item)
		}
	}
}
