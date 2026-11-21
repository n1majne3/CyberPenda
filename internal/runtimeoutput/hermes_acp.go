package runtimeoutput

import (
	"strings"
	"time"
)

func isHermesACPRecord(record map[string]any) bool {
	method := strings.ToLower(strings.TrimSpace(stringValue(record, "method")))
	if method == "session/update" || strings.HasPrefix(method, "session/") {
		return true
	}
	if update, ok := mapValue(record, "update"); ok && firstText(update, "sessionUpdate", "session_update") != "" {
		return true
	}
	return firstText(record, "sessionUpdate", "session_update") != ""
}

func parseHermesACPRecord(record map[string]any, opts ParseOptions, createdAt time.Time) []Turn {
	if !isHermesACPRecord(record) {
		return nil
	}
	update := record
	if params, ok := mapValue(record, "params"); ok {
		if nested, ok := mapValue(params, "update"); ok {
			update = nested
		} else {
			update = params
		}
	} else if nested, ok := mapValue(record, "update"); ok {
		update = nested
	}
	kind := strings.ToLower(strings.TrimSpace(firstText(update, "sessionUpdate", "session_update")))
	switch kind {
	case "agent_message_chunk", "agent_thought_chunk":
		if kind == "agent_thought_chunk" && !opts.IncludeThinking {
			return nil
		}
		text := firstText(update, "text")
		if text == "" {
			if content, ok := mapValue(update, "content"); ok {
				text = firstText(content, "text", "content")
			}
		}
		if text == "" {
			return nil
		}
		turnKind := KindText
		if kind == "agent_thought_chunk" {
			turnKind = KindReasoning
			text = stripANSI(text)
		}
		phase := ""
		if kind == "agent_thought_chunk" {
			phase = firstText(update, "phase")
			if phase == "" {
				phase = ReasoningPhaseStreaming
			}
		}
		return []Turn{{Kind: turnKind, Role: roleAssistant, Text: text, ProviderItemID: firstText(update, "item_id", "itemId", "provider_item_id"), LifecyclePhase: phase, ContentIndex: -1, CreatedAt: createdAt}}
	case "tool_call":
		turn := toolUseTurn(update, createdAt)
		if turn.Tool == "" {
			turn.Tool = firstText(update, "title")
		}
		if turn.ToolCallID == "" {
			turn.ToolCallID = firstText(update, "toolCallId", "tool_call_id")
		}
		if input, ok := mapValue(update, "rawInput"); ok {
			turn.Input = input
		} else if input, ok := mapValue(update, "raw_input"); ok {
			turn.Input = input
		}
		if len(turn.Input) == 0 {
			turn.Input = hermesToolInputFallback(update)
		}
		if len(turn.Input) > 0 {
			details := turn.Details
			if details == nil {
				details = map[string]any{}
			}
			if _, ok := details["input"]; !ok {
				details["input"] = turn.Input
			}
			turn.Details = details
		}
		return []Turn{turn}
	case "tool_call_update":
		turn := toolResultTurn(update, createdAt)
		if turn.Tool == "" {
			turn.Tool = firstText(update, "title")
		}
		if turn.ToolCallID == "" {
			turn.ToolCallID = firstText(update, "toolCallId", "tool_call_id")
		}
		if turn.Output == "" {
			turn.Output = firstText(update, "content", "rawOutput", "raw_output")
		}
		return []Turn{turn}
	default:
		return nil
	}
}

func hermesToolInputFallback(update map[string]any) map[string]any {
	if locs, ok := sliceValue(update, "locations"); ok {
		paths := make([]string, 0, len(locs))
		for _, loc := range locs {
			typed, ok := loc.(map[string]any)
			if !ok {
				continue
			}
			if path := firstText(typed, "path"); path != "" {
				paths = append(paths, path)
			}
		}
		if len(paths) == 1 {
			return map[string]any{"path": paths[0]}
		}
		if len(paths) > 1 {
			return map[string]any{"paths": paths}
		}
	}
	text := strings.TrimSpace(firstText(update, "content", "text"))
	if strings.HasPrefix(text, "$ ") {
		return map[string]any{"command": strings.TrimPrefix(text, "$ ")}
	}
	return nil
}
