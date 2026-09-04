package runtimeoutput_test

import (
	"testing"
	"time"

	"pentest/internal/runtimeoutput"
)

// Claude Code async Agent-tool children multiplex their transcript items into
// the main session stream as ordinary assistant/user records marked with a
// per-item agentId (plus isSidechain). The parser must carry that child
// identity on every derived Turn so projections never render child work as
// main-thread rows.
func TestParseRecordStampsAsyncAgentIdentityOnMessageTurns(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	turns := runtimeoutput.ParseRecord(map[string]any{
		"type":        "assistant",
		"agentId":     "accf5809249284700",
		"isSidechain": true,
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
		if turn.AgentID != "accf5809249284700" {
			t.Fatalf("AgentID = %q, want async child id on %#v", turn.AgentID, turn)
		}
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
		"type":    "tool_use",
		"agentId": "accf5809249284700",
		"id":      "call_child_9",
		"name":    "Bash",
		"input":   map[string]any{"command": "id"},
	}, runtimeoutput.ParseOptions{}, at)
	if len(toolTurns) != 1 || toolTurns[0].AgentID != "accf5809249284700" {
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
		"task_id":       "bbr05bd75",
		"tool_use_id":   "call_01a067a54dba7273b1e0e0d4",
		"description":   "Find buffer overflow bugs",
		"subagent_type": "Explore",
		"task_type":     "subagent",
	}, runtimeoutput.ParseOptions{}, at)
	if len(turns) != 1 {
		t.Fatalf("task_started turns = %#v", turns)
	}
	got := turns[0]
	if got.Kind != runtimeoutput.KindSubagentActivity || got.ProviderItemID != "bbr05bd75" {
		t.Fatalf("task_started = %#v", got)
	}
	if got.LifecyclePhase != runtimeoutput.SubagentActivityStarted {
		t.Fatalf("phase = %q, want started", got.LifecyclePhase)
	}
	if got.Text != "Find buffer overflow bugs" {
		t.Fatalf("label = %q", got.Text)
	}
	if got.Details["spawn_tool_use_id"] != "call_01a067a54dba7273b1e0e0d4" {
		t.Fatalf("spawn linkage details = %#v", got.Details)
	}
	if got.Details["subagent_type"] != "Explore" {
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
