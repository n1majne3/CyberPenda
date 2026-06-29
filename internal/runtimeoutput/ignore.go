package runtimeoutput

import (
	"encoding/json"
	"strings"
)

// ShouldIgnoreForStorage reports whether a raw runtime line should not be stored
// or shown in the task conversation transcript.
func ShouldIgnoreForStorage(text string) bool {
	record, ok := parseJSONRecord(text)
	if !ok {
		return false
	}
	return isIgnorableStorageRecord(record)
}

// IsThinkingOnlyAssistantLine reports assistant records that contain only thinking blocks.
func IsThinkingOnlyAssistantLine(text string) bool {
	record, ok := parseJSONRecord(text)
	if !ok {
		return false
	}
	return isThinkingOnlyAssistantRecord(record)
}

// ShouldIgnoreForTimeline reports whether a stored runtime line should be omitted
// from the agent timeline projection. Thinking-only assistant lines are kept.
func ShouldIgnoreForTimeline(text string) bool {
	record, ok := parseJSONRecord(text)
	if !ok {
		return false
	}
	if isIgnorableStorageRecord(record) && !isThinkingOnlyAssistantRecord(record) {
		return true
	}
	switch stringValue(record, "type") {
	case "ping", "keepalive":
		return true
	case "result":
		return !isTruthy(record["is_error"])
	}
	return false
}

func isIgnorableStorageRecord(record map[string]any) bool {
	return isIgnorableSystemRecord(record) || isIgnorableRecordType(record) || isThinkingOnlyAssistantRecord(record)
}

func isIgnorableRecordType(record map[string]any) bool {
	switch stringValue(record, "type") {
	case "ping", "keepalive", "result":
		return true
	default:
		return false
	}
}

func isIgnorableSystemRecord(record map[string]any) bool {
	if stringValue(record, "type") != "system" {
		return false
	}
	subtype := stringValue(record, "subtype")
	switch subtype {
	case "thinking_tokens", "init":
		return true
	}
	return strings.HasPrefix(subtype, "task_")
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

func parseJSONRecord(text string) (map[string]any, bool) {
	text = strings.TrimSpace(text)
	if text == "" || !strings.HasPrefix(text, "{") {
		return nil, false
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		return nil, false
	}
	return record, true
}