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
)

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
	if attemptHistory.Items[2].Relation != "produced" || attemptHistory.Items[2].To != "fact:login-outcome" {
		t.Fatalf("Attempt reusable outcome history = %#v", attemptHistory.Items[2])
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
