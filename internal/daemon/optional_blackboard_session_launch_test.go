package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
)

type optionalBlackboardSessionFixture struct {
	server   *Server
	factory  *recordingProviderSessionFactory
	goal     string
	found    session.Session
	profile  runtimeprofile.Profile
	input    sessionRuntimeInput
	prepared sessionRuntimePreparation
}

func newOptionalBlackboardSessionFixture(t *testing.T) optionalBlackboardSessionFixture {
	t.Helper()
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
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Optional Session Provider", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-session"}, DefaultModel: "claude-session"},
	})
	if err != nil {
		t.Fatalf("create Model Provider: %v", err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	profile, err := server.profiles.Create("Session Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		BinaryPath: "/bin/sh", ModelProviderID: provider.ID, ModelOverride: "claude-session", DefaultRunner: "host",
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
	goal := "continue this ordinary Session"
	found, err := server.sessions.Create(session.CreateRequest{
		Input: goal,
		InitialRuntime: &session.CreateContinuationRequest{
			RuntimeProfileID: profile.ID, RuntimeProvider: string(profile.Provider), Runner: session.RunnerHost,
			RuntimeConfig: prepared.RuntimeConfig,
		},
	})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	return optionalBlackboardSessionFixture{
		server: server, factory: factory, goal: goal, found: found, profile: profile, input: input, prepared: prepared,
	}
}

func (fixture optionalBlackboardSessionFixture) omittedPlan(t *testing.T) sessionRuntimePlan {
	t.Helper()
	plan, err := fixture.server.buildSessionRuntimePlanForBlackboardProjection(
		fixture.found, fixture.goal, fixture.input, fixture.profile, session.RunnerHost,
		"must-not-project", "", runner.BlackboardProjectionOmitted,
	)
	if err != nil {
		t.Fatalf("build Session plan without Blackboard: %v", err)
	}
	return plan
}

func TestSessionLaunchPlanCanOmitBlackboardProjection(t *testing.T) {
	fixture := newOptionalBlackboardSessionFixture(t)
	plan := fixture.omittedPlan(t)
	if plan.BlackboardProjection != runner.BlackboardProjectionOmitted {
		t.Fatalf("Session plan did not represent omitted Blackboard projection: %#v", plan)
	}
	if plan.LaunchGoal != fixture.goal || plan.Profile.ID != fixture.profile.ID || plan.Runner != session.RunnerHost {
		t.Fatalf("ordinary Session launch context changed: %#v", plan)
	}
	provenance, _ := plan.RuntimeConfig["runtime_profile"].(map[string]any)
	if plan.Facts.Workdir != fixture.found.Workdir || provenance["id"] != fixture.profile.ID {
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
		if _, err := os.Stat(filepath.Join(fixture.found.Workdir, absent)); !os.IsNotExist(err) {
			t.Fatalf("unexpected Session Blackboard launch file %s: %v", absent, err)
		}
	}
}

func TestSessionLaunchWithoutBlackboardStartsOrdinaryContinuation(t *testing.T) {
	fixture := newOptionalBlackboardSessionFixture(t)
	continuation, err := fixture.server.startPreparedSessionRuntimeForBlackboardProjection(
		t.Context(),
		fixture.found,
		fixture.goal,
		fixture.input,
		nil,
		fixture.prepared,
		nil,
		runner.BlackboardProjectionOmitted,
	)
	if err != nil {
		t.Fatalf("start ordinary Session Continuation: %v", err)
	}
	if continuation.SessionID != fixture.found.ID || continuation.RuntimeConfigID == "" || continuation.RuntimeProfileID != fixture.profile.ID {
		t.Fatalf("ordinary Session Continuation is incomplete: %#v", continuation)
	}
	if _, err := fixture.server.blackboardV2.ReadSessionContinuationPin(t.Context(), fixture.found.ID, continuation.ID); err == nil {
		t.Fatal("ordinary Session Continuation unexpectedly has a Launch Blackboard Pin")
	}
	requests := fixture.factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider launch requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Owner.ID != fixture.found.ID || request.Continuation.ID != continuation.ID || request.LaunchGoal != fixture.goal || request.Facts.Workdir != fixture.found.Workdir {
		t.Fatalf("provider launch lost ordinary Session context: %#v", request)
	}
}
