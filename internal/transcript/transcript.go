// Package transcript projects retained task events into a readable conversation
// transcript. It does not persist new state; unknown provider output is kept as
// collapsed runtime output so historical tasks remain readable.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pentest/internal/task"
)

const (
	KindMessage       = "message"
	KindToolCall      = "tool_call"
	KindToolResult    = "tool_result"
	KindRuntimeOutput = "runtime_output"
	KindContinuation  = "continuation"

	RoleAssistant = "assistant"
	RoleRuntime   = "runtime"
	RoleSystem    = "system"
	RoleTool      = "tool"
	RoleUser      = "user"

	StatusCollapsed = "collapsed"
)

// Entry is one projected transcript row.
type Entry struct {
	ID           string         `json:"id"`
	Seq          int            `json:"seq"`
	Continuation int            `json:"continuation"`
	Kind         string         `json:"kind"`
	Role         string         `json:"role"`
	Text         string         `json:"text,omitempty"`
	ToolCallID   string         `json:"tool_call_id,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
	Stream       string         `json:"stream,omitempty"`
	Status       string         `json:"status,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// Build projects a task goal and its retained events into transcript entries.
func Build(subject task.Task, events []task.Event) []Entry {
	entries := make([]Entry, 0, len(events)+1)
	if strings.TrimSpace(subject.Goal) != "" {
		entries = append(entries, Entry{
			ID:           "task-" + subject.ID + "-goal",
			Seq:          0,
			Continuation: 0,
			Kind:         KindMessage,
			Role:         RoleUser,
			Text:         subject.Goal,
			CreatedAt:    subject.CreatedAt,
		})
	}

	continuation := 0
	adapter := ""
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle {
			next, ok := lifecycleEntry(event, continuation)
			if ok {
				if stringValue(event.Payload, "phase") == "started" {
					continuation++
					adapter = stringValue(event.Payload, "adapter")
					next.Continuation = continuation
				}
				entries = append(entries, next)
			}
			continue
		}
		entries = append(entries, entriesForEvent(event, continuation, adapter)...)
	}
	return entries
}

func lifecycleEntry(event task.Event, continuation int) (Entry, bool) {
	phase := stringValue(event.Payload, "phase")
	if phase == "" {
		return Entry{}, false
	}

	entry := Entry{
		ID:           event.ID + "-continuation",
		Seq:          event.Seq,
		Continuation: continuation,
		Kind:         KindContinuation,
		Role:         RoleSystem,
		Status:       phase,
		CreatedAt:    event.CreatedAt,
	}

	switch phase {
	case "started":
		adapter := stringValue(event.Payload, "adapter")
		next := continuation + 1
		if adapter == "" {
			entry.Text = fmt.Sprintf("Continuation #%d started", next)
		} else {
			entry.Text = fmt.Sprintf("Continuation #%d started with %s", next, adapter)
		}
		return entry, true
	case "completed", "failed", "stopped":
		if continuation <= 0 {
			entry.Text = "Task " + phase
		} else {
			entry.Text = fmt.Sprintf("Continuation #%d %s", continuation, phase)
		}
		return entry, true
	case "process_started":
		entry.ID = event.ID + "-process"
		entry.Kind = KindRuntimeOutput
		entry.Role = RoleRuntime
		entry.Status = StatusCollapsed
		entry.Text = "Runtime process started"
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	default:
		entry.Text = "Runtime lifecycle: " + phase
		entry.Status = StatusCollapsed
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	}
}

func entriesForEvent(event task.Event, continuation int, adapter string) []Entry {
	switch event.Kind {
	case task.EventKindSteering:
		directive := stringValue(event.Payload, "directive")
		if directive == "" {
			return nil
		}
		return []Entry{{
			ID:           event.ID + "-steering",
			Seq:          event.Seq,
			Continuation: continuation,
			Kind:         KindMessage,
			Role:         RoleUser,
			Text:         directive,
			Details:      compactPayload(event.Payload, "directive"),
			CreatedAt:    event.CreatedAt,
		}}
	case task.EventKindConversation:
		text := firstText(event.Payload, "text", "content", "message")
		if text == "" {
			return nil
		}
		role := stringValue(event.Payload, "role")
		if role == "" {
			role = RoleAssistant
		}
		return []Entry{{
			ID:           event.ID + "-message",
			Seq:          event.Seq,
			Continuation: continuation,
			Kind:         KindMessage,
			Role:         role,
			Text:         text,
			CreatedAt:    event.CreatedAt,
		}}
	case task.EventKindRuntimeOutput:
		text := stringValue(event.Payload, "text")
		stream := stringValue(event.Payload, "stream")
		if text == "" {
			return nil
		}
		if parsed := parseRuntimeOutput(event, continuation, adapter, text); len(parsed) > 0 {
			return parsed
		}
		return []Entry{runtimeFallback(event, continuation, text, stream)}
	default:
		return nil
	}
}

func parseRuntimeOutput(event task.Event, continuation int, adapter, text string) []Entry {
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		return nil
	}
	entries := parseRecord(event, continuation, adapter, record, "")
	if len(entries) == 0 {
		return nil
	}
	return entries
}

