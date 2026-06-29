package runtimeoutput

import (
	"encoding/json"
	"strings"
	"time"
)

// ParseLine normalizes one runtime stdout/stderr line into turns. The bool is
// true when the line is treated as plain-text fallback rather than structured JSON.
func ParseLine(text string, createdAt time.Time, opts ParseOptions) ([]Turn, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return []Turn{{Kind: KindText, Text: trimmed, ContentIndex: -1, CreatedAt: createdAt}}, true
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
		return []Turn{{Kind: KindText, Text: trimmed, ContentIndex: -1, CreatedAt: createdAt}}, true
	}

	turns := ParseRecord(record, opts, createdAt)
	if len(turns) == 0 {
		return []Turn{{Kind: KindText, Text: trimmed, ContentIndex: -1, CreatedAt: createdAt}}, true
	}
	return turns, false
}

// ParseRecord projects one provider JSON object into normalized turns.
func ParseRecord(record map[string]any, opts ParseOptions, createdAt time.Time) []Turn {
	if item, ok := mapValue(record, "item"); ok {
		if turns := ParseRecord(item, opts, createdAt); len(turns) > 0 {
			return turns
		}
	}
	if delta, ok := mapValue(record, "delta"); ok {
		if text := firstText(delta, "text", "content"); text != "" {
			return []Turn{{Kind: KindText, Role: roleAssistant, Text: text, ContentIndex: -1, CreatedAt: createdAt}}
		}
	}

	recordType := stringValue(record, "type")
	switch recordType {
	case "system":
		if isIgnorableSystemRecord(record) {
			return nil
		}
		return parseMessageRecord(record, opts, roleFromType(recordType), createdAt)
	case "assistant", "user", "message", "assistant_message", "agent_message", "response.output_text", "output_text", "message_delta", "content_block_delta":
		return parseMessageRecord(record, opts, roleFromType(recordType), createdAt)
	case "tool_call", "function_call", "tool_use":
		return []Turn{toolUseTurn(record, createdAt)}
	case "tool_result", "function_call_output":
		return []Turn{toolResultTurn(record, createdAt)}
	case "result":
		if opts.IncludeErrors && isTruthy(record["is_error"]) {
			content := firstText(record, "error", "message", "content")
			if content == "" {
				content = stringValue(record, "subtype")
			}
			return []Turn{{Kind: KindError, Text: content, ContentIndex: -1, CreatedAt: createdAt}}
		}
		return nil
	case "error":
		if !opts.IncludeErrors {
			return nil
		}
		return []Turn{{Kind: KindError, Text: firstText(record, "error", "message", "content", "text"), ContentIndex: -1, CreatedAt: createdAt}}
	default:
		if recordType == "" && stringValue(record, "role") != "" {
			text := firstText(record, "text", "content", "message", "output")
			if text == "" {
				return nil
			}
			return []Turn{{Kind: KindText, Role: stringValue(record, "role"), Text: text, ContentIndex: -1, CreatedAt: createdAt}}
		}
		return nil
	}
}

func parseMessageRecord(record map[string]any, opts ParseOptions, role string, createdAt time.Time) []Turn {
	if message, ok := mapValue(record, "message"); ok {
		if mr := stringValue(message, "role"); mr == "toolResult" || mr == "tool" {
			if turn := toolResultTurn(message, createdAt); turn.Output != "" || turn.ToolCallID != "" || turn.Tool != "" {
				return []Turn{turn}
			}
		}
		if content, ok := sliceValue(message, "content"); ok {
			return parseContentBlocks(content, opts, role, createdAt)
		}
		if text := firstText(message, "text", "content", "message"); text != "" {
			return []Turn{{Kind: KindText, Role: role, Text: text, ContentIndex: -1, CreatedAt: createdAt}}
		}
	}
	if content, ok := sliceValue(record, "content"); ok {
		return parseContentBlocks(content, opts, role, createdAt)
	}
	if text := firstText(record, "text", "content", "message"); text != "" {
		return []Turn{{Kind: KindText, Role: role, Text: text, ContentIndex: -1, CreatedAt: createdAt}}
	}
	return nil
}

