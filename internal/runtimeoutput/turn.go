package runtimeoutput

import "time"

type Kind string

const (
	KindReasoning  Kind = "reasoning"
	KindText       Kind = "text"
	KindToolUse    Kind = "tool_use"
	KindToolResult Kind = "tool_result"
	KindError      Kind = "error"
	// KindSubagentActivity is one normalized Subagent Activity projection: a
	// provider child agent observed inside a Work Runtime Turn. ProviderItemID
	// carries the durable child identity, LifecyclePhase the coarse activity
	// state, Text the operator-facing label, and Tool the provider id.
	KindSubagentActivity Kind = "subagent_activity"
)

const (
	SubagentActivityStarted     = "started"
	SubagentActivityInterrupted = "interrupted"
	SubagentActivityCompleted   = "completed"
	SubagentActivityFailed      = "failed"
)

const (
	ReasoningPhaseStreaming = "streaming"
	ReasoningPhaseCompleted = "completed"
)

type Turn struct {
	SourceID       string
	SourceSeq      int
	ProviderItemID string
	LifecyclePhase string
	// AgentID carries the provider child-agent attribution key for multiplexed
	// stream items: the per-item agent id when the runtime emits one, otherwise
	// the spawning tool-call id (parent_tool_use_id). Empty means the item
	// belongs to the main thread.
	AgentID      string
	Incremental  bool
	Kind         Kind
	Role         string
	Text         string
	Tool         string
	Input        map[string]any
	Output       string
	ToolCallID   string
	Details      map[string]any
	ContentIndex int
	CreatedAt    time.Time
}

type ParseOptions struct {
	// AttributeChildStreams is enabled for new child-indexed Transcript Events.
	// Older stored stream wrappers keep their original projection.
	AttributeChildStreams     bool
	IncludeThinking           bool
	IncludeReasoningSummaries bool
	IncludeErrors             bool
}

// RecordMeta carries durable provider-event context that is not part of the
// provider JSON record itself.
type RecordMeta struct {
	ProviderEvent string
}
