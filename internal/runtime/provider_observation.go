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
// BlackboardOperation is the canonical trusted Project Interface identity
// carried for tool observations whose provider-visible ToolName resolves to
// the trusted registration; it stays empty for every untrusted tool name.
type ProviderSessionObservation struct {
	Kind                ProviderSessionObservationKind
	RequestID           string
	SessionID           string
	ProviderTurnID      string
	ToolCallID          string
	ToolName            string
	Status              string
	BlackboardOperation BlackboardOperation
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
		if err := validateCanonicalBlackboardOperation(toolName, observation.BlackboardOperation); err != nil {
			return err
		}
	case ProviderSessionObservationToolResult:
		if toolCallID == "" || toolName == "" || (status != "succeeded" && status != "failed") {
			return fmt.Errorf("%w: Tool Result requires terminal bounded metadata", ErrInvalidProviderSessionObservation)
		}
		if err := validateCanonicalBlackboardOperation(toolName, observation.BlackboardOperation); err != nil {
			return err
		}
	case ProviderSessionObservationTurnCompleted:
		if toolCallID != "" || toolName != "" || (status != "completed" && status != "failed" && status != "interrupted") {
			return fmt.Errorf("%w: Turn completion has invalid fields", ErrInvalidProviderSessionObservation)
		}
		if observation.BlackboardOperation != "" {
			return fmt.Errorf("%w: Turn completion cannot carry a canonical Blackboard operation", ErrInvalidProviderSessionObservation)
		}
	default:
		return fmt.Errorf("%w: unknown observation kind", ErrInvalidProviderSessionObservation)
	}
	return nil
}

// validateCanonicalBlackboardOperation enforces the closed identity contract:
// a trusted registered tool name must carry exactly its canonical operation,
// and an untrusted name must carry no canonical operation at all.
func validateCanonicalBlackboardOperation(toolName string, operation BlackboardOperation) error {
	canonical, trusted := ClassifyTrustedBlackboardTool(toolName)
	if operation == canonical {
		return nil
	}
	if trusted {
		return fmt.Errorf("%w: trusted tool identity requires its canonical Blackboard operation", ErrInvalidProviderSessionObservation)
	}
	return fmt.Errorf("%w: canonical Blackboard operation requires the trusted tool identity", ErrInvalidProviderSessionObservation)
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

// ProviderSessionTurnLineage freezes canonical request/Turn correlation, the
// Harness-owned kind, and the explicit Runtime Turn Selection sent for one
// provider request. Provider notifications may supply lookup keys, but never
// choose or rewrite the returned lineage.
type ProviderSessionTurnLineage struct {
	RequestID                string
	ProviderTurnID           string
	Kind                     RuntimeTurnKind
	ModelProviderID          string
	Model                    string
	RequestedReasoningEffort string
	EffectiveReasoningEffort string
}

// ProviderSessionCompleteTurnLineageResolver resolves immutable request
// lineage by either Harness request identity or provider Turn identity.
type ProviderSessionCompleteTurnLineageResolver interface {
	ResolveProviderSessionTurnLineage(requestID, providerTurnID string) (ProviderSessionTurnLineage, bool)
}

func providerSessionTurnLineage(request ProviderSessionRequest, providerTurnID string) ProviderSessionTurnLineage {
	return ProviderSessionTurnLineage{
		RequestID:                strings.TrimSpace(request.RequestID),
		ProviderTurnID:           strings.TrimSpace(providerTurnID),
		Kind:                     normalizeRuntimeTurnKind(request.TurnKind),
		ModelProviderID:          strings.TrimSpace(request.ModelProviderID),
		Model:                    strings.TrimSpace(request.Model),
		RequestedReasoningEffort: strings.TrimSpace(request.RequestedReasoningEffort),
		EffectiveReasoningEffort: strings.TrimSpace(request.EffectiveReasoningEffort),
	}
}

func normalizeRuntimeTurnKind(kind RuntimeTurnKind) RuntimeTurnKind {
	if kind == "" {
		return RuntimeTurnKindWork
	}
	return kind
}
