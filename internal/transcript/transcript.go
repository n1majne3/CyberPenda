// Package transcript projects retained task or session events into a readable
// conversation transcript. It does not persist new state; unknown provider
// output is kept as collapsed runtime output so historical owners remain
// readable. Both owner kinds feed the same builder.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pentest/internal/runtimeoutput"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

const (
	KindMessage       = "message"
	KindReasoning     = "reasoning"
	KindToolCall      = "tool_call"
	KindToolResult    = "tool_result"
	KindRuntimeOutput = "runtime_output"
	KindContinuation  = "continuation"
	// KindSubagentBlock is the one collapsed, attributed conversation block a
	// child agent projects as, anchored at the spawning tool-call row.
	KindSubagentBlock = "subagent_block"

	RoleAssistant = "assistant"
	RoleRuntime   = "runtime"
	RoleSystem    = "system"
	RoleTool      = "tool"
	RoleUser      = "user"

	StatusCollapsed = "collapsed"

	// PiSessionStream marks runtime_output events re-emitted from Pi's session
	// jsonl by the session-file tailer. Persistent Pi provider sessions run
	// through an adapter whose name ("provider-session:<id>") resolves to no
	// plugin parser, so the stream identifies the underlying provider instead.
	PiSessionStream = "pi_session"

	// HermesACPStream marks runtime_output events from the Hermes ACP session
	// bridge. Each agent_message_chunk is one token, so the transcript joins
	// adjacent chunks from the same Continuation into one sentence.
	HermesACPStream = "hermes_acp"
)

