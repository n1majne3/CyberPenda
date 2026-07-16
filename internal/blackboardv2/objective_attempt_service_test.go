package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "OWNER TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-owner", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	otherTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "OTHER TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-other", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create other Task: %v", err)
	}
	other, err := tasks.CreateContinuation(otherTask.ID, "profile-other", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create other Continuation: %v", err)
	}
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: otherProject.ID, Goal: "FOREIGN TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-foreign", Runner: task.RunnerSandbox})
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

func TestObjectiveLifecycleAndWorkflowChangeShapesAreClosed(t *testing.T) {
	for _, raw := range []string{
		`{"op":"create","key":"objective:bad","type":"objective","record":{"status":"open","objective":"Inspect login","task_goal":"copied goal"}}`,
		`{"op":"create","key":"attempt:bad","type":"attempt","record":{"status":"open","summary":"Inspect login","task_status":"running"}}`,
		`{"op":"update","key":"objective:bad","version":1,"type":"objective","record":{"status":"resolved"}}`,
		`{"op":"update","key":"attempt:bad","version":1,"type":"attempt","record":{"status":"failed"}}`,
		`{"op":"transition","key":"attempt:bad","version":1,"status":"failed","resolution_summary":"wrong terminal field"}`,
		`{"op":"transition","key":"objective:bad","version":1,"status":"resolved","summary":"wrong terminal field"}`,
	} {
		var change blackboardv2.Change
		if err := json.Unmarshal([]byte(raw), &change); err == nil {
			t.Fatalf("decoded non-closed workflow change: %s", raw)
		}
	}

	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Objective Lifecycle", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-objective-lifecycle",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:old", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the legacy login path"}},
			{Op: "create", Key: "objective:new", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the replacement login path"}},
			{Op: "create", Key: "objective:abandon", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test a removed login path"}},
			{Op: "create", Key: "attempt:not-supersedable", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Attempt records conclude instead of being superseded"}},
		},
	})
	if err != nil {
		t.Fatalf("seed Objective lifecycle: %v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-terminal-create",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:resolved-create", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "resolved", Objective: "Invalid terminal create", ResolutionSummary: "Must transition"}},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("terminal Objective create error = %#v, want semantic_validation", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-mixed-terminal-fields",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:abandon", Version: 1, Status: "abandoned", Summary: "Wrong field", ResolutionSummary: "The endpoint was removed before testing",
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("mixed terminal fields error = %#v, want semantic_validation", err)
	}

	abandoned, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "abandon-objective",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:abandon", Version: 1, Status: "abandoned", ResolutionSummary: "The endpoint was removed before testing",
		}},
	})
	if err != nil {
		t.Fatalf("abandon Objective: %v", err)
	}
	assertChangeRecords(t, abandoned, 5, [][]any{{"objective:abandon", float64(2)}})

	superseded, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "supersede-objective",
		Changes: []blackboardv2.Change{{
			Op: "supersede", Replacement: "objective:new", ReplacementVersion: 1, Replaced: "objective:old", ReplacedVersion: 1,
		}},
	})
	if err != nil {
		t.Fatalf("supersede Objective: %v", err)
	}
	assertChangeRecords(t, superseded, 6, [][]any{{"objective:old", float64(2)}})
	assertRelationResult(t, superseded, [][]any{{"objective:new", "supersedes", "objective:old", float64(1)}})

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-attempt-supersede",
		Changes: []blackboardv2.Change{{
			Op: "supersede", Replacement: "attempt:not-supersedable", ReplacementVersion: 1, Replaced: "objective:new", ReplacedVersion: 1,
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("Attempt supersede error = %#v, want semantic_validation", err)
	}

	history, err := service.ReadHistory(ctx, createdProject.ID, "objective:old", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read superseded Objective history: %v", err)
	}
	if len(history.Items) != 3 || history.Items[1].Record.Status != "superseded" || history.Items[2].Relation != "supersedes" {
		t.Fatalf("superseded Objective history = %#v", history.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", history)
}

func TestObjectiveAndAttemptRelationshipsEnforceEndpointsAndCycles(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Work Relationships", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-work-relationships",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:parent", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Understand the authentication boundary"}},
			{Op: "create", Key: "objective:child", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the administrative login"}},
			{Op: "create", Key: "attempt:login", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login behavior"}},
			{Op: "create", Key: "entity:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /admin/login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative login accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
		},
	})
	if err != nil {
		t.Fatalf("seed work relationships: %v", err)
	}

	related, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "relate-work",
		Changes: []blackboardv2.Change{
			{Op: "relate", From: "objective:child", Relation: "part_of", To: "objective:parent"},
			{Op: "relate", From: "objective:child", Relation: "derived_from", To: "fact:login"},
			{Op: "relate", From: "objective:child", Relation: "depends_on", To: "objective:parent", Reason: "The boundary must be understood first"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "objective:child"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "entity:login"},
			{Op: "relate", From: "attempt:login", Relation: "produced", To: "fact:login"},
		},
	})
	if err != nil {
		t.Fatalf("relate work: %v", err)
	}
	assertRelationResult(t, related, [][]any{
		{"attempt:login", "produced", "fact:login", float64(1)},
		{"attempt:login", "tests", "entity:login", float64(1)},
		{"attempt:login", "tests", "objective:child", float64(1)},
		{"objective:child", "depends_on", "objective:parent", float64(1)},
		{"objective:child", "derived_from", "fact:login", float64(1)},
		{"objective:child", "part_of", "objective:parent", float64(1)},
	})

	for _, tt := range []struct {
		name     string
		relation blackboardv2.Change
	}{
		{name: "objective part_of requires objective target", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "part_of", To: "entity:login"}},
		{name: "objective derived_from requires knowledge target", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "derived_from", To: "entity:login"}},
		{name: "attempt tests rejects attempt target", relation: blackboardv2.Change{Op: "relate", From: "attempt:login", Relation: "tests", To: "attempt:login"}},
		{name: "attempt produced rejects attempt target", relation: blackboardv2.Change{Op: "relate", From: "attempt:login", Relation: "produced", To: "attempt:login"}},
		{name: "part_of cycle", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "part_of", To: "objective:child"}},
		{name: "depends_on cycle", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "depends_on", To: "objective:child"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "reject-" + tt.name,
				Changes:        []blackboardv2.Change{tt.relation},
			})
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" {
				t.Fatalf("Apply error = %#v, want semantic_validation", err)
			}
		})
	}
}

