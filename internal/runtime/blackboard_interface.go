package runtime

import "strings"

// TrustedProjectInterfaceServerName is the daemon-owned MCP server identity of
// the single trusted Blackboard v2 Project Interface (ADR 0014). Launch
// projection registers exactly this server name, and provider sessions expose
// its tools under the provider-native mcp__<server>__<tool> identity.
const TrustedProjectInterfaceServerName = "pentest"

// BlackboardOperation is the closed canonical identity of one trusted
// Blackboard v2 Project Interface operation. It is separate from any
// provider-visible tool name: a display-name match alone is never proof of
// trusted origin.
type BlackboardOperation string

const (
	BlackboardOperationChange              BlackboardOperation = "blackboard_change"
	BlackboardOperationRecordAttemptResult BlackboardOperation = "blackboard_record_attempt_result"
	BlackboardOperationRead                BlackboardOperation = "blackboard_read"
	BlackboardOperationHistory             BlackboardOperation = "blackboard_history"
	BlackboardOperationRetainEvidence      BlackboardOperation = "blackboard_retain_evidence"
	BlackboardOperationCheckpointAttempt   BlackboardOperation = "blackboard_checkpoint_attempt"
	BlackboardOperationFinish              BlackboardOperation = "blackboard_finish"
)

// TrustedProjectInterfaceOperations returns the seven canonical Blackboard
// operations of the trusted Project Interface in registration order.
func TrustedProjectInterfaceOperations() []BlackboardOperation {
	return []BlackboardOperation{
		BlackboardOperationChange,
		BlackboardOperationRecordAttemptResult,
		BlackboardOperationRead,
		BlackboardOperationHistory,
		BlackboardOperationRetainEvidence,
		BlackboardOperationCheckpointAttempt,
		BlackboardOperationFinish,
	}
}

// TrustedProjectInterfaceToolName returns the exact provider-visible tool
// identity registered for one canonical Blackboard operation.
func TrustedProjectInterfaceToolName(op BlackboardOperation) string {
	return "mcp__" + TrustedProjectInterfaceServerName + "__" + string(op)
}

// ClassifyTrustedBlackboardTool maps a provider-visible tool name to its
// canonical Blackboard operation identity. Only the exact registered identity
// of the trusted Project Interface is trusted; a bare display name or a
// similar name from any other MCP server is never trusted.
func ClassifyTrustedBlackboardTool(name string) (BlackboardOperation, bool) {
	name = strings.TrimSpace(name)
	prefix := "mcp__" + TrustedProjectInterfaceServerName + "__"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	op := BlackboardOperation(strings.TrimPrefix(name, prefix))
	if !isRegisteredBlackboardOperation(op) {
		return "", false
	}
	return op, true
}

func isRegisteredBlackboardOperation(op BlackboardOperation) bool {
	for _, registered := range TrustedProjectInterfaceOperations() {
		if op == registered {
			return true
		}
	}
	return false
}

// CoversSourceWork reports whether a successful operation of this kind covers
// earlier source work for Semantic Debt Watermarks (ADR 0018). Reads, history,
// Evidence retention, and every failed mutation keep their current
// non-covering behavior.
func (op BlackboardOperation) CoversSourceWork() bool {
	switch op {
	case BlackboardOperationChange, BlackboardOperationRecordAttemptResult, BlackboardOperationCheckpointAttempt, BlackboardOperationFinish:
		return true
	default:
		return false
	}
}