// Entry is one projected transcript row. Truncated marks a bounded preview of
// an entry whose full serialized form exceeded the history window byte budget;
// Detail references the owner-authorized endpoint that returns the complete
// retained entry.
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
	Incremental  bool           `json:"incremental,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	Truncated    bool           `json:"truncated,omitempty"`
	Detail       string         `json:"detail,omitempty"`
}

// Event is the minimal owner-event surface Build consumes. Task and Session
// event stores both project into this shape so one builder serves both.
type Event struct {
	ID        string
	Seq       int
	Kind      string
	Payload   map[string]any
	CreatedAt time.Time
}

// Subject is the minimal owner identity Build needs for its first row.
type Subject struct {
	ID        string
	Title     string // task goal or session title
	CreatedAt time.Time
}

// WindowContext is the Transcript parser state immediately before a bounded
// Event window. It keeps paging independent of older retained Event loading.
type WindowContext struct {
	Continuation int
	Adapter      string
}

// Build projects an owner subject and its retained events into transcript entries.
func Build(subject Subject, events []Event) []Entry {
	return BuildWindow(subject, events, WindowContext{})
}

// BuildWindow projects a bounded owner Event window with the parser state that
// was active immediately before its first Event.
func BuildWindow(subject Subject, events []Event, context WindowContext) []Entry {
	entries := make([]Entry, 0, len(events)+1)
	if strings.TrimSpace(subject.Title) != "" {
		entries = append(entries, Entry{
			ID:           subject.ID + "-goal",
			Seq:          0,
			Continuation: 0,
			Kind:         KindMessage,
			Role:         RoleUser,
			Text:         subject.Title,
			CreatedAt:    subject.CreatedAt,
		})
	}

	continuation := context.Continuation
	adapter := context.Adapter
	children := newChildBlocks()
	for _, event := range events {
		if event.Kind == "lifecycle" {
			phase := stringValue(event.Payload, "phase")
			if phase == "completed" || phase == "failed" || phase == "stopped" {
				collapseStreamingReasoning(entries, continuation)
			}
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
		if event.Kind == "runtime_output" {
			children.beginEvent(lastEntryID(entries))
		}
		entries = appendOrCoalesceTranscript(entries, entriesForEvent(event, continuation, adapter, children))
	}
	return appendChildBlocks(entries, children)
}

func lastEntryID(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[len(entries)-1].ID
}

// appendOrCoalesceTranscript joins adjacent assistant message chunks from one
// Continuation so a streamed sentence is one row, not one row per token.
func collapseStreamingReasoning(entries []Entry, continuation int) {
	for index := range entries {
		if entries[index].Continuation == continuation && entries[index].Kind == KindReasoning && entries[index].Status == runtimeoutput.ReasoningPhaseStreaming {
			entries[index].Status = StatusCollapsed
		}
	}
}

func appendOrCoalesceTranscript(entries, next []Entry) []Entry {
	for _, entry := range next {
		if entry.ID != "" {
			replaced := false
			for index := range entries {
				if entries[index].ID == entry.ID {
					// A completed provider item enriches the row projected when the
					// item started. Keep the first projection's history position so
					// Transcript Seq order and same-Seq page boundaries stay valid.
					entry.Seq = entries[index].Seq
					entry.Continuation = entries[index].Continuation
					entry.CreatedAt = entries[index].CreatedAt
					if entry.Incremental {
						entry.Text = entries[index].Text + entry.Text
					}
					entries[index] = entry
					replaced = true
					break
				}
			}
			if replaced {
				continue
			}
		}
		if n := len(entries); n > 0 && canMergeAssistantMessage(entries[n-1], entry) {
			entries[n-1].Text += entry.Text
			entries[n-1].Seq = entry.Seq
			entries[n-1].CreatedAt = entry.CreatedAt
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func canMergeAssistantMessage(prev, next Entry) bool {
	return prev.Kind == KindMessage && next.Kind == KindMessage &&
		prev.Role == RoleAssistant && next.Role == RoleAssistant &&
		prev.Continuation == next.Continuation &&
		prev.Stream == HermesACPStream && next.Stream == HermesACPStream
}

func lifecycleEntry(event Event, continuation int) (Entry, bool) {
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
	case "provider_permission_requested":
		entry.Text = "Provider permission requested"
		entry.Status = "pending"
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	case "provider_permission_response_requested":
		entry.Text = "Provider permission response requested"
		entry.Status = "pending"
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	case "provider_permission_response_acknowledged":
		entry.Text = "Provider acknowledged permission response"
		entry.Status = "acknowledged"
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	case "provider_permission_response_applied":
		entry.Text = "Provider permission response applied"
		entry.Status = "applied"
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	case "provider_permission_response_failed":
		entry.Text = "Provider permission response failed"
		entry.Status = "failed"
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	default:
		entry.Text = "Runtime lifecycle: " + phase
		entry.Status = StatusCollapsed
		entry.Details = compactPayload(event.Payload, "phase")
		return entry, true
	}
}

func entriesForEvent(event Event, continuation int, adapter string, children *childBlocks) []Entry {
	switch event.Kind {
	case "steering":
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
	case "conversation":
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
	case "runtime_output":
		text := stringValue(event.Payload, "text")
		stream := stringValue(event.Payload, "stream")
		if text == "" {
			return nil
		}
		if IsIgnorableRuntimeLine(text) {
			return nil
		}
		// Pi session-tail lines are structured provider records but the
		// persistent-session adapter name resolves to no parser. Select the
		// parser from the stream so tailed output parses like Pi stdout.
		// Session events carry the real provider id on the payload when the
		// adapter is a synthetic provider-session:<id> handle.
		parseAdapter := adapter
		if stream == PiSessionStream {
			parseAdapter = string(runtimeprofile.ProviderPi)
		} else if stream == HermesACPStream {
			parseAdapter = string(runtimeprofile.ProviderHermes)
		} else if strings.HasPrefix(parseAdapter, "provider-session:") {
			if provider := stringValue(event.Payload, "provider"); provider != "" {
				parseAdapter = provider
			}
		}
		if parsed, recognized := parseRuntimeOutput(event, continuation, parseAdapter, text, children); recognized {
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

func nativeSteeringEntry(event Event, continuation int) (Entry, bool) {
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

func parseRuntimeOutput(event Event, continuation int, adapter, text string, children *childBlocks) ([]Entry, bool) {
	parser := ParserForAdapter(adapter, nil)
	if parser == "plain_runtime_output" {
		return nil, false
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		return nil, false
	}
	base := Entry{
		ID:           event.ID,
		Seq:          event.Seq,
		Continuation: continuation,
		CreatedAt:    event.CreatedAt,
		Stream:       stringValue(event.Payload, "stream"),
	}
	turns := runtimeoutput.ParseRecordWithMeta(record, runtimeoutput.RecordMeta{
		ProviderEvent: stringValue(event.Payload, "provider_event"),
	}, runtimeoutput.ParseOptions{IncludeReasoningSummaries: true, IncludeThinking: true}, base.CreatedAt)
	if len(turns) == 0 {
		return nil, false
	}
	return splitTurns(turns, base, children), true
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
	turns := runtimeoutput.ParseRecord(record, runtimeoutput.ParseOptions{IncludeReasoningSummaries: true, IncludeThinking: true}, base.CreatedAt)
	return turnsToEntries(turns, base)
}

func turnsToEntries(turns []runtimeoutput.Turn, base Entry) []Entry {
	return splitTurns(turns, base, nil)
}

// splitTurns projects normalized turns into main-thread entries. Subagent
// Activity observations and child-attributed items route into the child-block
// collector instead; with a nil collector both stay dropped, preserving the
// legacy behavior of the exported per-record parsers.
func splitTurns(turns []runtimeoutput.Turn, base Entry, children *childBlocks) []Entry {
	entries := make([]Entry, 0, len(turns))
	for _, turn := range runtimeoutput.ReconcileLifecycle(turns) {
		if children != nil && turn.Kind == runtimeoutput.KindSubagentActivity {
			children.observeActivity(turn, base)
			continue
		}
		entry, ok := entryFromTurn(turn, base)
		if !ok {
			continue
		}
		if children != nil && turn.AgentID != "" {
			children.observeItem(turn, entry, base)
			continue
		}
		if turn.AgentID != "" {
			// The single-record projection has no block to hold a child turn;
			// dropping keeps child work out of the main-thread flow.
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func entryFromTurn(turn runtimeoutput.Turn, base Entry) (Entry, bool) {
	switch turn.Kind {
	case runtimeoutput.KindReasoning:
		return reasoningEntryFromTurn(turn, base, stableTurnEntryID(turn, base, "reasoning")), true
	case runtimeoutput.KindText:
		// Provider user records are internal prompt/session frames. Operator
		// text is projected only from durable conversation or steering Events.
		if turn.Role == "user" {
			return Entry{}, false
		}
		return messageEntry(base, entryID(base.ID, "-message", turn.ContentIndex), mapRuntimeRole(turn.Role), turn.Text), true
	case runtimeoutput.KindToolUse:
		return toolCallEntryFromTurn(turn, base, stableTurnEntryID(turn, base, "tool-call")), true
	case runtimeoutput.KindToolResult:
		return toolResultEntryFromTurn(turn, base, stableTurnEntryID(turn, base, "tool-result")), true
	default:
		return Entry{}, false
	}
}

func stableTurnEntryID(turn runtimeoutput.Turn, base Entry, kind string) string {
	if stable := runtimeoutput.StableProviderItemID(turn.ProviderItemID, kind); stable != "" {
		return fmt.Sprintf("continuation-%d-%s", base.Continuation, stable)
	}
	return entryID(base.ID, "-"+kind, turn.ContentIndex)
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
		Stream:       base.Stream,
		CreatedAt:    base.CreatedAt,
	}
}

func reasoningEntryFromTurn(turn runtimeoutput.Turn, base Entry, id string) Entry {
	return Entry{
		ID:           id,
		Seq:          base.Seq,
		Continuation: base.Continuation,
		Kind:         KindReasoning,
		Role:         RoleAssistant,
		Text:         turn.Text,
		Status:       reasoningEntryStatus(turn),
		Incremental:  turn.Incremental,
		CreatedAt:    base.CreatedAt,
	}
}

func reasoningEntryStatus(turn runtimeoutput.Turn) string {
	if turn.LifecyclePhase == runtimeoutput.ReasoningPhaseStreaming {
		return runtimeoutput.ReasoningPhaseStreaming
	}
	return StatusCollapsed
}

func toolCallEntryFromTurn(turn runtimeoutput.Turn, base Entry, id string) Entry {
	details := turn.Details
	if len(turn.Input) > 0 {
		cloned := make(map[string]any, len(details)+1)
		for key, value := range details {
			cloned[key] = value
		}
		if _, ok := cloned["input"]; !ok {
			cloned["input"] = turn.Input
		}
		details = cloned
	}
	return Entry{
		ID:           id,
		Seq:          base.Seq,
		Continuation: base.Continuation,
		Kind:         KindToolCall,
		Role:         RoleAssistant,
		ToolCallID:   turn.ToolCallID,
		ToolName:     turn.Tool,
		Details:      details,
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
