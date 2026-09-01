package daemon

import (
	"context"
	"errors"
	"path/filepath"

	"pentest/internal/session"
	"pentest/internal/task"
	"pentest/internal/workinggraph"
)

func (server *Server) settleTaskWorkingGraph(ctx context.Context, found task.Task, allowActionRequired bool) (bool, error) {
	if found.RunControls.BlackboardMode != task.BlackboardModeWorkingGraph {
		return true, nil
	}
	continuation, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return false, err
	}
	if continuation == nil {
		return true, nil
	}
	workdir := filepath.Join(server.runtimeRoot, found.ID, "workdir")
	contract := found.OwnerContract(workdir)
	projection, err := server.workingGraph.Prepare(ctx, workinggraph.OwnerContext{
		Owner: contract, ContinuationID: continuation.ID, Workdir: workdir,
	})
	if err != nil {
		return false, err
	}
	result, err := server.workingGraph.Settle(ctx, workinggraph.SettlementRequest{
		Owner: contract, ContinuationID: continuation.ID, Projection: projection,
		Apply: server.workingGraphCompiler.Apply,
	})
	if err != nil {
		return false, err
	}
	if result.Blocked && !allowActionRequired {
		return false, errSemanticConclusionActionRequired
	}
	return true, nil
}

func (server *Server) settleSessionWorkingGraph(ctx context.Context, found session.Session, allowActionRequired bool) (bool, error) {
	if found.RunControls.BlackboardMode != session.BlackboardModeWorkingGraph {
		return true, nil
	}
	continuation, err := server.sessions.LatestContinuation(found.ID)
	if err != nil {
		return false, err
	}
	if continuation == nil {
		return true, nil
	}
	contract := found.OwnerContract()
	projection, err := server.workingGraph.Prepare(ctx, workinggraph.OwnerContext{
		Owner: contract, ContinuationID: continuation.ID, Workdir: found.Workdir,
	})
	if err != nil {
		return false, err
	}
	result, err := server.workingGraph.Settle(ctx, workinggraph.SettlementRequest{
		Owner: contract, ContinuationID: continuation.ID, Projection: projection,
		Apply: server.workingGraphCompiler.Apply,
	})
	if err != nil {
		return false, err
	}
	if result.Blocked && !allowActionRequired {
		return false, errSemanticConclusionActionRequired
	}
	return true, nil
}

func workingGraphActionRequired(err error) bool {
	return errors.Is(err, errSemanticConclusionActionRequired)
}
