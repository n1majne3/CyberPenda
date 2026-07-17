package blackboardv2

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	attentionBytesPerToken  = 4
	attentionTargetTokens   = 16_000
	attentionWarningTokens  = 32_000
	attentionRequiredTokens = 64_000
)

// AttentionBudgetState classifies the exact model-visible Runtime Snapshot.
// It is diagnostic metadata and is never serialized into runtime-blackboard/v2.
type AttentionBudgetState string

const (
	AttentionWithinTarget AttentionBudgetState = "within_target"
	AttentionAboveTarget  AttentionBudgetState = "above_target"
	AttentionWarning      AttentionBudgetState = "warning"
	AttentionRequired     AttentionBudgetState = "required"
)

// RuntimeSnapshotMeasurement describes exact Snapshot attention without
// changing launch behavior. Complete Snapshots remain launchable in all states.
type RuntimeSnapshotMeasurement struct {
	Bytes           int
	EstimatedTokens int
	State           AttentionBudgetState
	Complete        bool
	Launchable      bool
}

// RuntimeSnapshotProjection is the canonical current semantic projection and
// its non-model-visible attention measurement.
type RuntimeSnapshotProjection struct {
	Snapshot        RuntimeSnapshot
	Bytes           []byte
	ByteCount       int
	EstimatedTokens int
	AttentionState  AttentionBudgetState
	Complete        bool
	Launchable      bool
}

// ProjectRuntimeSnapshot derives and canonically encodes one topology-complete
// runtime-blackboard/v2 document from the current semantic read transaction.
func (s *Service) ProjectRuntimeSnapshot(ctx context.Context, projectID string) (RuntimeSnapshotProjection, error) {
	snapshot, err := s.RuntimeSnapshot(ctx, projectID)
	if err != nil {
		return RuntimeSnapshotProjection{}, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return RuntimeSnapshotProjection{}, fmt.Errorf("encode canonical Runtime Snapshot: %w", err)
	}
	measurement := MeasureRuntimeSnapshot(data)
	return RuntimeSnapshotProjection{
		Snapshot:        snapshot,
		Bytes:           append([]byte(nil), data...),
		ByteCount:       measurement.Bytes,
		EstimatedTokens: measurement.EstimatedTokens,
		AttentionState:  measurement.State,
		Complete:        measurement.Complete,
		Launchable:      measurement.Launchable,
	}, nil
}

// MeasureRuntimeSnapshot classifies the rounded exact-byte token estimate.
// The 16K target is inclusive; warning and required begin inclusively at 32K
// and 64K respectively.
func MeasureRuntimeSnapshot(snapshot []byte) RuntimeSnapshotMeasurement {
	byteCount := len(snapshot)
	estimatedTokens := (byteCount + attentionBytesPerToken - 1) / attentionBytesPerToken
	state := AttentionWithinTarget
	switch {
	case estimatedTokens >= attentionRequiredTokens:
		state = AttentionRequired
	case estimatedTokens >= attentionWarningTokens:
		state = AttentionWarning
	case estimatedTokens > attentionTargetTokens:
		state = AttentionAboveTarget
	}
	return RuntimeSnapshotMeasurement{
		Bytes:           byteCount,
		EstimatedTokens: estimatedTokens,
		State:           state,
		Complete:        true,
		Launchable:      true,
	}
}
