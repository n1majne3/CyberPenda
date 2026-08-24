package task_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

type recordingTerminalMarker struct {
	continuationIDs []string
}

func (m *recordingTerminalMarker) MarkContinuationTerminal(_ context.Context, continuationID string) error {
	m.continuationIDs = append(m.continuationIDs, continuationID)
	return nil
}

type restartOrderingReconciler struct {
	tasks        *task.Service
	reason       string
	eventVisible bool
}

func (r *restartOrderingReconciler) ReconcileTerminalContinuation(_ context.Context, continuationID, reason string) error {
	r.reason = reason
	continuation, err := r.tasks.Continuation(continuationID)
	if err != nil {
		return err
	}
	events, err := r.tasks.Events(continuation.TaskID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.ContinuationID == continuationID && event.Kind == task.EventKindLifecycle &&
			event.Payload["phase"] == "interrupted" && event.Payload["reason"] == "daemon_restart" {
			r.eventVisible = true
		}
	}
	return nil
}

func newStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return db
}

// TestCreateCapturesGoalRunControlsAndScopeSnapshot proves the tracer bullet:
// launching a task captures the goal, run controls, the runtime profile id, the
// selected runner, and an immutable snapshot of the project scope at launch.
func TestCreateCapturesGoalRunControlsAndScopeSnapshot(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	projectID, err := projects.Create(
		"Acme",
		"",
		project.Scope{Domains: []string{"example.com"}, Notes: "live scope"},
		project.Defaults{},
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	created, err := svc.Create(task.CreateRequest{
		ProjectID: projectID.ID,

		Type: task.TypePentest, Goal: "Enumerate example.com subdomains",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxNetwork: "host_proxy_only",
			Notes:          "business hours only",
			Extras:         map[string]string{"depth": "shallow"},
			Policy: task.TaskPolicy{
				MaxAttempts:            9,
				MaxWrongSubmissions:    4,
				MaxWallTimeSeconds:     3600,
				MaxConsecutiveFailures: 3,
				MaxRatingDrawdown:      40,
				MaxNoProgressSeconds:   900,
			},
		},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	if created.ID == "" {
		t.Fatal("expected task id")
	}
	if created.Goal != "Enumerate example.com subdomains" {
		t.Fatalf("expected goal, got %q", created.Goal)
	}
	if created.RuntimeProfileID != "profile-1" {
		t.Fatalf("expected runtime profile id, got %q", created.RuntimeProfileID)
	}
	if created.Runner != task.RunnerSandbox {
		t.Fatalf("expected sandbox runner, got %q", created.Runner)
	}
	if created.Type != task.TypePentest {
		t.Fatalf("expected Pentest Task Type snapshot, got %q", created.Type)
	}
	if created.RunControls.Notes != "business hours only" {
		t.Fatalf("expected run-control notes, got %q", created.RunControls.Notes)
	}
	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if fetched.RunControls.SandboxNetwork != "host_proxy_only" {
		t.Fatalf("expected persisted sandbox network, got %q", fetched.RunControls.SandboxNetwork)
	}
	if fetched.RunControls.SandboxVPNTun {
		t.Fatal("expected sandbox VPN TUN off by default")
	}
	if fetched.RunControls.Policy.MaxWrongSubmissions != 4 || fetched.RunControls.Policy.MaxRatingDrawdown != 40 {
		t.Fatalf("expected immutable task policy snapshot, got %#v", fetched.RunControls.Policy)
	}
	// Scope snapshot is an immutable copy captured at launch.
	if got := created.ScopeSnapshot.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope snapshot domain, got %#v", got)
	}
	if created.ScopeSnapshot.Notes != "live scope" {
		t.Fatalf("expected scope snapshot notes, got %q", created.ScopeSnapshot.Notes)
	}
	if created.Status != task.StatusPending {
		t.Fatalf("expected initial status pending, got %q", created.Status)
	}
}

func TestCreateCapturesExplicitCTFTaskTypeAndRejectsProjectKindMismatch(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	ctfProject, err := projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Challenge Project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID: ctfProject.ID,
		Type:      task.TypeCTFChallenge,
		Goal:      "Solve the selected challenge",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create CTF Challenge Task: %v", err)
	}
	if created.Type != task.TypeCTFChallenge {
		t.Fatalf("expected CTF Challenge Task Type snapshot, got %q", created.Type)
	}
	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get CTF Challenge Task: %v", err)
	}
	if fetched.Type != task.TypeCTFChallenge {
		t.Fatalf("expected stored CTF Challenge Task Type snapshot, got %q", fetched.Type)
	}

	_, err = svc.Create(task.CreateRequest{
		ProjectID: ctfProject.ID,
		Type:      task.TypePentest,
		Goal:      "Run a Pentest Task",
		Runner:    task.RunnerSandbox,
	})
	if !errors.Is(err, task.ErrTaskTypeProjectKindMismatch) {
		t.Fatalf("expected Task Type and Project Kind mismatch, got %v", err)
	}
}

func TestCreateRejectsMissingTaskType(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	createdProject, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	_, err = svc.Create(task.CreateRequest{
		ProjectID: createdProject.ID,
		Goal:      "Inspect the target",
		Runner:    task.RunnerSandbox,
	})
	if !errors.Is(err, task.ErrInvalidTaskType) {
		t.Fatalf("expected explicit Task Type requirement, got %v", err)
	}
}

func TestCreateRejectsInvalidTaskPolicy(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err = svc.Create(task.CreateRequest{
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "solve challenges",
		Runner: task.RunnerSandbox,
		RunControls: task.RunControls{Policy: task.TaskPolicy{
			MaxAttempts: -1,
		}},
	})
	if !errors.Is(err, task.ErrInvalidTaskPolicy) {
		t.Fatalf("expected invalid task policy, got %v", err)
	}
}

func TestCreateRejectsMissingGoal(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "", Runner: task.RunnerSandbox})
	if !errors.Is(err, task.ErrMissingGoal) {
		t.Fatalf("expected ErrMissingGoal, got %v", err)
	}
}

func TestCreateRejectsUnsupportedRunner(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "do something", Runner: "kali-magic"})
	if !errors.Is(err, task.ErrUnsupportedRunner) {
		t.Fatalf("expected ErrUnsupportedRunner, got %v", err)
	}
}

func TestCreateDefaultsAndPersistsBlackboardConclusionMode(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	interactive, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if interactive.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeInteractive ||
		interactive.BlackboardConclusion.Mode != task.BlackboardConclusionModeInteractive ||
		interactive.BlackboardConclusion.State != task.BlackboardConclusionStateClean {
		t.Fatalf("interactive Task = %#v", interactive)
	}
	fetched, err := svc.Get(interactive.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeInteractive {
		t.Fatalf("persisted mode = %q", fetched.RunControls.BlackboardConclusionMode)
	}

	assisted, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect with help", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	if assisted.RunControls.BlackboardConclusionMode != task.BlackboardConclusionModeAssisted || assisted.BlackboardConclusion.Mode != task.BlackboardConclusionModeAssisted {
		t.Fatalf("assisted Task = %#v", assisted)
	}
}

func TestCreateRejectsInvalidBlackboardConclusionMode(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: "automatic"},
	})
	if !errors.Is(err, task.ErrInvalidBlackboardConclusionMode) {
		t.Fatalf("error = %v", err)
	}
}

func TestRecordBlackboardConclusionCheckpointPersistsPendingDebtIdempotently(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}

	selection := task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1", ReasoningEffort: "high"}
	first, inserted, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-1", "session-1", "turn-1", selection, task.SemanticDebtWatermarks{SourceWork: 3, SemanticPersistence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !inserted || first.ID == "" || first.InternalState != task.BlackboardConclusionReceiptPending ||
		first.SourceWorkWatermark != 3 || first.SemanticPersistenceWatermark != 1 || first.SourceSelection != selection {
		t.Fatalf("first receipt = %#v, inserted=%v", first, inserted)
	}
	replayed, inserted, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-1", "session-1", "turn-1", task.TurnSelection{ModelProviderID: "provider-drift", Model: "model-drift"}, task.SemanticDebtWatermarks{SourceWork: 9, SemanticPersistence: 8})
	if err != nil {
		t.Fatal(err)
	}
	if inserted || replayed.ID != first.ID || replayed.SourceWorkWatermark != 3 || replayed.SemanticPersistenceWatermark != 1 {
		t.Fatalf("replayed receipt = %#v, inserted=%v", replayed, inserted)
	}
	latest, err := svc.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil || latest.ID != first.ID {
		t.Fatalf("latest receipt = %#v, err=%v", latest, err)
	}

	events, err := svc.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var conclusions []task.Event
	for _, event := range events {
		if event.Kind == task.EventKindBlackboardConclusion {
			conclusions = append(conclusions, event)
		}
	}
	if len(conclusions) != 1 || conclusions[0].ContinuationID != continuation.ID ||
		conclusions[0].Payload["phase"] != "pending_detected" || conclusions[0].Payload["receipt_id"] != first.ID ||
		conclusions[0].Payload["source_turn_id"] != "turn-1" || conclusions[0].Payload["source_work_watermark"] != float64(3) ||
		conclusions[0].Payload["semantic_persistence_watermark"] != float64(1) {
		t.Fatalf("conclusion events = %#v", conclusions)
	}
	if _, present := conclusions[0].Payload["terminal_tool_result_count"]; present {
		t.Fatalf("legacy tool-result count leaked into conclusion event: %#v", conclusions[0].Payload)
	}
}

func TestRecordBlackboardConclusionCheckpointRejectsContinuationOwnedByAnotherTask(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	newAssistedTask := func(goal string) task.Task {
		t.Helper()
		created, err := svc.Create(task.CreateRequest{
			ProjectID: proj.ID,
			Type:      task.TypePentest, Goal: goal, Runner: task.RunnerSandbox,
			RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
		})
		if err != nil {
			t.Fatalf("create Task: %v", err)
		}
		return created
	}
	first := newAssistedTask("first")
	second := newAssistedTask("second")
	secondContinuation, err := svc.CreateContinuation(second.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create second Task Continuation: %v", err)
	}

	_, inserted, err := svc.RecordBlackboardConclusionCheckpoint(
		first.ID, secondContinuation.ID, "work-request-1", "session-1", "turn-1",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 2, SemanticPersistence: 1},
	)
	if !errors.Is(err, task.ErrNotFound) || inserted {
		t.Fatalf("cross-Task checkpoint inserted=%v, error=%v", inserted, err)
	}
	latest, err := svc.LatestBlackboardConclusion(first.ID)
	if err != nil || latest != nil {
		t.Fatalf("first Task conclusion = %#v, error=%v", latest, err)
	}
}

