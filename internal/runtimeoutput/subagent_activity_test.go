package runtimeoutput_test

import (
	"testing"
	"time"

	"pentest/internal/runtimeoutput"
)

// Codex App Server v2 emits subAgentActivity thread items for child-thread
// lifecycle. Each must normalize into one Subagent Activity turn whose
// identity is the child thread id and whose status tracks the activity kind.
func TestParseRecordCodexSubAgentActivity(t *testing.T) {
	at := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	started := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"threadId": "thread-parent",
		"turnId":   "turn-1",
		"item": map[string]any{
			"type":          "subAgentActivity",
			"id":            "item-sa-1",
			"agentThreadId": "thread-child-1",
			"agentPath":     "security/recon",
			"kind":          "started",
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/started"}, runtimeoutput.ParseOptions{}, at)
	if len(started) != 1 {
		t.Fatalf("subAgentActivity turns = %#v", started)
	}
	got := started[0]
	if got.Kind != runtimeoutput.KindSubagentActivity {
		t.Fatalf("kind = %q, want subagent_activity", got.Kind)
	}
	if got.ProviderItemID != "thread-child-1" {
		t.Fatalf("ProviderItemID = %q, want child thread id", got.ProviderItemID)
	}
	if got.LifecyclePhase != "started" {
		t.Fatalf("LifecyclePhase = %q, want started", got.LifecyclePhase)
	}
	if got.Tool != "codex" {
		t.Fatalf("provider Tool = %q, want codex", got.Tool)
	}
	if got.Text == "" {
		t.Fatal("expected a human-readable label, got empty text")
	}
	if got.Details["agent_path"] != "security/recon" {
		t.Fatalf("Details agent_path = %#v", got.Details)
	}

	// An interrupted activity on the same child thread must carry the same
	// identity so the projection updates one entry instead of appending.
	interrupted := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":          "subAgentActivity",
			"id":            "item-sa-2",
			"agentThreadId": "thread-child-1",
			"agentPath":     "security/recon",
			"kind":          "interrupted",
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{}, at)
	if len(interrupted) != 1 || interrupted[0].ProviderItemID != "thread-child-1" || interrupted[0].LifecyclePhase != "interrupted" {
		t.Fatalf("interrupted subAgentActivity = %#v", interrupted)
	}
}

// Codex collabAgentToolCall thread items report the target agent's coarse
// state. A spawn with an in-progress/running target projects one started
// Subagent Activity keyed by the receiving child thread id; a settled target
// projects a settled entry.
func TestParseRecordCodexCollabAgentToolCall(t *testing.T) {
	at := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	spawn := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":              "collabAgentToolCall",
			"id":                "collab-1",
			"tool":              "spawnAgent",
			"senderThreadId":    "thread-parent",
			"receiverThreadIds": []any{"thread-child-9"},
			"status":            "inProgress",
			"agentsStates": map[string]any{
				"thread-child-9": map[string]any{"status": "running"},
			},
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/started"}, runtimeoutput.ParseOptions{}, at)
	if len(spawn) != 1 {
		t.Fatalf("spawn collabAgentToolCall turns = %#v", spawn)
	}
	if spawn[0].Kind != runtimeoutput.KindSubagentActivity || spawn[0].ProviderItemID != "thread-child-9" {
		t.Fatalf("spawn projection = %#v", spawn[0])
	}
	if spawn[0].LifecyclePhase != "started" {
		t.Fatalf("spawn status = %q, want started", spawn[0].LifecyclePhase)
	}

	settled := runtimeoutput.ParseRecordWithMeta(map[string]any{
		"item": map[string]any{
			"type":              "collabAgentToolCall",
			"id":                "collab-2",
			"tool":              "closeAgent",
			"senderThreadId":    "thread-parent",
			"receiverThreadIds": []any{"thread-child-9"},
			"status":            "completed",
			"agentsStates": map[string]any{
				"thread-child-9": map[string]any{"status": "completed"},
			},
		},
	}, runtimeoutput.RecordMeta{ProviderEvent: "item/completed"}, runtimeoutput.ParseOptions{}, at)
	if len(settled) != 1 || settled[0].ProviderItemID != "thread-child-9" || settled[0].LifecyclePhase != "completed" {
		t.Fatalf("settled collabAgentToolCall = %#v", settled)
	}
}

