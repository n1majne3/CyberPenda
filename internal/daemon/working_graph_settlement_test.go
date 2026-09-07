package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/fgs"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestTaskFGSSettlementPreservesLegacyOutboxAtLifecycleBoundary(t *testing.T) {
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
	// Model a retained pre-upgrade snapshot without changing new-input rules.
	if _, err := server.db.Exec(`UPDATE tasks SET run_controls_json='{"blackboard_mode":"interactive"}' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	created, err = server.tasks.Get(created.ID)
	if err != nil || created.RunControls.BlackboardMode != task.BlackboardModeInteractive {
		t.Fatalf("historical mode = %s, err=%v", created.RunControls.BlackboardMode, err)
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
	contract := created.OwnerContract(workdir)
	legacy := []byte(`{"schema":"working-graph-intent/v1","id":"intent_00000001","kind":"semantic_changes","payload":{"changes":[]}}`)
	legacyPath := filepath.Join(workdir, "graph", "outbox", "historical-continuation", "intent_00000001.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fgs.Emit(t.Context(), contract, launch.Continuation.ID, []fgs.Operation{{Op: "goal.create", Key: "goal:check", Title: "Check", SuccessCriteria: "Checked"}}); err != nil {
		t.Fatal(err)
	}
	settled, err := server.settleTaskWorkingGraph(t.Context(), created, false)
	if err != nil || !settled {
		t.Fatalf("settled=%v err=%v", settled, err)
	}
	graph, err := server.fgs.Read(t.Context(), contract)
	if err != nil || len(graph.Nodes) != 1 {
		t.Fatalf("graph=%+v err=%v", graph, err)
	}
	retained, err := os.ReadFile(legacyPath)
	if err != nil || string(retained) != string(legacy) {
		t.Fatalf("legacy intent changed: %s %v", retained, err)
	}
}