// #203 / ADR 0021: after an interrupt_then_replace native steer creates a
// replacement Continuation, an in-flight assisted-conclusion obligation gets a
// NEW immutable Conclusion Dispatch bound to the replacement (continuation_id +
// source_session_id) instead of rebinding the old dispatch. The historical
// dispatch identity is never rewritten, and a retry delivers the control turn
// against the live replacement session.
func TestRecoveryDispatchIsCreatedInsteadOfRebindingInFlightObligation(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "recover receipt", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldContinuation, err := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	const oldSession = "session-old"
	selection := task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}
	obligation, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, oldContinuation.ID, "work-request-1", oldSession, "turn-1", selection, task.SemanticDebtWatermarks{SourceWork: 3, SemanticPersistence: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Claim the initial dispatch so the obligation is in flight with a real
	// immutable binding to the old continuation + session.
	claimed, won, err := svc.ClaimBlackboardConclusionDispatch(obligation.ID, 7)
	if err != nil || !won {
		t.Fatalf("claim initial dispatch: %#v won=%v err=%v", claimed, won, err)
	}
	if claimed.ContinuationID != oldContinuation.ID || claimed.SourceSessionID != oldSession {
		t.Fatalf("initial dispatch binding = (%s, %s), want (%s, %s)", claimed.ContinuationID, claimed.SourceSessionID, oldContinuation.ID, oldSession)
	}

	// The steer creates a replacement Continuation carrying a NEW live session.
	replacement, err := svc.CreateReplacementContinuation(oldContinuation)
	if err != nil {
		t.Fatal(err)
	}
	const replacementSession = "session-replacement"
	if _, err := svc.UpdateContinuationRuntimeMetadata(replacement.ID, "", replacementSession, ""); err != nil {
		t.Fatal(err)
	}

	dispatches, err := svc.CreateRecoveryConclusionDispatches(created.ID, oldContinuation.ID, replacement.ID, replacementSession)
	if err != nil {
		t.Fatalf("create recovery dispatch: %v", err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("recovery created %d dispatches, want 1", len(dispatches))
	}
	recovered := dispatches[0]
	if recovered.ID != obligation.ID {
		t.Fatalf("recovery changed the obligation identity: %s != %s", recovered.ID, obligation.ID)
	}
	if recovered.ContinuationID != replacement.ID {
		t.Fatalf("recovery dispatch continuation_id = %s, want %s", recovered.ContinuationID, replacement.ID)
	}
	if recovered.SourceSessionID != replacementSession {
		t.Fatalf("recovery dispatch source_session_id = %s, want %s", recovered.SourceSessionID, replacementSession)
	}
	if recovered.DispatchRequestID == claimed.DispatchRequestID {
		t.Fatalf("recovery reused the old dispatch request id %s", recovered.DispatchRequestID)
	}

	// The old dispatch is immutable history: it keeps its original binding and
	// request id, and is terminal superseded.
	history, err := svc.ConclusionDispatches(obligation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("dispatch history = %d rows, want 2 (initial + recovery)", len(history))
	}
	var oldDispatch, recoveryDispatch *task.ConclusionDispatch
	for i := range history {
		if history[i].DispatchRequestID == claimed.DispatchRequestID {
			oldDispatch = &history[i]
		}
		if history[i].DispatchRequestID == recovered.DispatchRequestID {
			recoveryDispatch = &history[i]
		}
	}
	if oldDispatch == nil || recoveryDispatch == nil {
		t.Fatalf("dispatch history missing expected attempts: %#v", history)
	}
	if oldDispatch.ContinuationID != oldContinuation.ID || oldDispatch.SourceSessionID != oldSession {
		t.Fatalf("old dispatch binding was rewritten: (%s, %s)", oldDispatch.ContinuationID, oldDispatch.SourceSessionID)
	}
	if oldDispatch.DeliveryState != task.ConclusionDispatchSuperseded || oldDispatch.TerminalOutcome != "superseded_by_recovery" {
		t.Fatalf("old dispatch terminal state = %s/%s", oldDispatch.DeliveryState, oldDispatch.TerminalOutcome)
	}
	if recoveryDispatch.DeliveryState != task.ConclusionDispatchRequested || recoveryDispatch.SendAttemptCount != 0 {
		t.Fatalf("recovery dispatch should be pre-send: %#v", recoveryDispatch)
	}
}

// #203 / ADR 0021: terminal obligations (clean / applied) never get a recovery
// dispatch — the conclusion already landed, so a fresh attempt would risk
// re-applying a stale batch.
func TestRecoveryDispatchLeavesTerminalObligationAlone(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "no recovery terminal", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldContinuation, err := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	// A clean obligation (watermarks within target) is terminal — it must not
	// receive a recovery dispatch.
	if _, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, oldContinuation.ID, "work-clean", "session-old", "turn-clean", task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1, SemanticPersistence: 1}); err != nil {
		t.Fatal(err)
	}
	replacement, err := svc.CreateReplacementContinuation(oldContinuation)
	if err != nil {
		t.Fatal(err)
	}
	dispatches, err := svc.CreateRecoveryConclusionDispatches(created.ID, oldContinuation.ID, replacement.ID, "session-replacement")
	if err != nil {
		t.Fatalf("create recovery dispatch for terminal obligation: %v", err)
	}
	if len(dispatches) != 0 {
		t.Fatalf("terminal clean obligation received %d recovery dispatches", len(dispatches))
	}
	latest, err := svc.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest obligation: %v %#v", err, latest)
	}
	if latest.ContinuationID != oldContinuation.ID {
		t.Fatalf("terminal obligation continuation_id was moved: %s, want %s", latest.ContinuationID, oldContinuation.ID)
	}
}

func TestRecordBlackboardConclusionCheckpointRejectsInvalidWatermarks(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	selection := task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}

	for _, watermarks := range [][2]int{{-1, 0}, {2, 3}, {2, -1}} {
		_, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-invalid", "session-1", "turn-invalid", selection, task.SemanticDebtWatermarks{SourceWork: watermarks[0], SemanticPersistence: watermarks[1]})
		if !errors.Is(err, task.ErrInvalidBlackboardConclusionReceipt) {
			t.Fatalf("watermarks work=%d semantic=%d: error = %v", watermarks[0], watermarks[1], err)
		}
	}
}

func TestRecordBlackboardConclusionCheckpointPersistsCleanWatermarks(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	selection := task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1", ReasoningEffort: "high"}
	first, inserted, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-clean", "session-1", "turn-clean", selection, task.SemanticDebtWatermarks{SourceWork: 2, SemanticPersistence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !inserted || first.InternalState != task.BlackboardConclusionReceiptClean || first.SourceWorkWatermark != 2 || first.SemanticPersistenceWatermark != 2 {
		t.Fatalf("clean receipt = %#v, inserted=%v", first, inserted)
	}
	view := first.View(task.BlackboardConclusionModeAssisted)
	if view.State != task.BlackboardConclusionStateClean || view.SourceWorkWatermark != 2 || view.SemanticPersistenceWatermark != 2 {
		t.Fatalf("clean view = %#v", view)
	}
	replayed, inserted, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-clean", "session-1", "turn-clean", selection, task.SemanticDebtWatermarks{SourceWork: 5, SemanticPersistence: 5})
	if err != nil || inserted || replayed.ID != first.ID || replayed.SourceWorkWatermark != 2 || replayed.SemanticPersistenceWatermark != 2 {
		t.Fatalf("clean replay = %#v, inserted=%v, err=%v", replayed, inserted, err)
	}

	reopened := task.NewService(db, projects)
	latest, err := reopened.LatestBlackboardConclusion(created.ID)
	if err != nil || latest == nil || latest.InternalState != task.BlackboardConclusionReceiptClean || latest.SourceWorkWatermark != 2 || latest.SemanticPersistenceWatermark != 2 {
		t.Fatalf("reopened clean receipt = %#v, err=%v", latest, err)
	}
	events, err := reopened.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var phases []any
	for _, event := range events {
		if event.Kind == task.EventKindBlackboardConclusion {
			phases = append(phases, event.Payload["phase"])
		}
	}
	if !reflect.DeepEqual(phases, []any{"persistence_current"}) {
		t.Fatalf("clean phases = %#v", phases)
	}
}

