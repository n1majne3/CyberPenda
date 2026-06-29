package runtimeoutput

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
				Kind:         prev.Kind,
				Role:         prev.Role,
				Text:         prev.Text + turn.Text,
				Tool:         prev.Tool,
				Input:        prev.Input,
				Output:       prev.Output,
				ToolCallID:   prev.ToolCallID,
				Details:      prev.Details,
				ContentIndex: prev.ContentIndex,
				CreatedAt:    turn.CreatedAt,
			}
			continue
		}
		out = append(out, turn)
	}
	return out
}

func canMergeStreamingText(prev, next Turn) bool {
	return (prev.Kind == KindThinking || prev.Kind == KindText) && prev.Kind == next.Kind
}