package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// Cross-provider Blackboard v2 adapter conformance for Claude Code and Pi.
// Snapshot bytes, compact header, one-time checklist, recovery pin, resume
// fresh pin, and long-run reread semantics must match the Codex contract.

func firstBlackboardV2ProcessLeak(args, env []byte, argvOnly, argvOrEnv []string) string {
	argv, environment := string(args), string(env)
	for _, marker := range argvOnly {
		if marker != "" && strings.Contains(argv, marker) {
			return marker
		}
	}
	for _, marker := range argvOrEnv {
		if marker != "" && (strings.Contains(argv, marker) || strings.Contains(environment, marker)) {
			return marker
		}
	}
	return ""
}

func TestBlackboardV2ProcessLeak(t *testing.T) {
	const opaqueID = "project-opaque-123"
	tests := []struct {
		name       string
		args, env  []byte
		argvOnly   []string
		argvOrEnv  []string
		wantMarker string
	}{
		{
			name: "opaque ID in unrelated inherited env is ignored",
			args: []byte("--goal inspect\n"), env: []byte("CI_PROJECT_ID=" + opaqueID + "\n"),
			argvOnly: []string{opaqueID},
		},
		{
			name: "opaque ID in argv is caught",
			args: []byte("--project " + opaqueID + "\n"), env: []byte("CI=true\n"),
			argvOnly: []string{opaqueID}, wantMarker: opaqueID,
		},
		{
			name: "reserved PENTEST identity credential marker in env is caught",
			args: []byte("--goal inspect\n"), env: []byte("PENTEST_AUTH_TOKEN=secret\n"),
			argvOrEnv: []string{"PENTEST_AUTH_TOKEN="}, wantMarker: "PENTEST_AUTH_TOKEN=",
		},
		{
			name: "clean args and env pass",
			args: []byte("--goal inspect\n"), env: []byte("CI=true\n"),
			argvOnly: []string{opaqueID}, argvOrEnv: []string{"PENTEST_AUTH_TOKEN="},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstBlackboardV2ProcessLeak(tc.args, tc.env, tc.argvOnly, tc.argvOrEnv); got != tc.wantMarker {
				t.Fatalf("first process leak = %q, want %q", got, tc.wantMarker)
			}
		})
	}
}