func TestBlackboardConclusionReceiptAdvancesDurablyAndIdempotently(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	selection := task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1", ReasoningEffort: "high"}
	receipt, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-1", "session-1", "turn-1", selection, task.SemanticDebtWatermarks{SourceWork: 2})
	if err != nil {
		t.Fatal(err)
	}
	if view := receipt.View(task.BlackboardConclusionModeAssisted); view.State != task.BlackboardConclusionStatePending || view.SourceTurnID != "turn-1" || view.AppliedRevision != nil {
		t.Fatalf("pending view = %#v", view)
	}

	dispatched, won, err := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !won || dispatched.InternalState != task.BlackboardConclusionReceiptDispatchRequested ||
		dispatched.DispatchRequestID == "" || dispatched.ApplyIdempotencyKey == "" || dispatched.BaseRevision == nil || *dispatched.BaseRevision != 7 {
		t.Fatalf("dispatch claim = %#v, won=%v", dispatched, won)
	}
	if dispatched.SourceRequestID != "work-request-1" || !dispatched.SourceRequestCorrelationExact || dispatched.SendAttemptCount != 0 || dispatched.SendStartedAt != nil {
		t.Fatalf("dispatch lineage before send = %#v", dispatched)
	}
	sendStartedAt := time.Date(2026, 7, 27, 10, 11, 12, 0, time.UTC)
	sending, won, err := svc.MarkBlackboardConclusionSendStarted(dispatched.DispatchRequestID, sendStartedAt)
	if err != nil || !won || sending.SendAttemptCount != 1 || sending.SendStartedAt == nil || !sending.SendStartedAt.Equal(sendStartedAt) {
		t.Fatalf("send started = %#v, won=%v, err=%v", sending, won, err)
	}
	replayedSend, won, err := svc.MarkBlackboardConclusionSendStarted(dispatched.DispatchRequestID, sendStartedAt.Add(time.Hour))
	if err != nil || won || replayedSend.SendAttemptCount != 1 || replayedSend.SendStartedAt == nil || !replayedSend.SendStartedAt.Equal(sendStartedAt) {
		t.Fatalf("send replay = %#v, won=%v, err=%v", replayedSend, won, err)
	}
	replayedDispatch, won, err := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 7)
	if err != nil || won || replayedDispatch.DispatchRequestID != dispatched.DispatchRequestID || replayedDispatch.ApplyIdempotencyKey != dispatched.ApplyIdempotencyKey {
		t.Fatalf("dispatch replay = %#v, won=%v, err=%v", replayedDispatch, won, err)
	}

	awaiting, won, err := svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if !won || awaiting.InternalState != task.BlackboardConclusionReceiptAwaitingResult || awaiting.ControlTurnID != "control-turn-1" {
		t.Fatalf("awaiting = %#v, won=%v", awaiting, won)
	}
	if replay, replayWon, replayErr := svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-turn-1"); replayErr != nil || replayWon || replay.ID != receipt.ID {
		t.Fatalf("awaiting replay = %#v, won=%v, err=%v", replay, replayWon, replayErr)
	}

	lookedUp, err := svc.BlackboardConclusionByDispatchRequestID(dispatched.DispatchRequestID)
	if err != nil || lookedUp.ID != receipt.ID {
		t.Fatalf("lookup = %#v, err=%v", lookedUp, err)
	}
	canonical := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":7}`)
	validated, won, err := svc.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !won || validated.InternalState != task.BlackboardConclusionReceiptValidated || string(validated.CanonicalResultJSON) != string(canonical) || validated.CanonicalResultSHA256 == "" {
		t.Fatalf("validated = %#v, won=%v", validated, won)
	}
	if replay, replayWon, replayErr := svc.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, canonical); replayErr != nil || replayWon || replay.CanonicalResultSHA256 != validated.CanonicalResultSHA256 {
		t.Fatalf("validated replay = %#v, won=%v, err=%v", replay, replayWon, replayErr)
	}

	applied, won, err := svc.MarkBlackboardConclusionApplied(dispatched.DispatchRequestID, 11)
	if err != nil {
		t.Fatal(err)
	}
	if !won || applied.InternalState != task.BlackboardConclusionReceiptApplied || applied.AppliedRevision == nil || *applied.AppliedRevision != 11 {
		t.Fatalf("applied = %#v, won=%v", applied, won)
	}
	if view := applied.View(task.BlackboardConclusionModeAssisted); view.State != task.BlackboardConclusionStateClean || view.AppliedRevision == nil || *view.AppliedRevision != 11 {
		t.Fatalf("applied view = %#v", view)
	}
	if replay, replayWon, replayErr := svc.MarkBlackboardConclusionApplied(dispatched.DispatchRequestID, 11); replayErr != nil || replayWon || replay.ID != receipt.ID {
		t.Fatalf("applied replay = %#v, won=%v, err=%v", replay, replayWon, replayErr)
	}

	events, err := svc.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	phases := []string{}
	for _, event := range events {
		if event.Kind == task.EventKindBlackboardConclusion {
			phase, _ := event.Payload["phase"].(string)
			phases = append(phases, phase)
		}
	}
	wantPhases := []string{"pending_detected", "dispatch_requested", "awaiting_result", "result_validated", "applied"}
	if !reflect.DeepEqual(phases, wantPhases) {
		t.Fatalf("conclusion phases = %#v, want %#v", phases, wantPhases)
	}
}

func TestBlackboardConclusionInvalidResultRepairsOnceThenRequiresOperatorRetry(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-1", "session-1", "turn-1",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if initial.AutomaticTurnCount != 1 {
		t.Fatalf("initial automatic turns = %d", initial.AutomaticTurnCount)
	}
	if _, _, err = svc.MarkBlackboardConclusionAwaiting(initial.DispatchRequestID, "control-initial"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	detail := task.ConclusionValidationDetail{Reason: "invalid_key_format", FieldPath: "attempt.key", Expected: "the key must use the attempt: prefix"}
	repair, won, err := svc.HandleBlackboardConclusionFailure(initial.DispatchRequestID,
		task.BlackboardConclusionErrorInvalidResult, detail, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !won || repair.InternalState != task.BlackboardConclusionReceiptRepairDispatchRequested ||
		repair.AutomaticTurnCount != task.BlackboardConclusionAutomaticTurnLimit || repair.RepairCount != 1 ||
		repair.DispatchRequestID == initial.DispatchRequestID || repair.NextEligibleAt == nil || !repair.NextEligibleAt.Equal(now.Add(time.Minute)) ||
		repair.ValidationReason != "invalid_key_format" || repair.ValidationFieldPath != "attempt.key" || repair.ValidationExpected != "the key must use the attempt: prefix" {
		t.Fatalf("repair claim = %#v, won=%v", repair, won)
	}
	if _, _, replayErr := svc.HandleBlackboardConclusionFailure(initial.DispatchRequestID,
		task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, time.Minute); !errors.Is(replayErr, task.ErrNotFound) {
		t.Fatalf("stale failure did not fail closed: %v", replayErr)
	}
	if _, _, err = svc.MarkBlackboardConclusionAwaiting(repair.DispatchRequestID, "control-repair"); err != nil {
		t.Fatal(err)
	}
	action, changed, err := svc.HandleBlackboardConclusionFailure(repair.DispatchRequestID,
		task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{Reason: "invalid_key_format", FieldPath: "attempt.key", Expected: "the key must use the attempt: prefix"}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || action.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		action.ErrorCode != task.BlackboardConclusionErrorRepairExhausted || action.NextEligibleAt == nil ||
		!action.NextEligibleAt.Equal(now.Add(time.Minute)) ||
		action.ValidationReason != "invalid_key_format" || action.ValidationFieldPath != "attempt.key" {
		t.Fatalf("action required = %#v, changed=%v", action, changed)
	}
	view := action.ViewAt(task.BlackboardConclusionModeAssisted, now)
	if view.State != task.BlackboardConclusionStateActionRequired || view.ErrorCode != task.BlackboardConclusionErrorRepairExhausted || view.RetryAvailable ||
		view.ValidationReason != "invalid_key_format" || view.ValidationFieldPath != "attempt.key" || !strings.Contains(view.ValidationExpected, "attempt: prefix") {
		t.Fatalf("cooldown view = %#v", view)
	}
	if _, _, err := svc.RetryBlackboardConclusion(receipt.ID, "retry-key-1", now.Add(30*time.Second)); !errors.Is(err, task.ErrBlackboardConclusionRetryCooldown) {
		t.Fatalf("retry during cooldown = %v", err)
	}
	retry, won, err := svc.RetryBlackboardConclusion(receipt.ID, "retry-key-1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !won || retry.InternalState != task.BlackboardConclusionReceiptDispatchRequested || retry.ExplicitRetryCount != 1 ||
		retry.AutomaticTurnCount != task.BlackboardConclusionAutomaticTurnLimit || retry.DispatchRequestID == repair.DispatchRequestID {
		t.Fatalf("operator retry = %#v, won=%v", retry, won)
	}
	if replay, replayWon, replayErr := svc.RetryBlackboardConclusion(receipt.ID, "retry-key-1", now.Add(2*time.Minute)); replayErr != nil || replayWon || replay.DispatchRequestID != retry.DispatchRequestID {
		t.Fatalf("operator retry replay = %#v, won=%v, err=%v", replay, replayWon, replayErr)
	}
	if _, _, err = svc.MarkBlackboardConclusionAwaiting(retry.DispatchRequestID, "control-retry"); err != nil {
		t.Fatal(err)
	}
	validated, won, err := svc.MarkBlackboardConclusionValidated(retry.DispatchRequestID, []byte(`{"schema":"runtime-attempt-result/v1"}`))
	if err != nil || !won {
		t.Fatalf("newer result = %#v, won=%v, err=%v", validated, won, err)
	}
	if validated.ErrorCode != "" || validated.NextEligibleAt != nil {
		t.Fatalf("validated debt not cleared = %#v", validated)
	}
	if _, err := svc.BlackboardConclusionByDispatchRequestID(repair.DispatchRequestID); !errors.Is(err, task.ErrBlackboardConclusionDispatchInactive) {
		t.Fatalf("stale repair correlation remained live: %v", err)
	}
	if _, err := svc.BlackboardConclusionByDispatchRequestID(initial.DispatchRequestID); !errors.Is(err, task.ErrBlackboardConclusionDispatchInactive) {
		t.Fatalf("stale initial correlation remained live: %v", err)
	}
}

func TestBlackboardConclusionForbiddenControlToolUseRequiresAction(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-1", "session-1", "turn-1",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
	dispatched, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 0)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-1")
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	action, changed, err := svc.HandleBlackboardConclusionFailure(dispatched.DispatchRequestID,
		task.BlackboardConclusionErrorToolUseForbidden, task.ConclusionValidationDetail{}, now, 0)
	if err != nil || !changed || action.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		action.ErrorCode != task.BlackboardConclusionErrorToolUseForbidden || action.RepairCount != 0 {
		t.Fatalf("forbidden tool action = %#v, changed=%v, err=%v", action, changed, err)
	}
}

func TestBlackboardConclusionVersionConflictRegeneratesOnceThenRequiresAction(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	selection := task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1", ReasoningEffort: "high"}
	receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-1", "session-1", "turn-1",
		selection, task.SemanticDebtWatermarks{SourceWork: 1})
	initial, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 3)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(initial.DispatchRequestID, "control-initial")
	canonicalInitial := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":3}`)
	validated, _, _ := svc.MarkBlackboardConclusionValidated(initial.DispatchRequestID, canonicalInitial)
	syncRequested, won, err := svc.ClaimBlackboardConclusionVersionSync(validated.DispatchRequestID)
	if err != nil || !won || syncRequested.InternalState != task.BlackboardConclusionReceiptVersionSyncRequested ||
		syncRequested.DispatchRequestID != validated.DispatchRequestID ||
		string(syncRequested.CanonicalResultJSON) != string(canonicalInitial) ||
		syncRequested.ErrorCode != task.BlackboardConclusionErrorVersionConflict {
		t.Fatalf("claim version sync = %#v, won=%v, err=%v", syncRequested, won, err)
	}

	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	regeneration, changed, err := svc.HandleBlackboardConclusionVersionConflict(
		syncRequested.DispatchRequestID, 4, now, time.Minute)
	if err != nil || !changed {
		t.Fatalf("claim version regeneration = %#v, changed=%v, err=%v", regeneration, changed, err)
	}
	if regeneration.InternalState != task.BlackboardConclusionReceiptVersionRegenerationDispatchRequested ||
		regeneration.AutomaticTurnCount != task.BlackboardConclusionAutomaticTurnLimit || regeneration.VersionRegenerationCount != 1 ||
		regeneration.BaseRevision == nil || *regeneration.BaseRevision != 4 ||
		regeneration.DispatchRequestID == initial.DispatchRequestID || regeneration.ControlTurnID != "" ||
		len(regeneration.CanonicalResultJSON) != 0 || regeneration.CanonicalResultSHA256 != "" ||
		regeneration.ApplyIdempotencyKey != initial.ApplyIdempotencyKey || regeneration.ContinuationID != continuation.ID ||
		regeneration.SourceSelection != selection || regeneration.ErrorCode != task.BlackboardConclusionErrorVersionConflict ||
		regeneration.NextEligibleAt == nil || !regeneration.NextEligibleAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("version regeneration lost lineage or retained stale result: %#v", regeneration)
	}
	if _, err := svc.BlackboardConclusionByDispatchRequestID(initial.DispatchRequestID); !errors.Is(err, task.ErrBlackboardConclusionDispatchInactive) {
		t.Fatalf("stale version correlation remained live: %v", err)
	}
	if _, _, err := svc.MarkBlackboardConclusionAwaiting(regeneration.DispatchRequestID, "control-regenerated"); err != nil {
		t.Fatal(err)
	}
	canonicalRegenerated := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":4}`)
	regenerated, won, err := svc.MarkBlackboardConclusionValidated(regeneration.DispatchRequestID, canonicalRegenerated)
	if err != nil || !won || regenerated.VersionRegenerationCount != 1 || regenerated.ErrorCode != "" {
		t.Fatalf("validate regenerated result = %#v, won=%v, err=%v", regenerated, won, err)
	}
	regeneratedSync, _, _ := svc.ClaimBlackboardConclusionVersionSync(regenerated.DispatchRequestID)
	action, changed, err := svc.HandleBlackboardConclusionVersionConflict(
		regeneratedSync.DispatchRequestID, 5, now.Add(time.Minute), time.Minute)
	if err != nil || !changed || action.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		action.ErrorCode != task.BlackboardConclusionErrorVersionConflict || action.BaseRevision == nil || *action.BaseRevision != 5 ||
		action.VersionRegenerationCount != 1 || len(action.CanonicalResultJSON) != 0 || action.CanonicalResultSHA256 != "" ||
		action.NextEligibleAt == nil || !action.NextEligibleAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("second version conflict = %#v, changed=%v, err=%v", action, changed, err)
	}
	view := action.ViewAt(task.BlackboardConclusionModeAssisted, now.Add(time.Minute))
	if view.State != task.BlackboardConclusionStateActionRequired ||
		view.ErrorCode != task.BlackboardConclusionErrorVersionConflict || view.RetryAvailable {
		t.Fatalf("version conflict action view = %#v", view)
	}
}

