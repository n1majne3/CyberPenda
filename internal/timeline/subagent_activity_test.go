package timeline_test

import (
	"testing"
	"time"

	"pentest/internal/timeline"
)

// End to end: a Codex App Server subAgentActivity notification stored as a
// runtime_output event becomes one subagent_activity timeline entry carrying
// the child identity, coarse state, and provider.
func TestBuildProjectsCodexSubAgentActivity(t *testing.T) {
	at := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	events := []timeline.Event{
		event("runtime_output", `{"threadId":"thread-parent","turnId":"turn-1","item":{"id":"item-sa-1","type":"subAgentActivity","agentThreadId":"thread-child-1","agentPath":"security/recon","kind":"started"}}`, at),
	}

	got := timeline.Build(events)

	if len(got) != 1 {
		t.Fatalf("expected one timeline item, got %#v", got)
	}
	item := got[0]
	if item.Type != "subagent_activity" {
		t.Fatalf("type = %q, want subagent_activity", item.Type)
	}
	if item.Tool != "codex" {
		t.Fatalf("provider = %q, want codex", item.Tool)
	}
	if item.Status != "started" {
		t.Fatalf("status = %q, want started", item.Status)
	}
	if item.Content == "" {
		t.Fatal("expected an operator-facing label, got empty content")
	}
	if item.ID == "" {
		t.Fatal("expected a stable item id derived from the child identity")
	}
}

// A settled collab state advances the existing child entry in place rather
// than appending a second row.
func TestBuildAdvancesCodexChildEntryOnSettle(t *testing.T) {
	at := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	events := []timeline.Event{
		event("runtime_output", `{"item":{"id":"item-sa-1","type":"subAgentActivity","agentThreadId":"thread-child-1","agentPath":"security/recon","kind":"started"}}`, at),
		event("runtime_output", `{"item":{"id":"collab-2","type":"collabAgentToolCall","tool":"closeAgent","senderThreadId":"thread-parent","receiverThreadIds":["thread-child-1"],"status":"completed","agentsStates":{"thread-child-1":{"status":"completed"}}}}`, at.Add(time.Second)),
	}

	got := timeline.Build(events)

	if len(got) != 1 {
		t.Fatalf("expected one reconciled subagent entry, got %#v", got)
	}
	if got[0].Status != "completed" {
		t.Fatalf("status = %q, want completed", got[0].Status)
	}
}

// A Pi one-shot session subagents:record entry becomes a settled
// subagent_activity entry keyed by the durable AgentRecord id.
func TestBuildProjectsPiSubagentRecord(t *testing.T) {
	at := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	events := []timeline.Event{
		event("runtime_output", `{"type":"custom","customType":"subagents:record","data":{"id":"agent-abc123","type":"Explore","description":"Scan the target","status":"completed"}}`, at),
	}

	got := timeline.Build(events)

	if len(got) != 1 {
		t.Fatalf("expected one timeline item, got %#v", got)
	}
	item := got[0]
	if item.Type != "subagent_activity" || item.Tool != "pi" {
		t.Fatalf("item = %#v", item)
	}
	if item.Status != "completed" {
		t.Fatalf("status = %q, want completed", item.Status)
	}
	if item.Content != "Scan the target" {
		t.Fatalf("label = %q", item.Content)
	}
}

// Claude Code task_* records become a subagent_activity entry labeled by the
// task summary and keyed by task_id.
func TestBuildProjectsClaudeSubagentActivity(t *testing.T) {
	at := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	events := []timeline.Event{
		event("runtime_output", `{"type":"system","subtype":"task_started","task_id":"bbr05bd75","summary":"Explore FTP directory"}`, at),
	}

	got := timeline.Build(events)

	if len(got) != 1 {
		t.Fatalf("expected one timeline item, got %#v", got)
	}
	item := got[0]
	if item.Type != "subagent_activity" || item.Tool != "claude_code" {
		t.Fatalf("item = %#v", item)
	}
	if item.Status != "started" {
		t.Fatalf("status = %q, want started", item.Status)
	}
	if item.Content != "Explore FTP directory" {
		t.Fatalf("label = %q", item.Content)
	}
}
