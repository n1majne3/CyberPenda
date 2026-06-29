package runtimeoutput

import "time"

// Kind classifies one normalized runtime output turn.
type Kind string

const (
	KindThinking   Kind = "thinking"
	KindText       Kind = "text"
	KindToolUse    Kind = "tool_use"
	KindToolResult Kind = "tool_result"
	KindError      Kind = "error"
)

// Turn is one normalized fragment from a provider stdout/stderr JSON line.
type Turn struct {
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
	IncludeThinking bool
	IncludeErrors   bool
}