func TestBlackboardConclusionVersionRegenerationFailsClosed(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	makeValidated := func(turn string, base int) task.BlackboardConclusionReceipt {
		receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-"+turn, "session-1", turn,
			task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
		dispatched, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, base)
		_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-"+turn)
		validated, _, _ := svc.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, []byte(`{"schema":"runtime-attempt-result/v1"}`))
		return validated
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	validated := makeValidated("same-revision", 7)
	if _, _, err := svc.HandleBlackboardConclusionVersionConflict(validated.DispatchRequestID, 8, now, 0); !errors.Is(err, task.ErrInvalidBlackboardConclusionReceipt) {
		t.Fatalf("version regeneration without durable sync intent: %v", err)
	}
	validatedSync, _, _ := svc.ClaimBlackboardConclusionVersionSync(validated.DispatchRequestID)
	if _, _, err := svc.HandleBlackboardConclusionVersionConflict(validatedSync.DispatchRequestID, 7, now, 0); !errors.Is(err, task.ErrInvalidBlackboardConclusionReceipt) {
		t.Fatalf("same revision accepted as real conflict: %v", err)
	}
	stillValidated, _ := svc.BlackboardConclusionByDispatchRequestID(validated.DispatchRequestID)
	if stillValidated.InternalState != task.BlackboardConclusionReceiptVersionSyncRequested || len(stillValidated.CanonicalResultJSON) == 0 {
		t.Fatalf("rejected conflict mutated receipt: %#v", stillValidated)
	}
	syncFailure, changed, err := svc.MarkBlackboardConclusionApplyActionRequired(
		validatedSync.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, now, time.Minute)
	if err != nil || !changed || syncFailure.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		syncFailure.ErrorCode != task.BlackboardConclusionErrorInvalidResult || len(syncFailure.CanonicalResultJSON) != 0 {
		t.Fatalf("version sync failure = %#v, changed=%v, err=%v", syncFailure, changed, err)
	}
	incompatible := makeValidated("incompatible-version", 8)
	incompatibleAction, changed, err := svc.MarkBlackboardConclusionVersionConflictActionRequired(
		incompatible.DispatchRequestID, now, time.Minute)
	if err != nil || !changed || incompatibleAction.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		incompatibleAction.ErrorCode != task.BlackboardConclusionErrorVersionConflict ||
		incompatibleAction.BaseRevision == nil || *incompatibleAction.BaseRevision != 8 ||
		len(incompatibleAction.CanonicalResultJSON) != 0 || incompatibleAction.CanonicalResultSHA256 != "" {
		t.Fatalf("incompatible semantic version = %#v, changed=%v, err=%v", incompatibleAction, changed, err)
	}
	if replay, replayChanged, replayErr := svc.MarkBlackboardConclusionVersionConflictActionRequired(
		incompatible.DispatchRequestID, now, time.Minute); replayErr != nil || replayChanged || replay.ID != incompatible.ID {
		t.Fatalf("incompatible semantic version replay = %#v, changed=%v, err=%v", replay, replayChanged, replayErr)
	}
	retry, _, _ := svc.RetryBlackboardConclusion(incompatible.ID, "version-retry", now.Add(time.Minute))
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(retry.DispatchRequestID, "control-version-retry")
	retryValidated, _, _ := svc.MarkBlackboardConclusionValidated(retry.DispatchRequestID, []byte(`{"schema":"runtime-attempt-result/v1"}`))
	retrySync, _, _ := svc.ClaimBlackboardConclusionVersionSync(retryValidated.DispatchRequestID)
	retryAction, changed, err := svc.HandleBlackboardConclusionVersionConflict(
		retrySync.DispatchRequestID, 9, now.Add(time.Minute), 0)
	if err != nil || !changed || retryAction.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		retryAction.VersionRegenerationCount != 0 || retryAction.ExplicitRetryCount != 1 ||
		retryAction.ErrorCode != task.BlackboardConclusionErrorVersionConflict {
		t.Fatalf("operator retry reopened automatic version regeneration = %#v, changed=%v, err=%v", retryAction, changed, err)
	}

	repairReceipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-repair-conflict", "session-1", "repair-then-conflict",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
	repairInitial, _, _ := svc.ClaimBlackboardConclusionDispatch(repairReceipt.ID, 20)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(repairInitial.DispatchRequestID, "control-repair-source")
	repair, _, _ := svc.HandleBlackboardConclusionFailure(repairInitial.DispatchRequestID,
		task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, 0)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(repair.DispatchRequestID, "control-repair-result")
	repairValidated, _, _ := svc.MarkBlackboardConclusionValidated(repair.DispatchRequestID, []byte(`{"schema":"runtime-attempt-result/v1"}`))
	repairSync, _, _ := svc.ClaimBlackboardConclusionVersionSync(repairValidated.DispatchRequestID)
	repairAction, changed, err := svc.HandleBlackboardConclusionVersionConflict(
		repairSync.DispatchRequestID, 21, now, 0)
	if err != nil || !changed || repairAction.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		repairAction.AutomaticTurnCount != task.BlackboardConclusionAutomaticTurnLimit ||
		repairAction.VersionRegenerationCount != 0 || repairAction.ErrorCode != task.BlackboardConclusionErrorVersionConflict {
		t.Fatalf("repair exceeded automatic turn limit = %#v, changed=%v, err=%v", repairAction, changed, err)
	}

	invalidSource := makeValidated("invalid-regeneration", 1)
	invalidSync, _, _ := svc.ClaimBlackboardConclusionVersionSync(invalidSource.DispatchRequestID)
	regeneration, _, _ := svc.HandleBlackboardConclusionVersionConflict(invalidSync.DispatchRequestID, 2, now, 0)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(regeneration.DispatchRequestID, "control-invalid-regeneration")
	action, changed, err := svc.HandleBlackboardConclusionFailure(regeneration.DispatchRequestID,
		task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, time.Minute)
	if err != nil || !changed || action.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		action.VersionRegenerationCount != 1 || action.ErrorCode != task.BlackboardConclusionErrorInvalidResult {
		t.Fatalf("invalid regeneration = %#v, changed=%v, err=%v", action, changed, err)
	}

	strandedSource := makeValidated("stranded-regeneration", 10)
	stranded, _, _ := svc.ClaimBlackboardConclusionVersionSync(strandedSource.DispatchRequestID)
	reconciled, err := svc.ReconcileStrandedBlackboardConclusionRecoveries(now, time.Minute)
	if err != nil || len(reconciled) != 1 || reconciled[0].ID != stranded.ID ||
		reconciled[0].InternalState != task.BlackboardConclusionReceiptActionRequired ||
		reconciled[0].ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("stranded version regeneration = %#v, err=%v", reconciled, err)
	}
}

