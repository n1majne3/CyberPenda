// Package timeline projects retained task events into a multica-style agent
// transcript timeline: thinking, tool calls, tool results, agent text, and errors.
package timeline

import (
	"strings"
	"time"

	"pentest/internal/runtimeoutput"
	"pentest/internal/task"
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

var timelineParseOpts = runtimeoutput.ParseOptions{
	IncludeThinking: true,
	IncludeErrors:   true,
}

// Build projects task events into coalesced timeline items.
func Build(events []task.Event) []Item {
	items := make([]Item, 0, len(events))
	nextSeq := 1
	turns := make([]runtimeoutput.Turn, 0, len(events))
	flushTurns := func() {
		for _, item := range turnsToItems(runtimeoutput.CoalesceStreaming(turns)) {
			item.Seq = nextSeq
			nextSeq++
			items = append(items, item)
		}
		turns = turns[:0]
	}
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle {
			flushTurns()
			if item, ok := lifecycleItem(event); ok {
				item.Seq = nextSeq
				nextSeq++
				items = append(items, item)
			}
			continue
		}
		if event.Kind == task.EventKindSteering {
			flushTurns()
			if item, ok := steeringItem(event); ok {
				item.Seq = nextSeq
				nextSeq++
				items = append(items, item)
			}
			continue
		}
		if event.Kind != task.EventKindRuntimeOutput {
			continue
		}
		text := stringValue(event.Payload, "text")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if runtimeoutput.ShouldIgnoreForTimeline(text) {
			continue
		}
		lineTurns, _ := runtimeoutput.ParseLine(text, event.CreatedAt, timelineParseOpts)
		turns = append(turns, lineTurns...)
	}
	flushTurns()
	return items
}

func turnsToItems(turns []runtimeoutput.Turn) []Item {
	items := make([]Item, 0, len(turns))
	nextSeq := 1
	for _, turn := range turns {
		item, ok := turnToItem(turn)
		if !ok {
			continue
		}
		item.Seq = nextSeq
		nextSeq++
		items = append(items, item)
	}
	return items
}

func lifecycleItem(event task.Event) (Item, bool) {
	phase := stringValue(event.Payload, "phase")
	if strings.TrimSpace(phase) == "" {
		return Item{}, false
	}
	return Item{
		Type:      "lifecycle",
		Content:   "Lifecycle: " + phase,
		CreatedAt: event.CreatedAt,
	}, true
}

func steeringItem(event task.Event) (Item, bool) {
	phase := stringValue(event.Payload, "phase")
	if phase == "" {
		phase = "steering"
	}
	directive := stringValue(event.Payload, "directive")
	content := "Steering: " + phase
	if strings.TrimSpace(directive) != "" {
		content += " - " + directive
	}
	return Item{
		Type:      "steering",
		Content:   content,
		CreatedAt: event.CreatedAt,
	}, true
}

func turnToItem(turn runtimeoutput.Turn) (Item, bool) {
	switch turn.Kind {
	case runtimeoutput.KindThinking:
		return Item{Type: "thinking", Content: turn.Text, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindText:
		return Item{Type: "text", Content: turn.Text, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindToolUse:
		return Item{Type: "tool_use", Tool: turn.Tool, Input: turn.Input, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindToolResult:
		return Item{Type: "tool_result", Tool: turn.Tool, Output: turn.Output, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindError:
		return Item{Type: "error", Content: turn.Text, CreatedAt: turn.CreatedAt}, true
	default:
		return Item{}, false
	}
}

func stringValue(record map[string]any, key string) string {
	value, ok := record[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}
