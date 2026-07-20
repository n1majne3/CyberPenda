package runtimeprofile

import (
	"fmt"
	"strings"
)

// ReasoningEffort is a structured CyberPenda choice for how much model
// reasoning a Runtime should request on a Runtime Turn.
type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
	ReasoningEffortXHigh  ReasoningEffort = "xhigh"
	ReasoningEffortMax    ReasoningEffort = "max"
)

// ErrInvalidReasoningEffort reports a value outside the five allowed choices.
var ErrInvalidReasoningEffort = fmt.Errorf("reasoning effort must be one of low, medium, high, xhigh, max")

var reasoningEfforts = map[ReasoningEffort]bool{
	ReasoningEffortLow:    true,
	ReasoningEffortMedium: true,
	ReasoningEffortHigh:   true,
	ReasoningEffortXHigh:  true,
	ReasoningEffortMax:    true,
}

// NormalizeReasoningEffort validates a stored or requested value. Empty input
// resolves to high without requiring a stored rewrite.
func NormalizeReasoningEffort(value string) (ReasoningEffort, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ReasoningEffortHigh, nil
	}
	effort := ReasoningEffort(trimmed)
	if !reasoningEfforts[effort] {
		return "", fmt.Errorf("%w: %q", ErrInvalidReasoningEffort, trimmed)
	}
	return effort, nil
}

// ResolveRequestedReasoningEffort applies the documented precedence order for
// Requested Reasoning Effort: current Runtime Turn Selection, Launch
// Reasoning Effort Override, Runtime Profile default, then high.
// Empty candidates are skipped. A non-empty invalid candidate fails clearly
// instead of falling through to a lower-precedence value.
func ResolveRequestedReasoningEffort(turnSelection, launchOverride, profileDefault string) (ReasoningEffort, error) {
	for _, candidate := range []string{turnSelection, launchOverride, profileDefault} {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		effort, err := NormalizeReasoningEffort(trimmed)
		if err != nil {
			return "", err
		}
		return effort, nil
	}
	return ReasoningEffortHigh, nil
}
