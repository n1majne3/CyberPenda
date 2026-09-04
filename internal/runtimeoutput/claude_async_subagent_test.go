package runtimeoutput_test

import (
	"testing"
	"time"

	"pentest/internal/runtimeoutput"
)

// Claude Code async Agent-tool children multiplex their transcript items into
// the main session stream as ordinary assistant/user records. The stream marks
// every child record with the spawning tool-call id (parent_tool_use_id) plus
// subagent_type/task_description; runtimes that emit a per-item agentId use
// that instead. The parser must carry the child attribution key on every
// derived Turn so projections never render child work as main-thread rows.
func TestParseRecordStampsAsyncAgentIdentityOnMessageTurns(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	// Live wire shape (Claude Code 2.1.2xx SDK stream): child items carry
	// parent_tool_use_id, not agentId.
	turns := runtimeoutput.ParseRecord(map[string]any{
		"type":               "assistant",
		"parent_tool_use_id": "call_01a06c1712a57ca080ba25d8",
		"subagent_type":      "general-purpose",
		"task_description":   "Solve a-05 contract approval",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "telnet login succeeded"},
				map[string]any{"type": "tool_use", "id": "call_child_1", "name": "Bash", "input": map[string]any{"command": "ls"}},
			},
		},
	}, runtimeoutput.ParseOptions{}, at)
	if len(turns) != 2 {
		t.Fatalf("turns = %#v", turns)
	}
	for _, turn := range turns {
		if turn.AgentID != "call_01a06c1712a57ca080ba25d8" {
			t.Fatalf("AgentID = %q, want the spawning tool-call id on %#v", turn.AgentID, turn)
		}
	}

	// A per-item agentId, when a runtime emits one, wins over parent linkage.
	agentIDTurns := runtimeoutput.ParseRecord(map[string]any{
		"type":               "assistant",
		"agentId":            "accf5809249284700",
		"parent_tool_use_id": "call_other",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": "agent id marked"}},
		},
	}, runtimeoutput.ParseOptions{}, at)
	if len(agentIDTurns) != 1 || agentIDTurns[0].AgentID != "accf5809249284700" {
		t.Fatalf("agentId turns = %#v", agentIDTurns)
	}

	// snake_case and attributionAgent are accepted identity spellings; a user
	// tool_result from a child carries the same attribution.
	userTurns := runtimeoutput.ParseRecord(map[string]any{
		"type":     "user",
		"agent_id": "accf5809249284700",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_child_1", "content": "ok"}},
		},
	}, runtimeoutput.ParseOptions{}, at)
	if len(userTurns) != 1 || userTurns[0].AgentID != "accf5809249284700" {
		t.Fatalf("user child turns = %#v", userTurns)
	}

	attributed := runtimeoutput.ParseRecord(map[string]any{
		"type":             "assistant",
		"attributionAgent": "agent-a0f8d7221e42fb197",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": "attributed work"}},
		},
	}, runtimeoutput.ParseOptions{}, at)
	if len(attributed) != 1 || attributed[0].AgentID != "agent-a0f8d7221e42fb197" {
		t.Fatalf("attributed turns = %#v", attributed)
	}

	// Records without markers keep today's main-thread projection.
	plain := runtimeoutput.ParseRecord(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": "main thread"}},
		},
	}, runtimeoutput.ParseOptions{}, at)
	if len(plain) != 1 || plain[0].AgentID != "" {
		t.Fatalf("plain turns = %#v", plain)
	}

	// Top-level tool records from a child carry the same attribution.
	toolTurns := runtimeoutput.ParseRecord(map[string]any{
		"type":               "tool_use",
		"parent_tool_use_id": "call_01a06c1712a57ca080ba25d8",
		"id":                 "call_child_9",
		"name":               "Bash",
		"input":              map[string]any{"command": "id"},
	}, runtimeoutput.ParseOptions{}, at)
	if len(toolTurns) != 1 || toolTurns[0].AgentID != "call_01a06c1712a57ca080ba25d8" {
		t.Fatalf("top-level tool turns = %#v", toolTurns)
	}
}

// The current Claude stream vocabulary settles children through
// task_notification (status completed/failed/stopped), not the legacy
// task_failed subtype.
func TestParseRecordClaudeTaskNotificationSettlesChild(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	turns := runtimeoutput.ParseRecord(map[string]any{
		"type":        "system",
		"subtype":     "task_notification",
		"task_id":     "bbr05bd75",
		"tool_use_id": "call_01a067a54dba7273b1e0e0d4",
		"status":      "completed",
		"summary":     "Explored FTP directory",
	}, runtimeoutput.ParseOptions{}, at)
	if len(turns) != 1 {
		t.Fatalf("task_notification turns = %#v", turns)
	}
	got := turns[0]
	if got.Kind != runtimeoutput.KindSubagentActivity || got.ProviderItemID != "bbr05bd75" {
		t.Fatalf("task_notification = %#v", got)
	}
	if got.LifecyclePhase != runtimeoutput.SubagentActivityCompleted {
		t.Fatalf("status completed = %q, want completed", got.LifecyclePhase)
	}
	if got.Text != "Explored FTP directory" {
		t.Fatalf("label = %q", got.Text)
	}

	failed := runtimeoutput.ParseRecord(map[string]any{
		"type":    "system",
		"subtype": "task_notification",
		"task_id": "bbr05bd75",
		"status":  "failed",
		"summary": "Explored FTP directory",
	}, runtimeoutput.ParseOptions{}, at)
	if len(failed) != 1 || failed[0].LifecyclePhase != runtimeoutput.SubagentActivityFailed {
		t.Fatalf("status failed = %#v", failed)
	}

	stopped := runtimeoutput.ParseRecord(map[string]any{
		"type":    "system",
		"subtype": "task_notification",
		"task_id": "bbr05bd75",
		"status":  "stopped",
		"summary": "Explored FTP directory",
	}, runtimeoutput.ParseOptions{}, at)
	if len(stopped) != 1 || stopped[0].LifecyclePhase != runtimeoutput.SubagentActivityInterrupted {
		t.Fatalf("status stopped = %#v", stopped)
	}
}

