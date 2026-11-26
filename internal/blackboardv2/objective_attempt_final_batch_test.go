package blackboardv2_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestRuntimeAttemptCreateRequiresCurrentTestsInFinalBatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Runtime Attempt Final State", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	foreignProject, err := projects.Create("Foreign Runtime Attempt", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Create a tested Attempt", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-attempt-final", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: foreignProject.ID, Type: task.TypePentest, Goal: "Foreign Attempt", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreign, err := tasks.CreateContinuation(foreignTask.ID, "profile-foreign-attempt", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-runtime-attempt-target",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "objective:runtime-attempt-target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Provide a tested target"},
		}},
	}); err != nil {
		t.Fatalf("create Runtime Attempt target: %v", err)
	}

	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-create-only-runtime-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:runtime-attempt-rollback", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Must roll back", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "attempt:runtime-untested", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Would strand untested Current Work"}},
		},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[1]" {
		t.Fatalf("create-only Runtime Attempt error = %#v, want semantic_validation on changes[1]", err)
	}
	for _, key := range []string{"entity:runtime-attempt-rollback", "attempt:runtime-untested"} {
		if _, err := service.ReadCurrent(ctx, createdProject.ID, key); !isSemanticCode(err, "not_found") {
			t.Fatalf("invalid Runtime Attempt batch retained %s: %#v", key, err)
		}
	}

	valid := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-tested-runtime-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:runtime-tested", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Tests the required target"}},
			{Op: "relate", From: "attempt:runtime-tested", Relation: "tests", To: "objective:runtime-attempt-target"},
		},
	}
	created, err := service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, valid)
	if err != nil {
		t.Fatalf("create tested Runtime Attempt: %v", err)
	}
	if detail, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:runtime-tested"); err != nil || len(detail.Relationships) != 1 {
		t.Fatalf("tested Runtime Attempt detail = %#v, %v", detail, err)
	}
	if _, err := tasks.UpdateContinuationStatus(continuation.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt Continuation: %v", err)
	}
	if replay, err := service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, valid); err != nil || !reflect.DeepEqual(replay, created) {
		t.Fatalf("closed Continuation exact replay = %#v, %v; want %#v", replay, err, created)
	}

	_, err = service.ApplyForContinuation(ctx, createdProject.ID, foreign.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-foreign-runtime-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:foreign-runtime", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Must remain Project-isolated"}},
			{Op: "relate", From: "attempt:foreign-runtime", Relation: "tests", To: "objective:runtime-attempt-target"},
		},
	})
	if !isSemanticCode(err, "authority_denied") {
		t.Fatalf("foreign Continuation Runtime Attempt error = %#v, want authority_denied", err)
	}
}

func TestTerminalAttemptDoesNotRequireProducedOutcome(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("No Reusable Attempt Outcome", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-no-outcome-attempts",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise a target with no reusable result"}},
			{Op: "create", Key: "attempt:blocked", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing stopped at an authorization boundary"}},
			{Op: "create", Key: "attempt:inconclusive", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing could not distinguish the observed behavior"}},
			{Op: "create", Key: "attempt:failed", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing did not reproduce the suspected behavior"}},
			{Op: "relate", From: "attempt:blocked", Relation: "tests", To: "objective:target"},
			{Op: "relate", From: "attempt:inconclusive", Relation: "tests", To: "objective:target"},
			{Op: "relate", From: "attempt:failed", Relation: "tests", To: "objective:target"},
		},
	})
	if err != nil {
		t.Fatalf("seed Attempts without outcomes: %v", err)
	}

	for index, tt := range []struct {
		key     string
		status  string
		summary string
	}{
		{key: "attempt:blocked", status: "blocked", summary: "Authorization prevented the tested action and no reusable result was produced"},
		{key: "attempt:inconclusive", status: "inconclusive", summary: "The tested behavior remained ambiguous and yielded no reusable result"},
		{key: "attempt:failed", status: "failed", summary: "The tested behavior was not reproduced and yielded no reusable result"},
	} {
		result, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
			Schema:         "semantic-change-batch/v2",
			IdempotencyKey: "terminal-no-outcome-" + tt.status,
			Changes: []blackboardv2.Change{{
				Op: "transition", Key: tt.key, Version: 1, Status: tt.status, Summary: tt.summary,
			}},
		})
		if err != nil {
			t.Fatalf("terminal %s Attempt without outcome: %v", tt.status, err)
		}
		assertChangeRecords(t, result, 8+index, [][]any{{tt.key, float64(2)}})
		history, err := service.ReadHistory(ctx, createdProject.ID, tt.key, blackboardv2.HistoryOptions{})
		if err != nil {
			t.Fatalf("read %s Attempt history: %v", tt.status, err)
		}
		if len(history.Items) != 3 || history.Items[1].Record.Status != tt.status || history.Items[1].Record.Summary != tt.summary || history.Items[2].Relation != "tests" {
			t.Fatalf("%s Attempt history = %#v", tt.status, history.Items)
		}
	}
}

