package transcript

import (
	"fmt"

	"pentest/internal/runtimeoutput"
)

func entryFromTurn(turn runtimeoutput.Turn, base Entry) (Entry, bool) {
	switch turn.Kind {
	case runtimeoutput.KindReasoning:
		return reasoningEntryFromTurn(turn, base, stableTurnEntryID(turn, base, "reasoning")), true
	case runtimeoutput.KindText:
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
