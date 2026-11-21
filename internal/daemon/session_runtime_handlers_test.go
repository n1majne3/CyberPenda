package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/skill"
	"pentest/internal/workinggraph"
)

func testSessionRuntimeSnapshot(t *testing.T, server *Server, plugin runtimeprofile.Provider, run session.Runner) map[string]any {
	t.Helper()
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: string(plugin) + " test provider", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"test-model"}, DefaultModel: "test-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	return map[string]any{
		"snapshot_version": 1, "runtime_plugin_id": string(plugin), "runner": string(run),
		"model_provider_snapshot": map[string]any{
			"model_provider_id": provider.ID, "model_provider_name": provider.Name, "base_url": provider.BaseURL,
			"protocol": string(modelprovider.ProtocolOpenAIResponses), "model": "test-model", "api_key_env": provider.APIKeyEnv,
		},
		"runtime_turn_selection": map[string]any{"model_provider_id": provider.ID, "model": "test-model"},
		"settings":               map[string]any{"model_provider_id": provider.ID, "model_override": "test-model"},
		"enabled_skill_ids":      []string{}, "config_projection": map[string]any{},
	}
}

func TestSessionRuntimePlanProjectsOnlyCapturedSnapshotSkills(t *testing.T) {
	runtimeRoot := t.TempDir()
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: runtimeRoot, DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	provider := createSnapshotTestModelProvider(t, server, "captured-model")
	captured, err := server.skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "captured-skill", Name: "Captured Skill"},
		Files:    map[string]string{"SKILL.md": "# Captured Skill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runtimeconfig.RuntimeConfigurationSnapshot{
		SnapshotVersion: runtimeconfig.SnapshotVersion, RuntimePluginID: "codex", Runner: "host",
		ModelProvider: modelprovider.Snapshot{
			ModelProviderID: provider.ID, ModelProviderName: provider.Name, EndpointBaseURL: provider.BaseURL,
			Protocol: modelprovider.ProtocolOpenAIResponses, Model: "captured-model", APIKeyEnv: provider.APIKeyEnv,
		},
		TurnSelection: runtimeconfig.RuntimeTurnSelection{ModelProviderID: provider.ID, Model: "captured-model", ReasoningEffort: "high"},
		Settings: map[string]any{
			"binary_path": "/bin/true", "default_runner": "host", "model_provider_id": provider.ID, "model_override": "captured-model",
		},
		EnabledSkillIDs: []string{captured.ID}, ConfigProjection: map[string]any{},
	}
	config := runtimeSnapshotMap(snapshot)
	created, err := server.sessions.Create(session.CreateRequest{
		Input: "project captured skills",
		InitialRuntime: &session.CreateContinuationRequest{
			RuntimeProvider: "codex", Runner: session.RunnerHost, RuntimeConfig: config,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "late-skill", Name: "Late Skill"},
		Files:    map[string]string{"SKILL.md": "# Late Skill"},
	}); err != nil {
		t.Fatal(err)
	}
	profile, err := runtimeProfileFromSnapshot(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.buildSessionRuntimePlan(created, "continue", sessionRuntimeInput{HostActivated: true}, profile, session.RunnerHost, "token", ""); err != nil {
		t.Fatalf("build Session Runtime plan: %v", err)
	}
	skillsRoot := filepath.Join(created.Workdir, ".runtime", "skills")
	if _, err := os.Stat(filepath.Join(skillsRoot, captured.ID)); err != nil {
		t.Fatalf("captured Skill was not projected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsRoot, "late-skill")); !os.IsNotExist(err) {
		t.Fatalf("late global Skill was projected, stat error=%v", err)
	}
}

func TestPrepareSessionResumeUsesCapturedModelProviderSnapshot(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv("CAPTURED_API_KEY", "sk-test")
	snapshot := runtimeconfig.RuntimeConfigurationSnapshot{
		SnapshotVersion: runtimeconfig.SnapshotVersion, RuntimePluginID: "codex", Runner: "host",
		ModelProvider: modelprovider.Snapshot{
			ModelProviderID: "deleted-provider", ModelProviderName: "Captured Provider",
			EndpointBaseURL: "https://captured.example.test/v1", Protocol: modelprovider.ProtocolOpenAIResponses,
			Model: "captured-model", APIKeyEnv: "CAPTURED_API_KEY",
		},
		TurnSelection: runtimeconfig.RuntimeTurnSelection{
			ModelProviderID: "deleted-provider", Model: "captured-model", ReasoningEffort: "high",
		},
		Settings:        map[string]any{"binary_path": "/bin/true", "default_runner": "host"},
		EnabledSkillIDs: []string{}, ConfigProjection: map[string]any{},
	}
	created, err := server.sessions.Create(session.CreateRequest{
		Input: "resume captured provider",
		InitialRuntime: &session.CreateContinuationRequest{
			RuntimeProvider: "codex", Runner: session.RunnerHost, RuntimeConfig: runtimeSnapshotMap(snapshot),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := created.LatestContinuation
	if previous == nil {
		t.Fatal("initial Session continuation is missing")
	}
	late, err := server.skills.Publish(context.Background(), skill.PublishRequest{
		Metadata: skill.Metadata{ID: "late-invalid-skill", Name: "Late Invalid Skill"},
		Files:    map[string]string{"SKILL.md": "# Late Invalid Skill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(late.BundlePath); err != nil {
		t.Fatal(err)
	}

	prepared, err := server.prepareSessionRuntime(context.Background(), session.BlackboardModeInteractive, sessionRuntimeInput{HostActivated: true}, previous)
	if err != nil {
		t.Fatalf("prepare Session Resume: %v", err)
	}
	cloned, err := decodeRuntimeSnapshot(prepared.RuntimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if cloned.ModelProvider.EndpointBaseURL != snapshot.ModelProvider.EndpointBaseURL {
		t.Fatalf("resumed Model Provider = %#v, want captured %#v", cloned.ModelProvider, snapshot.ModelProvider)
	}
}

func TestCreateSessionDoesNotReportSuccessWhenFirstRuntimeTurnCannotLaunch(t *testing.T) {
	factory := &recordingProviderSessionFactory{err: errors.New("provider bridge unavailable")}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	modelProvider := createSnapshotTestModelProvider(t, server, "gpt-session")
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: modelProvider.ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(
		`{"input":"must start","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
	))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("create Session status = %d, body=%s", response.Code, response.Body.String())
	}
	if len(factory.Requests()) != 1 {
		t.Fatalf("provider launch requests = %d, want 1", len(factory.Requests()))
	}
}

func TestSessionRuntimePlanUsesNativeResumeArgvWithoutAProviderBridge(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{Input: "resume"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "codex", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	plan, err := server.buildSessionRuntimePlan(found, "continue", sessionRuntimeInput{HostActivated: true}, profile, session.RunnerHost, "token", "native-session-1")
	if err != nil {
		t.Fatalf("build Session Runtime plan: %v", err)
	}
	launch, ok := runtime.CommandAdapterLaunch(plan.Adapter)
	if !ok {
		t.Fatalf("Session host adapter = %T", plan.Adapter)
	}
	joined := strings.Join(append([]string{launch.Program}, launch.Args...), " ")
	if !strings.Contains(joined, "resume") || !strings.Contains(joined, "native-session-1") {
		t.Fatalf("Session native resume argv = %q", joined)
	}
}

func TestSessionPiProjectedProviderAllowedUsesLatestRuntimeConfig(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	created, err := server.sessions.Create(session.CreateRequest{Input: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSessionRuntimeSnapshot(t, server, runtimeprofile.ProviderPi, session.RunnerSandbox)
	snapshot["config_projection"] = map[string]any{"projected_model_provider_ids": []string{"provider-a", "provider-b"}}
	if _, err := server.sessions.CreateContinuation(created.ID, "profile-pi", string(runtimeprofile.ProviderPi), session.RunnerSandbox, snapshot); err != nil {
		t.Fatal(err)
	}
	if !server.sessionPiProjectedProviderAllowed(created.ID, "provider-b") {
		t.Fatal("projected Session Pi provider was rejected")
	}
	if server.sessionPiProjectedProviderAllowed(created.ID, "provider-c") {
		t.Fatal("unprojected Session Pi provider was accepted")
	}
}

func TestSessionPiSandboxUsesTheSharedBootstrapWrapper(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{Input: "run pi"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	profile, err := server.profiles.Create("Session Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		DefaultRunner: "sandbox", Model: "pi-model", SandboxImage: "kalilinux/kali-rolling",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	plan, err := server.buildSessionRuntimePlan(found, "inspect", sessionRuntimeInput{}, profile, session.RunnerSandbox, "token", "")
	if err != nil {
		t.Fatalf("build Session Pi plan: %v", err)
	}
	args, ok := runtime.DockerSandboxCreateArgs(plan.Adapter)
	if !ok {
		t.Fatalf("Session sandbox adapter = %T", plan.Adapter)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "command -v pi") {
		t.Fatalf("Session Pi sandbox argv lacks bootstrap wrapper: %q", joined)
	}
}

// TestSessionPiLaunchPinsDefaultThinkingLevelForSubagents proves the Session
// Launch Reasoning Effort Override reaches the Pi settings.json projection:
// pi-subagents child sessions read defaultThinkingLevel from settings.json, so
// a max launch must pin max there instead of leaving Pi's built-in default.
func TestSessionPiLaunchPinsDefaultThinkingLevelForSubagents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{Input: "run pi"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	profile, err := server.profiles.Create("Session Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		DefaultRunner: "sandbox", Model: "pi-model", SandboxImage: "kalilinux/kali-rolling", ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := server.buildSessionRuntimePlan(found, "inspect", sessionRuntimeInput{ReasoningEffort: "max"}, profile, session.RunnerSandbox, "token", ""); err != nil {
		t.Fatalf("build Session Pi plan: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(found.Workdir, ".runtime", "runtime-home", "pi", "agent", "settings.json"))
	if err != nil {
		t.Fatalf("read projected pi settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode pi settings.json: %v", err)
	}
	if got := settings["defaultThinkingLevel"]; got != "max" {
		t.Fatalf("defaultThinkingLevel = %#v, want max", got)
	}
}

func TestSessionSandboxProjectsWorkingGraphPathsThroughWorkdirMount(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	found, err := server.sessions.Create(session.CreateRequest{
		Input: "use Working Graph", BlackboardMode: session.BlackboardModeWorkingGraph,
	})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	profile, err := server.profiles.Create("Session Pi", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		DefaultRunner: "sandbox", Model: "pi-model", SandboxImage: "kalilinux/kali-rolling",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	continuationID := "session-continuation-1"
	graph, err := workinggraph.NewService().Prepare(t.Context(), workinggraph.OwnerContext{
		Owner: found.OwnerContract(), ContinuationID: continuationID, Workdir: found.Workdir,
	})
	if err != nil {
		t.Fatalf("prepare Working Graph: %v", err)
	}
	plan, err := server.buildSessionRuntimePlanForOwnerContext(
		found, "inspect", sessionRuntimeInput{}, profile, session.RunnerSandbox, "token", "",
		runner.BlackboardProjectionRequired, continuationID, &graph,
	)
	if err != nil {
		t.Fatalf("build Session plan: %v", err)
	}
	if !strings.Contains(plan.LaunchGoal, runner.FGSLaunchInstruction) || !strings.Contains(plan.LaunchGoal, "inspect") {
		t.Fatalf("Session launch goal did not require FGS instructions: %s", plan.LaunchGoal)
	}
	args, ok := runtime.DockerSandboxCreateArgs(plan.Adapter)
	if !ok {
		t.Fatalf("Session sandbox adapter = %T", plan.Adapter)
	}
	createEnv, ok := runtime.DockerSandboxCreateEnv(plan.Adapter)
	if !ok {
		t.Fatalf("Session sandbox create env unavailable for %T", plan.Adapter)
	}
	env := map[string]string{}
	for index := 0; index < len(args)-1; index++ {
		if args[index] != "-e" {
			continue
		}
		if key, value, hasValue := strings.Cut(args[index+1], "="); hasValue {
			env[key] = value
		} else if value, exists := createEnv[key]; exists {
			env[key] = value
		}
		index++
	}
	want := map[string]string{
		"PENTEST_WORKING_GRAPH_ROOT":     "/task/workdir",
		"PENTEST_WORKING_GRAPH_OUTBOX":   "/task/workdir/graph/outbox/" + continuationID,
		"PENTEST_WORKING_GRAPH_RECEIPTS": "/task/workdir/graph/receipts/" + continuationID,
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("%s = %q, want %q", key, env[key], value)
		}
	}
}
