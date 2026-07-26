package blackboardconclusion_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestCompiledRuntimeAttemptResultAppliesForContinuationOnEmptyBlackboard(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open Store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Empty assisted Blackboard", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "GOAL MUST NOT BE COPIED", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-assisted", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	validated, err := blackboardconclusion.Decode([]byte(`{
		"schema":"runtime-attempt-result/v1",
		"base_revision":0,
		"attempt":{"key":"attempt:search-sqli","create":true,"summary":"Tested search SQL injection payloads without success.","outcome":"failed"},
		"tested_targets":[{"key":"objective:search-sqli","create_objective":{"objective":"Determine whether search is vulnerable to SQL injection."}}],
		"produced_targets":[]
	}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	batch, err := blackboardconclusion.Compile(validated.Result, "assisted-conclusion:receipt-empty")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	service := blackboardv2.NewService(db)
	applied, err := service.ApplyForContinuationAtRevision(ctx, createdProject.ID, continuation.ID, validated.Result.BaseRevision, batch)
	if err != nil {
		t.Fatalf("ApplyForContinuationAtRevision() error = %v", err)
	}
	if applied.Revision != 4 {
		t.Fatalf("applied revision = %d, want 4", applied.Revision)
	}
	objective, err := service.ReadCurrent(ctx, createdProject.ID, "objective:search-sqli")
	if err != nil || objective.Type != "objective" || objective.Version != 1 || objective.Record.Status != "open" {
		t.Fatalf("current Objective = %#v, %v", objective, err)
	}
	if objective.Record.Objective == createdTask.Goal {
		t.Fatal("Task Goal was copied into the Exploration Objective")
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:search-sqli"); !semanticErrorCode(err, "not_found") {
		t.Fatalf("terminal Attempt remained current: %v", err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "attempt:search-sqli", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read Attempt history: %v", err)
	}
	if len(history.Items) != 3 || history.Items[1].Record.Status != "failed" || history.Items[2].Relation != "tests" || history.Items[2].To != "objective:search-sqli" {
		t.Fatalf("Attempt history = %#v", history.Items)
	}
}

func semanticErrorCode(err error, code string) bool {
	var semanticErr *blackboardv2.Error
	return errors.As(err, &semanticErr) && semanticErr.Code == code
}
