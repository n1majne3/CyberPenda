package projectinterface

import (
	"context"
	"fmt"

	"pentest/internal/blackboard"
	"pentest/internal/task"
)

// ContinuationLaunchRequest is the trusted daemon input for one graph-native
// Runtime Continuation launch.
type ContinuationLaunchRequest struct {
	ProjectID        string
	TaskID           string
	RuntimeProfileID string
	RuntimePluginID  string
	Runner           task.Runner
	RuntimeConfig    map[string]any
}

// ContinuationLaunch is the atomic database result. Snapshot bytes remain
// reconstructible from Projection and are materialized only after commit.
type ContinuationLaunch struct {
	RuntimeConfig task.RuntimeConfigVersion
	Continuation  task.TaskContinuation
	Grant         Grant
	Token         string
	Projection    blackboard.CanonicalMainGraphProjection
}

// CreateContinuationLaunch captures config, the current full graph, the
// Continuation reconciliation marker, and its grant in one short transaction.
func (s *Service) CreateContinuationLaunch(ctx context.Context, req ContinuationLaunchRequest) (ContinuationLaunch, error) {
	if s.db == nil || s.graph == nil || s.grants == nil || s.tasks == nil {
		return ContinuationLaunch{}, fmt.Errorf("graph-native Continuation launch is unavailable")
	}
	if err := s.tasks.PrepareContinuationLaunch(req.TaskID); err != nil {
		return ContinuationLaunch{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuationLaunch{}, fmt.Errorf("begin atomic Continuation launch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	projection, err := s.graph.PinCurrentCanonicalMainGraph(ctx, tx, req.ProjectID)
	if err != nil {
		return ContinuationLaunch{}, ValidationError(ErrCodeSnapshotUnavailable, "current full Blackboard snapshot is unavailable: "+err.Error(), "blackboard")
	}
	pin := projection.ImmutablePin()
	config, continuation, err := s.tasks.CreateContinuationLaunchTx(ctx, tx, task.ContinuationLaunchRequest{
		ProjectID: req.ProjectID, TaskID: req.TaskID, RuntimeProfileID: req.RuntimeProfileID,
		RuntimeProvider: req.RuntimePluginID, Runner: req.Runner, RuntimeConfig: req.RuntimeConfig,
		SnapshotPin: task.ContinuationSnapshotPin{
			BlackboardGraphRevision: pin.GraphRevision, BlackboardRendererVersion: pin.RendererVersion,
			BlackboardEstimatorVersion: pin.EstimatorVersion, BlackboardProjectionHash: pin.ProjectionHash,
			BlackboardProjectionBytes: pin.ProjectionBytes, BlackboardProjectionEstimatedTokens: pin.EstimatedTokens,
		},
	})
	if err != nil {
		return ContinuationLaunch{}, err
	}
	token, grant, err := s.grants.issueTx(ctx, tx, IssueGrantRequest{
		ProjectID: req.ProjectID, TaskID: req.TaskID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: config.ID, RuntimeProfileID: req.RuntimeProfileID,
		RuntimePluginID: req.RuntimePluginID, Runner: string(req.Runner),
	})
	if err != nil {
		return ContinuationLaunch{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContinuationLaunch{}, fmt.Errorf("commit atomic Continuation launch: %w", err)
	}
	return ContinuationLaunch{RuntimeConfig: config, Continuation: continuation, Grant: grant, Token: token, Projection: projection}, nil
}
