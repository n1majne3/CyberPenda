package daemon

import (
	"context"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/mcpserver"
	"pentest/internal/runtime"
	"pentest/internal/session"
	"pentest/internal/task"
)

// resolveBlackboardFinishIntentPolicy is the daemon-owned decision for one
// blackboard_finish call. In assisted mode the finish records a deferred
// Blackboard Finish Intent and the daemon supplies the source Work Turn
// provenance from its own observation state (ADR 0022). In interactive mode the
// call closes the Continuation immediately as before. The provenance never
// comes from caller input.
func (server *Server) resolveBlackboardFinishIntentPolicy(sessionOwner bool, ownerID, continuationID string) (mcpserver.FinishDecision, error) {
	if server.blackboardV2 == nil {
		return mcpserver.FinishDecision{}, nil
	}
	assisted := false
	provenance := runtime.AssistedConclusionTurnKey{}
	observed := runtime.AssistedConclusionObservedTurn{}
	hasTurn := false
	if sessionOwner {
		found, err := server.sessions.Get(ownerID)
		if err == nil && found.RunControls.BlackboardMode == session.BlackboardModeWorkingGraph {
			assisted = true
			provenance, observed, hasTurn = server.blackboardConclusions.ActiveWorkTurn(ownerID, continuationID)
		}
	} else {
		found, err := server.tasks.Get(ownerID)
		if err == nil && found.RunControls.BlackboardMode == task.BlackboardModeWorkingGraph {
			assisted = true
			provenance, observed, hasTurn = server.blackboardConclusions.ActiveWorkTurn(ownerID, continuationID)
		}
	}
	if !assisted {
		return mcpserver.FinishDecision{}, nil
	}
	sourceTurnID := strings.TrimSpace(provenance.TurnID)
	if !hasTurn || sourceTurnID == "" {
		// No observed Work Turn yet: record the intent with the best available
		// correlation. The watermark starts at zero and advances with later work.
		sourceTurnID = "unknown-work-turn"
	}
	return mcpserver.FinishDecision{
		RecordIntent: true,
		Provenance: blackboardv2.FinishIntentProvenance{
			SourceTurnID:        sourceTurnID,
			SourceWorkWatermark: observed.SourceWorkWatermark,
		},
	}, nil
}

// hasUnsettledTaskFinishIntent reports whether the Task's latest continuation
// has a recorded, still-valid Blackboard Finish Intent that has not yet closed
// the Blackboard write protocol. The public conclusion state must stay non-clean
// while such an intent exists, even after a daemon restart leaves the Task
// interrupted (ADR 0022, criterion 4). A continuation that has already settled
// (status completed / a finish receipt exists) is not unsettled.
func (server *Server) hasUnsettledTaskFinishIntent(taskID string) bool {
	if server.blackboardV2 == nil {
		return false
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return false
	}
	latest, err := server.tasks.LatestContinuation(taskID)
	if err != nil || latest == nil {
		return false
	}
	// A completed continuation has already closed its Blackboard write protocol
	// (the finish intent settled). Any other status — running, paused, or an
	// interrupted/stopped/failed continuation after restart or stop — leaves the
	// recorded intent as pending conclusion work.
	if latest.Status == task.StatusCompleted {
		return false
	}
	intent, has, err := server.blackboardV2.FinishIntentForContinuation(context.Background(), found.ProjectID, latest.ID)
	if err != nil || !has {
		return false
	}
	return intent.Valid
}

// hasUnsettledSessionFinishIntent is the Session mirror of
// hasUnsettledTaskFinishIntent.
func (server *Server) hasUnsettledSessionFinishIntent(sessionID string) bool {
	if server.blackboardV2 == nil {
		return false
	}
	latest, err := server.sessions.LatestContinuation(sessionID)
	if err != nil || latest == nil {
		return false
	}
	if latest.Status == session.RuntimeStatusCompleted {
		return false
	}
	intent, has, err := server.blackboardV2.SessionFinishIntentForContinuation(context.Background(), sessionID, latest.ID)
	if err != nil || !has {
		return false
	}
	return intent.Valid
}

// invalidateTaskFinishIntentOnLaterSourceWork invalidates a valid Task Blackboard
// Finish Intent when later non-Blackboard source work advances the Work Turn
// (ADR 0022). A bounded Timeline state change follows. Errors are logged because
// an invalidation failure must not abort the Turn observation.
func (server *Server) invalidateTaskFinishIntentOnLaterSourceWork(projectID, continuationID, taskID string) {
	if server.blackboardV2 == nil || projectID == "" || continuationID == "" {
		return
	}
	intent, found, err := server.blackboardV2.FinishIntentForContinuation(context.Background(), projectID, continuationID)
	if err != nil {
		server.logger.Printf("blackboard finish intent: read Task %s continuation %s: %v", taskID, continuationID, err)
		return
	}
	if !found || !intent.Valid {
		return
	}
	if err := server.blackboardV2.InvalidateFinishIntent(context.Background(), projectID, continuationID); err != nil {
		server.logger.Printf("blackboard finish intent: invalidate Task %s continuation %s: %v", taskID, continuationID, err)
		return
	}
	server.logger.Printf("blackboard finish intent: invalidated Task %s continuation %s after later source work", taskID, continuationID)
	// ADR 0022 / CONTEXT.md: invalidating a Blackboard Finish Intent produces a
	// bounded Runtime notice and a Timeline state change so the Runtime knows it
	// must persist the new semantics and record a new finish intent.
	if _, err := server.tasks.AppendEvent(taskID, task.EventKindBlackboardConclusion, task.EventPayload{
		"phase": "finish_intent_invalidated", "continuation_id": continuationID,
	}); err != nil {
		server.logger.Printf("blackboard finish intent: record invalidation Timeline event Task %s: %v", taskID, err)
	}
}

