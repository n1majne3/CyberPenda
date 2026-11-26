package blackboardv2_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestAbandonedObjectiveRequiresInvalidatingFact(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Objective abandonment proof", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	seeded, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-objective-abandonment-proof",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:invalidated", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the deprecated administrative endpoint"}},
			{Op: "create", Key: "objective:without-proof", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the unattainable legacy path"}},
			{Op: "create", Key: "fact:invalidating", Type: "fact", Record: blackboardv2.FactRecord{Category: "authorization", Summary: "The administrative endpoint was removed before testing started", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:invalidating", Relation: "invalidates", To: "objective:invalidated"},
		},
	})
	if err != nil {
		t.Fatalf("seed abandonment proof: %v", err)
	}
	if len(seeded.Relations) != 1 || seeded.Relations[0][1] != "invalidates" {
		t.Fatalf("seeded invalidates relationship = %#v", seeded.Relations)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-abandonment-without-proof",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:without-proof", Version: 1, Status: "abandoned", ResolutionSummary: "No invalidating Fact proves why this work stops",
		}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].status" {
		t.Fatalf("abandonment without invalidates error = %#v, want semantic_validation on changes[0].status", err)
	}

	abandoned, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "accept-abandonment-with-proof",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:invalidated", Version: 1, Status: "abandoned", ResolutionSummary: "The retained Fact proves that the endpoint no longer exists",
		}},
	})
	if err != nil {
		t.Fatalf("abandon objective with invalidates proof: %v", err)
	}
	if len(abandoned.Records) != 1 || abandoned.Records[0][0] != "objective:invalidated" {
		t.Fatalf("abandoned Objective result = %#v", abandoned.Records)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:invalidated"); !isSemanticCode(err, "not_found") {
		t.Fatalf("abandoned Objective current read = %#v, want not_found", err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "objective:invalidated", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read abandoned Objective history: %v", err)
	}
	if len(history.Items) < 2 || history.Items[1].Record.Status != "abandoned" {
		t.Fatalf("abandoned Objective history = %#v", history.Items)
	}
}
