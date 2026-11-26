package blackboardv2_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestOpenObjectivesAndAttemptsAreVersionedCurrentWork(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Current Work", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	created, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-current-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:zeta", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Map the administrative login surface"}},
			{Op: "create", Key: "objective:alpha", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Identify authentication entry points"}},
			{Op: "create", Key: "attempt:zeta", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login behavior"}},
			{Op: "create", Key: "attempt:alpha", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Enumerating authentication endpoints"}},
		},
	})
	if err != nil {
		t.Fatalf("create current work: %v", err)
	}
	assertChangeRecords(t, created, 4, [][]any{
		{"attempt:alpha", float64(1)},
		{"attempt:zeta", float64(1)},
		{"objective:alpha", float64(1)},
		{"objective:zeta", float64(1)},
	})

	updated, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "update-current-work",
		Changes: []blackboardv2.Change{
			{Op: "update", Key: "objective:alpha", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{Objective: strPtr("Identify and classify authentication entry points")}},
			{Op: "update", Key: "attempt:alpha", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Enumerating and classifying authentication endpoints")}},
		},
	})
	if err != nil {
		t.Fatalf("update current work: %v", err)
	}
	assertChangeRecords(t, updated, 6, [][]any{{"attempt:alpha", float64(2)}, {"objective:alpha", float64(2)}})

	objective, err := service.ReadCurrent(ctx, createdProject.ID, "objective:alpha")
	if err != nil {
		t.Fatalf("read objective: %v", err)
	}
	if objective.Type != "objective" || objective.Version != 2 || objective.Record.Status != "open" || objective.Record.Objective != "Identify and classify authentication entry points" {
		t.Fatalf("objective detail = %#v", objective)
	}
	assertContractJSON(t, harness, "currentDetail", objective)

	attempt, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:alpha")
	if err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if attempt.Type != "attempt" || attempt.Version != 2 || attempt.Record.Status != "open" || attempt.Record.Summary != "Enumerating and classifying authentication endpoints" {
		t.Fatalf("attempt detail = %#v", attempt)
	}
	assertContractJSON(t, harness, "currentDetail", attempt)

	objectiveHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:alpha", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read objective history: %v", err)
	}
	if len(objectiveHistory.Items) != 1 || objectiveHistory.Items[0].Version != 1 || objectiveHistory.Items[0].Record.Objective != "Identify authentication entry points" {
		t.Fatalf("objective history = %#v", objectiveHistory.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", objectiveHistory)

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	wantSnapshot := `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":6,"work":{"objectives":{"objective:alpha":{"version":2,"status":"open","objective":"Identify and classify authentication entry points"},"objective:zeta":{"version":1,"status":"open","objective":"Map the administrative login surface"}},"attempts":{"attempt:alpha":{"version":2,"status":"open","summary":"Enumerating and classifying authentication endpoints"},"attempt:zeta":{"version":1,"status":"open","summary":"Testing administrative login behavior"}}},"knowledge":{},"relations":[]}`
	if got := string(mustJSON(t, snapshot)); got != wantSnapshot {
		t.Fatalf("snapshot JSON = %s, want %s", got, wantSnapshot)
	}
	assertContractJSON(t, harness, "runtimeSnapshot", snapshot)
}

func TestTrustedContinuationOwnsAttemptsWithoutLeakingTaskState(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Trusted Ownership", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	otherProject, err := projects.Create("Other Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	tasks := task.NewService(db, projects)
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "OWNER TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-owner", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	otherTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "OTHER TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-other", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create other Task: %v", err)
	}
	other, err := tasks.CreateContinuation(otherTask.ID, "profile-other", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create other Continuation: %v", err)
	}
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: otherProject.ID, Type: task.TypePentest, Goal: "FOREIGN TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-foreign", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreign, err := tasks.CreateContinuation(foreignTask.ID, "profile-foreign", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)

	ownerCreate := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-create-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:login", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test administrative login"}},
			{Op: "create", Key: "attempt:login", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login"}},
			{Op: "create", Key: "fact:outcome", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative login rejected the payload", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "objective:login"},
			{Op: "relate", From: "attempt:login", Relation: "produced", To: "fact:outcome"},
		},
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, ownerCreate); err != nil {
		t.Fatalf("owner creates Attempt: %v", err)
	}

	for _, tt := range []struct {
		name           string
		continuationID string
		batch          blackboardv2.ChangeBatch
	}{
		{name: "other continuation update", continuationID: other.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "other-update", Changes: []blackboardv2.Change{{Op: "update", Key: "attempt:login", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Other continuation changed the Attempt")}}}}},
		{name: "other continuation relation", continuationID: other.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "other-relation", Changes: []blackboardv2.Change{{Op: "relate", From: "attempt:login", Relation: "tests", To: "fact:outcome"}}}},
		{name: "other continuation transition", continuationID: other.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "other-transition", Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:login", Version: 1, Status: "failed", Summary: "Other continuation tried to finish the Attempt"}}}},
		{name: "other continuation replay", continuationID: other.ID, batch: ownerCreate},
		{name: "foreign project continuation", continuationID: foreign.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "foreign-create", Changes: []blackboardv2.Change{{Op: "create", Key: "objective:foreign", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must not cross projects"}}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.ApplyForContinuation(ctx, createdProject.ID, tt.continuationID, tt.batch)
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "authority_denied" {
				t.Fatalf("ApplyForContinuation error = %#v, want authority_denied", err)
			}
		})
	}

	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-update-attempt",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "attempt:login", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Owner continued testing administrative login")},
		}},
	}); err != nil {
		t.Fatalf("owner updates Attempt: %v", err)
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-finishes-attempt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:login", Version: 2, Status: "failed", Summary: "Administrative login rejected the tested payload",
		}},
	}); err != nil {
		t.Fatalf("owner finishes Attempt: %v", err)
	}

	history, err := service.ReadHistory(ctx, createdProject.ID, "attempt:login", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read owned Attempt history: %v", err)
	}
	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	visible := string(mustJSON(t, history)) + string(mustJSON(t, snapshot))
	for _, forbidden := range []string{owner.ID, other.ID, foreign.ID, ownerTask.ID, otherTask.ID, "OWNER TASK GOAL MUST NOT LEAK", "OTHER TASK GOAL MUST NOT LEAK", "FOREIGN TASK GOAL MUST NOT LEAK", "profile-owner", "profile-other"} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("Blackboard DTO leaked trusted Task/Continuation state %q: %s", forbidden, visible)
		}
	}
}
