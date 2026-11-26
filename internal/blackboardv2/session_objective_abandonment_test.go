package blackboardv2_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/session"
	"pentest/internal/store"
)

func TestSessionAbandonedObjectiveRequiresInvalidatingFact(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "session-blackboard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := session.NewService(db, filepath.Join(t.TempDir(), "sessions"))
	owner, err := sessions.Create(session.CreateRequest{Input: "remember invalidated work"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	board := blackboardv2.NewService(db)
	ownerContract := owner.OwnerContract()

	_, err = board.ApplyForOwner(ctx, ownerContract, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-session-abandonment-proof",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:invalidated", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Remember the removed path"}},
			{Op: "create", Key: "objective:without-proof", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Remember the unproven path"}},
			{Op: "create", Key: "fact:invalidating", Type: "fact", Record: blackboardv2.FactRecord{Category: "note", Summary: "The path was removed", Confidence: "tentative"}},
			{Op: "relate", From: "fact:invalidating", Relation: "invalidates", To: "objective:invalidated"},
		},
	})
	if err != nil {
		t.Fatalf("seed Session abandonment proof: %v", err)
	}

	_, err = board.ApplyForOwner(ctx, ownerContract, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-session-abandonment-without-proof",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:without-proof", Version: 1, Status: "abandoned", ResolutionSummary: "No invalidating Fact proves why this work stops",
		}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].status" {
		t.Fatalf("Session abandonment without invalidates error = %#v, want semantic_validation on changes[0].status", err)
	}

	_, err = board.ApplyForOwner(ctx, ownerContract, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "accept-session-abandonment-with-proof",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:invalidated", Version: 1, Status: "abandoned", ResolutionSummary: "The retained Fact proves that the path was removed",
		}},
	})
	if err != nil {
		t.Fatalf("abandon Session Objective with invalidates proof: %v", err)
	}
}
