package daemon

import (
	"strings"
	"sync"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

type blackboardConclusionTurnKey struct {
	taskID         string
	continuationID string
	sessionID      string
	turnID         string
}

type blackboardConclusionObservedTurn struct {
	terminalToolResults int
}

// blackboardConclusionTracker retains only in-flight, bounded observation
// watermarks. The durable receipt created at the completed Turn boundary is
// the source of truth exposed by Task APIs.
type blackboardConclusionTracker struct {
	mu    sync.Mutex
	turns map[blackboardConclusionTurnKey]blackboardConclusionObservedTurn
}

func newBlackboardConclusionTracker() *blackboardConclusionTracker {
	return &blackboardConclusionTracker{turns: make(map[blackboardConclusionTurnKey]blackboardConclusionObservedTurn)}
}

func (tracker *blackboardConclusionTracker) deleteTask(taskID string) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for key := range tracker.turns {
		if key.taskID == taskID {
			delete(tracker.turns, key)
		}
	}
}

func (server *Server) observeProviderSession(taskID, continuationID, sessionID string, turnKind runtime.RuntimeTurnKind, observation runtime.ProviderSessionObservation) {
	found, err := server.tasks.Get(taskID)
	if err != nil || found.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted {
		return
	}
	if strings.TrimSpace(continuationID) == "" || turnKind != runtime.RuntimeTurnKindWork {
		return
	}
	turnID := strings.TrimSpace(observation.ProviderTurnID)
	if turnID == "" {
		return
	}
	key := blackboardConclusionTurnKey{
		taskID: taskID, continuationID: continuationID,
		sessionID: strings.TrimSpace(sessionID), turnID: turnID,
	}

	server.blackboardConclusions.mu.Lock()
	if observation.Kind == runtime.ProviderSessionObservationToolResult {
		for observedKey := range server.blackboardConclusions.turns {
			if observedKey.taskID == key.taskID && observedKey.continuationID == key.continuationID &&
				observedKey.sessionID == key.sessionID && observedKey.turnID != key.turnID {
				delete(server.blackboardConclusions.turns, observedKey)
			}
		}
	}
	state := server.blackboardConclusions.turns[key]
	switch observation.Kind {
	case runtime.ProviderSessionObservationToolResult:
		if !isTrustedBlackboardTool(observation.ToolName) {
			state.terminalToolResults++
			server.blackboardConclusions.turns[key] = state
		}
		server.blackboardConclusions.mu.Unlock()
		return
	case runtime.ProviderSessionObservationTurnCompleted:
	default:
		server.blackboardConclusions.mu.Unlock()
		return
	}
	server.blackboardConclusions.mu.Unlock()

	if observation.Status != "completed" || state.terminalToolResults == 0 {
		server.blackboardConclusions.mu.Lock()
		delete(server.blackboardConclusions.turns, key)
		server.blackboardConclusions.mu.Unlock()
		return
	}
	if _, _, err := server.tasks.RecordPendingBlackboardConclusion(
		taskID, continuationID, key.sessionID, turnID, state.terminalToolResults,
	); err != nil {
		// Retain the bounded watermark so duplicate provider completion delivery
		// can retry the idempotent durable receipt after a transient Store error.
		server.logger.Printf("assisted conclusion: record pending Task %s Turn %s (retained for retry): %v", taskID, turnID, err)
		return
	}
	server.blackboardConclusions.mu.Lock()
	delete(server.blackboardConclusions.turns, key)
	server.blackboardConclusions.mu.Unlock()
}

func isTrustedBlackboardTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "blackboard_change", "blackboard_read", "blackboard_history", "blackboard_retain_evidence", "blackboard_checkpoint_attempt", "blackboard_finish":
		return true
	default:
		return false
	}
}

func (server *Server) attachBlackboardConclusion(found task.Task) (task.Task, error) {
	view := task.BlackboardConclusion{
		Mode:  found.RunControls.BlackboardConclusionMode,
		State: task.BlackboardConclusionStateClean,
	}
	receipt, err := server.tasks.LatestBlackboardConclusion(found.ID)
	if err != nil {
		return task.Task{}, err
	}
	if receipt != nil {
		view.State = receipt.State
		view.SourceTurnID = receipt.SourceTurnID
	}
	found.BlackboardConclusion = view
	return found, nil
}
