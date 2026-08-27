package runtimeoutput

import "strings"

// CoalesceStreaming merges adjacent thinking or text turns split by flush timing.
func CoalesceStreaming(turns []Turn) []Turn {
	if len(turns) == 0 {
		return turns
	}
	out := make([]Turn, 0, len(turns))
	for _, turn := range turns {
		if len(out) == 0 {
			out = append(out, turn)
			continue
		}
		prev := out[len(out)-1]
		if canMergeStreamingText(prev, turn) {
			out[len(out)-1] = Turn{
				SourceID:       prev.SourceID,
				SourceSeq:      turn.SourceSeq,
				ProviderItemID: prev.ProviderItemID,
				LifecyclePhase: turn.LifecyclePhase,
				Kind:           prev.Kind,
				Role:           prev.Role,
				Text:           prev.Text + turn.Text,
				Tool:           prev.Tool,
				Input:          prev.Input,
				Output:         prev.Output,
				ToolCallID:     prev.ToolCallID,
				Details:        prev.Details,
				ContentIndex:   prev.ContentIndex,
				CreatedAt:      turn.CreatedAt,
			}
			continue
		}
		out = append(out, turn)
	}
	return out
}

// ReconcileLifecycle collapses duplicate projections of the same provider
// item while preserving completed-only records from older Sessions.
func ReconcileLifecycle(turns []Turn) []Turn {
	out := make([]Turn, 0, len(turns))
	toolUses := map[string]int{}
	toolResults := map[string]int{}
	thinking := map[string]int{}
	for _, turn := range turns {
		switch turn.Kind {
		case KindToolUse:
			if key := strings.TrimSpace(turn.ToolCallID); key != "" {
				if index, ok := toolUses[key]; ok {
					out[index] = mergeLifecycleTurn(out[index], turn)
					continue
				}
				toolUses[key] = len(out)
			}
		case KindToolResult:
			if key := strings.TrimSpace(turn.ToolCallID); key != "" {
				if index, ok := toolResults[key]; ok {
					out[index] = mergeLifecycleTurn(out[index], turn)
					continue
				}
				toolResults[key] = len(out)
			}
		case KindThinking:
			if key := strings.TrimSpace(turn.ProviderItemID); key != "" {
				if index, ok := thinking[key]; ok {
					out[index] = mergeLifecycleTurn(out[index], turn)
					continue
				}
				thinking[key] = len(out)
			}
		}
		out = append(out, turn)
	}
	return out
}

func mergeLifecycleTurn(previous, next Turn) Turn {
	if previous.SourceID != "" {
		next.SourceID = previous.SourceID
	}
	if previous.SourceSeq != 0 {
		next.SourceSeq = previous.SourceSeq
	}
	if next.ProviderItemID == "" {
		next.ProviderItemID = previous.ProviderItemID
	}
	if next.ToolCallID == "" {
		next.ToolCallID = previous.ToolCallID
	}
	if next.Tool == "" {
		next.Tool = previous.Tool
	}
	if len(next.Input) == 0 {
		next.Input = previous.Input
	}
	if len(next.Details) == 0 {
		next.Details = previous.Details
	}
	if !previous.CreatedAt.IsZero() {
		next.CreatedAt = previous.CreatedAt
	} else if next.CreatedAt.IsZero() {
		next.CreatedAt = previous.CreatedAt
	}
	return next
}

func canMergeStreamingText(prev, next Turn) bool {
	return (prev.Kind == KindThinking || prev.Kind == KindText) && prev.Kind == next.Kind
}
