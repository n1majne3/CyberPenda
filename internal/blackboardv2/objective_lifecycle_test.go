package blackboardv2_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestObjectiveLifecycleAndWorkflowChangeShapesAreClosed(t *testing.T) {
	for _, raw := range []string{
		`{"op":"create","key":"objective:bad","type":"objective","record":{"status":"open","objective":"Inspect login","task_goal":"copied goal"}}`,
		`{"op":"create","key":"objective:bad","type":"objective","record":{"status":"open","objective":"Inspect login","resolution_summary":""}}`,
		`{"op":"create","key":"attempt:bad","type":"attempt","record":{"status":"open","summary":"Inspect login","task_status":"running"}}`,
		`{"op":"update","key":"objective:bad","version":1,"type":"objective","record":{"status":"resolved"}}`,
		`{"op":"update","key":"attempt:bad","version":1,"type":"attempt","record":{"status":"failed"}}`,
		`{"op":"update","key":"objective:bad","version":1,"type":"objective","record":{}}`,
		`{"op":"update","key":"attempt:bad","version":1,"type":"attempt","record":{}}`,
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

	for _, tt := range []struct {
		name   string
		change blackboardv2.Change
	}{
		{name: "Objective", change: blackboardv2.Change{Op: "update", Key: "objective:old", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{}}},
		{name: "Attempt", change: blackboardv2.Change{Op: "update", Key: "attempt:not-supersedable", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{}}},
	} {
		t.Run("rejects empty programmatic "+tt.name+" partial", func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "reject-empty-" + strings.ToLower(tt.name) + "-partial",
				Changes:        []blackboardv2.Change{tt.change},
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("empty %s partial error = %#v, want semantic_validation", tt.name, err)
			}
		})
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
	abandonedHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:abandon", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read abandoned Objective history: %v", err)
	}
	if len(abandonedHistory.Items) != 2 || abandonedHistory.Items[1].Record.Status != "abandoned" || abandonedHistory.Items[1].Record.ResolutionSummary != "The endpoint was removed before testing" {
		t.Fatalf("abandoned Objective history = %#v", abandonedHistory.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", abandonedHistory)
	noMeaningSnapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("snapshot after no-meaning abandonment: %v", err)
	}
	if len(noMeaningSnapshot.Knowledge.Facts) != 0 {
		t.Fatalf("no-meaning abandonment manufactured Facts: %#v", noMeaningSnapshot.Knowledge.Facts)
	}

	meaningfulAbandonment, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "abandon-objective-with-reusable-meaning",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:meaningful-abandonment", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the retired administrative login endpoint"}},
			{Op: "create", Key: "fact:retired-admin-login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "The legacy administrative login endpoint was removed from the current deployment", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "objective:meaningful-abandonment", Relation: "derived_from", To: "fact:retired-admin-login"},
			{Op: "transition", Key: "objective:meaningful-abandonment", Version: 1, Status: "abandoned", ResolutionSummary: "The endpoint retirement is reusable deployment knowledge"},
		},
	})
	if err != nil {
		t.Fatalf("abandon Objective with reusable meaning: %v", err)
	}
	assertChangeRecords(t, meaningfulAbandonment, 9, [][]any{{"fact:retired-admin-login", float64(1)}, {"objective:meaningful-abandonment", float64(2)}})
	assertRelationResult(t, meaningfulAbandonment, [][]any{{"objective:meaningful-abandonment", "derived_from", "fact:retired-admin-login", float64(1)}})
	if fact, err := service.ReadCurrent(ctx, createdProject.ID, "fact:retired-admin-login"); err != nil || fact.Record.Summary != "The legacy administrative login endpoint was removed from the current deployment" {
		t.Fatalf("reusable abandonment Fact is not current = %#v, %v", fact, err)
	}
	meaningfulHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:meaningful-abandonment", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read meaningful abandoned Objective history: %v", err)
	}
	if len(meaningfulHistory.Items) != 3 || meaningfulHistory.Items[2].Relation != "derived_from" || meaningfulHistory.Items[2].To != "fact:retired-admin-login" {
		t.Fatalf("meaningful abandoned Objective history = %#v", meaningfulHistory.Items)
	}

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
	assertChangeRecords(t, superseded, 10, [][]any{{"objective:old", float64(2)}})
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
