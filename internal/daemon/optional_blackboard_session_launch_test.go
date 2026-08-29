package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
)

func TestSessionLaunchPlanCanOmitBlackboardProjectionAndKeepOrdinaryContinuation(t *testing.T) {
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "native-session-without-blackboard",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true,
			SendTurn:          true,
		},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	goal := "continue this ordinary Session"
	found, err := server.sessions.Create(session.CreateRequest{Input: goal})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	profile, err := server.profiles.Create("Session Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		BinaryPath: "/bin/sh", Model: "claude-session", DefaultRunner: "host",
		MCPServers: []runtimeprofile.MCPServer{{
			Name: "session-memory", Mode: runtimeprofile.MCPServerTrusted,
			URL: "http://daemon.test/mcp?token=stale-session-grant",
		}},
	})
	if err != nil {
		t.Fatalf("create Runtime Profile: %v", err)
	}
	input := sessionRuntimeInput{RuntimeProfileID: profile.ID, Runner: "host", HostActivated: true}
	prepared, err := server.prepareSessionRuntime(t.Context(), session.BlackboardConclusionModeInteractive, input, nil)
	if err != nil {
		t.Fatalf("prepare Session Runtime: %v", err)
	}
	plan, err := server.buildSessionRuntimePlanForBlackboardProjection(
		found,
		goal,
		input,
		profile,
		session.RunnerHost,
		"must-not-project",
		"",
		runner.BlackboardProjectionOmitted,
	)
	if err != nil {
		t.Fatalf("build Session plan without Blackboard: %v", err)
	}
	if plan.BlackboardProjection != runner.BlackboardProjectionOmitted {
		t.Fatalf("Session plan did not represent omitted Blackboard projection: %#v", plan)
	}
	if plan.LaunchGoal != goal || plan.Profile.ID != profile.ID || plan.Runner != session.RunnerHost {
		t.Fatalf("ordinary Session launch context changed: %#v", plan)
	}
	if plan.Facts.Workdir != found.Workdir || plan.RuntimeConfig["runtime_profile_id"] != profile.ID {
		t.Fatalf("ordinary Session Runtime configuration is incomplete: %#v", plan)
	}
	launch, ok := runtime.CommandAdapterLaunch(plan.Adapter)
	if !ok {
		t.Fatalf("Session host adapter = %T", plan.Adapter)
	}
	for _, forbidden := range []string{"PENTEST_SESSION_ID", "PENTEST_MCP_URL", "PENTEST_API_URL", "PENTEST_INTERFACE_TOKEN"} {
		if value := launch.Env[forbidden]; value != "" {
			t.Fatalf("Session launch environment retained %s=%q", forbidden, value)
		}
	}
	for _, absent := range []string{"AGENTS.md", "CLAUDE.md", ".mcp.json", filepath.Join(".pentest", "context.json"), filepath.Join(".pentest", "blackboard.json")} {
		if _, err := os.Stat(filepath.Join(found.Workdir, absent)); !os.IsNotExist(err) {
			t.Fatalf("unexpected Session Blackboard launch file %s: %v", absent, err)
		}
	}

	continuation, err := server.startPreparedSessionRuntimeForBlackboardProjection(
		t.Context(),
		found,
		goal,
		input,
		nil,
		prepared,
		nil,
		runner.BlackboardProjectionOmitted,
	)
	if err != nil {
		t.Fatalf("start ordinary Session Continuation: %v", err)
	}
	if continuation.SessionID != found.ID || continuation.RuntimeConfigID == "" || continuation.RuntimeProfileID != profile.ID {
		t.Fatalf("ordinary Session Continuation is incomplete: %#v", continuation)
	}
	for _, table := range []string{
		"blackboard_v2_session_continuation_pins",
		"session_continuation_interface_grants",
	} {
		var count int
		if err := server.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE continuation_id=?", continuation.ID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows = %d, want 0", table, count)
		}
	}
	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider launch requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Owner.ID != found.ID || request.Continuation.ID != continuation.ID || request.LaunchGoal != goal || request.Facts.Workdir != found.Workdir {
		t.Fatalf("provider launch lost ordinary Session context: %#v", request)
	}
}
