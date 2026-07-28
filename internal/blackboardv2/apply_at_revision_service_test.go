package blackboardv2_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestApplyForContinuationAtRevisionReplaysBeforeCheckingBaseRevision(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Assisted conclusion", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "TASK GOAL MUST NOT BECOME AN OBJECTIVE", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-assisted", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)
	batch := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "assisted-conclusion:receipt-1",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:search-sqli", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Determine whether search is vulnerable to SQL injection."}},
			{Op: "create", Key: "attempt:search-sqli", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Tested search SQL injection payloads."}},
			{Op: "relate", From: "attempt:search-sqli", Relation: "tests", To: "objective:search-sqli"},
			{Op: "transition", Key: "attempt:search-sqli", Version: 1, Status: "failed", Summary: "Tested search SQL injection payloads."},
		},
	}

	first, err := service.ApplyForContinuationAtRevision(ctx, createdProject.ID, continuation.ID, 0, batch)
	if err != nil {
		t.Fatalf("ApplyForContinuationAtRevision() error = %v", err)
	}
	if first.Revision != 4 {
		t.Fatalf("applied revision = %d, want 4", first.Revision)
	}
	replayed, err := service.ApplyForContinuationAtRevision(ctx, createdProject.ID, continuation.ID, 0, batch)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("exact replay = %#v, %v; want %#v", replayed, err, first)
	}

	_, err = service.ApplyForContinuationAtRevision(ctx, createdProject.ID, continuation.ID, 0, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "assisted-conclusion:receipt-2",
		Changes: []blackboardv2.Change{{Op: "create", Key: "objective:must-not-commit", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must not commit across a stale base revision."}}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "version_conflict" || semanticErr.Path != "base_revision" {
		t.Fatalf("stale base error = %#v, want version_conflict at base_revision", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:must-not-commit"); !isSemanticCode(err, "not_found") {
		t.Fatalf("stale batch changed Blackboard: %v", err)
	}
}
