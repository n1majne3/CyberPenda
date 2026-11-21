package runtimeoutput

import (
	"strings"
	"time"
)

func parseCodexCommandExecution(record map[string]any, meta RecordMeta, createdAt time.Time) []Turn {
	command := firstText(record, "command")
	output := firstText(record, "aggregatedOutput", "aggregated_output", "output")
	id := firstText(record, "id")
	if command == "" && output == "" && id == "" {
		return nil
	}
	use := Turn{
		ProviderItemID: id,
		LifecyclePhase: codexLifecyclePhase(meta.ProviderEvent),
		Kind:           KindToolUse,
		Role:           roleAssistant,
		Tool:           "command_execution",
		ToolCallID:     id,
		Input:          nilIfEmpty(map[string]any{"command": command}),
		Details:        nilIfEmpty(map[string]any{"command": command}),
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}
	status := strings.ToLower(firstText(record, "status"))
	if output == "" && (status == "" || status == "inprogress" || status == "in_progress") {
		return []Turn{use}
	}
	return []Turn{use, Turn{
		ProviderItemID: id,
		LifecyclePhase: codexLifecyclePhase(meta.ProviderEvent),
		Kind:           KindToolResult,
		Role:           roleTool,
		Tool:           "command_execution",
		ToolCallID:     id,
		Output:         output,
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}}
}

func parseCodexMCPToolCall(record map[string]any, meta RecordMeta, createdAt time.Time) []Turn {
	name := firstText(record, "tool", "toolName", "tool_name", "name")
	if name == "" {
		return nil
	}
	if !strings.HasPrefix(name, "mcp__") {
		if server := firstText(record, "server", "serverName", "server_name"); server != "" {
			name = "mcp__" + server + "__" + name
		}
	}
	id := firstText(record, "id")
	use := Turn{
		ProviderItemID: id,
		LifecyclePhase: codexLifecyclePhase(meta.ProviderEvent),
		Kind:           KindToolUse,
		Role:           roleAssistant,
		Tool:           name,
		ToolCallID:     id,
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}
	status := strings.ToLower(firstText(record, "status"))
	if status == "" || status == "inprogress" || status == "in_progress" {
		return []Turn{use}
	}
	return []Turn{use, Turn{
		ProviderItemID: id,
		LifecyclePhase: codexLifecyclePhase(meta.ProviderEvent),
		Kind:           KindToolResult,
		Role:           roleTool,
		Tool:           name,
		ToolCallID:     id,
		Output:         firstText(record, "aggregatedOutput", "aggregated_output", "output", "result", "content"),
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}}
}

func parseCodexReasoning(record map[string]any, meta RecordMeta, opts ParseOptions, createdAt time.Time) []Turn {
	if !opts.IncludeReasoningSummaries {
		return nil
	}
	id := firstText(record, "id")
	phase := codexLifecyclePhase(meta.ProviderEvent)
	if phase == "started" {
		return nil
	}
	text := stripANSI(firstText(record, "content", "summary"))
	if text == "" {
		return nil
	}
	return []Turn{{
		ProviderItemID: id,
		LifecyclePhase: phase,
		Kind:           KindReasoning,
		Role:           roleAssistant,
		Text:           text,
		ContentIndex:   -1,
		CreatedAt:      createdAt,
	}}
}

func codexLifecyclePhase(providerEvent string) string {
	switch strings.ToLower(strings.TrimSpace(providerEvent)) {
	case "item/started":
		return "started"
	case "item/completed":
		return ReasoningPhaseCompleted
	case "item/reasoning/summarytextdelta":
		return ReasoningPhaseStreaming
	default:
		return ""
	}
}