func TestClaudeAndPiV2LaunchHeaderChecklistAndExactSharedSnapshotBytes(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "claude-pi-v2.db")
	runtimeRoot := filepath.Join(root, "runs")
	server, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	createdProject, err := server.projects.Create("Claude Pi v2", "", project.Scope{Domains: []string{"shared.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	_, err = server.blackboardV2.Apply(context.Background(), createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-shared-conformance",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:shared-conformance", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Shared host", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("seed Blackboard v2: %v", err)
	}
	want, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
	if err != nil {
		t.Fatalf("project expected Snapshot: %v", err)
	}

	var snapshotByProvider [][]byte
	for _, tc := range []struct {
		name     string
		provider runtimeprofile.Provider
		binary   string
	}{
		{"claude", runtimeprofile.ProviderClaudeCode, "claude"},
		{"pi", runtimeprofile.ProviderPi, "pi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, err := server.profiles.Create(tc.name+" profile", tc.provider, runtimeprofile.Fields{BinaryPath: "/usr/bin/" + tc.binary, Model: "test-model"})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			createdTask, err := server.tasks.Create(task.CreateRequest{
				ProjectID: createdProject.ID, Goal: "inspect shared.example", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
			})
			if err != nil {
				t.Fatalf("create Task: %v", err)
			}
			plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
			if err != nil {
				t.Fatalf("build launch plan: %v", err)
			}
			if !plan.BlackboardV2 {
				t.Fatal("expected Blackboard v2 launch plan")
			}
			continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
			if err != nil {
				t.Fatalf("prepare v2 Continuation: %v", err)
			}

			workingPath := filepath.Join(runtimeRoot, createdTask.ID, "workdir", ".pentest", "blackboard.json")
			onDisk, err := os.ReadFile(workingPath)
			if err != nil {
				t.Fatalf("reread Working Snapshot after launch: %v", err)
			}
			if !bytes.Equal(onDisk, want.Bytes) {
				t.Fatalf("Working Snapshot differs from exact shared pin\ngot=%s\nwant=%s", onDisk, want.Bytes)
			}
			snapshotByProvider = append(snapshotByProvider, append([]byte(nil), onDisk...))

			header := blackboardv2.RenderLaunchHeader(blackboardv2.LaunchHeader{
				Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: "runtime-blackboard/v2", Revision: want.Snapshot.Revision,
			})
			if !strings.HasPrefix(bound.LaunchGoal, header+"\n\nTASK GOAL:\n") {
				t.Fatalf("launch content does not start with compact five-field header: %s", bound.LaunchGoal)
			}
			for _, forbidden := range []string{
				createdProject.ID, createdTask.ID, continuation.ID, profile.ID,
				"http://", "https://", "hash", "bytes", "tokens", "digest", "Trusted tools:", string(want.Bytes),
				"protocol_version", "protocol_rule_digest", "projection_hash",
			} {
				if forbidden != "" && strings.Contains(bound.LaunchGoal, forbidden) {
					t.Fatalf("launch content leaked %q: %s", forbidden, bound.LaunchGoal)
				}
			}

			instructionPath := filepath.Join(runtimeRoot, createdTask.ID, "workdir", blackboardV2InstructionName(tc.provider))
			instructions, err := os.ReadFile(instructionPath)
			if err != nil {
				t.Fatalf("read persistent checklist %s: %v", instructionPath, err)
			}
			checklist := blackboardv2.CodexChecklist()
			if strings.Count(string(instructions)+bound.LaunchGoal, checklist) != 1 {
				t.Fatalf("checklist is not projected exactly once\ninstructions=%s\nLAUNCH=%s", instructions, bound.LaunchGoal)
			}
			for _, leak := range []string{"project_id", "task_id", "continuation_id", "Trusted tools:", "blackboard_change"} {
				if strings.Contains(strings.ToLower(string(instructions)), leak) {
					t.Fatalf("instruction channel leaked %q: %s", leak, instructions)
				}
			}
			for _, absent := range []string{"context.json"} {
				if _, err := os.Stat(filepath.Join(runtimeRoot, createdTask.ID, "workdir", ".pentest", absent)); !os.IsNotExist(err) {
					t.Fatalf("unexpected %s after v2 projection: %v", absent, err)
				}
			}

			// Recovery: exact pin after crash/restart with missing Working Snapshot.
			if err := os.Remove(workingPath); err != nil {
				t.Fatalf("remove Working Snapshot before restart: %v", err)
			}
		})
	}
	if len(snapshotByProvider) != 2 || !bytes.Equal(snapshotByProvider[0], snapshotByProvider[1]) {
		t.Fatalf("Claude and Pi Working Snapshot bytes must be identical shared copies")
	}

	// Daemon restart rematerializes every active v2 provider pin.
	if err := server.Close(); err != nil {
		t.Fatalf("close daemon: %v", err)
	}
	restarted, err := NewServer(Config{Version: "test", DBPath: dbPath, RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true})
	if err != nil {
		t.Fatalf("restart v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })

	entries, err := os.ReadDir(runtimeRoot)
	if err != nil {
		t.Fatalf("list runtime tasks: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		recovered, err := os.ReadFile(filepath.Join(runtimeRoot, entry.Name(), "workdir", ".pentest", "blackboard.json"))
		if err != nil {
			t.Fatalf("reread Working Snapshot after restart for %s: %v", entry.Name(), err)
		}
		if !bytes.Equal(recovered, want.Bytes) {
			t.Fatalf("restart changed exact reread bytes for %s\ngot=%s\nwant=%s", entry.Name(), recovered, want.Bytes)
		}
	}
}

func TestClaudeAndPiV2ResumeUsesFreshPinAndSharedSnapshotBytes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider runtimeprofile.Provider
	}{
		{"claude", runtimeprofile.ProviderClaudeCode},
		{"pi", runtimeprofile.ProviderPi},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(root, "resume.db"),
				RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true,
			})
			if err != nil {
				t.Fatalf("start v2 daemon: %v", err)
			}
			t.Cleanup(func() { _ = server.Close() })

			createdProject, err := server.projects.Create(tc.name+" resume", "", project.Scope{Domains: []string{"resume.example"}}, project.Defaults{})
			if err != nil {
				t.Fatalf("create Project: %v", err)
			}
			profile, err := server.profiles.Create(tc.name, tc.provider, runtimeprofile.Fields{BinaryPath: "/usr/bin/" + string(tc.provider), Model: "test-model"})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			createdTask, err := server.tasks.Create(task.CreateRequest{
				ProjectID: createdProject.ID, Goal: "inspect resume.example", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
			})
			if err != nil {
				t.Fatalf("create Task: %v", err)
			}
			plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
			if err != nil {
				t.Fatalf("build first plan: %v", err)
			}
			first, _, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
			if err != nil {
				t.Fatalf("prepare first Continuation: %v", err)
			}
			if _, err := server.tasks.UpdateContinuationStatus(first.ID, task.StatusRunning); err != nil {
				t.Fatalf("start first Continuation: %v", err)
			}
			if _, err := server.blackboardV2.ApplyForContinuation(context.Background(), createdProject.ID, first.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: tc.name + "-finish-resume-state",
				Changes: []blackboardv2.Change{{
					Op: "create", Key: "entity:" + tc.name + "-resume", Type: "entity",
					Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Fresh resume state", ScopeStatus: "in_scope"},
				}},
			}); err != nil {
				t.Fatalf("write before Finish: %v", err)
			}
			if _, err := server.blackboardV2.FinishContinuation(context.Background(), createdProject.ID, first.ID, blackboardv2.FinishContinuationRequest{IdempotencyKey: tc.name + "-daemon-finish"}); err != nil {
				t.Fatalf("Finish first Continuation: %v", err)
			}
			if _, err := server.tasks.UpdateStatus(createdTask.ID, task.StatusCompleted); err != nil {
				t.Fatalf("record terminal Task state: %v", err)
			}

			if !server.acquireTaskControl(createdTask.ID) {
				t.Fatal("acquire resume task control")
			}
			found, resumeGoal, resumePlan, err := server.prepareFreshResumeContinuation(createdTask)
			if err != nil {
				server.releaseTaskControl(createdTask.ID)
				t.Fatalf("prepare v2 resume: %v", err)
			}
			if !strings.Contains(resumeGoal, createdTask.Goal) {
				server.releaseTaskControl(createdTask.ID)
				t.Fatalf("resume goal omitted Task Goal: %s", resumeGoal)
			}
			current, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
			if err != nil {
				server.releaseTaskControl(createdTask.ID)
				t.Fatalf("project current resume state: %v", err)
			}
			resumed, bound, err := server.prepareBlackboardV2ContinuationLaunch(found, resumePlan, resumeGoal)
			server.releaseTaskControl(createdTask.ID)
			if err != nil {
				t.Fatalf("create resumed Continuation: %v", err)
			}
			resumedPin, err := server.blackboardV2Continuity.ReadLaunchPin(context.Background(), resumed.ID)
			if err != nil {
				t.Fatalf("read resumed pin: %v", err)
			}
			if resumed.ID == first.ID || resumed.Number != first.Number+1 || !bytes.Equal(resumedPin.Bytes, current.Bytes) {
				t.Fatalf("resume did not create fresh current pin: first=%#v resumed=%#v", first, resumed)
			}
			working, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "workdir", ".pentest", "blackboard.json"))
			if err != nil {
				t.Fatalf("read resumed Working Snapshot: %v", err)
			}
			if !bytes.Equal(working, current.Bytes) {
				t.Fatalf("resumed Working Snapshot is not exact current bytes")
			}
			header := blackboardv2.RenderLaunchHeader(blackboardv2.LaunchHeader{
				Runner: "sandbox", ScopePath: ".pentest/scope.json", BlackboardPath: ".pentest/blackboard.json",
				Schema: "runtime-blackboard/v2", Revision: current.Snapshot.Revision,
			})
			if !strings.HasPrefix(bound.LaunchGoal, header+"\n\nTASK GOAL:\n") {
				t.Fatalf("resume launch missing compact header: %s", bound.LaunchGoal)
			}
		})
	}
}

