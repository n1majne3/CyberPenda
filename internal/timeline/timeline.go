// Package timeline projects retained task or session events into a
// multica-style agent transcript timeline: thinking, tool calls, tool results,
// agent text, and errors. Both owner kinds feed the same normalization chain
// (runtimeoutput parse + streaming coalesce) so task and session timelines
// render identically.
package timeline

import (
	"fmt"
	"strings"
	"time"

	"pentest/internal/runtimeoutput"
)

// Item is one chronologically ordered timeline entry. Truncated marks a
// bounded preview of an item whose full serialized form exceeded the history
// window byte budget; Detail references the owner-authorized endpoint that
// returns the complete retained item.
type Item struct {
	ID        string         `json:"id,omitempty"`
	Seq       int            `json:"seq"`
	Type      string         `json:"type"`
	Tool      string         `json:"tool,omitempty"`
	Content   string         `json:"content,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Output    string         `json:"output,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Truncated bool           `json:"truncated,omitempty"`
	Detail    string         `json:"detail,omitempty"`
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

var timelineParseOpts = runtimeoutput.ParseOptions{
	IncludeThinking:           true,
	IncludeReasoningSummaries: true,
	IncludeErrors:             true,
}

// Build projects owner events into coalesced timeline items.
func Build(events []Event) []Item {
	items := make([]Item, 0, len(events))
	nextSeq := 1
	turns := make([]runtimeoutput.Turn, 0, len(events))
	flushTurns := func() {
		for _, item := range turnsToItems(runtimeoutput.CoalesceStreaming(runtimeoutput.ReconcileLifecycle(turns))) {
			if item.Seq <= 0 {
				item.Seq = nextSeq
			}
			nextSeq++
			items = append(items, item)
		}
		turns = turns[:0]
	}
	for _, event := range events {
		switch event.Kind {
		case "blackboard_conclusion":
			flushTurns()
			if item, ok := blackboardConclusionItem(event); ok {
				item.ID = event.ID
				item.Seq = event.Seq
				if item.Seq <= 0 {
					item.Seq = nextSeq
				}
				nextSeq++
				items = append(items, item)
			}
			continue
		case "lifecycle":
			flushTurns()
			if item, ok := lifecycleItem(event); ok {
				item.ID = event.ID
				item.Seq = event.Seq
				if item.Seq <= 0 {
					item.Seq = nextSeq
				}
				nextSeq++
				items = append(items, item)
			}
			continue
		case "steering":
			flushTurns()
			if item, ok := steeringItem(event); ok {
				item.ID = event.ID
				item.Seq = event.Seq
				if item.Seq <= 0 {
					item.Seq = nextSeq
				}
				nextSeq++
				items = append(items, item)
			}
			continue
		case "attachment":
			flushTurns()
			if item, ok := attachmentItem(event); ok {
				item.ID = event.ID
				item.Seq = event.Seq
				if item.Seq <= 0 {
					item.Seq = nextSeq
				}
				nextSeq++
				items = append(items, item)
			}
			continue
		case "runtime_output":
			text := stringValue(event.Payload, "text")
			if strings.TrimSpace(text) == "" {
				continue
			}
			if runtimeoutput.ShouldIgnoreForTimeline(text) {
				continue
			}
			lineTurns, _ := runtimeoutput.ParseLineWithMeta(text, runtimeoutput.RecordMeta{
				ProviderEvent: stringValue(event.Payload, "provider_event"),
			}, event.CreatedAt, timelineParseOpts)
			for index := range lineTurns {
				lineTurns[index].SourceID = event.ID
				lineTurns[index].SourceSeq = event.Seq
			}
			turns = append(turns, lineTurns...)
		}
	}
	flushTurns()
	return items
}

func blackboardConclusionItem(event Event) (Item, bool) {
	var content string
	switch stringValue(event.Payload, "phase") {
	case "pending_detected":
		content = "Blackboard conclusion pending"
		if sourceTurnID := strings.TrimSpace(stringValue(event.Payload, "source_turn_id")); sourceTurnID != "" {
			content += " for work Turn " + sourceTurnID
		}
	case "dispatch_requested":
		content = "Blackboard Conclude Turn dispatch requested"
	case "awaiting_result":
		content = "Blackboard Conclude Turn started"
	case "result_validated":
		content = "Blackboard conclusion result validated"
	case "applied":
		content = "Blackboard conclusion applied"
		if revision, ok := event.Payload["applied_revision"]; ok {
			content += " at revision " + fmt.Sprint(revision)
		}
	case "repair_requested":
		content = "Blackboard conclusion repair requested"
	case "action_required":
		content = "Blackboard conclusion requires action"
		if code := strings.TrimSpace(stringValue(event.Payload, "error_code")); code != "" {
			content += " (" + code + ")"
		}
	case "retry_requested":
		content = "Blackboard conclusion retry requested"
	default:
		return Item{}, false
	}
	return Item{Type: "harness", Content: content, CreatedAt: event.CreatedAt}, true
}

func turnsToItems(turns []runtimeoutput.Turn) []Item {
	items := make([]Item, 0, len(turns))
	nextSeq := 1
	for _, turn := range turns {
		item, ok := turnToItem(turn)
		if !ok {
			continue
		}
		if item.Seq <= 0 {
			item.Seq = nextSeq
		}
		nextSeq++
		items = append(items, item)
	}
	return items
}

func lifecycleItem(event Event) (Item, bool) {
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

func attachmentItem(event Event) (Item, bool) {
	filename := strings.TrimSpace(stringValue(event.Payload, "filename"))
	if filename == "" {
		return Item{}, false
	}
	content := "Attached " + filename
	if size, ok := event.Payload["size"]; ok {
		switch typed := size.(type) {
		case float64:
			content += fmt.Sprintf(" (%.0f bytes)", typed)
		case int:
			content += fmt.Sprintf(" (%d bytes)", typed)
		case int64:
			content += fmt.Sprintf(" (%d bytes)", typed)
		}
	}
	return Item{Type: "lifecycle", Content: content, CreatedAt: event.CreatedAt}, true
}

func steeringItem(event Event) (Item, bool) {
	if requestID := stringValue(event.Payload, "request_id"); strings.TrimSpace(requestID) != "" {
		outcome := stringValue(event.Payload, "outcome")
		if outcome == "" {
			outcome = "pending"
		}
		content := "Steering: " + outcome
		if mode := stringValue(event.Payload, "mode"); mode != "" {
			content += " (" + mode + ")"
		} else if errorCode := stringValue(event.Payload, "error_code"); errorCode != "" {
			content += " (" + errorCode + ")"
		}
		return Item{Type: "steering", Content: content, CreatedAt: event.CreatedAt}, true
	}
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
	id := turn.SourceID
	if stable := runtimeoutput.StableProviderItemID(turn.ProviderItemID, string(turn.Kind)); stable != "" {
		id = stable
	} else if id != "" {
		id = fmt.Sprintf("%s-%s-%d", id, turn.Kind, turn.ContentIndex)
	}
	switch turn.Kind {
	case runtimeoutput.KindThinking:
		return Item{ID: id, Seq: turn.SourceSeq, Type: "thinking", Content: turn.Text, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindText:
		return Item{ID: id, Seq: turn.SourceSeq, Type: "text", Content: turn.Text, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindToolUse:
		return Item{ID: id, Seq: turn.SourceSeq, Type: "tool_use", Tool: turn.Tool, Input: turn.Input, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindToolResult:
		return Item{ID: id, Seq: turn.SourceSeq, Type: "tool_result", Tool: turn.Tool, Output: turn.Output, CreatedAt: turn.CreatedAt}, true
	case runtimeoutput.KindError:
		return Item{ID: id, Seq: turn.SourceSeq, Type: "error", Content: turn.Text, CreatedAt: turn.CreatedAt}, true
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