func TestValidatedBlackboardConclusionsReturnsDurableApplyIntentsInOrder(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	makeValidated := func(turn string, base int, canonical []byte) task.BlackboardConclusionReceipt {
		receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-"+turn, "session-1", turn,
			task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
		dispatched, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, base)
		_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-"+turn)
		validated, _, _ := svc.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, canonical)
		return validated
	}
	first := makeValidated("turn-first", 3, []byte(`{"schema":"runtime-attempt-result/v1","base_revision":3}`))
	second := makeValidated("turn-second", 4, []byte(`{"schema":"runtime-attempt-result/v1","base_revision":4}`))
	nonValidated := makeValidated("turn-syncing", 5, []byte(`{"schema":"runtime-attempt-result/v1","base_revision":5}`))
	_, _, _ = svc.ClaimBlackboardConclusionVersionSync(nonValidated.DispatchRequestID)

	intents, err := svc.ValidatedBlackboardConclusions()
	if err != nil || len(intents) != 2 {
		t.Fatalf("validated apply intents = %#v, err=%v", intents, err)
	}
	if intents[0].ID != first.ID || intents[1].ID != second.ID {
		t.Fatalf("validated apply intent order = [%q,%q], want [%q,%q]", intents[0].ID, intents[1].ID, first.ID, second.ID)
	}
	for index, want := range []task.BlackboardConclusionReceipt{first, second} {
		got := intents[index]
		if got.InternalState != task.BlackboardConclusionReceiptValidated || got.DispatchRequestID != want.DispatchRequestID ||
			got.BaseRevision == nil || want.BaseRevision == nil || *got.BaseRevision != *want.BaseRevision ||
			got.ApplyIdempotencyKey != want.ApplyIdempotencyKey || string(got.CanonicalResultJSON) != string(want.CanonicalResultJSON) ||
			got.CanonicalResultSHA256 != want.CanonicalResultSHA256 {
			t.Fatalf("validated apply intent %d lost persisted lineage: got=%#v want=%#v", index, got, want)
		}
	}
	again, err := svc.ValidatedBlackboardConclusions()
	if err != nil || len(again) != 2 || again[0].InternalState != task.BlackboardConclusionReceiptValidated {
		t.Fatalf("read-only apply intent query mutated state: %#v, err=%v", again, err)
	}
}

func TestBlackboardConclusionRetryRemembersEveryOperatorIdempotencyKey(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-1", "session-1", "turn-1",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
	dispatched, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 0)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-1")
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	_, _, _ = svc.HandleBlackboardConclusionFailure(dispatched.DispatchRequestID, task.BlackboardConclusionErrorToolUseForbidden, task.ConclusionValidationDetail{}, now, 0)

	retry1, won, err := svc.RetryBlackboardConclusion(receipt.ID, "retry-key-1", now)
	if err != nil || !won {
		t.Fatalf("retry 1 = %#v, won=%v, err=%v", retry1, won, err)
	}
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(retry1.DispatchRequestID, "control-retry-1")
	_, _, _ = svc.HandleBlackboardConclusionFailure(retry1.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, 0)
	retry2, won, err := svc.RetryBlackboardConclusion(receipt.ID, "retry-key-2", now)
	if err != nil || !won || retry2.ExplicitRetryCount != 2 {
		t.Fatalf("retry 2 = %#v, won=%v, err=%v", retry2, won, err)
	}
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(retry2.DispatchRequestID, "control-retry-2")
	action, _, _ := svc.HandleBlackboardConclusionFailure(retry2.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, 0)

	replayed, replayWon, replayErr := svc.RetryBlackboardConclusion(receipt.ID, "retry-key-1", now)
	if replayErr != nil || replayWon || replayed.ID != receipt.ID || replayed.ExplicitRetryCount != 2 ||
		replayed.InternalState != task.BlackboardConclusionReceiptActionRequired || replayed.DispatchRequestID != action.DispatchRequestID {
		t.Fatalf("old retry key replay = %#v, won=%v, err=%v", replayed, replayWon, replayErr)
	}
}

func TestRetryLatestBlackboardConclusionIsAtomicAndTaskIdempotentAcrossReceipts(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	makeActionRequired := func(sourceTurnID string) task.BlackboardConclusionReceipt {
		receipt, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-"+sourceTurnID, "session-1", sourceTurnID,
			task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
		if err != nil {
			t.Fatal(err)
		}
		dispatched, _, err := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-"+sourceTurnID)
		action, _, err := svc.HandleBlackboardConclusionFailure(dispatched.DispatchRequestID,
			task.BlackboardConclusionErrorToolUseForbidden, task.ConclusionValidationDetail{}, now, 0)
		if err != nil {
			t.Fatal(err)
		}
		return action
	}

	receiptA := makeActionRequired("turn-a")
	retryA, won, err := svc.RetryLatestBlackboardConclusion(created.ID, "task-retry-key", now)
	if err != nil || !won || retryA.ID != receiptA.ID {
		t.Fatalf("retry A = %#v, won=%v, err=%v", retryA, won, err)
	}
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(retryA.DispatchRequestID, "control-retry-a")
	_, _, _ = svc.HandleBlackboardConclusionFailure(retryA.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, 0)
	receiptB := makeActionRequired("turn-b")

	replayed, replayWon, replayErr := svc.RetryLatestBlackboardConclusion(created.ID, "task-retry-key", now)
	if replayErr != nil || replayWon || replayed.ID != receiptA.ID {
		t.Fatalf("cross-receipt replay = %#v, won=%v, err=%v", replayed, replayWon, replayErr)
	}
	// The superseded dispatch request id fails closed (ADR 0021: only the
	// ACTIVE dispatch may resolve), so the newer obligation must be read by
	// owner identity and stay untouched by the delayed replay.
	if _, err := svc.BlackboardConclusionByDispatchRequestID(receiptB.DispatchRequestID); !errors.Is(err, task.ErrBlackboardConclusionDispatchInactive) {
		t.Fatalf("stale dispatch request id resolved after replay: %v", err)
	}
	latestB, err := svc.LatestBlackboardConclusion(created.ID)
	if err != nil || latestB == nil || latestB.ID != receiptB.ID || latestB.InternalState != task.BlackboardConclusionReceiptActionRequired || latestB.ExplicitRetryCount != 0 {
		t.Fatalf("newer obligation mutated by delayed replay = %#v, err=%v", latestB, err)
	}
	retryB, won, err := svc.RetryLatestBlackboardConclusion(created.ID, "task-retry-key-2", now)
	if err != nil || !won || retryB.ID != receiptB.ID || retryB.ExplicitRetryCount != 1 {
		t.Fatalf("latest atomic retry = %#v, won=%v, err=%v", retryB, won, err)
	}
}

func TestBlackboardConclusionRecoveryDispatchFailureRequiresActionIdempotently(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	_, _ = svc.UpdateStatus(created.ID, task.StatusRunning)
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-repair", "session-1", "turn-repair",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
	initial, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 0)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(initial.DispatchRequestID, "control-initial")
	repair, _, _ := svc.HandleBlackboardConclusionFailure(initial.DispatchRequestID,
		task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, 5*time.Minute)

	action, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequired(repair.DispatchRequestID, task.ConclusionRecoveryDispatchFailed, now, time.Minute)
	if err != nil || !changed || action.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		action.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired || action.AutomaticTurnCount != 2 || action.RepairCount != 1 ||
		action.NextEligibleAt == nil || !action.NextEligibleAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("repair dispatch recovery = %#v, changed=%v, err=%v", action, changed, err)
	}
	replay, replayChanged, replayErr := svc.MarkBlackboardConclusionRecoveryActionRequired(repair.DispatchRequestID, task.ConclusionRecoveryDispatchFailed, now, time.Minute)
	if replayErr != nil || replayChanged || replay.ID != receipt.ID || replay.RepairCount != 1 ||
		replay.NextEligibleAt == nil || !replay.NextEligibleAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("repair recovery replay = %#v, changed=%v, err=%v", replay, replayChanged, replayErr)
	}
	events, _ := svc.Events(created.ID)
	recoveryEvents := 0
	for _, event := range events {
		if event.Kind == task.EventKindBlackboardConclusion && event.Payload["reason"] == "dispatch_recovery" {
			recoveryEvents++
		}
	}
	if recoveryEvents != 1 {
		t.Fatalf("dispatch recovery events = %d, want 1", recoveryEvents)
	}
	found, _ := svc.Get(created.ID)
	if found.Status != task.StatusRunning {
		t.Fatalf("recovery changed Task status to %q", found.Status)
	}
}

func TestBlackboardConclusionRecoveryOverridesVersionConflictAndPreservesBudget(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-version-recovery",
		"session-1", "turn-version-recovery", task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1})
	dispatched, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 4)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-version-recovery")
	validated, _, _ := svc.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, []byte(`{"schema":"runtime-attempt-result/v1"}`))
	syncing, _, _ := svc.ClaimBlackboardConclusionVersionSync(validated.DispatchRequestID)
	regeneration, _, _ := svc.HandleBlackboardConclusionVersionConflict(syncing.DispatchRequestID, 5, now, 7*time.Minute)

	action, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(regeneration.ID, task.ConclusionRecoveryDispatchFailed, now.Add(time.Minute), time.Minute)
	if err != nil || !changed || action.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired ||
		action.AutomaticTurnCount != 2 || action.VersionRegenerationCount != 1 ||
		action.NextEligibleAt == nil || !action.NextEligibleAt.Equal(now.Add(7*time.Minute)) {
		t.Fatalf("version-conflict restart recovery = %#v, changed=%v, err=%v", action, changed, err)
	}
	replay, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(regeneration.ID, task.ConclusionRecoveryDispatchFailed, now.Add(time.Hour), time.Hour)
	if err != nil || changed || replay.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired ||
		replay.AutomaticTurnCount != 2 || replay.VersionRegenerationCount != 1 ||
		replay.NextEligibleAt == nil || !replay.NextEligibleAt.Equal(now.Add(7*time.Minute)) {
		t.Fatalf("version-conflict recovery replay = %#v, changed=%v, err=%v", replay, changed, err)
	}
}

