package blackboardv2_test

import (
	"context"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestReconciliationAcceptsHistoricalTestsAfterTargetTerminalization(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Historical Attempt Tests", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Reconcile historical tests", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-historical-tests", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("run Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-terminal-tested-target",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:terminal-tested-target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Temporary tested endpoint", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("create tested target: %v", err)
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-attempt-with-terminal-target",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:historically-tested", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Tests a target that later leaves Current Work"}},
			{Op: "relate", From: "attempt:historically-tested", Relation: "tests", To: "entity:terminal-tested-target"},
		},
	}); err != nil {
		t.Fatalf("create historically tested Attempt: %v", err)
	}
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "terminalize-tested-target",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "entity:terminal-tested-target", Version: 1, Status: "retired", ResolutionSummary: "The tested endpoint was removed before Continuation interruption",
		}},
	}); err != nil {
		t.Fatalf("terminalize tested target: %v", err)
	}
	before, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:historically-tested")
	if err != nil {
		t.Fatalf("read Attempt after target terminalization: %v", err)
	}
	if len(before.Relationships) != 0 {
		t.Fatalf("Attempt retained current tests after target terminalization: %#v", before.Relationships)
	}
	if _, err := tasks.UpdateContinuationStatus(continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt Continuation: %v", err)
	}
	reconciled, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, continuation.ID)
	if err != nil {
		t.Fatalf("reconcile historically tested Attempt: %v", err)
	}
	assertChangeRecords(t, reconciled, 5, [][]any{{"attempt:historically-tested", float64(2)}})
	history, err := service.ReadHistory(ctx, createdProject.ID, "attempt:historically-tested", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read historically tested Attempt history: %v", err)
	}
	if len(history.Items) != 3 || history.Items[1].Record.Status != "interrupted" || history.Items[2].Relation != "tests" || history.Items[2].To != "entity:terminal-tested-target" {
		t.Fatalf("historically tested Attempt history = %#v", history.Items)
	}
	replay, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, continuation.ID)
	if err != nil {
		t.Fatalf("replay historical-tests reconciliation: %v", err)
	}
	assertChangeRecords(t, replay, 5, [][]any{})
}