func TestSucceededAttemptRequiresProducedOutcomeInFinalBatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Successful Attempt Outcome", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-success-outcome-guard",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Establish a reusable authentication conclusion"}},
			{Op: "create", Key: "attempt:missing-outcome", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing authentication behavior"}},
			{Op: "relate", From: "attempt:missing-outcome", Relation: "tests", To: "objective:target"},
		},
	})
	if err != nil {
		t.Fatalf("seed success outcome guard: %v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-success-without-outcome",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:must-roll-back", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "This Fact must roll back with the invalid success", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "transition", Key: "attempt:missing-outcome", Version: 1, Status: "succeeded", Summary: "Authentication testing succeeded without a reusable outcome"},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("succeeded Attempt without produced error = %#v, want semantic_validation", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "fact:must-roll-back"); !isSemanticCode(err, "not_found") {
		t.Fatalf("invalid success batch retained Fact: %#v", err)
	}
	if attempt, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:missing-outcome"); err != nil || attempt.Version != 1 || attempt.Record.Status != "open" {
		t.Fatalf("invalid success batch changed Attempt = %#v, %v", attempt, err)
	}

	succeeded, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "succeed-with-final-batch-outcome",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:with-outcome", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative authentication"}},
			{Op: "create", Key: "fact:admin-auth", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative authentication rejects invalid credentials", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:with-outcome", Relation: "tests", To: "objective:target"},
			{Op: "relate", From: "attempt:with-outcome", Relation: "produced", To: "fact:admin-auth"},
			{Op: "transition", Key: "attempt:with-outcome", Version: 1, Status: "succeeded", Summary: "Administrative authentication rejected the tested invalid credentials"},
		},
	})
	if err != nil {
		t.Fatalf("succeed Attempt with same-batch outcome: %v", err)
	}
	assertChangeRecords(t, succeeded, 8, [][]any{{"attempt:with-outcome", float64(2)}, {"fact:admin-auth", float64(1)}})
	assertRelationResult(t, succeeded, [][]any{
		{"attempt:with-outcome", "produced", "fact:admin-auth", float64(1)},
		{"attempt:with-outcome", "tests", "objective:target", float64(1)},
	})
	if fact, err := service.ReadCurrent(ctx, createdProject.ID, "fact:admin-auth"); err != nil || fact.Record.Summary != "Administrative authentication rejects invalid credentials" {
		t.Fatalf("successful Attempt outcome is not current = %#v, %v", fact, err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "attempt:with-outcome", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read succeeded Attempt history: %v", err)
	}
	if len(history.Items) != 4 || history.Items[1].Record.Status != "succeeded" || history.Items[2].Relation != "produced" || history.Items[2].To != "fact:admin-auth" {
		t.Fatalf("succeeded Attempt history = %#v", history.Items)
	}
}
