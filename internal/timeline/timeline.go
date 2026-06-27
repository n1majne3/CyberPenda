// Package timeline projects retained task events into a multica-style agent
// transcript timeline: thinking, tool calls, tool results, agent text, and errors.
package timeline

import (
	"encoding/json"
	"strings"
	"time"

	"pentest/internal/task"
	"pentest/internal/transcript"
)

// Item is one chronologically ordered timeline entry.
type Item struct {
	Seq       int            `json:"seq"`
	Type      string         `json:"type"`
	Tool      string         `json:"tool,omitempty"`
	Content   string         `json:"content,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Output    string         `json:"output,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Build projects task events into coalesced timeline items.
func Build(events []task.Event) []Item {
	items := make([]Item, 0, len(events))
	nextSeq := 1
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle {
			continue
		}
		if event.Kind != task.EventKindRuntimeOutput {
			continue
		}
		text := stringValue(event.Payload, "text")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if isIgnorableTimelineLine(text) {
			continue
		}
		parsed := itemsForRuntimeLine(text, event)
		for _, item := range parsed {
			item.Seq = nextSeq
			nextSeq++
			items = append(items, item)
		}
	}
	return coalesceItems(items)
}

func itemsForRuntimeLine(text string, event task.Event) []Item {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return []Item{{
			Type:      "text",
			Content:   trimmed,
			CreatedAt: event.CreatedAt,
		}}
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
		return []Item{{
			Type:      "text",
			Content:   trimmed,
			CreatedAt: event.CreatedAt,
		}}
	}

	if items := parseRecord(record, event.CreatedAt); len(items) > 0 {
		return items
	}

	return []Item{{
		Type:      "text",
		Content:   trimmed,
		CreatedAt: event.CreatedAt,
	}}
}

func parseRecord(record map[string]any, createdAt time.Time) []Item {
	if item, ok := mapValue(record, "item"); ok {
		if items := parseRecord(item, createdAt); len(items) > 0 {
			return items
		}
	}
	if delta, ok := mapValue(record, "delta"); ok {
		if text := firstText(delta, "text", "content"); text != "" {
			return []Item{{Type: "text", Content: text, CreatedAt: createdAt}}
		}
	}

	recordType := stringValue(record, "type")
	switch recordType {
	case "system":
		return nil
	case "assistant", "user", "message", "assistant_message", "agent_message", "response.output_text", "output_text", "message_delta", "content_block_delta":
		return parseMessageRecord(record, createdAt)
	case "tool_call", "function_call", "tool_use":
		return []Item{toolUseItem(record, createdAt)}
	case "tool_result", "function_call_output":
		return []Item{toolResultItem(record, createdAt)}
	case "result":
		if isTruthy(record["is_error"]) {
			content := firstText(record, "error", "message", "content")
			if content == "" {
				content = stringValue(record, "subtype")
			}
			return []Item{{Type: "error", Content: content, CreatedAt: createdAt}}
		}
		return nil
	case "error":
		return []Item{{Type: "error", Content: firstText(record, "error", "message", "content", "text"), CreatedAt: createdAt}}
	default:
		if recordType == "" && stringValue(record, "role") != "" {
			text := firstText(record, "text", "content", "message", "output")
			if text == "" {
				return nil
			}
			return []Item{{Type: "text", Content: text, CreatedAt: createdAt}}
		}
		return nil
	}
}

func parseMessageRecord(record map[string]any, createdAt time.Time) []Item {
	if message, ok := mapValue(record, "message"); ok {
		if mr := stringValue(message, "role"); mr == "toolResult" || mr == "tool" {
			if item := toolResultItem(message, createdAt); item.Output != "" || item.Tool != "" {
				return []Item{item}
			}
		}
		if content, ok := sliceValue(message, "content"); ok {
			return parseContentBlocks(content, createdAt)
		}
		if text := firstText(message, "text", "content", "message"); text != "" {
			return []Item{{Type: "text", Content: text, CreatedAt: createdAt}}
		}
	}
	if content, ok := sliceValue(record, "content"); ok {
		return parseContentBlocks(content, createdAt)
	}
	if text := firstText(record, "text", "content", "message"); text != "" {
		return []Item{{Type: "text", Content: text, CreatedAt: createdAt}}
	}
	return nil
}

