package session

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pentest/internal/store"
)

func TestSessionBlackboardConclusionRestartRecoveryAndRetryAreOwnerScoped(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	service := NewService(db, filepath.Join(root, "sessions"))
	first, err := service.Create(CreateRequest{Input: "First Session", BlackboardConclusionMode: BlackboardConclusionModeAssisted})
	if err != nil {
		t.Fatalf("create first Session: %v", err)
	}
	second, err := service.Create(CreateRequest{Input: "Second Session", BlackboardConclusionMode: BlackboardConclusionModeAssisted})
	if err != nil {
		t.Fatalf("create second Session: %v", err)
	}
	continuation, err := service.CreateContinuation(first.ID, "profile-1", "codex", RunnerSandbox)
	if err != nil {
		t.Fatalf("create Session continuation: %v", err)
	}
	otherContinuation, err := service.CreateContinuation(second.ID, "profile-1", "codex", RunnerSandbox)
	if err != nil {
		t.Fatalf("create other Session continuation: %v", err)
	}

	receipt, inserted, err := service.RecordBlackboardConclusionCheckpoint(
		first.ID, continuation.ID, "work-request-1", "provider-session-1", "work-turn-1",
		RuntimeTurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		SemanticDebtWatermarks{SourceWork: 1, SemanticPersistence: 0},
	)
	if err != nil || !inserted {
		t.Fatalf("record Session checkpoint = %#v inserted=%v err=%v", receipt, inserted, err)
	}
	if _, _, err := service.RecordBlackboardConclusionCheckpoint(
		first.ID, otherContinuation.ID, "wrong-owner-request", "provider-session-2", "wrong-owner-turn",
		RuntimeTurnSelection{ModelProviderID: "provider-2", Model: "model-2"},
		SemanticDebtWatermarks{SourceWork: 1, SemanticPersistence: 0},
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-Session continuation checkpoint error = %v, want ErrNotFound", err)
	}

	recovered, err := service.ReconcileStrandedBlackboardConclusionRecoveries(time.Now().UTC(), 0)
	if err != nil || len(recovered) != 1 || recovered[0].ID != receipt.ID {
		t.Fatalf("restart recovery = %#v, err=%v", recovered, err)
	}
	latest, err := service.LatestBlackboardConclusion(first.ID)
	if err != nil || latest == nil || latest.InternalState != BlackboardConclusionReceiptActionRequired || latest.ErrorCode != BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("recovered Session conclusion = %#v, err=%v", latest, err)
	}

	retryAt := latest.NextEligibleAt.Add(time.Nanosecond)
	retried, won, err := service.RetryLatestBlackboardConclusion(first.ID, "session-retry-1", retryAt)
	if err != nil || !won || retried.InternalState != BlackboardConclusionReceiptPending {
		t.Fatalf("Session retry = %#v won=%v err=%v", retried, won, err)
	}
	replayed, won, err := service.RetryLatestBlackboardConclusion(first.ID, "session-retry-1", retryAt)
	if err != nil || won || replayed.ID != receipt.ID || replayed.ExplicitRetryCount != 1 {
		t.Fatalf("Session retry replay = %#v won=%v err=%v", replayed, won, err)
	}
	if _, err := service.LatestBlackboardConclusion(second.ID); err != nil {
		t.Fatalf("read unrelated Session conclusion: %v", err)
	}
}

func TestSessionBlackboardConclusionRecoveryReplayIsCompareAndSet(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	service := NewService(db, filepath.Join(root, "sessions"))
	found, err := service.Create(CreateRequest{Input: "Recovery replay", BlackboardConclusionMode: BlackboardConclusionModeAssisted})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	continuation, err := service.CreateContinuation(found.ID, "profile-1", "codex", RunnerSandbox)
	if err != nil {
		t.Fatalf("create Session continuation: %v", err)
	}
	receipt, inserted, err := service.RecordBlackboardConclusionCheckpoint(
		found.ID, continuation.ID, "work-request-replay", "provider-session", "work-turn-replay",
		RuntimeTurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		SemanticDebtWatermarks{SourceWork: 1, SemanticPersistence: 0},
	)
	if err != nil || !inserted {
		t.Fatalf("record pending receipt = %#v inserted=%v err=%v", receipt, inserted, err)
	}

	services := []*Service{service, NewService(db, filepath.Join(root, "sessions"))}
	start := make(chan struct{})
	results := make(chan struct {
		changed bool
		err     error
	}, len(services))
	var wg sync.WaitGroup
	for _, current := range services {
		wg.Add(1)
		go func(current *Service) {
			defer wg.Done()
			<-start
			_, changed, callErr := current.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(receipt.ID, ConclusionRecoveryDispatchFailed, time.Now().UTC(), 0)
			results <- struct {
				changed bool
				err     error
			}{changed: changed, err: callErr}
		}(current)
	}
	close(start)
	wg.Wait()
	close(results)

	changedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent recovery replay error: %v", result.err)
		}
		if result.changed {
			changedCount++
		}
	}
	if changedCount != 1 {
		t.Fatalf("concurrent recovery changed %d times, want exactly once", changedCount)
	}
	events, err := service.Events(found.ID)
	if err != nil {
		t.Fatalf("read recovery events: %v", err)
	}
	actionRequiredEvents := 0
	for _, event := range events {
		if event.Kind == EventKindBlackboardConclusion && event.Payload["phase"] == "action_required" {
			actionRequiredEvents++
		}
	}
	if actionRequiredEvents != 1 {
		t.Fatalf("recovery appended %d action-required events, want exactly one", actionRequiredEvents)
	}
}