func parseContentBlocks(content []any, opts ParseOptions, role string, createdAt time.Time) []Turn {
	turns := make([]Turn, 0, len(content))
	for index, block := range content {
		switch value := block.(type) {
		case string:
			if value != "" {
				turns = append(turns, Turn{Kind: KindText, Role: role, Text: value, ContentIndex: index, CreatedAt: createdAt})
			}
		case map[string]any:
			blockType := strings.ToLower(stringValue(value, "type"))
			switch blockType {
			case "thinking":
				if !opts.IncludeThinking {
					continue
				}
				if text := thinkingText(value); text != "" {
					turns = append(turns, Turn{Kind: KindThinking, Role: role, Text: text, ContentIndex: index, CreatedAt: createdAt})
				}
			case "text":
				if text := firstText(value, "text", "content"); text != "" {
					turns = append(turns, Turn{Kind: KindText, Role: role, Text: text, ContentIndex: index, CreatedAt: createdAt})
				}
			case "tool_use", "tool_call", "toolcall", "function_call":
				turn := toolUseTurn(value, createdAt)
				turn.ContentIndex = index
				turns = append(turns, turn)
			case "tool_result", "toolresult", "function_call_output":
				turn := toolResultTurn(value, createdAt)
				turn.ContentIndex = index
				turns = append(turns, turn)
			default:
				if text := firstText(value, "text", "content", "message", "output"); text != "" {
					turns = append(turns, Turn{Kind: KindText, Role: role, Text: text, ContentIndex: index, CreatedAt: createdAt})
				}
			}
		}
	}
	return turns
}

func toolUseTurn(record map[string]any, createdAt time.Time) Turn {
	input := map[string]any{}
	details := map[string]any{}
	for _, key := range []string{"input", "arguments", "parameters"} {
		if value, ok := record[key]; ok {
			details[key] = value
			if typed, ok := value.(map[string]any); ok {
				for k, v := range typed {
					input[k] = v
				}
			}
		}
	}
	return Turn{
		Kind:         KindToolUse,
		Role:         roleAssistant,
		Tool:         toolName(record),
		ToolCallID:   firstText(record, "tool_call_id", "tool_use_id", "call_id", "id"),
		Input:        nilIfEmpty(input),
		Details:      nilIfEmpty(details),
		ContentIndex: -1,
		CreatedAt:    createdAt,
	}
}

func toolResultTurn(record map[string]any, createdAt time.Time) Turn {
	details := map[string]any{}
	for _, key := range []string{"is_error", "isError", "error"} {
		if value, ok := record[key]; ok {
			details[key] = value
		}
	}
	return Turn{
		Kind:         KindToolResult,
		Role:         roleTool,
		Tool:         firstText(record, "tool_name", "toolName", "name"),
		Output:       firstText(record, "output", "content", "result", "text"),
		ToolCallID:   firstText(record, "tool_call_id", "tool_use_id", "call_id", "toolCallId", "id"),
		Details:      nilIfEmpty(details),
		ContentIndex: -1,
		CreatedAt:    createdAt,
	}
}

func thinkingText(record map[string]any) string {
	return firstText(record, "thinking", "text", "content")
}

const (
	roleAssistant = "assistant"
	roleUser      = "user"
	roleSystem    = "system"
	roleTool      = "tool"
)

func roleFromType(recordType string) string {
	switch recordType {
	case "user":
		return roleUser
	case "system":
		return roleSystem
	default:
		return roleAssistant
	}
}

func toolName(record map[string]any) string {
	if name := firstText(record, "name", "tool_name"); name != "" {
		return name
	}
	if function, ok := mapValue(record, "function"); ok {
		return firstText(function, "name")
	}
	return ""
}

func stringValue(record map[string]any, key string) string {
	value, ok := record[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

func firstText(record map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		if text := valueToText(value); text != "" {
			return text
		}
	}
	return ""
}

func valueToText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := valueToText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text := firstText(typed, "text", "content", "message", "output"); text != "" {
			return text
		}
		return ""
	case nil:
		return ""
	default:
		return ""
	}
}

func mapValue(record map[string]any, key string) (map[string]any, bool) {
	value, ok := record[key]
	if !ok {
		return nil, false
	}
	typed, ok := value.(map[string]any)
	return typed, ok
}

func sliceValue(record map[string]any, key string) ([]any, bool) {
	value, ok := record[key]
	if !ok {
		return nil, false
	}
	typed, ok := value.([]any)
	return typed, ok
}

func nilIfEmpty(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	return values
}

func isTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "true" || typed == "1"
	default:
		return false
	}
}