func parseContentBlocks(content []any, createdAt time.Time) []Item {
	items := make([]Item, 0, len(content))
	for _, block := range content {
		switch value := block.(type) {
		case string:
			if value != "" {
				items = append(items, Item{Type: "text", Content: value, CreatedAt: createdAt})
			}
		case map[string]any:
			blockType := strings.ToLower(stringValue(value, "type"))
			switch blockType {
			case "thinking":
				if text := thinkingText(value); text != "" {
					items = append(items, Item{Type: "thinking", Content: text, CreatedAt: createdAt})
				}
			case "text":
				if text := firstText(value, "text", "content"); text != "" {
					items = append(items, Item{Type: "text", Content: text, CreatedAt: createdAt})
				}
			case "tool_use", "tool_call", "toolcall", "function_call":
				items = append(items, toolUseItem(value, createdAt))
			case "tool_result", "toolresult", "function_call_output":
				items = append(items, toolResultItem(value, createdAt))
			default:
				if text := firstText(value, "text", "content", "message", "output"); text != "" {
					items = append(items, Item{Type: "text", Content: text, CreatedAt: createdAt})
				}
			}
		}
	}
	return items
}

func toolUseItem(record map[string]any, createdAt time.Time) Item {
	input := map[string]any{}
	for _, key := range []string{"input", "arguments", "parameters"} {
		if value, ok := record[key]; ok {
			if typed, ok := value.(map[string]any); ok {
				for k, v := range typed {
					input[k] = v
				}
			}
		}
	}
	return Item{
		Type:      "tool_use",
		Tool:      toolName(record),
		Input:     nilIfEmpty(input),
		CreatedAt: createdAt,
	}
}

func toolResultItem(record map[string]any, createdAt time.Time) Item {
	return Item{
		Type:      "tool_result",
		Tool:      firstText(record, "tool_name", "toolName", "name"),
		Output:    firstText(record, "output", "content", "result", "text"),
		CreatedAt: createdAt,
	}
}

func thinkingText(record map[string]any) string {
	if text := firstText(record, "thinking", "text", "content"); text != "" {
		return text
	}
	return ""
}

func isIgnorableTimelineLine(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || !strings.HasPrefix(text, "{") {
		return false
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		return false
	}
	if transcript.IsIgnorableRuntimeLine(text) && !isThinkingOnlyAssistantRecord(record) {
		return true
	}
	recordType := stringValue(record, "type")
	switch recordType {
	case "ping", "keepalive":
		return true
	case "result":
		return !isTruthy(record["is_error"])
	}
	return false
}

func isThinkingOnlyAssistantRecord(record map[string]any) bool {
	if stringValue(record, "type") != "assistant" {
		return false
	}
	message, ok := mapValue(record, "message")
	if !ok {
		return false
	}
	content, ok := sliceValue(message, "content")
	if !ok || len(content) == 0 {
		return false
	}
	for _, block := range content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			return false
		}
		blockType := strings.ToLower(stringValue(blockMap, "type"))
		if blockType != "thinking" {
			return false
		}
	}
	return true
}

func coalesceItems(items []Item) []Item {
	if len(items) == 0 {
		return items
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		if len(out) == 0 {
			out = append(out, item)
			continue
		}
		prev := out[len(out)-1]
		if canMergeStreamingText(prev, item) {
			out[len(out)-1] = Item{
				Seq:       prev.Seq,
				Type:      prev.Type,
				Tool:      prev.Tool,
				Content:   prev.Content + item.Content,
				Input:     prev.Input,
				Output:    prev.Output,
				CreatedAt: item.CreatedAt,
			}
			continue
		}
		out = append(out, item)
	}
	return out
}

func canMergeStreamingText(prev, next Item) bool {
	return (prev.Type == "thinking" || prev.Type == "text") && prev.Type == next.Type
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