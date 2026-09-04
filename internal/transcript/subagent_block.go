package transcript

import (
	"encoding/json"
	"time"

	"pentest/internal/runtimeoutput"
)

// This file implements the unified conversation rule for child agents: every
// child, on any provider, projects into the conversation as one collapsed,
// attributed block anchored at the spawning tool-call row and aggregating all
// of that child's items regardless of stream interleaving. The Subagent
// Activity Timeline lifecycle entry stays the other projection.

// childBlocks accumulates per-child observations while a window's events are
// projected. Child-attributed items never enter the main entry flow; they are
// held here and materialized into one block per child afterwards.
//
// One child carries two provider key namespaces: Subagent Activity lifecycle
// records are keyed by the durable task id, while multiplexed stream items
// are keyed by the spawning tool-call id (or a per-item agent id on runtimes
// that emit one). The spawn linkage joins them: a task_started record carries
// both ids, so bySpawn resolves either namespace to one child state.
type childBlocks struct {
	order []string
	byID  map[string]*childBlockState
	// bySpawn maps a spawning tool-call id to the child state key.
	bySpawn map[string]string
	// prevEntryID is the id of the last main-thread entry appended before the
	// event currently being projected; it is the fallback anchor for a child
	// without spawn linkage.
	prevEntryID string
}

type childBlockState struct {
	key            string
	durableID      string // task id, once a lifecycle record supplied it
	spawnToolUseID string
	label          string
	subagentType   string
	provider       string
	phase          string
	firstSeen      time.Time
	lastSeq        int
	continuation   int
	// anchorAfterID is the main entry that preceded the child's first held
	// item; set once.
	anchorAfterID string
	items         []Entry
}

func newChildBlocks() *childBlocks {
	return &childBlocks{byID: map[string]*childBlockState{}, bySpawn: map[string]string{}}
}

// beginEvent records the main-thread position the next observations would
// follow; it is the fallback anchor for a child without spawn linkage.
func (c *childBlocks) beginEvent(lastEntryID string) {
	c.prevEntryID = lastEntryID
}

// resolve returns the child state for one observation, joining the lifecycle
// and item key namespaces through the spawn linkage.
func (c *childBlocks) resolve(key, spawnToolUseID string) *childBlockState {
	if child, ok := c.byID[key]; ok {
		return child
	}
	if spawnToolUseID != "" {
		if existing, ok := c.bySpawn[spawnToolUseID]; ok {
			return c.byID[existing]
		}
	}
	// An item key may itself be a spawn id that a lifecycle record registered.
	if existing, ok := c.bySpawn[key]; ok {
		return c.byID[existing]
	}
	child := &childBlockState{key: key, continuation: -1}
	alias := spawnToolUseID
	if alias == "" {
		alias = key
	}
	child.spawnToolUseID = alias
	c.bySpawn[alias] = key
	c.byID[key] = child
	c.order = append(c.order, key)
	return child
}

// observeActivity folds one Subagent Activity observation into the child's
// header: identity label, agent type, provider, spawn linkage, and the latest
// coarse lifecycle state.
func (c *childBlocks) observeActivity(turn runtimeoutput.Turn, base Entry) {
	spawn, _ := turn.Details["spawn_tool_use_id"].(string)
	child := c.resolve(turn.ProviderItemID, spawn)
	if turn.Text != "" {
		child.label = turn.Text
	}
	child.phase = turn.LifecyclePhase
	if child.phase == "" {
		child.phase = runtimeoutput.SubagentActivityStarted
	}
	if turn.Tool != "" {
		child.provider = turn.Tool
	}
	if spawn != "" {
		child.spawnToolUseID = spawn
	}
	if turn.ProviderItemID != "" {
		child.durableID = turn.ProviderItemID
	}
	if subagentType, ok := turn.Details["subagent_type"].(string); ok && subagentType != "" {
		child.subagentType = subagentType
	}
	c.observeCommon(child, base)
}

// observeItem holds one child-attributed entry for the child's block instead
// of letting it render as a main-thread row.
func (c *childBlocks) observeItem(turn runtimeoutput.Turn, entry Entry, base Entry) {
	child := c.resolve(turn.AgentID, "")
	if len(child.items) == 0 {
		child.anchorAfterID = c.prevEntryID
	}
	child.items = append(child.items, entry)
	c.observeCommon(child, base)
}

