package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"pentest/internal/runtimeoutput"
)

func IsIgnorableRuntimeLine(text string) bool {
	return runtimeoutput.ShouldIgnoreForStorage(text)
}

func isIgnorableUnparsedRuntimeLine(text string) bool {
	return runtimeoutput.IsThinkingOnlyAssistantLine(text)
}

func runtimeFallback(event Event, continuation int, text, stream string) Entry {
	return Entry{
		ID:           event.ID + "-runtime",
		Seq:          event.Seq,
		Continuation: continuation,
		Kind:         KindRuntimeOutput,
		Role:         RoleRuntime,
		Text:         text,
		Stream:       stream,
		Status:       StatusCollapsed,
		CreatedAt:    event.CreatedAt,
	}
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

func stringValue(record map[string]any, key string) string {
	value, ok := record[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
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
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(data)
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
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

func compactPayload(payload map[string]any, skipKeys ...string) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	skip := map[string]bool{}
	for _, key := range skipKeys {
		skip[key] = true
	}
	details := map[string]any{}
	for key, value := range payload {
		if !skip[key] {
			details[key] = value
		}
	}
	return nilIfEmpty(details)
}

func nilIfEmpty(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	return values
}
