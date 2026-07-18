package blackboard

import (
	"encoding/json"
	"fmt"
)

const taskGoalProjectorActor = "task-goal-projector"

func jsonUnmarshalProps(data []byte, value any) error {
	return json.Unmarshal(data, value)
}

// decodeResultJSON is ledger replay support, not a current read projection.
func decodeResultJSON(data string) (*MutationResult, error) {
	var stored resultLedgerForm
	if err := json.Unmarshal([]byte(data), &stored); err != nil {
		return nil, fmt.Errorf("decode result json: %w", err)
	}
	result := &MutationResult{
		MutationSequence: stored.MutationSequence, MutationID: stored.MutationID, RecordedAt: stored.RecordedAt,
		GraphRevision: stored.GraphRevision, RequestHash: stored.RequestHash, ResultingStateHash: stored.ResultingStateHash,
		Operations: make([]OperationResult, len(stored.Operations)), ResultBytes: append([]byte(nil), data...),
	}
	for index, operation := range stored.Operations {
		result.Operations[index] = OperationResult{
			OpID: operation.OpID, NodeID: operation.NodeID, NodeType: operation.NodeType, StableKey: operation.StableKey,
			NodeVersion: operation.NodeVersion, EdgeID: operation.EdgeID, EdgeType: operation.EdgeType, EdgeVersion: operation.EdgeVersion,
			SemanticHash: operation.SemanticHash, ResolvedFromAlias: operation.ResolvedFromAlias,
			ResolvedFromMergedID: operation.ResolvedFromMergedID, Changed: operation.Changed,
		}
	}
	return result, nil
}
