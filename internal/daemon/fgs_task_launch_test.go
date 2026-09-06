package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestFGSTaskBoundLaunchKeepsInstructionsAndSchema(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{runtimeprofile.ProviderPi, runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderHermes} {
		t.Run(string(provider), func(t *testing.T) {
			root := t.TempDir()
			server, err := NewServer(Config{Version: "test", DBPath: filepath.Join(root, "test.db"), RuntimeRoot: filepath.Join(root, "runs"), SandboxImage: "sandbox:test", DisableBuiltinSkills: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			p, err := server.projects.Create("FGS launch", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{})
			if err != nil {
				t.Fatal(err)
			}
			profile, err := server.profiles.Create("Runtime", provider, runtimeprofile.Fields{Model: "test-model"})
			if err != nil {
				t.Fatal(err)
			}
			found, err := server.tasks.Create(task.CreateRequest{ProjectID: p.ID, Type: task.TypePentest, Goal: "Check access", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox, RunControls: task.RunControls{BlackboardMode: task.BlackboardModeWorkingGraph}, RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox)})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := server.buildTaskLaunchPlan(found, found.Goal, "", "", "high")
			if err != nil {
				t.Fatal(err)
			}
			_, bound, err := server.prepareBlackboardV2ContinuationLaunch(found, plan, found.Goal)
			if err != nil {
				t.Fatal(err)
			}
			if err := server.recoverBlackboardV2ContinuationFiles(t.Context()); err != nil {
				t.Fatalf("recover bound FGS files: %v", err)
			}
			workdir := filepath.Join(root, "runs", found.ID, "workdir")
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				raw, err := os.ReadFile(filepath.Join(workdir, name))
				if err != nil {
					t.Errorf("final %s: %v", name, err)
					continue
				}
				if !strings.Contains(string(raw), "## FGS work protocol") || strings.Contains(string(raw), "Exploration flows through an open Attempt") {
					t.Errorf("final %s lost FGS instructions: %s", name, raw)
				}
			}
			if _, err := os.Stat(filepath.Join(workdir, ".pentest", "fgs-input.schema.json")); err != nil {
				t.Errorf("final schema: %v", err)
			}
			rawScope, err := os.ReadFile(filepath.Join(workdir, ".pentest", "scope.json"))
			var scope project.Scope
			if err != nil || json.Unmarshal(rawScope, &scope) != nil || len(scope.Domains) != 1 || scope.Domains[0] != "example.test" {
				t.Fatalf("final Scope Snapshot was lost: %s (%v)", rawScope, err)
			}
			args, ok := runtime.DockerSandboxCreateArgs(bound.Adapter)
			if !ok {
				t.Fatal("missing sandbox adapter")
			}
			if !strings.Contains(strings.Join(args, "\n"), "PENTEST_BLACKBOARD_PROTOCOL=fgs") {
				t.Fatal("missing FGS process protocol")
			}
			// Repeat the projection used on subsequent launch preparation.
			if _, err := server.buildTaskLaunchPlan(found, "Continue", "", "", "high"); err != nil {
				t.Fatalf("repeated projection: %v", err)
			}
		})
	}
}