// settleTaskFinishIntentAfterApply closes the Task Continuation's Blackboard
// write protocol after a conclusion obligation reached terminal-clean. The
// terminal apply is itself proof that the Work Turn's semantic debt was covered.
// This path covers Work-Turn settlement (debt-free Turns are born clean at the
// checkpoint), the post-apply settlement after a Conclude Runtime Turn, and
// daemon restart recovery, where the observer does not run (ADR 0022).
func (server *Server) settleTaskFinishIntentAfterApply(ctx context.Context, projectID, continuationID string) {
	if server.blackboardV2 == nil || projectID == "" || continuationID == "" {
		return
	}
	if err := server.applyTaskFinishIntentSettlement(ctx, projectID, continuationID); err != nil {
		server.logger.Printf("blackboard finish intent: settle Task continuation %s after apply: %v", continuationID, err)
	}
}

func (server *Server) applyTaskFinishIntentSettlement(ctx context.Context, projectID, continuationID string) error {
	intent, found, err := server.blackboardV2.FinishIntentForContinuation(ctx, projectID, continuationID)
	if err != nil {
		return err
	}
	if !found || !intent.Valid {
		return nil
	}
	settled, err := server.blackboardV2.SettleFinishIntent(ctx, projectID, continuationID)
	if err != nil {
		return err
	}
	if settled {
		server.logger.Printf("blackboard finish intent: settled Task continuation %s at Work Turn close", continuationID)
	}
	return nil
}

// invalidateSessionFinishIntentOnLaterSourceWork invalidates a valid Session
// Blackboard Finish Intent when later non-Blackboard source work advances the
// Work Turn (ADR 0022). Mirrors the Task path.
func (server *Server) invalidateSessionFinishIntentOnLaterSourceWork(sessionID, continuationID string) {
	if server.blackboardV2 == nil || sessionID == "" || continuationID == "" {
		return
	}
	intent, found, err := server.blackboardV2.SessionFinishIntentForContinuation(context.Background(), sessionID, continuationID)
	if err != nil {
		server.logger.Printf("blackboard finish intent: read Session %s continuation %s: %v", sessionID, continuationID, err)
		return
	}
	if !found || !intent.Valid {
		return
	}
	if err := server.blackboardV2.InvalidateSessionFinishIntent(context.Background(), sessionID, continuationID); err != nil {
		server.logger.Printf("blackboard finish intent: invalidate Session %s continuation %s: %v", sessionID, continuationID, err)
		return
	}
	server.logger.Printf("blackboard finish intent: invalidated Session %s continuation %s after later source work", sessionID, continuationID)
	if _, err := server.sessions.AppendEvent(sessionID, session.EventKindBlackboardConclusion, session.EventPayload{
		"phase": "finish_intent_invalidated", "continuation_id": continuationID,
	}); err != nil {
		server.logger.Printf("blackboard finish intent: record invalidation Timeline event Session %s: %v", sessionID, err)
	}
}

// settleSessionFinishIntentAfterApply closes the Session Continuation's
// Blackboard write protocol after a conclusion obligation reached terminal-clean
// (covers Work-Turn settlement, Conclude-Turn apply, and daemon restart recovery).
func (server *Server) settleSessionFinishIntentAfterApply(ctx context.Context, sessionID, continuationID string) {
	if server.blackboardV2 == nil || sessionID == "" || continuationID == "" {
		return
	}
	if err := server.applySessionFinishIntentSettlement(ctx, sessionID, continuationID); err != nil {
		server.logger.Printf("blackboard finish intent: settle Session continuation %s after apply: %v", continuationID, err)
	}
}

func (server *Server) applySessionFinishIntentSettlement(ctx context.Context, sessionID, continuationID string) error {
	intent, found, err := server.blackboardV2.SessionFinishIntentForContinuation(ctx, sessionID, continuationID)
	if err != nil {
		return err
	}
	if !found || !intent.Valid {
		return nil
	}
	settled, err := server.blackboardV2.SettleSessionFinishIntent(ctx, sessionID, continuationID)
	if err != nil {
		return err
	}
	if settled {
		server.logger.Printf("blackboard finish intent: settled Session continuation %s at Work Turn close", continuationID)
	}
	return nil
}
