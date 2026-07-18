// Package transcript projects retained task events into a readable conversation
// transcript. It does not persist new state; unknown provider output is kept as
// collapsed runtime output so historical tasks remain readable.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pentest/internal/runtimeoutput"
	"pentest/internal/runtimeplugin"
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
		if entry, ok := nativeSteeringEntry(event, continuation); ok {
			return []Entry{entry}
		}
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
		if IsIgnorableRuntimeLine(text) {
			return nil
		}
		if parsed := parseRuntimeOutput(event, continuation, adapter, text); len(parsed) > 0 {
			return parsed
		}
		if isIgnorableUnparsedRuntimeLine(text) {
			return nil
		}
		return []Entry{runtimeFallback(event, continuation, text, stream)}
	default:
		return nil
	}
}

func nativeSteeringEntry(event task.Event, continuation int) (Entry, bool) {
	if strings.TrimSpace(stringValue(event.Payload, "request_id")) == "" {
		return Entry{}, false
	}
	outcome := strings.TrimSpace(stringValue(event.Payload, "outcome"))
	if outcome == "" {
		return Entry{}, false
	}
	labels := map[string]string{
		"requested":    "Native steer requested",
		"acknowledged": "Provider acknowledged native steer",
		"settled":      "Previous provider turn settled",
		"started":      "Replacement provider turn started",
		"applied":      "Native steer applied",
		"failed":       "Native steer failed",
		"unsupported":  "Native steer unsupported",
	}
	text := labels[outcome]
	if text == "" {
		text = "Native steer: " + outcome
	}
	return Entry{
		ID:           event.ID + "-native-steer",
		Seq:          event.Seq,
		Continuation: continuation,
		Kind:         KindContinuation,
		Role:         RoleSystem,
		Text:         text,
		Details:      compactPayload(event.Payload, "outcome"),
		Status:       outcome,
		CreatedAt:    event.CreatedAt,
	}, true
}

func parseRuntimeOutput(event task.Event, continuation int, adapter, text string) []Entry {
	parser := ParserForAdapter(adapter, nil)
	if parser == "plain_runtime_output" {
		return nil
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		return nil
	}
	base := Entry{
		ID:           event.ID,
		Seq:          event.Seq,
		Continuation: continuation,
		CreatedAt:    event.CreatedAt,
	}
	entries := ParseRecord(record, base)
	if len(entries) == 0 {
		return nil
	}
	return entries
}

// ParserForAdapter returns the manifest-selected transcript parser for a runtime
// adapter. Unknown adapters intentionally fall back to plain runtime output.
func ParserForAdapter(adapter string, registry *runtimeplugin.Registry) string {
	if registry == nil {
		registry = runtimeplugin.MustBuiltinRegistry()
	}
	plugin, ok := registry.Get(adapter)
	if !ok || strings.TrimSpace(plugin.Transcript.Parser) == "" {
		return "plain_runtime_output"
	}
	return plugin.Transcript.Parser
}

// ParseRecord projects a single normalized provider record (one JSON object,
// e.g. one line of a pi session jsonl or one stdout JSON line) into transcript
// entries. base supplies the ID prefix, Seq, Continuation, and CreatedAt that
// derived entries inherit. It is exported so runtime tails can reuse the same
// parsing as the post-hoc transcript builder.
func ParseRecord(record map[string]any, base Entry) []Entry {
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{}, base.CreatedAt)
	return turnsToEntries(turns, base)
}

func turnsToEntries(turns []runtimeoutput.Turn, base Entry) []Entry {
	entries := make([]Entry, 0, len(turns))
	for _, turn := range turns {
		switch turn.Kind {
		case runtimeoutput.KindText:
			entries = append(entries, messageEntry(base, entryID(base.ID, "-message", turn.ContentIndex), mapRuntimeRole(turn.Role), turn.Text))
		case runtimeoutput.KindToolUse:
			entries = append(entries, toolCallEntryFromTurn(turn, base, entryID(base.ID, "-tool-call", turn.ContentIndex)))
		case runtimeoutput.KindToolResult:
			entries = append(entries, toolResultEntryFromTurn(turn, base, entryID(base.ID, "-tool-result", turn.ContentIndex)))
		}
	}
	return entries
}

func entryID(baseID, suffix string, index int) string {
	if index < 0 {
		return baseID + suffix
	}
	return fmt.Sprintf("%s%s-%d", baseID, suffix, index)
}

func mapRuntimeRole(role string) string {
	switch role {
	case "user":
		return RoleUser
	case "system":
		return RoleSystem
	case "tool":
		return RoleTool
	default:
		return RoleAssistant
	}
}

func messageEntry(base Entry, id, role, text string) Entry {
	return Entry{
		ID:           id,
		Seq:          base.Seq,
		Continuation: base.Continuation,
		Kind:         KindMessage,
		Role:         role,
		Text:         text,
		CreatedAt:    base.CreatedAt,
	}
}

func toolCallEntryFromTurn(turn runtimeoutput.Turn, base Entry, id string) Entry {
	return Entry{
		ID:           id,
		Seq:          base.Seq,
		Continuation: base.Continuation,
		Kind:         KindToolCall,
		Role:         RoleAssistant,
		ToolCallID:   turn.ToolCallID,
		ToolName:     turn.Tool,
		Details:      turn.Details,
		Status:       StatusCollapsed,
		CreatedAt:    base.CreatedAt,
	}
}

func toolResultEntryFromTurn(turn runtimeoutput.Turn, base Entry, id string) Entry {
	return Entry{
		ID:           id,
		Seq:          base.Seq,
		Continuation: base.Continuation,
		Kind:         KindToolResult,
		Role:         RoleTool,
		Text:         turn.Output,
		ToolCallID:   turn.ToolCallID,
		ToolName:     turn.Tool,
		Details:      turn.Details,
		Status:       StatusCollapsed,
		CreatedAt:    base.CreatedAt,
	}
}

// IsIgnorableRuntimeLine reports whether a raw runtime stdout/stderr line is
// provider metadata that should not be stored or shown in the task conversation transcript.
func IsIgnorableRuntimeLine(text string) bool {
	return runtimeoutput.ShouldIgnoreForStorage(text)
}

func isIgnorableUnparsedRuntimeLine(text string) bool {
	return runtimeoutput.IsThinkingOnlyAssistantLine(text)
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