func TestVersionSyncRecoveryPreservesCanonicalResultUntilOperatorRetryStartsNewGeneration(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	continuation, _ = svc.UpdateContinuationRuntimeMetadata(continuation.ID, "", "session-1", "")
	_, _ = svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	receipt, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-sync-recovery",
		"session-1", "turn-sync-recovery", task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1})
	dispatched, _, _ := svc.ClaimBlackboardConclusionDispatch(receipt.ID, 4)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-sync-recovery")
	canonical := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":4}`)
	validated, _, _ := svc.MarkBlackboardConclusionValidated(dispatched.DispatchRequestID, canonical)
	syncing, _, _ := svc.ClaimBlackboardConclusionVersionSync(validated.DispatchRequestID)

	action, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(syncing.ID, task.ConclusionRecoveryDispatchFailed, now, 5*time.Minute)
	if err != nil || !changed || action.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		action.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired ||
		!bytes.Equal(action.CanonicalResultJSON, canonical) || action.CanonicalResultSHA256 != validated.CanonicalResultSHA256 {
		t.Fatalf("version-sync recovery action = %#v, changed=%v, err=%v", action, changed, err)
	}
	replayed, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(syncing.ID, task.ConclusionRecoveryDispatchFailed, now.Add(time.Hour), time.Hour)
	if err != nil || changed || !bytes.Equal(replayed.CanonicalResultJSON, canonical) ||
		replayed.CanonicalResultSHA256 != validated.CanonicalResultSHA256 {
		t.Fatalf("version-sync recovery replay = %#v, changed=%v, err=%v", replayed, changed, err)
	}
	retried, won, _, err := svc.RetryLatestBlackboardConclusionForRuntime(
		created.ID, "sync-recovery-retry", continuation.ID, "session-1", 4, now.Add(5*time.Minute),
	)
	if err != nil || !won || retried.InternalState != task.BlackboardConclusionReceiptDispatchRequested ||
		len(retried.CanonicalResultJSON) != 0 || retried.CanonicalResultSHA256 != "" {
		t.Fatalf("version-sync recovery retry = %#v, won=%v, err=%v", retried, won, err)
	}
}

func TestPendingBlackboardConclusionRecoveryProjectsActionRequiredWithoutExtendingCooldown(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	continuation, _ = svc.UpdateContinuationRuntimeMetadata(continuation.ID, "", "session-1", "")
	_, _ = svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning)
	pending, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-pending",
		"session-1", "turn-pending", task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := svc.BlackboardConclusionRecoveryCandidates()
	if err != nil || len(candidates) != 1 || candidates[0].ID != pending.ID || candidates[0].SourceRequestID != "work-request-pending" {
		t.Fatalf("pending recovery candidates = %#v, err=%v", candidates, err)
	}
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	action, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(pending.ID, task.ConclusionRecoveryDispatchFailed, now, 5*time.Minute)
	if err != nil || !changed || action.AutomaticTurnCount != 0 || action.DispatchRequestID != "" {
		t.Fatalf("pending recovery action = %#v, changed=%v, err=%v", action, changed, err)
	}
	view := action.ViewAt(task.BlackboardConclusionModeAssisted, now)
	if view.State != task.BlackboardConclusionStateActionRequired || view.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired ||
		view.RetryAvailable || view.NextEligibleAt == nil || !view.NextEligibleAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("pending recovery view = %#v", view)
	}
	replay, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(pending.ID, task.ConclusionRecoveryDispatchFailed, now.Add(time.Hour), time.Hour)
	if err != nil || changed || replay.NextEligibleAt == nil || !replay.NextEligibleAt.Equal(now.Add(5*time.Minute)) || replay.AutomaticTurnCount != 0 {
		t.Fatalf("pending recovery replay = %#v, changed=%v, err=%v", replay, changed, err)
	}
	rearmed, won, initial, err := svc.RetryLatestBlackboardConclusionForRuntime(
		created.ID, "pending-recovery-retry", continuation.ID, "session-1", 13, now.Add(5*time.Minute),
	)
	if err != nil || !won || !initial || rearmed.InternalState != task.BlackboardConclusionReceiptDispatchRequested ||
		rearmed.DispatchRequestID == "" || rearmed.BaseRevision == nil || *rearmed.BaseRevision != 13 || rearmed.ApplyIdempotencyKey == "" ||
		rearmed.ExplicitRetryCount != 1 || rearmed.OperatorRetryKey != "pending-recovery-retry" ||
		rearmed.AutomaticTurnCount != 1 || rearmed.ErrorCode != "" || rearmed.NextEligibleAt != nil {
		t.Fatalf("pending recovery retry = %#v, won=%v, err=%v", rearmed, won, err)
	}
	replayedRetry, won, err := svc.RetryLatestBlackboardConclusion(created.ID, "pending-recovery-retry", now.Add(time.Hour))
	if err != nil || won || replayedRetry.ID != pending.ID || replayedRetry.InternalState != task.BlackboardConclusionReceiptDispatchRequested {
		t.Fatalf("pending recovery retry replay = %#v, won=%v, err=%v", replayedRetry, won, err)
	}
	replacement, err := svc.CreateReplacementContinuation(continuation)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err = svc.UpdateContinuationRuntimeMetadata(replacement.ID, "", "session-2", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateContinuationStatus(replacement.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	dispatched, reboundWon, err := svc.CreateRecoveryConclusionDispatch(
		pending.ID, replacement.ID, "session-2", now.Add(6*time.Minute),
	)
	if err != nil || !reboundWon || dispatched.DispatchKind != task.ConclusionDispatchKindInitial {
		t.Fatalf("rebound initial retry = %#v, created=%v, err=%v", dispatched, reboundWon, err)
	}
	_, _, _ = svc.MarkBlackboardConclusionSendStarted(dispatched.DispatchRequestID, now.Add(6*time.Minute))
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(dispatched.DispatchRequestID, "control-after-recovery")
	exhausted, dispatchedRepair, err := svc.HandleBlackboardConclusionFailure(dispatched.DispatchRequestID,
		task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now.Add(7*time.Minute), 0)
	if err != nil || !dispatchedRepair || exhausted.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		exhausted.AutomaticTurnCount != 1 || exhausted.RepairCount != 0 {
		t.Fatalf("explicit pending retry reopened automatic repair = %#v, dispatched=%v, err=%v", exhausted, dispatchedRepair, err)
	}
}

func TestRetryLatestBlackboardConclusionFailsClosedWhenDispatchFailureAppearsBeforeTransaction(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	receipt, _, err := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID,
		"work-request-race", "session-dead", "turn-race",
		task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"},
		task.SemanticDebtWatermarks{SourceWork: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	if _, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(
		receipt.ID, task.ConclusionRecoveryDispatchFailed, now, 0,
	); err != nil || !changed {
		t.Fatalf("mark dispatch_failed: changed=%v err=%v", changed, err)
	}

	retried, won, err := svc.RetryLatestBlackboardConclusion(created.ID, "stale-adapter-retry", now)
	if !errors.Is(err, task.ErrInvalidBlackboardConclusionReceipt) || won {
		t.Fatalf("retry without proven Runtime = %#v, won=%v, err=%v", retried, won, err)
	}
	retried, won, err = svc.RetryBlackboardConclusion(receipt.ID, "stale-receipt-retry", now)
	if !errors.Is(err, task.ErrInvalidBlackboardConclusionReceipt) || won {
		t.Fatalf("receipt retry without proven Runtime = %#v, won=%v, err=%v", retried, won, err)
	}
	latest, latestErr := svc.LatestBlackboardConclusion(created.ID)
	if latestErr != nil || latest == nil || latest.InternalState != task.BlackboardConclusionReceiptActionRequired ||
		latest.RecoveryReason != string(task.ConclusionRecoveryDispatchFailed) || latest.ExplicitRetryCount != 0 {
		t.Fatalf("failed-closed conclusion = %#v, err=%v", latest, latestErr)
	}
	if dispatches, historyErr := svc.ConclusionDispatches(receipt.ID); historyErr != nil || len(dispatches) != 0 {
		t.Fatalf("failed-closed dispatch history = %#v, err=%v", dispatches, historyErr)
	}
}

func TestRecoveryCandidatesIncludeInitialDispatchWithoutMutatingIt(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID,
		Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardConclusionMode: task.BlackboardConclusionModeAssisted}})
	_, _ = svc.UpdateStatus(created.ID, task.StatusRunning)
	continuation, _ := svc.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	makePending := func(turn string) task.BlackboardConclusionReceipt {
		r, _, _ := svc.RecordBlackboardConclusionCheckpoint(created.ID, continuation.ID, "work-request-"+turn, "session-1", turn,
			task.TurnSelection{ModelProviderID: "provider-1", Model: "model-1"}, task.SemanticDebtWatermarks{SourceWork: 1})
		r, _, _ = svc.ClaimBlackboardConclusionDispatch(r.ID, 0)
		return r
	}
	initialOnly := makePending("turn-initial-only")
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(initialOnly.DispatchRequestID, "control-initial-only")

	repairSource := makePending("turn-repair")
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(repairSource.DispatchRequestID, "control-repair-source")
	repair, _, _ := svc.HandleBlackboardConclusionFailure(repairSource.DispatchRequestID, task.BlackboardConclusionErrorInvalidResult, task.ConclusionValidationDetail{}, now, 0)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(repair.DispatchRequestID, "control-repair")

	retrySource := makePending("turn-retry")
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(retrySource.DispatchRequestID, "control-retry-source")
	_, _, _ = svc.HandleBlackboardConclusionFailure(retrySource.DispatchRequestID, task.BlackboardConclusionErrorToolUseForbidden, task.ConclusionValidationDetail{}, now, 0)
	retry, _, _ := svc.RetryBlackboardConclusion(retrySource.ID, "retry-key", now)
	_, _, _ = svc.MarkBlackboardConclusionAwaiting(retry.DispatchRequestID, "control-retry")

	reconciled, err := svc.ReconcileStrandedBlackboardConclusionRecoveries(now, time.Minute)
	if err != nil || len(reconciled) != 2 {
		t.Fatalf("startup reconciliation = %#v, err=%v", reconciled, err)
	}
	for _, obligationID := range []string{repairSource.ID, retrySource.ID} {
		got, lookupErr := svc.LatestBlackboardConclusion(created.ID)
		if lookupErr != nil {
			t.Fatalf("reconciled obligation lookup: %v", lookupErr)
		}
		_ = obligationID
		_ = got
		break
	}
	// The reconciled receipts carry the per-obligation counters; the obsolete
	// dispatch request ids no longer resolve (ADR 0021 active-dispatch gating).
	reconciledByID := make(map[string]task.BlackboardConclusionReceipt, len(reconciled))
	for _, receipt := range reconciled {
		reconciledByID[receipt.ID] = receipt
	}
	for _, obligationID := range []string{repairSource.ID, retrySource.ID} {
		got, ok := reconciledByID[obligationID]
		if !ok || got.InternalState != task.BlackboardConclusionReceiptActionRequired {
			t.Fatalf("reconciled obligation %s = %#v (present=%v)", obligationID, got, ok)
		}
	}
	untouched, err := svc.BlackboardConclusionByDispatchRequestID(initialOnly.DispatchRequestID)
	if err != nil || untouched.InternalState != task.BlackboardConclusionReceiptAwaitingResult || untouched.ExplicitRetryCount != 0 || untouched.RepairCount != 0 {
		t.Fatalf("initial dispatch was reconciled = %#v, err=%v", untouched, err)
	}
	candidates, err := svc.BlackboardConclusionRecoveryCandidates()
	if err != nil || len(candidates) != 1 || candidates[0].ID != initialOnly.ID {
		t.Fatalf("remaining recovery candidates = %#v, err=%v", candidates, err)
	}
	initialAction, changed, err := svc.MarkBlackboardConclusionRecoveryActionRequiredByReceiptID(initialOnly.ID, task.ConclusionRecoveryDispatchFailed, now, time.Minute)
	if err != nil || !changed || initialAction.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("initial dispatch recovery = %#v, changed=%v, err=%v", initialAction, changed, err)
	}
	again, err := svc.ReconcileStrandedBlackboardConclusionRecoveries(now, time.Minute)
	if err != nil || len(again) != 0 {
		t.Fatalf("duplicate startup reconciliation = %#v, err=%v", again, err)
	}
	repairAfter := reconciledByID[repairSource.ID]
	retryAfter := reconciledByID[retrySource.ID]
	if repairAfter.AutomaticTurnCount != 2 || repairAfter.RepairCount != 1 || repairAfter.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired ||
		retryAfter.ExplicitRetryCount != 1 || retryAfter.ErrorCode != task.BlackboardConclusionErrorRuntimeRecoveryRequired {
		t.Fatalf("reconciliation changed counters: repair=%#v retry=%#v", repairAfter, retryAfter)
	}
	found, _ := svc.Get(created.ID)
	if found.Status != task.StatusRunning {
		t.Fatalf("startup reconciliation changed Task status to %q", found.Status)
	}
}

