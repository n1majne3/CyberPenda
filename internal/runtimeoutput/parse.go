package runtimeoutput

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ParseLine normalizes one runtime stdout/stderr line into turns. The bool is
// true when the line is treated as plain-text fallback rather than structured JSON.
func ParseLine(text string, createdAt time.Time, opts ParseOptions) ([]Turn, bool) {
	return ParseLineWithMeta(text, RecordMeta{}, createdAt, opts)
}

// ParseLineWithMeta normalizes one runtime line with durable provider-event
// lifecycle metadata.
func ParseLineWithMeta(text string, meta RecordMeta, createdAt time.Time, opts ParseOptions) ([]Turn, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return []Turn{{Kind: KindText, Text: trimmed, ContentIndex: -1, CreatedAt: createdAt}}, true
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
		return []Turn{{Kind: KindText, Text: trimmed, ContentIndex: -1, CreatedAt: createdAt}}, true
	}

	turns := ParseRecordWithMeta(record, meta, opts, createdAt)
	if len(turns) == 0 {
		return []Turn{{Kind: KindText, Text: trimmed, ContentIndex: -1, CreatedAt: createdAt}}, true
	}
	return turns, false
}

// ParseRecord projects one provider JSON object into normalized turns.
func ParseRecord(record map[string]any, opts ParseOptions, createdAt time.Time) []Turn {
	return ParseRecordWithMeta(record, RecordMeta{}, opts, createdAt)
}

