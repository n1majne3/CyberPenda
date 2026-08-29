package runtimeoutput

import (
	"strings"
	"time"
)

// This file normalizes provider child-agent activity into Subagent Activity
// turns. The Runtime Harness only observes these records; it never spawns or
// schedules subagents. Each provider parser projects the wire shape into the
// shared normalized concept: durable child identity (ProviderItemID), a coarse
// activity state (LifecyclePhase), an operator-facing label (Text), and the
// provider id (Tool). Raw provider payloads are never dumped into the label.

// parseCodexSubAgentActivity projects a Codex App Server subAgentActivity
// thread item. Identity is the child thread id; status is the activity kind.
func parseCodexSubAgentActivity(record map[string]any, createdAt time.Time) []Turn {
	childThreadID := firstText(record, "agentThreadId", "agent_thread_id")
	if childThreadID == "" {
		return nil
	}
	kind := strings.ToLower(firstText(record, "kind"))
	agentPath := firstText(record, "agentPath", "agent_path")
	label := agentPath
	if label == "" {
		label = "Subagent " + childThreadID
	}
	return []Turn{{
		ProviderItemID: childThreadID,
		LifecyclePhase: codexSubagentActivityState(kind),
		Kind:           KindSubagentActivity,
		Role:           roleAssistant,
		Text:           label,
		Tool:           "codex",
		Details:        nilIfEmpty(map[string]any{"agent_path": agentPath}),
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}}
}

// parseCodexCollabAgentToolCall projects a Codex collabAgentToolCall thread
// item. The collab tool call reports the target agents' coarse states keyed by
// child thread id; each receiving thread becomes one Subagent Activity entry.
func parseCodexCollabAgentToolCall(record map[string]any, createdAt time.Time) []Turn {
	receivers := stringSlice(record, "receiverThreadIds", "receiver_thread_ids")
	if len(receivers) == 0 {
		return nil
	}
	states, _ := record["agentsStates"].(map[string]any)
	toolName := firstText(record, "tool")
	turns := make([]Turn, 0, len(receivers))
	for _, childThreadID := range receivers {
		childThreadID = strings.TrimSpace(childThreadID)
		if childThreadID == "" {
			continue
		}
		status := ""
		if state, ok := states[childThreadID].(map[string]any); ok {
			status = firstText(state, "status")
		}
		label := "Subagent " + childThreadID
		if toolName != "" {
			label = toolName + ": " + childThreadID
		}
		turns = append(turns, Turn{
			ProviderItemID: childThreadID,
			LifecyclePhase: codexCollabAgentState(status),
			Kind:           KindSubagentActivity,
			Role:           roleAssistant,
			Text:           label,
			Tool:           "codex",
			Details:        nilIfEmpty(map[string]any{"collab_tool": toolName}),
			ContentIndex:   -1,
			CreatedAt:      createdAt,
		})
	}
	return turns
}

// parseClaudeSubagentActivity projects a Claude Code stream-json system record
// whose subtype is task_started / task_progress / task_failed into one
// Subagent Activity turn keyed by task_id.
func parseClaudeSubagentActivity(record map[string]any, createdAt time.Time) []Turn {
	taskID := firstText(record, "task_id", "taskId")
	if taskID == "" {
		return nil
	}
	subtype := strings.ToLower(firstText(record, "subtype"))
	label := firstText(record, "summary", "description")
	if label == "" {
		label = "Subagent " + taskID
	}
	return []Turn{{
		ProviderItemID: taskID,
		LifecyclePhase: claudeSubagentActivityState(subtype, firstText(record, "status")),
		Kind:           KindSubagentActivity,
		Role:           roleAssistant,
		Text:           label,
		Tool:           "claude_code",
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}}
}

// isClaudeSubagentSystemRecord reports whether a Claude system record carries
// Task-tool subagent activity worth projecting (rather than noise to drop).
func isClaudeSubagentSystemRecord(record map[string]any) bool {
	if !strings.EqualFold(firstText(record, "type"), "system") {
		return false
	}
	switch strings.ToLower(firstText(record, "subtype")) {
	case "task_started", "task_progress", "task_failed":
		return true
	default:
		return false
	}
}

// codexSubagentActivityState maps a subAgentActivity kind onto the coarse
// shared state. "interacted" is mid-activity, so the entry stays started. The
// codex-cli 0.149.0 subAgentActivity kind enum is only started|interacted|
// interrupted; a terminal "completed" state arrives via collabAgentToolCall
// instead, so this parser intentionally never produces completed.
func codexSubagentActivityState(kind string) string {
	switch kind {
	case SubagentActivityInterrupted:
		return SubagentActivityInterrupted
	default:
		return SubagentActivityStarted
	}
}

// codexCollabAgentState maps a CollabAgentStatus onto the coarse shared state.
func codexCollabAgentState(status string) string {
	switch strings.ToLower(status) {
	case "completed":
		return SubagentActivityCompleted
	case "errored", "notfound", "shutdown":
		return SubagentActivityFailed
	case "interrupted":
		return SubagentActivityInterrupted
	default:
		// pendingInit, running, or unknown: the child is live.
		return SubagentActivityStarted
	}
}

// claudeSubagentActivityState maps a task_* subtype onto the coarse shared
// state. Progress keeps the subagent live; only a terminal failure settles it.
// Claude Code's stream-json emits no task_completed signal, so a successful
// Task-tool subagent remains "started" — that is the only terminal state the
// wire exposes for this provider.
func claudeSubagentActivityState(subtype, status string) string {
	if subtype == "task_failed" || strings.EqualFold(status, "failed") {
		return SubagentActivityFailed
	}
	return SubagentActivityStarted
}

func stringSlice(record map[string]any, keys ...string) []string {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []any:
			out := make([]string, 0, len(typed))
			for _, entry := range typed {
				if text, ok := entry.(string); ok {
					out = append(out, text)
				}
			}
			return out
		case []string:
			return typed
		}
	}
	return nil
}