func TestCreateRejectsUnknownProject(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	_, err := svc.Create(task.CreateRequest{ProjectID: "missing", Type: task.TypePentest, Goal: "x", Runner: task.RunnerSandbox})
	if !errors.Is(err, task.ErrProjectNotFound) {
		t.Fatalf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestGetReturnsPersistedTask(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})

	created, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate", RuntimeProfileID: "prof", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Goal != "enumerate" {
		t.Fatalf("expected goal, got %q", fetched.Goal)
	}
	if got := fetched.ScopeSnapshot.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope snapshot persisted, got %#v", got)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	_, err := svc.Get("missing")
	if !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListForProjectReturnsTasksInCreationOrder(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	if _, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "first", Runner: task.RunnerSandbox}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "second", Runner: task.RunnerSandbox}); err != nil {
		t.Fatalf("create second: %v", err)
	}

	tasks, err := svc.ListForProject(proj.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Goal != "first" || tasks[1].Goal != "second" {
		t.Fatalf("expected creation order, got %q then %q", tasks[0].Goal, tasks[1].Goal)
	}
}

// TestAppendEventStoresEventsInOrder proves task events are appended with a
// monotonically increasing sequence and read back in order.
func TestAppendEventStoresEventsInOrder(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox})

	if _, err := svc.AppendEvent(created.ID, task.EventKindRuntimeOutput, task.EventPayload{"text": "started"}); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if _, err := svc.AppendEvent(created.ID, task.EventKindRuntimeOutput, task.EventPayload{"text": "working"}); err != nil {
		t.Fatalf("append second: %v", err)
	}

	events, err := svc.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("expected seq 1 then 2, got %d then %d", events[0].Seq, events[1].Seq)
	}
	if events[0].Payload["text"] != "started" || events[1].Payload["text"] != "working" {
		t.Fatalf("expected payload preserved in order, got %#v", events)
	}
}

// TestHistoryEventWindowReadsOnlyTheRequestedProjectionWindow proves History
// reads use a keyset-bounded database query. Unrelated conversation Events do
// not consume the Timeline limit or move its projection cursor.
func TestHistoryEventWindowReadsOnlyTheRequestedProjectionWindow(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox})

	for index := 1; index <= 5; index++ {
		if _, err := svc.AppendEvent(created.ID, task.EventKindLifecycle, task.EventPayload{"phase": index}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.AppendEvent(created.ID, task.EventKindConversation, task.EventPayload{"role": "assistant", "text": "irrelevant"}); err != nil {
		t.Fatal(err)
	}

	window, err := svc.HistoryEventWindow(created.ID, task.EventWindowQuery{
		Projection: task.EventProjectionTimeline,
		Limit:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Events) != 3 || window.Events[0].Seq != 3 || window.Events[2].Seq != 5 {
		t.Fatalf("initial Timeline Event window = %#v, want seqs 3..5", window.Events)
	}
	if window.Cursor != 5 || !window.HasOlder {
		t.Fatalf("initial Timeline Event window cursor=%d has_older=%t, want 5/true", window.Cursor, window.HasOlder)
	}

	delta, err := svc.HistoryEventWindow(created.ID, task.EventWindowQuery{
		Projection: task.EventProjectionTimeline,
		AfterSet:   true,
		After:      3,
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Events) != 1 || delta.Events[0].Seq != 4 || !delta.HasNewer || delta.ScanCursor != 4 {
		t.Fatalf("Timeline Event delta = %#v, want seq 4 with more new Events", delta)
	}
}

// TestAppendEventOnUnknownTaskFails proves events cannot be added to a phantom
// task.
func TestAppendEventOnUnknownTaskFails(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	_, err := svc.AppendEvent("missing", task.EventKindRuntimeOutput, task.EventPayload{})
	if !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestRuntimeConfigVersionsIncrementOnProfileSwitch proves a profile switch
// inside a task creates a new runtime config version rather than a new task.
func TestRuntimeConfigVersionsIncrementOnProfileSwitch(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type:

	// First config version is captured at launch.
	task.TypePentest, Goal: "g", RuntimeProfileID: "prof-a", Runner: task.RunnerSandbox})

	first, err := svc.RecordRuntimeConfig(created.ID, "prof-a", map[string]any{"model": "a"})
	if err != nil {
		t.Fatalf("record first config: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("expected first version 1, got %d", first.Version)
	}

	// A profile switch creates version 2, not a new task.
	second, err := svc.RecordRuntimeConfig(created.ID, "prof-b", map[string]any{"model": "b"})
	if err != nil {
		t.Fatalf("record second config: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("expected second version 2, got %d", second.Version)
	}
	if second.RuntimeProfileID != "prof-b" {
		t.Fatalf("expected new profile, got %q", second.RuntimeProfileID)
	}

	versions, err := svc.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 config versions, got %d", len(versions))
	}
	// Task identity is unchanged.
	fetched, _ := svc.Get(created.ID)
	if fetched.RuntimeProfileID != "prof-a" {
		t.Fatalf("task original profile must be unchanged; profile switch affects next continuation, got %q", fetched.RuntimeProfileID)
	}
}

func TestContinuationLifecycleTracksLatestAndActiveRun(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "g",
		RuntimeProfileID: "prof-a",
		Runner:           task.RunnerSandbox,
	})

	first, err := svc.CreateContinuation(created.ID, "prof-a", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create first continuation: %v", err)
	}
	if first.Number != 1 {
		t.Fatalf("expected first continuation number 1, got %d", first.Number)
	}
	if first.Status != task.StatusPending {
		t.Fatalf("expected first continuation pending, got %q", first.Status)
	}

	active, err := svc.ActiveContinuation(created.ID)
	if err != nil {
		t.Fatalf("active continuation: %v", err)
	}
	if active == nil || active.ID != first.ID {
		t.Fatalf("expected active continuation %q, got %#v", first.ID, active)
	}

	if _, err := svc.UpdateContinuationStatus(first.ID, task.StatusRunning); err != nil {
		t.Fatalf("mark first running: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(first.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark first completed: %v", err)
	}

	active, err = svc.ActiveContinuation(created.ID)
	if err != nil {
		t.Fatalf("active continuation after completion: %v", err)
	}
	if active != nil {
		t.Fatalf("expected no active continuation after completion, got %#v", active)
	}

	second, err := svc.CreateContinuation(created.ID, "prof-a", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create second continuation: %v", err)
	}
	if second.Number != 2 {
		t.Fatalf("expected second continuation number 2, got %d", second.Number)
	}

	latest, err := svc.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if latest == nil || latest.ID != second.ID {
		t.Fatalf("expected latest continuation %q, got %#v", second.ID, latest)
	}
}

func TestTerminalContinuationStatusCannotBeOverwrittenByLateReconciliation(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	createdProject, err := projects.Create("Terminal monotonicity", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "finish once", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("complete Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusInterrupted); !errors.Is(err, task.ErrContinuationStatusConflict) {
		t.Fatalf("late reconciliation error = %v, want status conflict", err)
	}
	stored, err := svc.Continuation(continuation.ID)
	if err != nil {
		t.Fatalf("read terminal Continuation: %v", err)
	}
	if stored.Status != task.StatusCompleted {
		t.Fatalf("late reconciliation overwrote terminal status with %q", stored.Status)
	}
}

func TestContinuationRuntimeMetadataIsPersisted(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "g",
		RuntimeProfileID: "prof-a",
		Runner:           task.RunnerSandbox,
	})

	continuation, err := svc.CreateContinuation(created.ID, "prof-a", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}

	updated, err := svc.UpdateContinuationRuntimeMetadata(continuation.ID, "ctr-1", "sess-123", "/tmp/session.jsonl")
	if err != nil {
		t.Fatalf("update continuation metadata: %v", err)
	}
	if updated.ContainerID != "ctr-1" {
		t.Fatalf("expected container id ctr-1, got %q", updated.ContainerID)
	}
	if updated.NativeSessionID != "sess-123" {
		t.Fatalf("expected native session id sess-123, got %q", updated.NativeSessionID)
	}
	if updated.NativeSessionPath != "/tmp/session.jsonl" {
		t.Fatalf("expected native session path /tmp/session.jsonl, got %q", updated.NativeSessionPath)
	}

	latest, err := svc.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if latest == nil || latest.NativeSessionID != "sess-123" {
		t.Fatalf("expected persisted native session id sess-123, got %#v", latest)
	}
}