// ParseRecordWithMeta projects one provider JSON object with its durable
// lifecycle metadata into normalized turns.
func ParseRecordWithMeta(record map[string]any, meta RecordMeta, opts ParseOptions, createdAt time.Time) []Turn {
	if strings.EqualFold(stringValue(record, "type"), "stream_event") {
		if event, ok := mapValue(record, "event"); ok {
			nested := make(map[string]any, len(event)+1)
			for key, value := range event {
				nested[key] = value
			}
			if delta, ok := mapValue(event, "delta"); ok && firstText(delta, "thinking", "reasoning", "reasoning_content") != "" {
				nested["incremental"] = true
				if messageID := firstText(record, "uuid", "message_id", "messageId", "id"); messageID != "" {
					if index, ok := numberValue(event, "index"); ok {
						nested["item_id"] = messageID + "-reasoning-" + strconv.Itoa(index)
					}
				}
			}
			return ParseRecordWithMeta(nested, meta, opts, createdAt)
		}
	}
	if turns := parseHermesACPRecord(record, opts, createdAt); len(turns) > 0 || isHermesACPRecord(record) {
		return turns
	}
	recordType := strings.ToLower(stringValue(record, "type"))
	// Subagent activity is recognized before the generic message and item
	// handling so a wrapped {"item": {...}} record dispatches on its inner type.
	switch recordType {
	case "subagentactivity":
		return parseCodexSubAgentActivity(record, createdAt)
	case "collabagenttoolcall":
		return parseCodexCollabAgentToolCall(record, createdAt)
	case "system":
		if isClaudeSubagentSystemRecord(record) {
			return parseClaudeSubagentActivity(record, createdAt)
		}
	}
	// Pi subagent settle records (one-shot session custom entries and
	// bridge-forwarded entry_appended frames).
	if isPiSubagentRecord(record) {
		return parsePiSubagentActivity(record, createdAt)
	}
	if item, ok := mapValue(record, "item"); ok {
		if turns := ParseRecordWithMeta(item, meta, opts, createdAt); len(turns) > 0 {
			return turns
		}
	}
	if delta, ok := mapValue(record, "delta"); ok {
		if text := firstText(delta, "text", "content"); text != "" {
			return stampAgentIdentity([]Turn{{Kind: KindText, Role: roleAssistant, Text: text, ContentIndex: -1, CreatedAt: createdAt}}, record)
		}
		// Streaming reasoning deltas carry a synthesized provider item id so
		// every batched projection replaces one stable transcript row.
		if opts.IncludeThinking {
			if text := firstText(delta, "thinking", "reasoning", "reasoning_content"); text != "" {
				phase := firstText(record, "phase")
				if phase == "" {
					phase = ReasoningPhaseStreaming
				}
				return []Turn{{Kind: KindReasoning, Role: roleAssistant, Text: text, ProviderItemID: firstText(record, "item_id", "itemId", "provider_item_id"), LifecyclePhase: phase, Incremental: isTruthy(record["incremental"]), ContentIndex: -1, CreatedAt: createdAt}}
			}
		}
	}

	switch recordType {
	case "system":
		if isIgnorableSystemRecord(record) {
			return nil
		}
		return parseMessageRecord(record, opts, roleFromType(recordType), createdAt)
	case "assistant", "user", "message", "assistant_message", "agent_message", "agentmessage", "response.output_text", "output_text", "message_delta", "content_block_delta":
		return stampAgentIdentity(parseMessageRecord(record, opts, roleFromType(recordType), createdAt), record)
	case "commandexecution":
		return parseCodexCommandExecution(record, meta, createdAt)
	case "mcptoolcall":
		return parseCodexMCPToolCall(record, meta, createdAt)
	case "reasoning":
		return parseCodexReasoning(record, meta, opts, createdAt)
	case "usermessage":
		return nil
	case "tool_call", "function_call", "tool_use":
		return stampAgentIdentity([]Turn{toolUseTurn(record, createdAt)}, record)
	case "tool_result", "function_call_output":
		return stampAgentIdentity([]Turn{toolResultTurn(record, createdAt)}, record)
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

// stampAgentIdentity copies provider child-agent identity from a multiplexed
// stream record onto every Turn it produced. Unmarked records pass through
// unchanged.
func stampAgentIdentity(turns []Turn, record map[string]any) []Turn {
	if len(turns) == 0 {
		return turns
	}
	agentID := firstText(record, "agentId", "agent_id", "attributionAgent", "attribution_agent")
	if agentID == "" {
		agentID = firstText(record, "parent_tool_use_id", "parentToolUseId")
	}
	if agentID == "" {
		return turns
	}
	stamped := make([]Turn, len(turns))
	copy(stamped, turns)
	for index := range stamped {
		stamped[index].AgentID = agentID
	}
	return stamped
}

func parseMessageRecord(record map[string]any, opts ParseOptions, role string, createdAt time.Time) []Turn {
	if message, ok := mapValue(record, "message"); ok {
		if mr := stringValue(message, "role"); mr == "toolResult" || mr == "tool" {
			if turn := toolResultTurn(message, createdAt); turn.Output != "" || turn.ToolCallID != "" || turn.Tool != "" {
				return []Turn{turn}
			}
		}
		if content, ok := sliceValue(message, "content"); ok {
			turns := parseContentBlocks(content, opts, role, createdAt)
			messageID := firstText(record, "uuid", "message_id", "messageId", "id")
			if messageID == "" {
				messageID = firstText(message, "uuid", "message_id", "messageId", "id")
			}
			return assignReasoningBlockIDs(turns, messageID)
		}
		if text := firstText(message, "text", "content", "message"); text != "" {
			return []Turn{{Kind: KindText, Role: role, Text: text, ContentIndex: -1, CreatedAt: createdAt}}
		}
	}
	if content, ok := sliceValue(record, "content"); ok {
		return assignReasoningBlockIDs(parseContentBlocks(content, opts, role, createdAt), firstText(record, "uuid", "message_id", "messageId", "id"))
	}
	if text := firstText(record, "text", "content", "message"); text != "" {
		return []Turn{{Kind: KindText, Role: role, Text: text, ContentIndex: -1, CreatedAt: createdAt}}
	}
	return nil
}

func assignReasoningBlockIDs(turns []Turn, messageID string) []Turn {
	if messageID == "" {
		return turns
	}
	for index := range turns {
		if turns[index].Kind == KindReasoning && turns[index].ProviderItemID == "" {
			turns[index].ProviderItemID = messageID + "-reasoning-" + strconv.Itoa(turns[index].ContentIndex)
			turns[index].LifecyclePhase = ReasoningPhaseCompleted
		}
	}
	return turns
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
			case "thinking", "reasoning":
				if !opts.IncludeThinking {
					continue
				}
				if text := thinkingText(value); text != "" {
					turns = append(turns, Turn{Kind: KindReasoning, Role: role, Text: text, ProviderItemID: firstText(value, "id", "item_id", "itemId"), ContentIndex: index, CreatedAt: createdAt})
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
	text := firstText(record, "content", "summary")
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

func toolUseTurn(record map[string]any, createdAt time.Time) Turn {
	input := map[string]any{}
	details := map[string]any{}
	for _, key := range []string{"input", "arguments", "parameters", "rawInput", "raw_input"} {
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
	return firstText(record, "thinking", "reasoning", "text", "content")
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

func numberValue(record map[string]any, key string) (int, bool) {
	value, ok := record[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
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
