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

type optionalBlackboardTaskFixture struct {
	server      *Server
	runtimeRoot string
	profile     runtimeprofile.Profile
	created     task.Task
	policy      task.TaskPolicy
}

func newOptionalBlackboardTaskFixture(t *testing.T) optionalBlackboardTaskFixture {
	t.Helper()
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
		"Optional Blackboard", "", project.Scope{Domains: []string{"example.test"}}, project.Defaults{},
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
	return optionalBlackboardTaskFixture{server: server, runtimeRoot: runtimeRoot, profile: profile, created: created, policy: policy}
}

func (fixture optionalBlackboardTaskFixture) omittedPlan(t *testing.T, goal string) taskLaunchPlan {
	t.Helper()
	plan, err := fixture.server.buildTaskLaunchPlanForBlackboardProjection(
		fixture.created, goal, "", "", "high", runner.BlackboardProjectionOmitted,
	)
	if err != nil {
		t.Fatalf("build Task plan without Blackboard: %v", err)
	}
	return plan
}

func TestTaskLaunchPlanCanOmitBlackboardProjection(t *testing.T) {
	fixture := newOptionalBlackboardTaskFixture(t)
	plan := fixture.omittedPlan(t, fixture.created.Goal)
	if plan.BlackboardProjection != runner.BlackboardProjectionOmitted || plan.BlackboardV2 {
		t.Fatalf("Task plan did not represent omitted Blackboard projection: %#v", plan)
	}
	if plan.LaunchGoal != fixture.created.Goal || plan.Facts.Workdir != filepath.Join(fixture.runtimeRoot, fixture.created.ID, "workdir") {
		t.Fatalf("ordinary Task launch context changed: %#v", plan)
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
	if plan.CapturedRuntimeConfig["runtime_profile_id"] != fixture.profile.ID || plan.CapturedRuntimeConfig["runner"] != task.RunnerHost {
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
}

func TestTaskLaunchWithoutBlackboardKeepsOrdinaryOwnerContext(t *testing.T) {
	fixture := newOptionalBlackboardTaskFixture(t)
	if fixture.created.RunControls.Policy != fixture.policy || fixture.created.ScopeSnapshot.Domains[0] != "example.test" {
		t.Fatalf("Task Policy or Scope Snapshot changed: %#v", fixture.created)
	}
	events, err := fixture.server.tasks.Events(fixture.created.ID)
	if err != nil || len(events) != 1 || events[0].Payload["message"] != "prior Transcript context" {
		t.Fatalf("Task Transcript context changed: %#v, err=%v", events, err)
	}
}

func TestTaskLaunchWithoutBlackboardCreatesOrdinaryContinuation(t *testing.T) {
	fixture := newOptionalBlackboardTaskFixture(t)
	plan := fixture.omittedPlan(t, fixture.created.Goal)
	continuation, bound, err := fixture.server.prepareTaskContinuationLaunch(fixture.created, plan, fixture.created.Goal)
	if err != nil {
		t.Fatalf("prepare ordinary Task Continuation: %v", err)
	}
	if continuation.TaskID != fixture.created.ID || continuation.RuntimeConfigVersionID == "" {
		t.Fatalf("ordinary Task Continuation is incomplete: %#v", continuation)
	}
	if continuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		t.Fatalf("ordinary Task Continuation retained Blackboard reconciliation: %#v", continuation)
	}
	if bound.BlackboardProjection != runner.BlackboardProjectionOmitted || bound.LaunchGoal != fixture.created.Goal {
		t.Fatalf("bound Task plan changed launch context: %#v", bound)
	}
	if _, err := fixture.server.blackboardV2Continuity.ReadLaunchPin(t.Context(), continuation.ID); err == nil {
		t.Fatal("ordinary Task Continuation unexpectedly has a Launch Blackboard Pin")
	}
}

func TestTaskLaunchWithoutBlackboardCanReplaceOrdinaryContinuation(t *testing.T) {
	fixture := newOptionalBlackboardTaskFixture(t)
	continuation, _, err := fixture.server.prepareTaskContinuationLaunch(
		fixture.created, fixture.omittedPlan(t, fixture.created.Goal), fixture.created.Goal,
	)
	if err != nil {
		t.Fatalf("prepare ordinary Task Continuation: %v", err)
	}
	if _, err := fixture.server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusStopped); err != nil {
		t.Fatalf("stop ordinary Task Continuation: %v", err)
	}
	replacement, _, err := fixture.server.prepareTaskContinuationLaunch(
		fixture.created, fixture.omittedPlan(t, "continue ordinary Task"), "continue ordinary Task",
	)
	if err != nil {
		t.Fatalf("prepare replacement ordinary Task Continuation: %v", err)
	}
	if replacement.Number != continuation.Number+1 || replacement.RuntimeConfigVersionID == continuation.RuntimeConfigVersionID {
		t.Fatalf("replacement ordinary Task Continuation = %#v, first=%#v", replacement, continuation)
	}
}