// TestReconcileInterruptedStatusesMarksActiveTasksInterrupted proves the daemon
// startup reconcile: tasks left running/paused/pending by a previous daemon
// instance become interrupted, while already-terminal tasks are untouched.
func TestReconcileInterruptedStatusesMarksActiveTasksInterrupted(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	marker := &recordingTerminalMarker{}
	svc.SetContinuationTerminalMarker(marker)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	createTaskWithStatus := func(goal string) task.Task {
		created, err := svc.Create(task.CreateRequest{
			ProjectID: proj.ID,

			Type: task.TypePentest, Goal: goal,
			RuntimeProfileID: "profile-1",
			Runner:           task.RunnerSandbox,
		})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		return created
	}

	running := createTaskWithStatus("running ghost")
	paused := createTaskWithStatus("paused ghost")
	completed := createTaskWithStatus("done task")
	runningContinuation, err := svc.CreateContinuation(running.ID, "profile-1", "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create running continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(runningContinuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("set running continuation: %v", err)
	}
	if _, err := svc.UpdateStatus(running.ID, task.StatusRunning); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := svc.UpdateStatus(paused.ID, task.StatusPaused); err != nil {
		t.Fatalf("set paused: %v", err)
	}
	if _, err := svc.UpdateStatus(completed.ID, task.StatusCompleted); err != nil {
		t.Fatalf("set completed: %v", err)
	}

	changed, err := svc.ReconcileInterruptedStatuses()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("expected 2 interrupted tasks, got %d", len(changed))
	}

	runningGot, _ := svc.Get(running.ID)
	pausedGot, _ := svc.Get(paused.ID)
	completedGot, _ := svc.Get(completed.ID)
	if runningGot.Status != task.StatusInterrupted {
		t.Fatalf("expected running task -> interrupted, got %q", runningGot.Status)
	}
	runningContinuationGot, err := svc.LatestContinuation(running.ID)
	if err != nil {
		t.Fatalf("latest running continuation: %v", err)
	}
	if runningContinuationGot == nil || runningContinuationGot.Status != task.StatusInterrupted {
		t.Fatalf("expected running continuation -> interrupted, got %#v", runningContinuationGot)
	}
	if len(marker.continuationIDs) != 1 || marker.continuationIDs[0] != runningContinuation.ID {
		t.Fatalf("startup reconciliation terminal marker calls = %v", marker.continuationIDs)
	}
	if pausedGot.Status != task.StatusInterrupted {
		t.Fatalf("expected paused task -> interrupted, got %q", pausedGot.Status)
	}
	if completedGot.Status != task.StatusCompleted {
		t.Fatalf("expected completed task untouched, got %q", completedGot.Status)
	}
}

func TestReconcileInterruptedStatusesClearsStaleActiveContinuations(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	marker := &recordingTerminalMarker{}
	svc.SetContinuationTerminalMarker(marker)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "already interrupted task",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile-1", "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("set continuation running: %v", err)
	}
	if _, err := svc.UpdateStatus(created.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("set task interrupted: %v", err)
	}

	changed, err := svc.ReconcileInterruptedStatuses()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("expected no task status changes, got %d", len(changed))
	}
	latest, err := svc.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if latest == nil || latest.Status != task.StatusInterrupted {
		t.Fatalf("expected stale active continuation -> interrupted, got %#v", latest)
	}
	if len(marker.continuationIDs) != 1 || marker.continuationIDs[0] != continuation.ID {
		t.Fatalf("stale reconciliation terminal marker calls = %v", marker.continuationIDs)
	}
}

func TestRestartInterruptionPersistsTerminalEventBeforeReconciliation(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID, Type: task.TypePentest, Goal: "restart ordering", RuntimeProfileID: "profile-1", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile-1", "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := svc.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Task: %v", err)
	}

	reconciler := &restartOrderingReconciler{tasks: svc}
	svc.SetContinuationReconciler(reconciler)
	if _, err := svc.ReconcileInterruptedState(); err != nil {
		t.Fatalf("reconcile restart state: %v", err)
	}
	if reconciler.reason != "daemon_restart" {
		t.Fatalf("reconciliation reason = %q, want daemon_restart", reconciler.reason)
	}
	if !reconciler.eventVisible {
		t.Fatal("daemon_restart terminal Event was not visible before reconciliation")
	}
}

func TestReconcileInterruptedStateIgnoresTerminalSandboxContainers(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "completed sandbox task",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile-1", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationRuntimeMetadata(continuation.ID, "ctr-completed", "", ""); err != nil {
		t.Fatalf("set continuation container: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("set continuation completed: %v", err)
	}
	if _, err := svc.UpdateStatus(created.ID, task.StatusCompleted); err != nil {
		t.Fatalf("set task completed: %v", err)
	}

	reconciled, err := svc.ReconcileInterruptedState()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(reconciled.Tasks) != 0 {
		t.Fatalf("expected no task status changes, got %d", len(reconciled.Tasks))
	}
	if len(reconciled.Continuations) != 0 {
		t.Fatalf("expected no terminal continuation cleanup candidates, got %#v", reconciled.Continuations)
	}
}

func TestReconcileInterruptedStateExceptPreservesOwnedTaskAndReturnsHostIdentity(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("Acme", "", project.Scope{}, project.Defaults{})

	owned, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "owned", RuntimeProfileID: "profile", Runner: task.RunnerHost})
	ownedContinuation, _ := svc.CreateContinuation(owned.ID, "profile", "codex", task.RunnerHost)
	_, _ = svc.UpdateContinuationRuntimeMetadata(ownedContinuation.ID, "41001", "native-owned", "")
	_, _ = svc.UpdateContinuationStatus(ownedContinuation.ID, task.StatusRunning)
	_, _ = svc.UpdateStatus(owned.ID, task.StatusRunning)

	stale, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "stale", RuntimeProfileID: "profile", Runner: task.RunnerHost})
	staleContinuation, _ := svc.CreateContinuation(stale.ID, "profile", "codex", task.RunnerHost)
	_, _ = svc.UpdateContinuationRuntimeMetadata(staleContinuation.ID, "41002", "native-stale", "")
	_, _ = svc.UpdateContinuationStatus(staleContinuation.ID, task.StatusRunning)
	_, _ = svc.UpdateStatus(stale.ID, task.StatusRunning)

	result, err := svc.ReconcileInterruptedStateExcept([]string{"", owned.ID, owned.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID != stale.ID {
		t.Fatalf("interrupted Tasks = %#v", result.Tasks)
	}
	if len(result.Continuations) != 1 || result.Continuations[0].ID != staleContinuation.ID ||
		result.Continuations[0].Runner != task.RunnerHost || result.Continuations[0].ContainerID != "41002" {
		t.Fatalf("runtime cleanup candidates = %#v", result.Continuations)
	}
	ownedAfter, _ := svc.Get(owned.ID)
	ownedContinuationAfter, _ := svc.LatestContinuation(owned.ID)
	if ownedAfter.Status != task.StatusRunning || ownedContinuationAfter == nil || ownedContinuationAfter.Status != task.StatusRunning {
		t.Fatalf("owned Runtime was interrupted: Task=%#v Continuation=%#v", ownedAfter, ownedContinuationAfter)
	}
}

func TestTerminalContinuationClosesBoundCapabilities(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	marker := &recordingTerminalMarker{}
	svc.SetContinuationTerminalMarker(marker)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox})
	continuation, err := svc.CreateContinuation(created.ID, "prof-a", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if len(marker.continuationIDs) != 0 {
		t.Fatalf("non-terminal status closed capabilities: %v", marker.continuationIDs)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	if len(marker.continuationIDs) != 1 || marker.continuationIDs[0] != continuation.ID {
		t.Fatalf("terminal marker calls = %v", marker.continuationIDs)
	}
}

// TestCreatePersistsSandboxVPNTunRunControl captures the opt-in TUN device
// capability so CTF OpenVPN clients can create /dev/net/tun inside the sandbox.
func TestCreatePersistsSandboxVPNTunRunControl(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("VPN CTF", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	created, err := svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Type:             task.TypePentest,
		Goal:             "Connect VPN then solve",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxVPNTun: true,
			ContainerCLI:  "podman",
		},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if !created.RunControls.SandboxVPNTun {
		t.Fatal("expected sandbox VPN TUN enabled on create")
	}
	if created.RunControls.ContainerCLI != "podman" {
		t.Fatalf("container_cli = %q, want podman", created.RunControls.ContainerCLI)
	}
	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !fetched.RunControls.SandboxVPNTun {
		t.Fatal("expected sandbox VPN TUN to persist")
	}
	if fetched.RunControls.ContainerCLI != "podman" {
		t.Fatalf("persisted container_cli = %q", fetched.RunControls.ContainerCLI)
	}
}

func TestCreateRejectsInvalidContainerCLI(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("P", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	_, err = svc.Create(task.CreateRequest{
		ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{ContainerCLI: "containerd"},
	})
	if !errors.Is(err, task.ErrInvalidContainerCLI) {
		t.Fatalf("err = %v, want %v", err, task.ErrInvalidContainerCLI)
	}
}

// TestCreateRejectsSandboxVPNTunWithHostProxyOnly proves the two network
// shapes are mutually exclusive: host_proxy_only drops NET_ADMIN after the
// firewall is installed, which cannot host an in-sandbox OpenVPN client.
func TestCreateRejectsSandboxVPNTunWithHostProxyOnly(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("VPN CTF", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err = svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Type:             task.TypePentest,
		Goal:             "Connect VPN then solve",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxNetwork: "host_proxy_only",
			SandboxVPNTun:  true,
		},
	})
	if !errors.Is(err, task.ErrSandboxVPNTunHostProxyConflict) {
		t.Fatalf("create error = %v, want %v", err, task.ErrSandboxVPNTunHostProxyConflict)
	}
}
