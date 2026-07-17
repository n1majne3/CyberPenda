package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestCodexV2ContinuationLaunchAndRestartConformanceKeepsSnapshotRereadable(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "codex-v2.db")
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	createdProject, err := server.projects.Create("Codex v2", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Codex profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "inspect example.test", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Task: %v", err)
	}
	_, err = server.blackboardV2.Apply(context.Background(), createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-codex-conformance",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:conformance", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Conformance host", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("seed Blackboard v2: %v", err)
	}
	want, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
	if err != nil {
		_ = server.Close()
		t.Fatalf("project expected Snapshot: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
	if err != nil {
		_ = server.Close()
		t.Fatalf("build Codex launch plan: %v", err)
	}
	continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		_ = server.Close()
		t.Fatalf("prepare Codex v2 Continuation: %v", err)
	}

	workingPath := filepath.Join(runtimeRoot, createdTask.ID, "workdir", ".pentest", "blackboard.json")
	onDisk, err := os.ReadFile(workingPath)
	if err != nil {
		_ = server.Close()
		t.Fatalf("reread Working Snapshot after launch: %v", err)
	}
	if !bytes.Equal(onDisk, want.Bytes) {
		_ = server.Close()
		t.Fatalf("Codex Working Snapshot differs from exact pin\ngot=%s\nwant=%s", onDisk, want.Bytes)
	}
	header := blackboardv2.RenderLaunchHeader(blackboardv2.LaunchHeader{
		Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
		Schema: "runtime-blackboard/v2", Revision: want.Snapshot.Revision,
	})
	if !strings.HasPrefix(bound.LaunchGoal, header+"\n\nTASK GOAL:\n") {
		_ = server.Close()
		t.Fatalf("Codex launch content does not start with compact header: %s", bound.LaunchGoal)
	}
	for _, forbidden := range []string{
		createdProject.ID, createdTask.ID, continuation.ID, profile.ID,
		"http://", "https://", "hash", "bytes", "tokens", "digest", "Trusted tools:", string(want.Bytes),
	} {
		if forbidden != "" && strings.Contains(bound.LaunchGoal, forbidden) {
			_ = server.Close()
			t.Fatalf("Codex launch content leaked %q: %s", forbidden, bound.LaunchGoal)
		}
	}
	agents, err := os.ReadFile(filepath.Join(runtimeRoot, createdTask.ID, "workdir", "AGENTS.md"))
	if err != nil {
		_ = server.Close()
		t.Fatalf("read persistent Codex checklist: %v", err)
	}
	if strings.Count(string(agents)+bound.LaunchGoal, blackboardv2.CodexChecklist()) != 1 {
		_ = server.Close()
		t.Fatalf("checklist is not projected exactly once\nAGENTS=%s\nLAUNCH=%s", agents, bound.LaunchGoal)
	}

	if err := os.Remove(workingPath); err != nil {
		_ = server.Close()
		t.Fatalf("remove Working Snapshot before restart: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close daemon: %v", err)
	}
	restarted, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("restart v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	recovered, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("reread Working Snapshot after restart/context compaction: %v", err)
	}
	if !bytes.Equal(recovered, want.Bytes) {
		t.Fatalf("restart changed exact reread bytes\ngot=%s\nwant=%s", recovered, want.Bytes)
	}
}