// #203 / ADR 0021: the Session layer mirrors the Task obligation/dispatch
// split — recovery creates a NEW immutable dispatch bound to the replacement
// continuation + session, and a late result from the superseded dispatch fails
// closed without corrupting the obligation.
func TestSessionConclusionRecoveryCreatesNewDispatchAndRejectsLateResult(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	service := NewService(db, filepath.Join(root, "sessions"))
	found, err := service.Create(CreateRequest{Input: "Recovery dispatch", BlackboardConclusionMode: BlackboardConclusionModeAssisted})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	continuation, err := service.CreateContinuation(found.ID, "profile-1", "codex", RunnerSandbox)
	if err != nil {
		t.Fatalf("create Session continuation: %v", err)
	}
	obligation, inserted, err := service.RecordBlackboardConclusionCheckpoint(
		found.ID, continuation.ID, "work-request-recovery", "provider-session-1", "work-turn-recovery",
		RuntimeTurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		SemanticDebtWatermarks{SourceWork: 1, SemanticPersistence: 0},
	)
	if err != nil || !inserted {
		t.Fatalf("record Session checkpoint = %#v inserted=%v err=%v", obligation, inserted, err)
	}
	initial, won, err := service.ClaimBlackboardConclusionDispatch(obligation.ID, 0)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch = %#v won=%v err=%v", initial, won, err)
	}
	if initial.ContinuationID != continuation.ID || initial.SourceSessionID != "provider-session-1" {
		t.Fatalf("initial dispatch binding = (%s, %s)", initial.ContinuationID, initial.SourceSessionID)
	}

	replacement, err := service.CreateReplacementContinuation(continuation, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovered, won, err := service.CreateRecoveryConclusionDispatch(obligation.ID, replacement.ID, "provider-session-replacement", time.Now().UTC())
	if err != nil || !won {
		t.Fatalf("create recovery dispatch = %#v won=%v err=%v", recovered, won, err)
	}
	if recovered.ID != obligation.ID || recovered.ContinuationID != replacement.ID ||
		recovered.SourceSessionID != "provider-session-replacement" ||
		recovered.DispatchRequestID == initial.DispatchRequestID {
		t.Fatalf("recovery dispatch = %#v", recovered)
	}

	// A late result for the old dispatch fails closed and is recorded as a late
	// terminal delivery outcome; the obligation stays dispatch_requested on the
	// new dispatch.
	if _, err := service.BlackboardConclusionByDispatchRequestID(initial.DispatchRequestID); !errors.Is(err, ErrBlackboardConclusionDispatchInactive) {
		t.Fatalf("late Session result resolved active dispatch: %v", err)
	}
	latest, err := service.LatestBlackboardConclusion(found.ID)
	if err != nil || latest == nil || latest.InternalState != BlackboardConclusionReceiptDispatchRequested ||
		latest.DispatchRequestID != recovered.DispatchRequestID {
		t.Fatalf("Session obligation after late result = %#v err=%v", latest, err)
	}
	history, err := service.ConclusionDispatches(obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("Session dispatch history = %d rows, want 2", len(history))
	}
	for i := range history {
		if history[i].DispatchRequestID == initial.DispatchRequestID {
			if history[i].DeliveryState != ConclusionDispatchLateTerminal || history[i].TerminalOutcome != "late_result" {
				t.Fatalf("Session late result not recorded durably: %#v", history[i])
			}
		}
	}
}
