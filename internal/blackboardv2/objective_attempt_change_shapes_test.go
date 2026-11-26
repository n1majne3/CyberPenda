package blackboardv2_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestObjectiveAndAttemptUpdatesRequirePartialDTOs(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Closed Work Update DTOs", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-closed-work-update-dtos",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:record-value", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Reject a complete Objective value"}},
			{Op: "create", Key: "objective:record-pointer", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Reject a complete Objective pointer"}},
			{Op: "create", Key: "attempt:record-value", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Reject a complete Attempt value"}},
			{Op: "create", Key: "attempt:record-pointer", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Reject a complete Attempt pointer"}},
			{Op: "create", Key: "objective:patch-value", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Accept an Objective patch value"}},
			{Op: "create", Key: "objective:patch-pointer", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Accept an Objective patch pointer"}},
			{Op: "create", Key: "attempt:patch-value", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Accept an Attempt patch value"}},
			{Op: "create", Key: "attempt:patch-pointer", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Accept an Attempt patch pointer"}},
		},
	})
	if err != nil {
		t.Fatalf("seed closed work update DTOs: %v", err)
	}

	objectivePointer := &blackboardv2.ObjectiveRecord{Status: "open", Objective: "Complete Objective pointer must be rejected"}
	attemptPointer := &blackboardv2.AttemptRecord{Status: "open", Summary: "Complete Attempt pointer must be rejected"}
	for index, tt := range []struct {
		name   string
		change blackboardv2.Change
	}{
		{name: "Objective record value", change: blackboardv2.Change{Op: "update", Key: "objective:record-value", Version: 1, Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Complete Objective value must be rejected"}}},
		{name: "Objective record pointer", change: blackboardv2.Change{Op: "update", Key: "objective:record-pointer", Version: 1, Type: "objective", Record: objectivePointer}},
		{name: "Attempt record value", change: blackboardv2.Change{Op: "update", Key: "attempt:record-value", Version: 1, Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Complete Attempt value must be rejected"}}},
		{name: "Attempt record pointer", change: blackboardv2.Change{Op: "update", Key: "attempt:record-pointer", Version: 1, Type: "attempt", Record: attemptPointer}},
	} {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: fmt.Sprintf("reject-complete-work-update-%d", index),
				Changes:        []blackboardv2.Change{tt.change},
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("complete update DTO error = %#v, want semantic_validation", err)
			}
		})
	}

	objectiveText := "Updated through an Objective patch pointer"
	attemptSummary := "Updated through an Attempt patch pointer"
	updated, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "accept-work-partial-dtos",
		Changes: []blackboardv2.Change{
			{Op: "update", Key: "objective:patch-value", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{Objective: strPtr("Updated through an Objective patch value")}},
			{Op: "update", Key: "objective:patch-pointer", Version: 1, Type: "objective", Record: &blackboardv2.ObjectivePatch{Objective: &objectiveText}},
			{Op: "update", Key: "attempt:patch-value", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Updated through an Attempt patch value")}},
			{Op: "update", Key: "attempt:patch-pointer", Version: 1, Type: "attempt", Record: &blackboardv2.AttemptPatch{Summary: &attemptSummary}},
		},
	})
	if err != nil {
		t.Fatalf("apply valid work partial DTOs: %v", err)
	}
	assertChangeRecords(t, updated, 12, [][]any{
		{"attempt:patch-pointer", float64(2)},
		{"attempt:patch-value", float64(2)},
		{"objective:patch-pointer", float64(2)},
		{"objective:patch-value", float64(2)},
	})
}

func TestProgrammaticChangeShapesRejectEveryUnrelatedField(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Closed Programmatic Changes", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-programmatic-change-shapes",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:shape-target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise closed programmatic operation shapes"}},
			{Op: "create", Key: "objective:shape-update", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Remain unchanged when an unrelated field is ignored"}},
			{Op: "create", Key: "attempt:shape-transition", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Remain open behind a stale transition version"}},
			{Op: "create", Key: "objective:shape-replacement", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replacement Objective"}},
			{Op: "create", Key: "objective:shape-replaced", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replaced Objective"}},
		},
	})
	if err != nil {
		t.Fatalf("seed programmatic change shapes: %v", err)
	}

	type populatedField struct {
		name string
		set  func(*blackboardv2.Change)
	}
	fields := []populatedField{
		{name: "key", set: func(change *blackboardv2.Change) { change.Key = "objective:unrelated" }},
		{name: "version", set: func(change *blackboardv2.Change) { change.Version = 1 }},
		{name: "type", set: func(change *blackboardv2.Change) { change.Type = "objective" }},
		{name: "record", set: func(change *blackboardv2.Change) {
			change.Record = blackboardv2.ObjectivePatch{Objective: strPtr("Unrelated record")}
		}},
		{name: "clear", set: func(change *blackboardv2.Change) { change.Clear = []string{"body"} }},
		{name: "from", set: func(change *blackboardv2.Change) { change.From = "attempt:shape-transition" }},
		{name: "relation", set: func(change *blackboardv2.Change) { change.Relation = "tests" }},
		{name: "to", set: func(change *blackboardv2.Change) { change.To = "objective:shape-target" }},
		{name: "reason", set: func(change *blackboardv2.Change) { change.Reason = "Unrelated relationship reason" }},
		{name: "status", set: func(change *blackboardv2.Change) { change.Status = "failed" }},
		{name: "summary", set: func(change *blackboardv2.Change) { change.Summary = "Unrelated terminal summary" }},
		{name: "resolution_summary", set: func(change *blackboardv2.Change) { change.ResolutionSummary = "Unrelated resolution summary" }},
		{name: "replacement", set: func(change *blackboardv2.Change) { change.Replacement = "objective:shape-replacement" }},
		{name: "replacement_version", set: func(change *blackboardv2.Change) { change.ReplacementVersion = 1 }},
		{name: "replaced", set: func(change *blackboardv2.Change) { change.Replaced = "objective:shape-replaced" }},
		{name: "replaced_version", set: func(change *blackboardv2.Change) { change.ReplacedVersion = 1 }},
	}
	type operationShape struct {
		name    string
		allowed map[string]bool
		base    func(string) blackboardv2.Change
	}
	operations := []operationShape{
		{
			name:    "create",
			allowed: map[string]bool{"key": true, "type": true, "record": true},
			base: func(field string) blackboardv2.Change {
				return blackboardv2.Change{Op: "create", Key: "objective:shape-create-" + field, Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must reject the unrelated create field"}}
			},
		},
		{
			name:    "update",
			allowed: map[string]bool{"key": true, "version": true, "type": true, "record": true, "clear": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "update", Key: "objective:shape-update", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{Objective: strPtr("Remain unchanged when an unrelated field is ignored")}}
			},
		},
		{
			name:    "relate",
			allowed: map[string]bool{"version": true, "from": true, "relation": true, "to": true, "reason": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "relate", From: "attempt:shape-transition", Relation: "tests", To: "objective:shape-target", Version: 1}
			},
		},
		{
			name:    "transition",
			allowed: map[string]bool{"key": true, "version": true, "status": true, "summary": true, "resolution_summary": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "transition", Key: "attempt:shape-transition", Version: 99, Status: "failed", Summary: "Stale transition must not execute"}
			},
		},
		{
			name:    "supersede",
			allowed: map[string]bool{"replacement": true, "replacement_version": true, "replaced": true, "replaced_version": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "supersede", Replacement: "objective:shape-replacement", ReplacementVersion: 1, Replaced: "objective:shape-replaced", ReplacedVersion: 99}
			},
		},
	}

	for _, operation := range operations {
		for _, field := range fields {
			if operation.allowed[field.name] {
				continue
			}
			t.Run(operation.name+" rejects "+field.name, func(t *testing.T) {
				change := operation.base(field.name)
				field.set(&change)
				_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
					Schema:         "semantic-change-batch/v2",
					IdempotencyKey: "reject-programmatic-" + operation.name + "-" + field.name,
					Changes:        []blackboardv2.Change{change},
				})
				var semanticErr *blackboardv2.Error
				if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0]."+field.name {
					t.Fatalf("unrelated %s field error = %#v, want semantic_validation on changes[0].%s", field.name, err, field.name)
				}
			})
		}
	}
}
