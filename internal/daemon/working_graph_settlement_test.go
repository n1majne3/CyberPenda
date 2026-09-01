package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/workinggraph"
)

func TestTaskWorkingGraphSettlementCompilesOutboxBeforeLifecycleBoundary(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runs")
	server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Working Graph", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "settle graph", RuntimeProfileID: profile.ID,
		Runner: task.RunnerHost, RunControls: task.RunControls{BlackboardMode: task.BlackboardModeWorkingGraph},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := server.blackboardV2Continuity.CreateContinuation(context.Background(), blackboardv2.ContinuationLaunchRequest{
		ProjectID: createdProject.ID, TaskID: created.ID, RuntimeProfileID: profile.ID,
		RuntimeProvider: string(runtimeprofile.ProviderCodex), Runner: task.RunnerHost,
		RuntimeConfig: map[string]any{"provider": "codex", "model": "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(launch.Continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(runtimeRoot, created.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	projection, err := server.workingGraph.Prepare(context.Background(), workinggraph.OwnerContext{
		Owner: created.OwnerContract(workdir), ContinuationID: launch.Continuation.ID, Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workinggraph.Emit(projection.Outbox, workinggraph.OwnerKindTask, workinggraph.IntentInput{
		Kind: workinggraph.IntentSemanticChanges,
		Payload: map[string]any{"changes": []any{map[string]any{
			"op": "upsert", "key": "fact:settled", "type": "fact",
			"record": map[string]any{"category": "asset", "summary": "settled from outbox", "confidence": "tentative", "scope_status": "in_scope"},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	settled, err := server.settleTaskWorkingGraph(context.Background(), created, false)
	if err != nil || !settled {
		t.Fatalf("settled=%v err=%v", settled, err)
	}
	detail, err := server.blackboardV2.ReadCurrent(context.Background(), createdProject.ID, "fact:settled")
	if err != nil || detail.Version != 1 || detail.Record.Summary != "settled from outbox" {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}
	if _, err := os.Stat(filepath.Join(projection.Receipts, "intent_00000001.json")); err != nil {
		t.Fatal(err)
	}
}
