package runtime

import (
	"errors"
	"fmt"
	"strings"
)

// RuntimeTurnKind distinguishes operator work from a Harness-owned control
// turn. The daemon, not the provider payload, owns this classification.
type RuntimeTurnKind string

const (
	RuntimeTurnKindWork    RuntimeTurnKind = "work"
	RuntimeTurnKindControl RuntimeTurnKind = "control"
)

// ProviderSessionObservationKind is the closed metadata vocabulary that a
// provider session may expose to daemon orchestration. Tool arguments, raw
// results, and model reasoning have no representation here.
type ProviderSessionObservationKind string

const (
	ProviderSessionObservationToolUse       ProviderSessionObservationKind = "tool_use"
	ProviderSessionObservationToolResult    ProviderSessionObservationKind = "tool_result"
	ProviderSessionObservationTurnCompleted ProviderSessionObservationKind = "turn_completed"
)

var ErrInvalidProviderSessionObservation = errors.New("invalid provider session observation")

const (
	maxProviderObservationIDLength       = 1024
	maxProviderObservationToolNameLength = 256
)

// ProviderSessionObservation contains only bounded correlation metadata.
// Status is empty for Tool Use, succeeded/failed for Tool Result, and
// completed/failed/interrupted for a completed Turn notification.
type ProviderSessionObservation struct {
	Kind           ProviderSessionObservationKind
	RequestID      string
	SessionID      string
	ProviderTurnID string
	ToolCallID     string
	ToolName       string
	Status         string
}

// Validate enforces the closed, kind-specific observation shape.
func (observation ProviderSessionObservation) Validate() error {
	for name, value := range map[string]string{
		"request": observation.RequestID, "session": observation.SessionID,
		"provider Turn": observation.ProviderTurnID, "Tool Call": observation.ToolCallID,
	} {
		if len(value) > maxProviderObservationIDLength {
			return fmt.Errorf("%w: %s identity exceeds bound", ErrInvalidProviderSessionObservation, name)
		}
	}
	if len(observation.ToolName) > maxProviderObservationToolNameLength {
		return fmt.Errorf("%w: tool identity exceeds bound", ErrInvalidProviderSessionObservation)
	}
	if strings.TrimSpace(observation.RequestID) == "" || strings.TrimSpace(observation.SessionID) == "" || strings.TrimSpace(observation.ProviderTurnID) == "" {
		return fmt.Errorf("%w: Harness request, session, and provider Turn correlation are required", ErrInvalidProviderSessionObservation)
	}
	toolCallID := strings.TrimSpace(observation.ToolCallID)
	toolName := strings.TrimSpace(observation.ToolName)
	status := strings.TrimSpace(observation.Status)
	switch observation.Kind {
	case ProviderSessionObservationToolUse:
		if toolCallID == "" || toolName == "" || status != "" {
			return fmt.Errorf("%w: Tool Use requires call and tool identity only", ErrInvalidProviderSessionObservation)
		}
	case ProviderSessionObservationToolResult:
		if toolCallID == "" || toolName == "" || (status != "succeeded" && status != "failed") {
			return fmt.Errorf("%w: Tool Result requires terminal bounded metadata", ErrInvalidProviderSessionObservation)
		}
	case ProviderSessionObservationTurnCompleted:
		if toolCallID != "" || toolName != "" || (status != "completed" && status != "failed" && status != "interrupted") {
			return fmt.Errorf("%w: Turn completion has invalid fields", ErrInvalidProviderSessionObservation)
		}
	default:
		return fmt.Errorf("%w: unknown observation kind", ErrInvalidProviderSessionObservation)
	}
	return nil
}

// ProviderSessionObserve receives one validated observation.
type ProviderSessionObserve func(ProviderSessionObservation)

// ProviderSessionObservationSink is implemented by sessions that expose
// bounded semantic-work metadata independently from transcript events.
type ProviderSessionObservationSink interface {
	SetObservationSink(ProviderSessionObserve)
}

// ProviderSessionTurnLineageResolver resolves provider correlation against
// Harness-owned requests. Observations never classify their own Runtime Turn.
type ProviderSessionTurnLineageResolver interface {
	ResolveProviderSessionTurnKind(requestID, providerTurnID string) (RuntimeTurnKind, bool)
}

func normalizeRuntimeTurnKind(kind RuntimeTurnKind) RuntimeTurnKind {
	if kind == "" {
		return RuntimeTurnKindWork
	}
	return kind
}
