package runtime

import (
	"errors"
	"testing"
)

// #192: semantic-debt accounting recognizes a trusted Blackboard v2 operation
// by its canonical Project Interface identity, never by a provider-visible
// display name. Only the exact registered mcp__pentest__<operation> tool
// identity is trusted.
func TestClassifyTrustedBlackboardToolOnlyTrustsRegisteredProjectInterfaceIdentity(t *testing.T) {
	trusted := []struct {
		name string
		op   BlackboardOperation
	}{
		{name: "mcp__pentest__blackboard_change", op: BlackboardOperationChange},
		{name: "mcp__pentest__blackboard_read", op: BlackboardOperationRead},
		{name: "mcp__pentest__blackboard_history", op: BlackboardOperationHistory},
		{name: "mcp__pentest__blackboard_retain_evidence", op: BlackboardOperationRetainEvidence},
		{name: "mcp__pentest__blackboard_checkpoint_attempt", op: BlackboardOperationCheckpointAttempt},
		{name: "mcp__pentest__blackboard_finish", op: BlackboardOperationFinish},
	}
	for _, test := range trusted {
		op, ok := ClassifyTrustedBlackboardTool(test.name)
		if !ok || op != test.op {
			t.Fatalf("classify %q = %q, %v; want %q, true", test.name, op, ok, test.op)
		}
	}

	for _, name := range []string{
		// Bare display names are not registered identities.
		"blackboard_change", "blackboard_read", "blackboard_history",
		"blackboard_retain_evidence", "blackboard_checkpoint_attempt", "blackboard_finish",
		// Similar names from any other server, near matches, and garbage.
		"mcp__evil__blackboard_change", "mcp__pentest__blackboard_changes",
		"mcp__pentest__blackboard_attempt", "mcp__PENTEST__blackboard_change",
		"mcp__pentest", "mcp__pentest__", "mcp__", "shell", "",
	} {
		if op, ok := ClassifyTrustedBlackboardTool(name); ok {
			t.Fatalf("classify %q = %q, true; want untrusted", name, op)
		}
	}
}

// ADR 0018: only successful change, Attempt checkpoint, and finish cover
// earlier source work. Reads, history, Evidence retention, and every failed
// mutation keep their current non-covering behavior.
func TestBlackboardOperationCoversSourceWorkContract(t *testing.T) {
	for _, op := range []BlackboardOperation{
		BlackboardOperationChange, BlackboardOperationCheckpointAttempt, BlackboardOperationFinish,
	} {
		if !op.CoversSourceWork() {
			t.Fatalf("operation %q must cover earlier source work", op)
		}
	}
	for _, op := range []BlackboardOperation{
		BlackboardOperationRead, BlackboardOperationHistory, BlackboardOperationRetainEvidence,
	} {
		if op.CoversSourceWork() {
			t.Fatalf("operation %q must not cover earlier source work", op)
		}
	}
	if BlackboardOperation("").CoversSourceWork() {
		t.Fatal("empty canonical identity must not cover earlier source work")
	}
}

func TestTrustedProjectInterfaceRegistrationIsClosed(t *testing.T) {
	operations := TrustedProjectInterfaceOperations()
	if len(operations) != 6 {
		t.Fatalf("registered operations = %d, want the six trusted Blackboard v2 operations", len(operations))
	}
	seen := map[BlackboardOperation]bool{}
	for _, op := range operations {
		if seen[op] {
			t.Fatalf("duplicate registered operation %q", op)
		}
		seen[op] = true
		if op == "" {
			t.Fatal("registered operation identity is empty")
		}
		name := TrustedProjectInterfaceToolName(op)
		classified, ok := ClassifyTrustedBlackboardTool(name)
		if !ok || classified != op {
			t.Fatalf("registered identity %q classifies as %q, %v", name, classified, ok)
		}
		if op.CoversSourceWork() && op != BlackboardOperationChange && op != BlackboardOperationCheckpointAttempt && op != BlackboardOperationFinish {
			t.Fatalf("unexpected covering operation %q", op)
		}
	}
}

// The observation contract is closed: a canonical Blackboard operation identity
// may be present only when the provider-visible tool name resolves to exactly
// that identity under the trusted Project Interface registration.
func TestProviderSessionObservationCanonicalBlackboardOperationValidation(t *testing.T) {
	base := ProviderSessionObservation{
		Kind: ProviderSessionObservationToolResult, RequestID: "request-1",
		SessionID: "session-1", ProviderTurnID: "turn-1",
		ToolCallID: "call-1", ToolName: "mcp__pentest__blackboard_change", Status: "succeeded",
	}
	valid := []ProviderSessionObservation{
		{Kind: ProviderSessionObservationToolResult, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "mcp__pentest__blackboard_change", Status: "succeeded", BlackboardOperation: BlackboardOperationChange},
		{Kind: ProviderSessionObservationToolUse, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "mcp__pentest__blackboard_read", BlackboardOperation: BlackboardOperationRead},
		{Kind: ProviderSessionObservationToolResult, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "shell", Status: "succeeded"},
		{Kind: ProviderSessionObservationToolResult, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "mcp__evil__blackboard_change", Status: "failed"},
	}
	for _, observation := range valid {
		if err := observation.Validate(); err != nil {
			t.Fatalf("valid observation %#v rejected: %v", observation, err)
		}
	}
	invalid := []ProviderSessionObservation{
		// Trusted identity missing on a registered tool name.
		base,
		{Kind: ProviderSessionObservationToolResult, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "mcp__pentest__blackboard_finish", Status: "succeeded"},
		// Fabricated canonical identity on an untrusted or near-match name.
		{Kind: ProviderSessionObservationToolResult, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "mcp__evil__blackboard_change", Status: "succeeded", BlackboardOperation: BlackboardOperationChange},
		{Kind: ProviderSessionObservationToolResult, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "blackboard_change", Status: "succeeded", BlackboardOperation: BlackboardOperationChange},
		{Kind: ProviderSessionObservationToolUse, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", ToolCallID: "call-1", ToolName: "mcp__pentest__blackboard_change", BlackboardOperation: BlackboardOperationRead},
		// Turn completion cannot carry a canonical identity.
		{Kind: ProviderSessionObservationTurnCompleted, RequestID: "request-1", SessionID: "session-1", ProviderTurnID: "turn-1", Status: "completed", BlackboardOperation: BlackboardOperationChange},
	}
	for _, observation := range invalid {
		if err := observation.Validate(); !errors.Is(err, ErrInvalidProviderSessionObservation) {
			t.Fatalf("invalid observation %#v error = %v", observation, err)
		}
	}
}
