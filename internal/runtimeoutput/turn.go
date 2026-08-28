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
	Incremental    bool
	Kind           Kind
	Role           string
	Text           string
	Tool           string
	Input          map[string]any
	Output         string
	ToolCallID     string
	Details        map[string]any
	ContentIndex   int // block index within provider content; -1 omits the numeric suffix
	CreatedAt      time.Time
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