// task_started carries the async child's spawn linkage: the tool-use id of the
// Agent call that spawned it plus the subagent type for the block header.
func TestParseRecordClaudeTaskStartedCarriesSpawnLinkage(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	turns := runtimeoutput.ParseRecord(map[string]any{
		"type":          "system",
		"subtype":       "task_started",
		"task_id":       "a3653e970f04f2dc0",
		"tool_use_id":   "call_01a06c1712a57ca080ba25d8",
		"description":   "Solve a-05 contract approval",
		"subagent_type": "general-purpose",
		"task_type":     "local_agent",
	}, runtimeoutput.ParseOptions{}, at)
	if len(turns) != 1 {
		t.Fatalf("task_started turns = %#v", turns)
	}
	got := turns[0]
	if got.Kind != runtimeoutput.KindSubagentActivity || got.ProviderItemID != "a3653e970f04f2dc0" {
		t.Fatalf("task_started = %#v", got)
	}
	if got.LifecyclePhase != runtimeoutput.SubagentActivityStarted {
		t.Fatalf("phase = %q, want started", got.LifecyclePhase)
	}
	if got.Text != "Solve a-05 contract approval" {
		t.Fatalf("label = %q", got.Text)
	}
	if got.Details["spawn_tool_use_id"] != "call_01a06c1712a57ca080ba25d8" {
		t.Fatalf("spawn linkage details = %#v", got.Details)
	}
	if got.Details["subagent_type"] != "general-purpose" {
		t.Fatalf("subagent_type details = %#v", got.Details)
	}
}

// task_updated patch statuses advance the coarse child state in place.
func TestParseRecordClaudeTaskUpdatedAdvancesState(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	turns := runtimeoutput.ParseRecord(map[string]any{
		"type":    "system",
		"subtype": "task_updated",
		"task_id": "bbr05bd75",
		"patch":   map[string]any{"status": "completed"},
	}, runtimeoutput.ParseOptions{}, at)
	if len(turns) != 1 || turns[0].Kind != runtimeoutput.KindSubagentActivity {
		t.Fatalf("task_updated turns = %#v", turns)
	}
	if turns[0].LifecyclePhase != runtimeoutput.SubagentActivityCompleted {
		t.Fatalf("patch completed = %q", turns[0].LifecyclePhase)
	}

	running := runtimeoutput.ParseRecord(map[string]any{
		"type":    "system",
		"subtype": "task_updated",
		"task_id": "bbr05bd75",
		"patch":   map[string]any{"status": "running"},
	}, runtimeoutput.ParseOptions{}, at)
	if len(running) != 1 || running[0].LifecyclePhase != runtimeoutput.SubagentActivityStarted {
		t.Fatalf("patch running = %#v", running)
	}
}

// The task lifecycle system records must survive storage/timeline filtering;
// the level-signal background_tasks_changed remains ignorable noise.
func TestTaskLifecycleRecordsSurviveFiltering(t *testing.T) {
	kept := []string{
		`{"type":"system","subtype":"task_notification","task_id":"t1","status":"completed","summary":"done"}`,
		`{"type":"system","subtype":"task_updated","task_id":"t1","patch":{"status":"completed"}}`,
	}
	for _, line := range kept {
		if runtimeoutput.ShouldIgnoreForStorage(line) {
			t.Fatalf("expected %s to be stored", line)
		}
		if runtimeoutput.ShouldIgnoreForTimeline(line) {
			t.Fatalf("expected %s to reach the timeline", line)
		}
	}
	level := `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"t1","task_type":"subagent","description":"d"}]}`
	if !runtimeoutput.ShouldIgnoreForTimeline(level) {
		t.Fatal("expected background_tasks_changed to stay timeline noise")
	}
}

// Only agent-like background tasks are child agents. A backgrounded bash
// command (task_type local_bash) is not Subagent Activity and must not
// project; the type is only present on task_started, so later records are
// filtered by the bridge's task-type tracking.
func TestParseRecordClaudeTaskStartedFiltersNonAgentTasks(t *testing.T) {
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		taskType string
		present  bool
	}{
		{"local_agent is a child agent", "local_agent", true},
		{"subagent is a child agent", "subagent", true},
		{"absent type stays supported", "", true},
		{"local_bash is not a child agent", "local_bash", false},
		{"monitor is not a child agent", "monitor", false},
	}
	for _, tc := range cases {
		record := map[string]any{
			"type":        "system",
			"subtype":     "task_started",
			"task_id":     "b" + tc.taskType,
			"description": "d",
		}
		if tc.taskType != "" {
			record["task_type"] = tc.taskType
		}
		turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{}, at)
		if tc.present && len(turns) != 1 {
			t.Fatalf("%s: expected one turn, got %#v", tc.name, turns)
		}
		if !tc.present && len(turns) != 0 {
			t.Fatalf("%s: expected no turns, got %#v", tc.name, turns)
		}
	}
}
