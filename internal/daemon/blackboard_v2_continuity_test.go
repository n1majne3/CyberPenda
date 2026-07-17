package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestCodexV2LaunchExcludesIdentityMetadataAndOperatorCredentialSurface(t *testing.T) {
	root := t.TempDir()
	const operatorToken = "operator-token-must-never-reach-codex"
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "metadata.db"), RuntimeRoot: filepath.Join(root, "runs"),
		AuthToken: operatorToken, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectA, err := server.projects.Create("A", "", project.Scope{Domains: []string{"a.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project A: %v", err)
	}
	projectB, err := server.projects.Create("B", "", project.Scope{Domains: []string{"b.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project B: %v", err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{BinaryPath: "/usr/local/bin/codex", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Codex profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectA.ID, Goal: "inspect a.example", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
	if err != nil {
		t.Fatalf("build initial plan: %v", err)
	}
	continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("prepare bound plan: %v", err)
	}
	runtimeConfig, err := json.Marshal(bound.RuntimeConfig)
	if err != nil {
		t.Fatalf("encode runtime config: %v", err)
	}
	configTOML, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "runtime-home", "codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(root, "runs", createdTask.ID, "workdir", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	visible := strings.Join([]string{bound.LaunchGoal, string(runtimeConfig), string(configTOML), string(agents)}, "\n")
	for _, forbidden := range []string{
		projectA.ID, projectB.ID, createdTask.ID, continuation.ID, profile.ID, operatorToken,
		"project_id", "task_id", "continuation_id", "runtime_profile_id", "runtime_plugin_id",
		"PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_MCP_URL", "PENTEST_AUTH_TOKEN",
		"[mcp_servers.pentest]", "/mcp?token=", "projection_hash", "estimated_tokens", "protocol_rule_digest",
	} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("Codex v2 launch surface leaked %q:\n%s", forbidden, visible)
		}
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/projects", nil),
		httptest.NewRequest(http.MethodGet, "/api/projects/"+projectB.ID+"/tasks", nil),
		httptest.NewRequest(http.MethodPost, "/api/projects/"+projectB.ID+"/blackboard/mutations", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/mcp?token=continuation-capability-not-issued", nil),
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("networkless Codex request %s %s status = %d, want 401", request.Method, request.URL, response.Code)
		}
	}
}

func TestRealCodexAdapterHarnessRereadsAcknowledgedSnapshotAfterContextLoss(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "codex-shim")
	shimScript := `#!/bin/sh
set -eu
printf '%s\n' "$@" > .shim-args
env > .shim-env
cat .pentest/blackboard.json > .shim-discarded
: > .shim-discarded
: > .shim-ready
while [ ! -f .shim-continue ]; do sleep 0.05; done
cat .pentest/blackboard.json > .shim-reread
cat .pentest/blackboard.json
`
	if err := os.WriteFile(shim, []byte(shimScript), 0o700); err != nil {
		t.Fatalf("write Codex shim: %v", err)
	}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(root, "harness.db"), RuntimeRoot: filepath.Join(root, "runs"), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start v2 daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create("Harness", "", project.Scope{Domains: []string{"harness.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Codex shim", runtimeprofile.ProviderCodex, runtimeprofile.Fields{BinaryPath: shim, Model: "gpt-test"})
	if err != nil {
		t.Fatalf("create Codex profile: %v", err)
	}
	createdTask, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "exercise long Codex continuation", RuntimeProfileID: profile.ID,
		Runner: task.RunnerHost, RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	plan, err := server.buildTaskLaunchPlan(createdTask, createdTask.Goal, "", "")
	if err != nil {
		t.Fatalf("build Codex plan: %v", err)
	}
	continuation, bound, err := server.prepareBlackboardV2ContinuationLaunch(createdTask, plan, createdTask.Goal)
	if err != nil {
		t.Fatalf("prepare Codex Continuation: %v", err)
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
		Schema: "semantic-change-batch/v2", IdempotencyKey: "harness-acknowledged-write",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "entity:harness-current", Type: "entity",
			Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Current after compaction", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("acknowledge Runtime write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".shim-continue"), []byte("continue"), 0o600); err != nil {
		t.Fatalf("release Codex shim: %v", err)
	}
	select {
	case err := <-launchDone:
		if err != nil {
			t.Fatalf("production Codex harness launch: %v", err)
		}
	case <-time.After(8 * time.Second):
		server.harness.Stop(createdTask.ID)
		t.Fatal("Codex shim did not finish after acknowledged replacement")
	}
	current, err := server.blackboardV2.ProjectRuntimeSnapshot(context.Background(), createdProject.ID)
	if err != nil {
		t.Fatalf("project current Snapshot: %v", err)
	}
	reread, err := os.ReadFile(filepath.Join(workdir, ".shim-reread"))
	if err != nil {
		t.Fatalf("read Codex shim reread: %v", err)
	}
	if !bytes.Equal(reread, current.Bytes) {
		t.Fatalf("Codex shim reread stale bytes\ngot=%s\nwant=%s", reread, current.Bytes)
	}
	discarded, err := os.ReadFile(filepath.Join(workdir, ".shim-discarded"))
	if err != nil || len(discarded) != 0 {
		t.Fatalf("context-loss simulation retained prior Snapshot: %q, %v", discarded, err)
	}
	args, err := os.ReadFile(filepath.Join(workdir, ".shim-args"))
	if err != nil {
		t.Fatalf("read Codex shim args: %v", err)
	}
	env, err := os.ReadFile(filepath.Join(workdir, ".shim-env"))
	if err != nil {
		t.Fatalf("read Codex shim env: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(workdir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read Codex checklist: %v", err)
	}
	for _, label := range []string{"Runner:", "Scope:", "Blackboard:", "Schema:", "Revision:"} {
		if strings.Count(string(args), label) != 1 {
			t.Fatalf("Codex argv header label %q count = %d:\n%s", label, strings.Count(string(args), label), args)
		}
	}
	if strings.Count(string(args)+string(agents), blackboardv2.CodexChecklist()) != 1 {
		t.Fatalf("checklist repeated across Codex argv/instructions\nargs=%s\nagents=%s", args, agents)
	}
	for _, forbidden := range []string{
		createdProject.ID, createdTask.ID, continuation.ID, profile.ID,
		"PENTEST_PROJECT_ID=", "PENTEST_TASK_ID=", "PENTEST_MCP_URL=", "PENTEST_AUTH_TOKEN=", "/mcp?token=",
	} {
		if strings.Contains(string(args)+string(env), forbidden) {
			t.Fatalf("production Codex process received forbidden metadata %q\nargs=%s\nenv=%s", forbidden, args, env)
		}
	}
}

func waitForLocalFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