// Claude Code stream-json emits system records with subtypes task_started /
// task_progress / task_failed for Task-tool subagents. These must normalize
// into Subagent Activity turns keyed by task_id.
func TestParseRecordClaudeSubagentActivity(t *testing.T) {
	at := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	started := runtimeoutput.ParseRecord(map[string]any{
		"type":    "system",
		"subtype": "task_started",
		"task_id": "bbr05bd75",
		"summary": "Explore FTP directory",
	}, runtimeoutput.ParseOptions{}, at)
	if len(started) != 1 {
		t.Fatalf("task_started turns = %#v", started)
	}
	got := started[0]
	if got.Kind != runtimeoutput.KindSubagentActivity {
		t.Fatalf("kind = %q, want subagent_activity", got.Kind)
	}
	if got.ProviderItemID != "bbr05bd75" {
		t.Fatalf("ProviderItemID = %q, want task_id", got.ProviderItemID)
	}
	if got.LifecyclePhase != "started" {
		t.Fatalf("LifecyclePhase = %q, want started", got.LifecyclePhase)
	}
	if got.Tool != "claude_code" {
		t.Fatalf("provider Tool = %q, want claude_code", got.Tool)
	}
	if got.Text != "Explore FTP directory" {
		t.Fatalf("label Text = %q", got.Text)
	}

	failed := runtimeoutput.ParseRecord(map[string]any{
		"type":    "system",
		"subtype": "task_failed",
		"task_id": "bbr05bd75",
		"status":  "failed",
		"summary": "Explore FTP directory",
	}, runtimeoutput.ParseOptions{}, at)
	if len(failed) != 1 || failed[0].ProviderItemID != "bbr05bd75" || failed[0].LifecyclePhase != "failed" {
		t.Fatalf("task_failed = %#v", failed)
	}
}

// task_progress keeps the subagent live without settling it.
func TestParseRecordClaudeTaskProgressStaysStarted(t *testing.T) {
	at := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	progress := runtimeoutput.ParseRecord(map[string]any{
		"type":        "system",
		"subtype":     "task_progress",
		"task_id":     "bbr05bd75",
		"description": "Exploit: privacy-policy",
	}, runtimeoutput.ParseOptions{}, at)
	if len(progress) != 1 {
		t.Fatalf("task_progress turns = %#v", progress)
	}
	if progress[0].Kind != runtimeoutput.KindSubagentActivity || progress[0].LifecyclePhase != "started" {
		t.Fatalf("task_progress = %#v", progress[0])
	}
	if progress[0].Text != "Exploit: privacy-policy" {
		t.Fatalf("task_progress label = %q", progress[0].Text)
	}
}

// ReconcileLifecycle must collapse repeated activity for one subagent identity
// into a single entry whose status advances, preserving original ordering.
func TestReconcileLifecycleAdvancesSubagentActivity(t *testing.T) {
	at := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	turns := []runtimeoutput.Turn{
		{Kind: runtimeoutput.KindSubagentActivity, ProviderItemID: "thread-child-1", LifecyclePhase: "started", Tool: "codex", Text: "recon", SourceSeq: 1, CreatedAt: at},
		{Kind: runtimeoutput.KindSubagentActivity, ProviderItemID: "thread-child-1", LifecyclePhase: "completed", Tool: "codex", Text: "recon", SourceSeq: 2, CreatedAt: at},
	}
	out := runtimeoutput.ReconcileLifecycle(turns)
	if len(out) != 1 {
		t.Fatalf("reconciled subagent activity = %#v", out)
	}
	if out[0].LifecyclePhase != "completed" {
		t.Fatalf("advanced status = %q, want completed", out[0].LifecyclePhase)
	}
	if out[0].SourceSeq != 1 {
		t.Fatalf("SourceSeq = %d, want original 1", out[0].SourceSeq)
	}
}
