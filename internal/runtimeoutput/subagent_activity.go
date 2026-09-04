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
// whose subtype is task_started / task_progress / task_failed (legacy sync
// Task-tool vocabulary) or task_notification / task_updated (the current
// async-agent vocabulary) into one Subagent Activity turn keyed by task_id.
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
	phase := claudeSubagentActivityState(subtype, firstText(record, "status"))
	if subtype == "task_updated" {
		if patch, ok := mapValue(record, "patch"); ok {
			phase = claudeSubagentActivityState("task_updated", firstText(patch, "status"))
		}
	}
	details := map[string]any{}
	if spawnToolUseID := firstText(record, "tool_use_id", "toolUseId"); spawnToolUseID != "" {
		details["spawn_tool_use_id"] = spawnToolUseID
	}
	if subagentType := firstText(record, "subagent_type", "subagentType", "agent_type", "agentType"); subagentType != "" {
		details["subagent_type"] = subagentType
	}
	if taskType := firstText(record, "task_type", "taskType"); taskType != "" {
		details["task_type"] = taskType
	}
	return []Turn{{
		ProviderItemID: taskID,
		LifecyclePhase: phase,
		Kind:           KindSubagentActivity,
		Role:           roleAssistant,
		Text:           label,
		Tool:           "claude_code",
		Details:        nilIfEmpty(details),
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}}
}

// isClaudeSubagentSystemRecord reports whether a Claude system record carries
// child-agent activity worth projecting (rather than noise to drop).
func isClaudeSubagentSystemRecord(record map[string]any) bool {
	if !strings.EqualFold(firstText(record, "type"), "system") {
		return false
	}
	return isClaudeSubagentLifecycleSubtype(firstText(record, "subtype"))
}

// isClaudeSubagentLifecycleSubtype reports whether a Claude system record
// subtype carries child-agent lifecycle activity. task_started / task_progress
// / task_failed are the legacy sync Task-tool vocabulary; task_notification /
// task_updated are the current async-agent vocabulary.
func isClaudeSubagentLifecycleSubtype(subtype string) bool {
	switch strings.ToLower(subtype) {
	case "task_started", "task_progress", "task_failed", "task_notification", "task_updated":
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

// claudeSubagentActivityState maps a task_* subtype or task_notification /
// task_updated status onto the coarse shared state. Progress keeps the
// subagent live; task_notification status and task_updated patch statuses are
// the current terminal vocabulary (completed / failed / stopped). Legacy
// stream-json emitted task_failed instead; that spelling stays supported.
func claudeSubagentActivityState(subtype, status string) string {
	switch subtype {
	case "task_failed":
		return SubagentActivityFailed
	case "task_notification":
		switch strings.ToLower(status) {
		case "completed":
			return SubagentActivityCompleted
		case "failed":
			return SubagentActivityFailed
		case "stopped", "interrupted":
			return SubagentActivityInterrupted
		default:
			return SubagentActivityStarted
		}
	case "task_updated":
		switch strings.ToLower(status) {
		case "completed":
			return SubagentActivityCompleted
		case "failed", "killed":
			return SubagentActivityFailed
		default:
			// pending, running, paused: the child is live.
			return SubagentActivityStarted
		}
	default:
		if strings.EqualFold(status, "failed") {
			return SubagentActivityFailed
		}
		return SubagentActivityStarted
	}
}

// isPiSubagentRecord reports whether a Pi record carries a settled top-level
// subagent record. One-shot mode: a session custom entry
// {type:"custom", customType:"subagents:record"}. Persistent RPC mode: the
// bridge-forwarded entry_appended frame
// {type:"entry_appended", entry:{customType:"subagents:record", ...}}.
func isPiSubagentRecord(record map[string]any) bool {
	if firstText(record, "customType", "custom_type") == "subagents:record" {
		return true
	}
	if entry, ok := mapValue(record, "entry"); ok {
		return firstText(entry, "customType", "custom_type") == "subagents:record"
	}
	return false
}

// parsePiSubagentActivity projects a Pi subagents:record into one settled
// Subagent Activity turn keyed by the durable AgentRecord id. The Harness only
// observes these records; it never spawns or schedules subagents.
func parsePiSubagentActivity(record map[string]any, createdAt time.Time) []Turn {
	source := record
	if entry, ok := mapValue(record, "entry"); ok {
		source = entry
	}
	data, ok := mapValue(source, "data")
	if !ok {
		return nil
	}
	id := firstText(data, "id")
	if id == "" {
		return nil
	}
	label := firstText(data, "description", "type")
	if label == "" {
		label = "Subagent " + id
	}
	return []Turn{{
		ProviderItemID: id,
		LifecyclePhase: piSubagentActivityState(firstText(data, "status")),
		Kind:           KindSubagentActivity,
		Role:           roleAssistant,
		Text:           label,
		Tool:           "pi",
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}}
}

// piSubagentActivityState maps the AgentRecord status vocabulary onto the
// coarse shared state. Running/queued agents stay started.
func piSubagentActivityState(status string) string {
	switch strings.ToLower(status) {
	case "completed", "steered":
		return SubagentActivityCompleted
	case "error", "aborted", "stopped":
		return SubagentActivityFailed
	default:
		return SubagentActivityStarted
	}
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
