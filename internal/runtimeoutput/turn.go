package runtimeoutput

import "time"

// Kind classifies one normalized runtime output turn.
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

// Coarse Subagent Activity states shared by every provider parser.
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

// Turn is one normalized fragment from a provider stdout/stderr JSON line.
type Turn struct {
	SourceID       string
	SourceSeq      int
	ProviderItemID string
	LifecyclePhase string
	// AgentID carries provider child-agent attribution for multiplexed stream
	// items (for example a Claude async Agent-tool child). Empty means the
	// item belongs to the main thread.
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
	ContentIndex int // block index within provider content; -1 omits the numeric suffix
	CreatedAt    time.Time
}

// ParseOptions controls which fragments are emitted from a provider record.
type ParseOptions struct {
	IncludeThinking           bool
	IncludeReasoningSummaries bool
	IncludeErrors             bool
}

// RecordMeta carries durable provider-event context that is not part of the
// provider JSON record itself.
type RecordMeta struct {
	ProviderEvent string
}
