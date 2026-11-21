package blackboardv2_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestOperatorFirstEvidenceRetentionFromTerminalDisabledSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	projectOwner, err := projects.Create("Selected output", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	tasks := task.NewService(db, projects)
	source, err := tasks.Create(task.CreateRequest{
		ProjectID: projectOwner.ID, Type: task.TypePentest, Goal: "Capture selected output", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardMode: task.BlackboardModeDisabled},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := tasks.CreateContinuation(source.ID, "profile-output", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateContinuationStatus(continuation.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	board := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: root, ArtifactRoot: root})
	if _, err := board.ApplyForOperator(ctx, projectOwner.ID, "operator", blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-operator-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:output", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Review selected output"}},
			{Op: "create", Key: "attempt:output", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Retain selected output"}},
			{Op: "relate", From: "attempt:output", Relation: "tests", To: "objective:output"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(root, source.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "output.txt"), []byte("selected proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "selected-output", Key: "evidence:output", Attempt: "attempt:output",
		SourcePath: "output.txt", ArtifactType: "text", Summary: "Selected output from a completed Runtime",
	}
	result, err := board.RetainTaskEvidenceForOperator(ctx, projectOwner.ID, source.ID, continuation.ID, "operator", request)
	if err != nil {
		t.Fatalf("first operator retention from terminal Disabled source: %v", err)
	}
	replay, err := board.RetainTaskEvidenceForOperator(ctx, projectOwner.ID, source.ID, continuation.ID, "operator", request)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("operator retention replay = %#v, %v; want %#v", replay, err, result)
	}
	var actor, taskID, continuationID string
	if err := db.QueryRow(`SELECT actor_id,source_task_id,source_continuation_id FROM blackboard_v2_operator_evidence_origins WHERE project_id=? AND key=?`, projectOwner.ID, request.Key).Scan(&actor, &taskID, &continuationID); err != nil {
		t.Fatal(err)
	}
	if actor != "operator" || taskID != source.ID || continuationID != continuation.ID {
		t.Fatalf("operator Evidence origin = %q, %q, %q", actor, taskID, continuationID)
	}
	request.IdempotencyKey, request.Key = "runtime-closed", "evidence:runtime-closed"
	if _, err := board.RetainEvidenceForContinuation(ctx, projectOwner.ID, continuation.ID, request); !isSemanticCode(err, "closed_continuation") {
		t.Fatalf("Runtime retention from terminal source = %v", err)
	}
	request.IdempotencyKey, request.Key = "wrong-operator", "evidence:wrong-operator"
	if _, err := board.RetainTaskEvidenceForOperator(ctx, projectOwner.ID, source.ID, continuation.ID, "other-operator", request); !isSemanticCode(err, "authority_denied") {
		t.Fatalf("retention with another operator's Attempt = %v", err)
	}
}
