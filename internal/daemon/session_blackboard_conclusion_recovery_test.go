package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"pentest/internal/session"
)

func TestAssistedSessionRestartReplaysAPreSendReceiptWithProvenRuntimeOwnership(t *testing.T) {
	provider := newConclusionRecoverySession()
	factory := &conclusionRecoveryFactory{session: provider, liveness: ProviderSessionRecoveryLive}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{
		Input: "recover Session conclusion", BlackboardConclusionMode: session.BlackboardConclusionModeAssisted,
	})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	continuation, err := server.sessions.CreateContinuation(found.ID, "profile-1", "codex", session.RunnerHost)
	if err != nil {
		t.Fatalf("create Session Continuation: %v", err)
	}
	continuation, err = server.sessions.UpdateContinuationRuntimeMetadata(continuation.ID, "", provider.SessionID(), "/sessions/source.jsonl")
	if err != nil {
		t.Fatalf("store native Session identity: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusRunning); err != nil {
		t.Fatalf("mark Session Continuation running: %v", err)
	}
	receipt, inserted, err := server.sessions.RecordBlackboardConclusionCheckpoint(
		found.ID, continuation.ID, "work-request", provider.SessionID(), "work-turn",
		session.RuntimeTurnSelection{ModelProviderID: "model-provider", Model: "model"}, session.SemanticDebtWatermarks{SourceWork: 1},
	)
	if err != nil || !inserted {
		t.Fatalf("record Session conclusion checkpoint: %#v inserted=%v err=%v", receipt, inserted, err)
	}

	report := server.recoverSessionBlackboardConclusionReceipts(context.Background())
	server.providerControlWG.Wait()
	if len(report.LiveOwnerIDs) != 1 || report.LiveOwnerIDs[0] != found.ID {
		t.Fatalf("live recovered Session IDs = %#v", report.LiveOwnerIDs)
	}
	recovered, err := server.sessions.LatestBlackboardConclusion(found.ID)
	if err != nil || recovered == nil || recovered.InternalState != session.BlackboardConclusionReceiptAwaitingResult {
		t.Fatalf("recovered Session receipt = %#v, err=%v", recovered, err)
	}
	requests := provider.LastRequests()
	if len(requests) != 1 || requests[0].RequestID != recovered.DispatchRequestID {
		t.Fatalf("recovered Session provider requests = %#v", requests)
	}
}
