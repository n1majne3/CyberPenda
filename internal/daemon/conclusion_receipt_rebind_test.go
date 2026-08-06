package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/task"
)

// #197: the steer path's assisted-conclusion receipt rebind helper must move an
// in-flight Receipt to the replacement Continuation + live provider session so
// a later retry delivers its control turn instead of looping on the dead
// pre-steer session. This exercises the daemon wrapper used by both steer call
// sites (advanceNativeSteerContinuation and createWritableContinuationForLiveSession).

func newAssistedConclusionRebindServer(t *testing.T) (*Server, task.Task, task.TaskContinuation) {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectRecord, err := server.projects.Create("Rebind", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "rebind assisted conclusion", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	return server, created, continuation
}

func TestRebindInFlightAssistedConclusionReceiptsRepointsToReplacement(t *testing.T) {
	server, created, original := newAssistedConclusionRebindServer(t)

	// Simulate the stuck-live-task condition: an assisted-conclusion Receipt in
	// action_required against the original continuation + session.
	receipt, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-request-1", "session-original", "turn-1",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 3, SemanticPersistence: 1},
	)
	if err != nil {
		t.Fatalf("record checkpoint: %v", err)
	}
	if _, _, err := server.tasks.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(receipt.ID, time.Now().UTC(), 0); err != nil {
		t.Fatalf("mark recovery required: %v", err)
	}

	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	const replacementSessionID = "session-replacement"

	// This is the wrapper invoked by both steer call sites.
	server.rebindInFlightAssistedConclusionReceipts(created.ID, original.ID, replacement.ID, replacementSessionID)

	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest receipt after rebind: %v %#v", err, latest)
	}
	if latest.ID != receipt.ID {
		t.Fatalf("rebind changed the receipt identity: %s != %s", latest.ID, receipt.ID)
	}
	if latest.ContinuationID != replacement.ID {
		t.Fatalf("receipt continuation_id not rebound: %s, want %s", latest.ContinuationID, replacement.ID)
	}
	if latest.SourceSessionID != replacementSessionID {
		t.Fatalf("receipt source_session_id not rebound: %s, want %s", latest.SourceSessionID, replacementSessionID)
	}
	if latest.InternalState != task.BlackboardConclusionReceiptActionRequired {
		t.Fatalf("rebind changed the in-flight state: %s", latest.InternalState)
	}
}

func TestRebindInFlightAssistedConclusionReceiptsLeavesTerminalReceiptAlone(t *testing.T) {
	server, created, original := newAssistedConclusionRebindServer(t)

	// A clean Receipt is terminal; it must not be moved by the rebind.
	if _, _, err := server.tasks.RecordBlackboardConclusionCheckpoint(
		created.ID, original.ID, "work-clean", "session-original", "turn-clean",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1, SemanticPersistence: 1},
	); err != nil {
		t.Fatalf("record clean checkpoint: %v", err)
	}
	replacement, err := server.tasks.CreateReplacementContinuation(original)
	if err != nil {
		t.Fatal(err)
	}
	server.rebindInFlightAssistedConclusionReceipts(created.ID, original.ID, replacement.ID, "session-replacement")

	latest, err := server.tasks.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest receipt: %v %#v", err, latest)
	}
	if latest.ContinuationID != original.ID {
		t.Fatalf("terminal clean Receipt was moved: continuation_id=%s, want %s", latest.ContinuationID, original.ID)
	}
}
