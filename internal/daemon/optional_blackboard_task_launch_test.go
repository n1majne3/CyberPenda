package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func TestTaskLaunchPlanCanOmitBlackboardProjectionAndKeepOrdinaryContinuation(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runs")
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: runtimeRoot,
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	createdProject, err := server.projects.Create(
		"Optional Blackboard",
		"",
		project.Scope{Domains: []string{"example.test"}},
		project.Defaults{},
	)
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	profile, err := server.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		BinaryPath: "/bin/sh", Model: "claude-test",
		Env: map[string]string{"PENTEST_INTERFACE_TOKEN": "profile-grant"},
		MCPServers: []runtimeprofile.MCPServer{{
			Name: "project-memory", Mode: runtimeprofile.MCPServerTrusted,
			URL: "http://daemon.test/mcp?token=stale-grant",
		}},
	})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	policy := task.TaskPolicy{MaxWallTimeSeconds: 300}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "inspect example.test",
		RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls: task.RunControls{Policy: policy},
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	if _, err := server.tasks.AppendEvent(created.ID, task.EventKindConversation, task.EventPayload{"message": "prior Transcript context"}); err != nil {
		t.Fatalf("seed Task Transcript context: %v", err)
	}

	plan, err := server.buildTaskLaunchPlanForBlackboardProjection(
		created,
		created.Goal,
		"",
		"",
		"high",
		runner.BlackboardProjectionOmitted,
	)
	if err != nil {
		t.Fatalf("build Task plan without Blackboard: %v", err)
	}
	if plan.BlackboardProjection != runner.BlackboardProjectionOmitted || plan.BlackboardV2 {
		t.Fatalf("Task plan did not represent omitted Blackboard projection: %#v", plan)
	}
	if plan.LaunchGoal != created.Goal {
		t.Fatalf("Task Goal = %q, want %q", plan.LaunchGoal, created.Goal)
	}
	if plan.Facts.Workdir != filepath.Join(runtimeRoot, created.ID, "workdir") {
		t.Fatalf("Runtime Workdir = %q", plan.Facts.Workdir)
	}
	launch, ok := runtime.CommandAdapterLaunch(plan.Adapter)
	if !ok {
		t.Fatalf("Task host adapter = %T", plan.Adapter)
	}
	for _, forbidden := range []string{"PENTEST_PROJECT_ID", "PENTEST_TASK_ID", "PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_INTERFACE_TOKEN"} {
		if value := launch.Env[forbidden]; value != "" {
			t.Fatalf("Task launch environment retained %s=%q", forbidden, value)
		}
	}
	if plan.CapturedRuntimeConfig["runtime_profile_id"] != profile.ID || plan.CapturedRuntimeConfig["runner"] != task.RunnerHost {
		t.Fatalf("ordinary Runtime configuration is incomplete: %#v", plan.CapturedRuntimeConfig)
	}
	captured, err := json.Marshal(plan.CapturedRuntimeConfig)
	if err != nil {
		t.Fatalf("encode captured Runtime configuration: %v", err)
	}
	for _, forbidden := range []string{"project-memory", "stale-grant", "PENTEST_INTERFACE_TOKEN"} {
		if strings.Contains(string(captured), forbidden) {
			t.Fatalf("captured Runtime configuration retained Blackboard authority %q: %s", forbidden, captured)
		}
	}
	if created.RunControls.Policy != policy || created.ScopeSnapshot.Domains[0] != "example.test" {
		t.Fatalf("Task Policy or Scope Snapshot changed: %#v", created)
	}

	continuation, bound, err := server.prepareTaskContinuationLaunch(created, plan, created.Goal)
	if err != nil {
		t.Fatalf("prepare ordinary Task Continuation: %v", err)
	}
	if continuation.TaskID != created.ID || continuation.RuntimeConfigVersionID == "" {
		t.Fatalf("ordinary Task Continuation is incomplete: %#v", continuation)
	}
	if continuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("ordinary Task Continuation retained Blackboard reconciliation: %#v", continuation)
	}
	if bound.BlackboardProjection != runner.BlackboardProjectionOmitted || bound.LaunchGoal != created.Goal {
		t.Fatalf("bound Task plan changed launch context: %#v", bound)
	}
	for _, table := range []string{
		"blackboard_v2_continuation_pins",
		"blackboard_v2_continuation_state",
		"blackboard_continuation_grants",
	} {
		var count int
		if err := server.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE continuation_id=?", continuation.ID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows = %d, want 0", table, count)
		}
	}
	for _, absent := range []string{
		"AGENTS.md", "CLAUDE.md", filepath.Join(".pentest", "context.json"), filepath.Join(".pentest", "blackboard.json"), ".mcp.json",
	} {
		if _, err := os.Stat(filepath.Join(plan.Facts.Workdir, absent)); !os.IsNotExist(err) {
			t.Fatalf("unexpected Blackboard launch file %s: %v", absent, err)
		}
	}
	scope, err := os.ReadFile(filepath.Join(plan.Facts.Workdir, ".pentest", "scope.json"))
	if err != nil || !strings.Contains(string(scope), "example.test") {
		t.Fatalf("Scope Snapshot = %s, err=%v", scope, err)
	}
	events, err := server.tasks.Events(created.ID)
	if err != nil || len(events) != 1 || events[0].Payload["message"] != "prior Transcript context" {
		t.Fatalf("Task Transcript context changed: %#v, err=%v", events, err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusStopped); err != nil {
		t.Fatalf("stop ordinary Task Continuation: %v", err)
	}
	replacementPlan, err := server.buildTaskLaunchPlanForBlackboardProjection(
		created, "continue ordinary Task", "", "", "high", runner.BlackboardProjectionOmitted,
	)
	if err != nil {
		t.Fatalf("build replacement Task plan: %v", err)
	}
	replacement, _, err := server.prepareTaskContinuationLaunch(created, replacementPlan, "continue ordinary Task")
	if err != nil {
		t.Fatalf("prepare replacement ordinary Task Continuation: %v", err)
	}
	if replacement.Number != continuation.Number+1 || replacement.RuntimeConfigVersionID == continuation.RuntimeConfigVersionID {
		t.Fatalf("replacement ordinary Task Continuation = %#v, first=%#v", replacement, continuation)
	}
}
