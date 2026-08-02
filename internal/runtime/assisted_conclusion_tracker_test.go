package runtime

import "testing"

func TestAssistedConclusionTrackerKeepsObservationWatermarksOwnerLocal(t *testing.T) {
	tracker := NewAssistedConclusionTracker()
	taskKey := AssistedConclusionTurnKey{OwnerID: "task-1", ContinuationID: "cont-1", ProviderSessionID: "provider-1", TurnID: "turn-1"}
	sessionKey := AssistedConclusionTurnKey{OwnerID: "session-1", ContinuationID: "cont-1", ProviderSessionID: "provider-1", TurnID: "turn-1"}

	if _, accepted := tracker.RecordToolResult(taskKey, "tool-1", true, false); !accepted {
		t.Fatal("first Task Tool Result was not accepted")
	}
	if _, accepted := tracker.RecordToolResult(taskKey, "tool-1", true, false); accepted {
		t.Fatal("duplicate Task Tool Result was accepted twice")
	}
	state, ok := tracker.SnapshotTurn(taskKey)
	if !ok || state.SourceWorkWatermark != 1 || state.SemanticPersistenceWatermark != 0 || len(state.CompletedToolCalls) != 1 {
		t.Fatalf("unexpected Task observation state: %#v, %v", state, ok)
	}
	delete(state.CompletedToolCalls, "tool-1")
	if snapshot, ok := tracker.SnapshotTurn(taskKey); !ok || len(snapshot.CompletedToolCalls) != 1 {
		t.Fatalf("snapshot exposed mutable tracker state: %#v, %v", snapshot, ok)
	}

	if _, accepted := tracker.RecordToolResult(sessionKey, "tool-1", true, false); !accepted {
		t.Fatal("first Session Tool Result was not accepted")
	}
	if state, ok := tracker.SnapshotTurn(taskKey); !ok || state.SourceWorkWatermark != 1 {
		t.Fatalf("Session observation changed Task state: %#v, %v", state, ok)
	}
	tracker.DeleteOwner("task-1")
	if _, ok := tracker.SnapshotTurn(taskKey); ok {
		t.Fatal("deleted Task observation remained in tracker")
	}
	if _, ok := tracker.SnapshotTurn(sessionKey); !ok {
		t.Fatal("deleting Task observation removed Session state")
	}
}

func TestAssistedConclusionTrackerRequiresCanonicalTerminalForCallbacks(t *testing.T) {
	tracker := NewAssistedConclusionTracker()
	result := ProviderSessionAttemptResult{RequestID: "request-1", SessionID: "provider-1", ProviderTurnID: "control-1"}
	tracker.QueueResult("session-1", result)

	if _, ok := tracker.TakeActionableResult("session-1", result.RequestID); ok {
		t.Fatal("result became actionable before terminal observation")
	}
	tracker.MarkTerminal(result.RequestID, AssistedConclusionQueuedTerminal{
		OwnerID: "session-1", ProviderSessionID: "provider-1", ProviderTurnID: "control-1",
	})
	if got, ok := tracker.TakeActionableResult("session-1", result.RequestID); !ok || got.RequestID != result.RequestID || got.SessionID != result.SessionID || got.ProviderTurnID != result.ProviderTurnID {
		t.Fatalf("canonical terminal did not release result: %#v, %v", got, ok)
	}

	tracker.QueueFailure("request-2", AssistedConclusionQueuedFailure{
		OwnerID: "task-1", ProviderSessionID: "provider-2", ProviderTurnID: "control-2", Code: "semantic_conclusion_invalid_result",
	})
	if _, ok := tracker.TakeActionableFailure("task-1", "request-2"); ok {
		t.Fatal("ordinary failure became actionable before terminal observation")
	}
	tracker.MarkTerminal("request-2", AssistedConclusionQueuedTerminal{
		OwnerID: "task-1", ProviderSessionID: "provider-2", ProviderTurnID: "control-2",
	})
	if failure, ok := tracker.TakeActionableFailure("task-1", "request-2"); !ok || failure.Code != "semantic_conclusion_invalid_result" {
		t.Fatalf("canonical terminal did not release failure: %#v, %v", failure, ok)
	}
}

func TestAssistedConclusionTrackerKeysCallbackStateByOwnerAndRequest(t *testing.T) {
	tracker := NewAssistedConclusionTracker()
	requestID := "colliding-request"
	tracker.QueueResult("task-1", ProviderSessionAttemptResult{RequestID: requestID, SessionID: "provider-task", ProviderTurnID: "turn-task"})
	tracker.QueueResult("session-1", ProviderSessionAttemptResult{RequestID: requestID, SessionID: "provider-session", ProviderTurnID: "turn-session"})
	tracker.MarkTerminal(requestID, AssistedConclusionQueuedTerminal{OwnerID: "task-1", ProviderSessionID: "provider-task", ProviderTurnID: "turn-task"})
	tracker.MarkTerminal(requestID, AssistedConclusionQueuedTerminal{OwnerID: "session-1", ProviderSessionID: "provider-session", ProviderTurnID: "turn-session"})

	if result, ok := tracker.TakeActionableResult("task-1", requestID); !ok || result.SessionID != "provider-task" {
		t.Fatalf("Task callback collision displaced owner-local result: %#v, %v", result, ok)
	}
	if result, ok := tracker.TakeActionableResult("session-1", requestID); !ok || result.SessionID != "provider-session" {
		t.Fatalf("Session callback collision displaced owner-local result: %#v, %v", result, ok)
	}

	tracker.QueueFailure(requestID, AssistedConclusionQueuedFailure{OwnerID: "task-1", ProviderSessionID: "provider-task", ProviderTurnID: "turn-task", Code: "semantic_conclusion_invalid_result"})
	tracker.QueueFailure(requestID, AssistedConclusionQueuedFailure{OwnerID: "session-1", ProviderSessionID: "provider-session", ProviderTurnID: "turn-session", Code: "semantic_conclusion_invalid_result"})
	tracker.ClearRequest("task-1", requestID)
	if tracker.HasRequest("task-1", requestID) {
		t.Fatal("clearing Task callback state retained the Task request")
	}
	if !tracker.HasRequest("session-1", requestID) {
		t.Fatal("clearing Task callback state removed the Session request")
	}
}