func TestClaudeAndPiV2LongRunRereadAfterAtomicReplacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider runtimeprofile.Provider
		shimName string
		script   string
	}{
		{
			name: "claude", provider: runtimeprofile.ProviderClaudeCode, shimName: "claude-shim",
			script: `#!/bin/sh
set -eu
printf '%s\n' "$@" > .shim-args
env > .shim-env
cat .pentest/blackboard.json > .shim-discarded
: > .shim-discarded
: > .shim-ready
while [ ! -f .shim-continue ]; do sleep 0.05; done
cat .pentest/blackboard.json > .shim-reread
cat .pentest/blackboard.json
`,
		},
		{
			name: "pi", provider: runtimeprofile.ProviderPi, shimName: "pi-shim",
			script: `#!/bin/sh
set -eu
printf '%s\n' "$@" > .shim-args
env > .shim-env
cat .pentest/blackboard.json > .shim-discarded
: > .shim-discarded
: > .shim-ready
while [ ! -f .shim-continue ]; do sleep 0.05; done
cat .pentest/blackboard.json > .shim-reread
cat .pentest/blackboard.json
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			shim := filepath.Join(root, tc.shimName)
			if err := os.WriteFile(shim, []byte(tc.script), 0o700); err != nil {
				t.Fatalf("write shim: %v", err)
			}
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(root, "harness.db"), RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true,
			})
			if err != nil {
				t.Fatalf("start v2 daemon: %v", err)
			}
			t.Cleanup(func() { _ = server.Close() })
			createdProject, err := server.projects.Create(tc.name+" harness", "", project.Scope{Domains: []string{"harness.example"}}, project.Defaults{})
			if err != nil {
				t.Fatalf("create Project: %v", err)
			}
			profile, err := server.profiles.Create(tc.name+" shim", tc.provider, runtimeprofile.Fields{BinaryPath: shim, Model: "test-model"})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			createdTask, err := server.tasks.Create(task.CreateRequest{
				ProjectID: createdProject.ID, Goal: "exercise long " + tc.name + " continuation", RuntimeProfileID: profile.ID,
				Runner: task.RunnerHost, RunControls: task.RunControls{HostActivated: true},
			})
			if err != nil {
				t.Fatalf("create Task: %v", err)
			}
			plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
			if err != nil {
				t.Fatalf("build plan: %v", err)
			}
			continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
			if err != nil {
				t.Fatalf("prepare Continuation: %v", err)
			}
			workdir := filepath.Join(root, "runs", createdTask.ID, "workdir")
			launchDone := make(chan error, 1)
			go func() {
				launchDone <- server.harness.Launch(context.Background(), runtime.LaunchRequest{
					TaskID: createdTask.ID, Goal: bound.LaunchGoal, Adapter: bound.Adapter, ContinuationID: continuation.ID,
				})
			}()
			waitForLocalFile(t, filepath.Join(workdir, ".shim-ready"), 5*time.Second)
			_, err = server.blackboardV2.ApplyForContinuation(context.Background(), createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: tc.name + "-harness-ack",
				Changes: []blackboardv2.Change{{
					Op: "create", Key: "entity:" + tc.name + "-harness-current", Type: "entity",
					Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Current after compaction", ScopeStatus: "in_scope"},
				}},
			})
			if err != nil {
				t.Fatalf("acknowledge Runtime write: %v", err)
			}
			if err := os.WriteFile(filepath.Join(workdir, ".shim-continue"), []byte("continue"), 0o600); err != nil {
				t.Fatalf("release shim: %v", err)
			}
			select {
			case err := <-launchDone:
				if err != nil {
					t.Fatalf("harness launch: %v", err)
				}
			case <-time.After(8 * time.Second):
				server.harness.Stop(createdTask.ID)
				t.Fatal("shim did not finish after acknowledged replacement")
			}
			current, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
			if err != nil {
				t.Fatalf("project current Snapshot: %v", err)
			}
			reread, err := os.ReadFile(filepath.Join(workdir, ".shim-reread"))
			if err != nil {
				t.Fatalf("read shim reread: %v", err)
			}
			if !bytes.Equal(reread, current.Bytes) {
				t.Fatalf("shim reread stale bytes\ngot=%s\nwant=%s", reread, current.Bytes)
			}
			args, err := os.ReadFile(filepath.Join(workdir, ".shim-args"))
			if err != nil {
				t.Fatalf("read shim args: %v", err)
			}
			env, err := os.ReadFile(filepath.Join(workdir, ".shim-env"))
			if err != nil {
				t.Fatalf("read shim env: %v", err)
			}
			instructions, err := os.ReadFile(filepath.Join(workdir, blackboardV2InstructionName(tc.provider)))
			if err != nil {
				t.Fatalf("read checklist: %v", err)
			}
			for _, label := range []string{"Runner:", "Scope:", "Blackboard:", "Schema:", "Revision:"} {
				if strings.Count(string(args), label) != 1 {
					t.Fatalf("argv header label %q count = %d:\n%s", label, strings.Count(string(args), label), args)
				}
			}
			if strings.Count(string(args)+string(instructions), blackboardv2.CodexChecklist()) != 1 {
				t.Fatalf("checklist repeated across argv/instructions\nargs=%s\ninstructions=%s", args, instructions)
			}
			if leak := firstBlackboardV2ProcessLeak(args, env,
				[]string{createdProject.ID, createdTask.ID, continuation.ID, profile.ID},
				[]string{"PENTEST_PROJECT_ID=", "PENTEST_TASK_ID=", "PENTEST_AUTH_TOKEN="},
			); leak != "" {
				t.Fatalf("process received forbidden metadata %q\nargs=%s\nenv=%s", leak, args, env)
			}
		})
	}
}

func TestClaudeV2SettingsAllowExactlySixTrustedMCPToolsAndPiProjectsTrustedServer(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "tools.db"), RuntimeRoot: filepath.Join(root, "runs"),
		AuthToken: "operator-token-must-not-be-the-grant", DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Trusted tools", "", project.Scope{Domains: []string{"tools.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}

	t.Run("claude", func(t *testing.T) {
		profile, err := server.profiles.Create("Claude tools", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{BinaryPath: "/usr/bin/claude", Model: "test-model"})
		if err != nil {
			t.Fatalf("create Claude profile: %v", err)
		}
		createdTask, err := server.tasks.Create(task.CreateRequest{
			ProjectID: createdProject.ID, Goal: "use trusted tools", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		})
		if err != nil {
			t.Fatalf("create Task: %v", err)
		}
		plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
		if err != nil {
			t.Fatalf("build plan: %v", err)
		}
		continuation, _, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
		if err != nil {
			t.Fatalf("prepare Continuation: %v", err)
		}
		settingsRaw, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "runtime-home", "claude", "settings.json"))
		if err != nil {
			t.Fatalf("read Claude settings: %v", err)
		}
		var settings struct {
			Permissions struct {
				Allow []string `json:"allow"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(settingsRaw, &settings); err != nil {
			t.Fatalf("decode Claude settings: %v", err)
		}
		want := map[string]bool{
			"mcp__pentest__blackboard_change": true, "mcp__pentest__blackboard_read": true,
			"mcp__pentest__blackboard_history": true, "mcp__pentest__blackboard_retain_evidence": true,
			"mcp__pentest__blackboard_checkpoint_attempt": true, "mcp__pentest__blackboard_finish": true,
		}
		if len(want) != 6 {
			t.Fatalf("canonical trusted tools = %d, want 6", len(want))
		}
		for _, allowed := range settings.Permissions.Allow {
			if !want[allowed] {
				t.Fatalf("Claude settings unexpectedly pre-authorize %q: %s", allowed, settingsRaw)
			}
			delete(want, allowed)
		}
		if len(want) != 0 {
			t.Fatalf("Claude settings missing trusted tools %#v: %s", want, settingsRaw)
		}
		mcpRaw, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "workdir", ".mcp.json"))
		if err != nil {
			t.Fatalf("read Claude MCP config: %v", err)
		}
		if !strings.Contains(string(mcpRaw), "/mcp?token=") {
			t.Fatalf("Claude MCP config missing grant-authenticated trusted URL: %s", mcpRaw)
		}
		if strings.Contains(string(mcpRaw), "operator-token-must-not-be-the-grant") {
			t.Fatalf("Claude MCP config used operator token instead of Continuation grant")
		}
		if strings.Contains(string(settingsRaw), createdProject.ID) || strings.Contains(string(settingsRaw), createdTask.ID) || strings.Contains(string(settingsRaw), continuation.ID) {
			t.Fatalf("Claude settings leaked identity: %s", settingsRaw)
		}
	})

	t.Run("pi", func(t *testing.T) {
		profile, err := server.profiles.Create("Pi tools", runtimeprofile.ProviderPi, runtimeprofile.Fields{BinaryPath: "/usr/bin/pi", Model: "test-model"})
		if err != nil {
			t.Fatalf("create Pi profile: %v", err)
		}
		createdTask, err := server.tasks.Create(task.CreateRequest{
			ProjectID: createdProject.ID, Goal: "use trusted tools", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		})
		if err != nil {
			t.Fatalf("create Task: %v", err)
		}
		plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "", "")
		if err != nil {
			t.Fatalf("build plan: %v", err)
		}
		_, _, err = server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
		if err != nil {
			t.Fatalf("prepare Continuation: %v", err)
		}
		mcpRaw, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "runtime-home", "pi", "agent", "mcp.json"))
		if err != nil {
			t.Fatalf("read Pi MCP config: %v", err)
		}
		var doc struct {
			MCPServers map[string]struct {
				Transport string `json:"transport"`
				URL       string `json:"url"`
				Lifecycle string `json:"lifecycle"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(mcpRaw, &doc); err != nil {
			t.Fatalf("decode Pi mcp.json: %v", err)
		}
		pentest, ok := doc.MCPServers["pentest"]
		if !ok {
			t.Fatalf("Pi missing trusted pentest server: %s", mcpRaw)
		}
		if pentest.Transport != "streamable-http" || pentest.Lifecycle != "eager" {
			t.Fatalf("Pi trusted server transport = %#v", pentest)
		}
		if !strings.Contains(pentest.URL, "/mcp?token=") {
			t.Fatalf("Pi trusted URL missing grant token: %q", pentest.URL)
		}
		if strings.Contains(pentest.URL, "operator-token-must-not-be-the-grant") {
			t.Fatalf("Pi MCP used operator token instead of Continuation grant")
		}
		// No provider-specific Blackboard semantics in Pi config.
		for _, forbidden := range []string{"blackboard_revision", "protocol_rule_digest", "projection_hash", "runtime-blackboard"} {
			if strings.Contains(string(mcpRaw), forbidden) {
				t.Fatalf("Pi MCP config carried provider-specific Blackboard semantics %q: %s", forbidden, mcpRaw)
			}
		}
	})
}

func blackboardV2InstructionName(provider runtimeprofile.Provider) string {
	if provider == runtimeprofile.ProviderClaudeCode {
		return "CLAUDE.md"
	}
	return "AGENTS.md"
}