func parseRecord(event task.Event, continuation int, adapter string, record map[string]any, suffix string) []Entry {
	_ = adapter
	if item, ok := mapValue(record, "item"); ok {
		if entries := parseRecord(event, continuation, adapter, item, suffix); len(entries) > 0 {
			return entries
		}
	}
	if delta, ok := mapValue(record, "delta"); ok {
		if text := firstText(delta, "text", "content"); text != "" {
			return []Entry{messageEntry(event, continuation, event.ID+indexedSuffix("-message", suffix), RoleAssistant, text)}
		}
	}

	recordType := stringValue(record, "type")
	switch recordType {
	case "assistant", "user", "system":
		return parseMessageRecord(event, continuation, record, roleFromType(recordType))
	case "message", "assistant_message", "agent_message", "response.output_text", "output_text", "message_delta", "content_block_delta":
		role := stringValue(record, "role")
		if role == "" {
			role = RoleAssistant
		}
		text := firstText(record, "text", "content", "message", "delta", "output")
		if text == "" {
			return nil
		}
		return []Entry{messageEntry(event, continuation, event.ID+indexedSuffix("-message", suffix), role, text)}
	case "tool_call", "function_call", "tool_use":
		return []Entry{toolCallEntry(event, continuation, record, event.ID+indexedSuffix("-tool-call", suffix))}
	case "tool_result", "function_call_output":
		return []Entry{toolResultEntry(event, continuation, record, event.ID+indexedSuffix("-tool-result", suffix))}
	default:
		if recordType == "" && stringValue(record, "role") != "" {
			text := firstText(record, "text", "content", "message", "output")
			if text == "" {
				return nil
			}
			role := stringValue(record, "role")
			return []Entry{messageEntry(event, continuation, event.ID+indexedSuffix("-message", suffix), role, text)}
		}
		return nil
	}
}

func parseMessageRecord(event task.Event, continuation int, record map[string]any, role string) []Entry {
	if message, ok := mapValue(record, "message"); ok {
		if content, ok := sliceValue(message, "content"); ok {
			return parseContentBlocks(event, continuation, role, content)
		}
		if text := firstText(message, "text", "content", "message"); text != "" {
			return []Entry{messageEntry(event, continuation, event.ID+"-message", role, text)}
		}
	}
	if content, ok := sliceValue(record, "content"); ok {
		return parseContentBlocks(event, continuation, role, content)
	}
	if text := firstText(record, "text", "content", "message"); text != "" {
		return []Entry{messageEntry(event, continuation, event.ID+"-message", role, text)}
	}
	return nil
}

func parseContentBlocks(event task.Event, continuation int, role string, content []any) []Entry {
	entries := make([]Entry, 0, len(content))
	for index, block := range content {
		switch value := block.(type) {
		case string:
			if value != "" {
				entries = append(entries, messageEntry(event, continuation, fmt.Sprintf("%s-message-%d", event.ID, index), role, value))
			}
		case map[string]any:
			blockType := stringValue(value, "type")
			switch blockType {
			case "text":
				if text := firstText(value, "text", "content"); text != "" {
					entries = append(entries, messageEntry(event, continuation, fmt.Sprintf("%s-message-%d", event.ID, index), role, text))
				}
			case "tool_use", "tool_call", "function_call":
				entries = append(entries, toolCallEntry(event, continuation, value, fmt.Sprintf("%s-tool-call-%d", event.ID, index)))
			case "tool_result", "function_call_output":
				entries = append(entries, toolResultEntry(event, continuation, value, fmt.Sprintf("%s-tool-result-%d", event.ID, index)))
			default:
				if text := firstText(value, "text", "content", "message", "output"); text != "" {
					entries = append(entries, messageEntry(event, continuation, fmt.Sprintf("%s-message-%d", event.ID, index), role, text))
				}
			}
		}
	}
	return entries
}

func messageEntry(event task.Event, continuation int, id, role, text string) Entry {
	return Entry{
		ID:           id,
		Seq:          event.Seq,
		Continuation: continuation,
		Kind:         KindMessage,
		Role:         role,
		Text:         text,
		CreatedAt:    event.CreatedAt,
	}
}

func toolCallEntry(event task.Event, continuation int, record map[string]any, id string) Entry {
	details := map[string]any{}
	for _, key := range []string{"arguments", "input", "parameters"} {
		if value, ok := record[key]; ok {
			details[key] = value
		}
	}
	return Entry{
		ID:           id,
		Seq:          event.Seq,
		Continuation: continuation,
		Kind:         KindToolCall,
		Role:         RoleAssistant,
		ToolCallID:   firstText(record, "tool_call_id", "tool_use_id", "call_id", "id"),
		ToolName:     toolName(record),
		Details:      nilIfEmpty(details),
		Status:       StatusCollapsed,
		CreatedAt:    event.CreatedAt,
	}
}

func toolResultEntry(event task.Event, continuation int, record map[string]any, id string) Entry {
	details := map[string]any{}
	for _, key := range []string{"is_error", "error"} {
		if value, ok := record[key]; ok {
			details[key] = value
		}
	}
	return Entry{
		ID:           id,
		Seq:          event.Seq,
		Continuation: continuation,
		Kind:         KindToolResult,
		Role:         RoleTool,
		Text:         firstText(record, "output", "content", "result", "text"),
		ToolCallID:   firstText(record, "tool_call_id", "tool_use_id", "call_id", "id"),
		Details:      nilIfEmpty(details),
		Status:       StatusCollapsed,
		CreatedAt:    event.CreatedAt,
	}
}

func runtimeFallback(event task.Event, continuation int, text, stream string) Entry {
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

func roleFromType(recordType string) string {
	switch recordType {
	case "user":
		return RoleUser
	case "system":
		return RoleSystem
	default:
		return RoleAssistant
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

func compactPayload(payload task.EventPayload, skipKeys ...string) map[string]any {
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

func indexedSuffix(base, suffix string) string {
	if suffix == "" {
		return base
	}
	return base + "-" + suffix
}