func TestAttemptAndObjectiveTerminalGuardsPreserveSemanticHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Terminal Work", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-terminal-work-valid",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:login", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Determine whether administrative login can be bypassed"}},
			{Op: "create", Key: "objective:follow-up", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Assess post-authentication administrative access"}},
			{Op: "create", Key: "objective:unsatisfied", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Resolve only with semantic support"}},
			{Op: "create", Key: "attempt:login", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login bypasses"}},
			{Op: "create", Key: "attempt:no-tests", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Collecting an outcome without a tested target"}},
			{Op: "create", Key: "attempt:no-outcome", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing without retaining a reusable outcome"}},
			{Op: "create", Key: "entity:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /admin/login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:login-outcome", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative login rejected the tested bypass payloads", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "objective:follow-up", Relation: "part_of", To: "objective:login"},
			{Op: "relate", From: "objective:follow-up", Relation: "depends_on", To: "objective:login"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "objective:login"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "entity:login"},
			{Op: "relate", From: "attempt:login", Relation: "produced", To: "fact:login-outcome"},
			{Op: "relate", From: "attempt:no-tests", Relation: "produced", To: "fact:login-outcome"},
			{Op: "relate", From: "attempt:no-outcome", Relation: "tests", To: "entity:login"},
			{Op: "relate", From: "fact:login-outcome", Relation: "satisfies", To: "objective:login"},
		},
	})
	if err != nil {
		t.Fatalf("seed terminal work: %v", err)
	}

	for _, tt := range []struct {
		key     string
		version int
		status  string
		summary string
	}{
		{key: "attempt:no-tests", version: 1, status: "failed", summary: "No tested target was recorded"},
		{key: "attempt:no-outcome", version: 1, status: "inconclusive", summary: "Testing produced no retained semantic outcome"},
		{key: "attempt:login", version: 1, status: "interrupted", summary: "Runtime stopped unexpectedly"},
	} {
		_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
			Schema:         "semantic-change-batch/v2",
			IdempotencyKey: "reject-terminal-" + tt.key + "-" + tt.status,
			Changes: []blackboardv2.Change{{
				Op: "transition", Key: tt.key, Version: tt.version, Status: tt.status, Summary: tt.summary,
			}},
		})
		var semanticErr *blackboardv2.Error
		if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" {
			t.Fatalf("terminal guard for %s = %#v, want semantic_validation", tt.key, err)
		}
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-unsatisfied-objective",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:unsatisfied", Version: 1, Status: "resolved", ResolutionSummary: "No current satisfying knowledge exists",
		}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" {
		t.Fatalf("unsatisfied resolution error = %#v, want semantic_validation", err)
	}

	terminalAttempt, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "finish-login-attempt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:login", Version: 1, Status: "failed", Summary: "The tested bypass payloads were rejected by administrative login",
		}},
	})
	if err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	assertChangeRecords(t, terminalAttempt, 17, [][]any{{"attempt:login", float64(2)}})
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:login"); !isSemanticCode(err, "not_found") {
		t.Fatalf("terminal Attempt current read = %#v, want not_found", err)
	}
	attemptHistory, err := service.ReadHistory(ctx, createdProject.ID, "attempt:login", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read Attempt history: %v", err)
	}
	if len(attemptHistory.Items) != 5 || attemptHistory.Items[1].Version != 2 || attemptHistory.Items[1].Record.Status != "failed" || attemptHistory.Items[1].Record.Summary != "The tested bypass payloads were rejected by administrative login" {
		t.Fatalf("Attempt history = %#v", attemptHistory.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", attemptHistory)

	terminalObjective, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "resolve-login-objective",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:login", Version: 1, Status: "resolved", ResolutionSummary: "The tested bypasses failed and the retained Fact records the result",
		}},
	})
	if err != nil {
		t.Fatalf("resolve Objective: %v", err)
	}
	assertChangeRecords(t, terminalObjective, 18, [][]any{{"objective:login", float64(2)}})
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:login"); !isSemanticCode(err, "not_found") {
		t.Fatalf("terminal Objective current read = %#v, want not_found", err)
	}
	followUp, err := service.ReadCurrent(ctx, createdProject.ID, "objective:follow-up")
	if err != nil {
		t.Fatalf("read child Objective: %v", err)
	}
	if followUp.Record.Status != "open" {
		t.Fatalf("child Objective status = %q, want open", followUp.Record.Status)
	}

	objectiveHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:login", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read Objective history: %v", err)
	}
	if len(objectiveHistory.Items) != 6 || objectiveHistory.Items[1].Version != 2 || objectiveHistory.Items[1].Record.Status != "resolved" || objectiveHistory.Items[1].Record.ResolutionSummary == "" {
		t.Fatalf("Objective history = %#v", objectiveHistory.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", objectiveHistory)

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	snapshotJSON := string(mustJSON(t, snapshot))
	for _, terminalKey := range []string{"attempt:login", "objective:login"} {
		if contains := strings.Contains(snapshotJSON, terminalKey); contains {
			t.Fatalf("snapshot retained terminal key %q: %s", terminalKey, snapshotJSON)
		}
	}
	for _, openKey := range []string{"attempt:no-outcome", "attempt:no-tests", "objective:follow-up", "objective:unsatisfied"} {
		if !strings.Contains(snapshotJSON, openKey) {
			t.Fatalf("snapshot omitted open key %q: %s", openKey, snapshotJSON)
		}
	}
	assertContractJSON(t, harness, "runtimeSnapshot", snapshot)
}