func (c *childBlocks) observeCommon(child *childBlockState, base Entry) {
	if child.firstSeen.IsZero() {
		child.firstSeen = base.CreatedAt
	}
	if base.Seq > child.lastSeq {
		child.lastSeq = base.Seq
	}
	if child.continuation < 0 {
		child.continuation = base.Continuation
	}
}

// appendChildBlocks materializes one collapsed block per observed child into
// the projected entries. Each block anchors directly after its spawning
// tool-call row (and that call's paired tool result); without spawn linkage
// it anchors after the main entry that preceded its first held item, and as a
// last resort after the final entry.
func appendChildBlocks(entries []Entry, children *childBlocks) []Entry {
	if children == nil || len(children.order) == 0 {
		return entries
	}
	blocksByAnchor := map[int][]Entry{}
	for _, key := range children.order {
		child := children.byID[key]
		anchor := anchorIndexForChild(entries, child)
		blocksByAnchor[anchor] = append(blocksByAnchor[anchor], childBlockEntry(child))
	}
	if len(blocksByAnchor) == 0 {
		return entries
	}
	out := make([]Entry, 0, len(entries)+len(children.order))
	for index, entry := range entries {
		out = append(out, entry)
		out = append(out, blocksByAnchor[index]...)
	}
	out = append(out, blocksByAnchor[len(entries)]...)
	return out
}

// anchorIndexForChild returns the entry index after which the child's block
// is inserted.
func anchorIndexForChild(entries []Entry, child *childBlockState) int {
	if child.spawnToolUseID != "" {
		for index := len(entries) - 1; index >= 0; index-- {
			entry := entries[index]
			if entry.Kind != KindToolCall || entry.ToolCallID != child.spawnToolUseID {
				continue
			}
			// The spawning call's paired async-launch ack result stays
			// adjacent to its call; the block follows that pair.
			if index+1 < len(entries) && entries[index+1].Kind == KindToolResult && entries[index+1].ToolCallID == child.spawnToolUseID {
				return index + 1
			}
			return index
		}
	}
	if child.anchorAfterID != "" {
		for index := len(entries) - 1; index >= 0; index-- {
			if entries[index].ID == child.anchorAfterID {
				return index
			}
		}
	}
	// No known position: after the final entry (or first when empty).
	return len(entries)
}

func childBlockEntry(child *childBlockState) Entry {
	agentID := child.durableID
	if agentID == "" {
		agentID = child.key
	}
	details := map[string]any{
		"agent_id": agentID,
	}
	if child.subagentType != "" {
		details["subagent_type"] = child.subagentType
	}
	if child.label != "" {
		details["description"] = child.label
	}
	if child.provider != "" {
		details["provider"] = child.provider
	}
	if child.spawnToolUseID != "" {
		details["spawn_tool_use_id"] = child.spawnToolUseID
	}
	if len(child.items) > 0 {
		// Held child entries serialize as plain objects so the daemon's
		// bounded-preview truncation applies to an oversized block.
		details["items"] = childItemsForDetails(child.items)
	}
	continuation := child.continuation
	if continuation < 0 {
		continuation = 0
	}
	return Entry{
		// The spawn tool-call id is stable from first observation in both key
		// namespaces, so the block identity survives window boundaries.
		ID:           "subagent-" + child.blockKey(),
		Seq:          child.lastSeq,
		Continuation: continuation,
		Kind:         KindSubagentBlock,
		Role:         RoleRuntime,
		Text:         childBlockHeader(child),
		ToolName:     child.provider,
		Status:       child.phase,
		Details:      details,
		CreatedAt:    child.firstSeen,
	}
}

// blockKey is the stable block identity: the spawn linkage when known,
// otherwise the provider child id.
func (child *childBlockState) blockKey() string {
	if child.spawnToolUseID != "" {
		return child.spawnToolUseID
	}
	return child.key
}

func childBlockHeader(child *childBlockState) string {
	label := child.label
	if label == "" {
		label = "Subagent " + child.blockKey()
	}
	if child.subagentType != "" {
		return "Subagent " + child.subagentType + ": " + label
	}
	return label
}

// childItemsForDetails serializes the held child entries into the same
// JSON-shaped maps the API returns.
func childItemsForDetails(items []Entry) []any {
	out := make([]any, 0, len(items))
	for _, entry := range items {
		raw, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		var shape map[string]any
		if json.Unmarshal(raw, &shape) == nil {
			out = append(out, shape)
		}
	}
	return out
}
