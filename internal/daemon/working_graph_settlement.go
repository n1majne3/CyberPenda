package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"pentest/internal/session"
	"pentest/internal/task"
)

func (server *Server) settleTaskWorkingGraph(ctx context.Context, found task.Task, allowActionRequired bool) (bool, error) {
	if found.BlackboardProtocol != "fgs" || found.RunControls.BlackboardMode == task.BlackboardModeDisabled {
		return true, nil
	}
	continuation, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return false, err
	}
	if continuation == nil {
		return true, nil
	}
	workdir, err := filepath.Abs(filepath.Join(server.runtimeRoot, found.ID, "workdir"))
	if err != nil {
		return false, err
	}
	contract := found.OwnerContract(workdir)
	if _, err := os.Stat(filepath.Join(contract.Workdir, "graph", "outbox", continuation.ID)); os.IsNotExist(err) {
		return true, nil
	}
	result, err := server.fgs.Drain(ctx, contract, continuation.ID)
	if err != nil {
		return false, err
	}
	if result.Blocked && !allowActionRequired {
		return false, errSemanticConclusionActionRequired
	}
	return true, nil
}

func (server *Server) settleSessionWorkingGraph(ctx context.Context, found session.Session, allowActionRequired bool) (bool, error) {
	if found.BlackboardProtocol != "fgs" || found.RunControls.BlackboardMode == session.BlackboardModeDisabled {
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
	if _, err := os.Stat(filepath.Join(contract.Workdir, "graph", "outbox", continuation.ID)); os.IsNotExist(err) {
		return true, nil
	}
	result, err := server.fgs.Drain(ctx, contract, continuation.ID)
	if err != nil {
		return false, err
	}
	if result.Blocked && !allowActionRequired {
		return false, errSemanticConclusionActionRequired
	}
	return true, nil
}

func (server *Server) sessionWorkingGraphSettlement(sessionID string, allowActionRequired bool) providerControlSettlement {
	return func(ctx context.Context, _ bool) (bool, error) {
		found, err := server.sessions.Get(sessionID)
		if err != nil {
			return false, err
		}
		return server.settleSessionWorkingGraph(ctx, found, allowActionRequired)
	}
}

func (server *Server) waitForSessionWorkingGraphSettlement(ctx context.Context, sessionID string, allowActionRequired bool) error {
	_, err := server.sessionWorkingGraphSettlement(sessionID, allowActionRequired)(ctx, true)
	return err
}

func workingGraphActionRequired(err error) bool {
	return errors.Is(err, errSemanticConclusionActionRequired)
}
