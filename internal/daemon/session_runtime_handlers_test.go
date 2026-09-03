package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/skill"
	"pentest/internal/task"
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
	graph, err := server.workingGraph.Prepare(t.Context(), workinggraph.OwnerContext{
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
	if strings.Count(plan.LaunchGoal, "`cyberpenda-blackboard-working-graph`") != 1 ||
		!strings.Contains(plan.LaunchGoal, "TASK GOAL:\ninspect") {
		t.Fatalf("Session launch goal did not invoke Working Graph Skill: %s", plan.LaunchGoal)
	}
	args, ok := runtime.DockerSandboxCreateArgs(plan.Adapter)
	if !ok {
		t.Fatalf("Session sandbox adapter = %T", plan.Adapter)
	}
	env := map[string]string{}
	for index := 0; index < len(args)-1; index++ {
		if args[index] != "-e" {
			continue
		}
		parts := strings.SplitN(args[index+1], "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
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

func TestCreateSandboxSessionMountsExternalManagedWorkdirThroughPublicHTTP(t *testing.T) {
	volumeRoot := filepath.Join(t.TempDir(), "data")
	sessionRoot := filepath.Join(t.TempDir(), "external-sessions")
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-external-workdir",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot: filepath.Join(volumeRoot, "runs"), SessionRoot: sessionRoot,
		TaskVolume: "cyberpenda-data", TaskVolumeRoot: volumeRoot,
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "sandbox", BinaryPath: "codex", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session", SandboxImage: "sandbox:test",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	request := multipartSessionRequest(t, http.MethodPost, "/api/sessions",
		`{"input":"inspect attachment","runtime_profile_id":"`+profile.ID+`","runner":"sandbox"}`,
		"sample.bin", "payload")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create sandbox Session status = %d, body=%s", response.Code, response.Body.String())
	}
	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider launch requests = %d, want 1", len(requests))
	}
	args, ok := runtime.DockerSandboxCreateArgs(requests[0].LegacyAdapter)
	if !ok {
		t.Fatalf("Session sandbox adapter = %T", requests[0].LegacyAdapter)
	}
	joined := strings.Join(args, " ")
	found, err := server.sessions.Get(requests[0].Owner.ID)
	if err != nil {
		t.Fatalf("read created Session: %v", err)
	}
	wantMount := "type=bind,src=" + found.Workdir + ",dst=/task/workdir"
	if !strings.Contains(joined, wantMount) {
		t.Fatalf("Session sandbox does not mount its managed Workdir at /task/workdir: %q", joined)
	}
	if !strings.Contains(joined, "ATTACHED FILES:") || !strings.Contains(joined, "/task/workdir/sample.bin") {
		t.Fatalf("Session sandbox Runtime goal does not identify its uploaded file: %q", joined)
	}
}

func multipartSessionRequest(t *testing.T, method, target, payload, filename, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("payload", payload); err != nil {
		t.Fatalf("write multipart payload: %v", err)
	}
	part, err := writer.CreateFormFile("attachments", filename)
	if err != nil {
		t.Fatalf("create attachment field: %v", err)
	}
	if _, err := part.Write([]byte(contents)); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	request := httptest.NewRequest(method, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestCreateSessionProjectsUploadedAttachmentPathIntoInitialRuntimeTurn(t *testing.T) {
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-attachment-path",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{
		session:          providerSession,
		adapter:          runtime.NewProviderSessionRunAdapter(providerSession, make(chan struct{})),
		bindContinuation: true,
	}
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

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	payload, err := writer.CreateFormField("payload")
	if err != nil {
		t.Fatalf("create payload field: %v", err)
	}
	if _, err := payload.Write([]byte(`{"input":"inspect the upload","runtime_profile_id":"` + profile.ID + `","runner":"host","host_activated":true}`)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	file, err := writer.CreateFormFile("attachments", "sample.bin")
	if err != nil {
		t.Fatalf("create attachment field: %v", err)
	}
	if _, err := file.Write([]byte("payload")); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", response.Code, response.Body.String())
	}

	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider launch requests = %d, want 1", len(requests))
	}
	found, err := server.sessions.Get(requests[0].Owner.ID)
	if err != nil {
		t.Fatalf("read created Session: %v", err)
	}
	wantPath := filepath.Join(found.Workdir, "sample.bin")
	if !strings.Contains(requests[0].LaunchGoal, "ATTACHED FILES:\n- "+wantPath) {
		t.Fatalf("initial Runtime goal = %q, want attachment path %q", requests[0].LaunchGoal, wantPath)
	}
	waitForProviderRequests(t, providerSession, 1)
	if got := providerSession.LastRequests()[0].Message; !strings.Contains(got, "ATTACHED FILES:\n- "+wantPath) {
		t.Fatalf("provider initial Turn = %q, want attachment path %q", got, wantPath)
	}
}

func TestResumedSessionDoesNotAdvertiseDeletedHistoricalAttachments(t *testing.T) {
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-stale-attachment-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	server.ServeHTTP(create, multipartSessionRequest(t, http.MethodPost, "/api/sessions",
		`{"input":"inspect stale evidence","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
		"stale.txt", "temporary evidence"))
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	found, err := server.sessions.Get(created.ID)
	if err != nil {
		t.Fatalf("read Session Workdir: %v", err)
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("stop Session status = %d, body=%s", stop.Code, stop.Body.String())
	}
	if err := os.Remove(filepath.Join(found.Workdir, "stale.txt")); err != nil {
		t.Fatalf("remove historical attachment: %v", err)
	}
	factory.mu.Lock()
	factory.session = runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-stale-attachment-2",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory.adapter = &persistentTestAdapter{}
	factory.mu.Unlock()

	resume := httptest.NewRecorder()
	resumeRequest := multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/messages",
		`{"message":"resume with fresh file","host_activated":true}`, "fresh.txt", "fresh evidence")
	server.ServeHTTP(resume, resumeRequest)
	if resume.Code != http.StatusAccepted {
		t.Fatalf("resume Session status = %d, body=%s", resume.Code, resume.Body.String())
	}
	requests := factory.Requests()
	if len(requests) != 2 {
		events, _ := server.sessions.Events(created.ID)
		t.Fatalf("provider launch requests = %d, want 2; response=%s events=%#v", len(requests), resume.Body.String(), events)
	}
	if strings.Contains(requests[1].LaunchGoal, "stale.txt") || !strings.Contains(requests[1].LaunchGoal, "ATTACHED FILES:\n- "+filepath.Join(found.Workdir, "fresh.txt")) {
		t.Fatalf("resumed Runtime goal does not contain only available attachments: %q", requests[1].LaunchGoal)
	}
	eventsResponse := httptest.NewRecorder()
	server.ServeHTTP(eventsResponse, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/events", nil))
	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("Session Events status = %d, body=%s", eventsResponse.Code, eventsResponse.Body.String())
	}
	var eventsPayload struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&eventsPayload); err != nil {
		t.Fatalf("decode Session Events: %v", err)
	}
	for _, event := range eventsPayload.Events {
		if event.Kind == session.EventKindAttachment && event.Payload["filename"] == "fresh.txt" {
			if event.Payload["continuation_id"] != requests[1].Continuation.ID {
				t.Fatalf("resumed attachment continuation = %#v, want %q", event.Payload["continuation_id"], requests[1].Continuation.ID)
			}
			return
		}
	}
	t.Fatal("resumed Session has no fresh attachment Event")
}

func TestRejectedResumeAttachmentDoesNotCreateAContinuationOrEvent(t *testing.T) {
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-invalid-resume-attachment",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(
		`{"input":"inspect","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
	))
	createRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, createRequest)
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created session.Session
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	if created.LatestContinuation == nil {
		t.Fatal("created Session has no continuation")
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("stop Session status = %d, body=%s", stop.Code, stop.Body.String())
	}
	beforeEvents := httptest.NewRecorder()
	server.ServeHTTP(beforeEvents, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/events", nil))
	var before struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(beforeEvents.Body).Decode(&before); err != nil {
		t.Fatalf("decode Events before rejected resume: %v", err)
	}

	rejected := httptest.NewRecorder()
	server.ServeHTTP(rejected, multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/messages",
		`{"message":"resume with invalid attachment","host_activated":true}`, "..", "invalid"))
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("rejected resume status = %d, body=%s", rejected.Code, rejected.Body.String())
	}

	read := httptest.NewRecorder()
	server.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID, nil))
	var found session.Session
	if err := json.NewDecoder(read.Body).Decode(&found); err != nil {
		t.Fatalf("decode Session after rejected resume: %v", err)
	}
	if found.LatestContinuation == nil || found.LatestContinuation.ID != created.LatestContinuation.ID {
		t.Fatalf("rejected resume created continuation: before=%#v after=%#v", created.LatestContinuation, found.LatestContinuation)
	}
	afterEvents := httptest.NewRecorder()
	server.ServeHTTP(afterEvents, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/events", nil))
	var after struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(afterEvents.Body).Decode(&after); err != nil {
		t.Fatalf("decode Events after rejected resume: %v", err)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("rejected resume appended Events: before=%d after=%d", len(before.Events), len(after.Events))
	}
}

func TestRejectedSessionRuntimeTurnsDoNotPersistAttachments(t *testing.T) {
	for _, endpoint := range []string{"messages", "steer"} {
		t.Run(endpoint, func(t *testing.T) {
			providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
				SessionID: "session-rejected-" + endpoint,
				Capabilities: runtimeplugin.Capabilities{
					PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
				},
			})
			factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
				DisableBuiltinSkills: true, ProviderSessionFactory: factory,
			})
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			t.Cleanup(func() { _ = server.Close() })
			profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
				DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
			})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}

			create := httptest.NewRecorder()
			createRequest := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(
				`{"input":"inspect","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
			))
			createRequest.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(create, createRequest)
			if create.Code != http.StatusCreated {
				t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
			}
			var created struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
				t.Fatalf("decode Session: %v", err)
			}
			found, err := server.sessions.Get(created.ID)
			if err != nil {
				t.Fatalf("read Session Workdir: %v", err)
			}

			filename := "rejected-" + endpoint + ".txt"
			request := multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/"+endpoint,
				`{"message":"must be rejected","runner":"sandbox"}`, filename, "must not persist")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusConflict {
				t.Fatalf("rejected %s status = %d, body=%s", endpoint, response.Code, response.Body.String())
			}
			events, err := server.sessions.Events(created.ID)
			if err != nil {
				t.Fatalf("read Session Events: %v", err)
			}
			for _, event := range events {
				if event.Kind == session.EventKindAttachment && event.Payload["filename"] == filename {
					t.Fatalf("rejected %s persisted attachment Event: %#v", endpoint, event)
				}
			}
			if _, err := os.Stat(filepath.Join(found.Workdir, filename)); !os.IsNotExist(err) {
				t.Fatalf("rejected %s attachment remains in Workdir: %v", endpoint, err)
			}
		})
	}
}

func TestSessionProviderRuntimeOutputKeepsReasoningCorrelationFields(t *testing.T) {
	kind, payload := sessionProviderEventPayload(task.EventKindRuntimeOutput, task.EventPayload{
		"provider": "claude_code", "provider_event": "claude/runtime_output",
		"session_id": "claude-1", "provider_turn_id": "turn-1", "provider_item_id": "turn-1-thinking-0",
		"phase": "streaming", "stream": "stream_event", "text": `{"type":"content_block_delta","delta":{"thinking":"bearer secret-session-token-123456"}}`,
		"raw": "must not persist",
	}, "continuation-1")
	if kind != session.EventKindRuntimeOutput {
		t.Fatalf("provider runtime output kind = %q, want %q", kind, session.EventKindRuntimeOutput)
	}
	if payload["provider_item_id"] != "turn-1-thinking-0" || payload["phase"] != "streaming" || payload["continuation_id"] != "continuation-1" {
		t.Fatalf("provider runtime output lost reasoning correlation: %#v", payload)
	}
	text, _ := payload["text"].(string)
	if strings.Contains(text, "secret-session-token-123456") || !strings.Contains(text, "bearer [REDACTED]") {
		t.Fatalf("session reasoning was not shape-redacted: %q", text)
	}
	if _, leaked := payload["raw"]; leaked {
		t.Fatalf("provider runtime output leaked raw payload: %#v", payload)
	}
}

func TestSessionProviderResultsRemainOnTimelineOutsideConversation(t *testing.T) {
	kind, payload := sessionProviderEventPayload(task.EventKindConversation, task.EventPayload{
		"provider": "codex", "request_id": "request-1", "outcome": "applied", "phase": "provider_turn_applied",
	}, "continuation-1")
	if kind != session.EventKindTurn {
		t.Fatalf("provider result event kind = %q, want %q", kind, session.EventKindTurn)
	}
	if _, ok := payload["continuation_id"]; !ok {
		t.Fatalf("provider result lost continuation correlation: %#v", payload)
	}
}

func TestSessionRuntimeRecoveryFailsClosedAcrossDaemonRestart(t *testing.T) {
	root := t.TempDir()
	config := Config{
		Version: "test", DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	}
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	created, err := server.sessions.Create(session.CreateRequest{Input: "recover this Session"})
	if err != nil {
		_ = server.Close()
		t.Fatalf("create Session: %v", err)
	}
	continuation, err := server.sessions.CreateContinuation(created.ID, "profile:restart", "codex", session.RunnerSandbox)
	if err != nil {
		_ = server.Close()
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationRuntimeMetadata(continuation.ID, "", "native-session", "/managed/native-session.jsonl"); err != nil {
		_ = server.Close()
		t.Fatalf("persist native metadata: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(continuation.ID, session.RuntimeStatusRunning); err != nil {
		_ = server.Close()
		t.Fatalf("mark continuation running: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close initial daemon: %v", err)
	}

	restarted, err := NewServer(config)
	if err != nil {
		t.Fatalf("restart daemon: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	active, err := restarted.sessions.ActiveContinuation(created.ID)
	if err != nil {
		t.Fatalf("read recovered active continuation: %v", err)
	}
	if active != nil {
		t.Fatalf("restart left an active Session continuation: %#v", active)
	}
	latest, err := restarted.sessions.LatestContinuation(created.ID)
	if err != nil || latest == nil || latest.Status != session.RuntimeStatusInterrupted {
		t.Fatalf("recovered continuation = %#v, %v; want interrupted", latest, err)
	}
	events, err := restarted.sessions.Events(created.ID)
	if err != nil {
		t.Fatalf("read recovery events: %v", err)
	}
	var recoveryEvent bool
	for _, event := range events {
		if event.Payload["phase"] == "provider_session_recovery_required" && event.Payload["recovery_state"] == "failed_closed" {
			recoveryEvent = true
		}
	}
	if !recoveryEvent {
		t.Fatalf("restart did not record Session recovery event: %#v", events)
	}
	decorated, err := restarted.decorateSession(created)
	if err != nil {
		t.Fatalf("decorate recovered Session: %v", err)
	}
	if !decorated.RuntimeControls.QueueSteerAvailable {
		t.Fatal("recovered Session does not allow the message that starts its replacement Runtime")
	}
}

func TestSessionRestartCarriesNativeProviderIdentityIntoTheNewContinuation(t *testing.T) {
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "native-session-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, ResumeSession: true},
	})
	factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	created, err := server.sessions.Create(session.CreateRequest{Input: "resume this Session"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: createSnapshotTestModelProvider(t, server, "gpt-session").ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	previous, err := server.sessions.CreateContinuation(created.ID, profile.ID, "codex", session.RunnerHost, testSessionProfileRuntimeSnapshot(t, server, profile, session.RunnerHost))
	if err != nil {
		t.Fatalf("create previous continuation: %v", err)
	}
	previous, err = server.sessions.UpdateContinuationRuntimeMetadata(previous.ID, "", "native-session-1", "/sessions/one.jsonl")
	if err != nil {
		t.Fatalf("store native identity: %v", err)
	}
	if _, err := server.sessions.UpdateContinuationStatus(previous.ID, session.RuntimeStatusStopped); err != nil {
		t.Fatalf("stop previous continuation: %v", err)
	}

	if _, err := server.startSessionRuntime(t.Context(), created, "continue", sessionRuntimeInput{
		Runner: "host", HostActivated: true,
	}); err != nil {
		t.Fatalf("restart Session Runtime: %v", err)
	}
	requests := factory.Requests()
	if len(requests) != 1 {
		t.Fatalf("factory requests = %d, want 1", len(requests))
	}
	if requests[0].Continuation.NativeSessionID != previous.NativeSessionID || requests[0].Continuation.NativeSessionPath != previous.NativeSessionPath {
		t.Fatalf("restart lost native identity: %#v", requests[0].Continuation)
	}
}

func TestResolveSessionRuntimeProfileHonorsNewModelProviderAfterPreviousContinuation(t *testing.T) {
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	glm, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "GLM", BaseURL: "https://glm.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"glm-5.2"}, DefaultModel: "glm-5.2"},
	})
	if err != nil {
		t.Fatalf("create glm provider: %v", err)
	}
	hub, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "HUB", BaseURL: "https://hub.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"MiniMax-M3", "deepseek-v4-flash"}, DefaultModel: "deepseek-v4-flash"},
	})
	if err != nil {
		t.Fatalf("create hub provider: %v", err)
	}
	t.Setenv(glm.APIKeyEnv, "sk-glm")
	t.Setenv(hub.APIKeyEnv, "sk-hub")

	glmProfile, err := server.profiles.Create("Hermes · GLM · glm-5.2", runtimeprofile.ProviderHermes, runtimeprofile.Fields{
		ModelProviderID: glm.ID, ModelOverride: "glm-5.2", DefaultRunner: "sandbox",
	})
	if err != nil {
		t.Fatalf("create glm profile: %v", err)
	}
	created, err := server.sessions.Create(session.CreateRequest{
		Input: "switch provider",
		InitialRuntime: &session.CreateContinuationRequest{
			RuntimeProfileID: glmProfile.ID, RuntimeProvider: "hermes", Runner: session.RunnerSandbox,
			RuntimeConfig: testSessionProfileRuntimeSnapshot(t, server, glmProfile, session.RunnerSandbox),
		},
	})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	previous, err := server.sessions.LatestContinuation(created.ID)
	if err != nil || previous == nil {
		t.Fatalf("latest continuation: %#v, %v", previous, err)
	}

	resolved, err := server.resolveSessionRuntimeProfile(sessionRuntimeInput{
		ModelProviderID: hub.ID,
		Model:           "MiniMax-M3",
	}, previous)
	if err != nil {
		t.Fatalf("resolve Session Runtime Profile: %v", err)
	}
	if resolved.Fields.ModelProviderID != hub.ID {
		t.Fatalf("model provider = %q, want %q", resolved.Fields.ModelProviderID, hub.ID)
	}
	if resolved.Fields.ModelOverride != "MiniMax-M3" {
		t.Fatalf("model override = %q, want MiniMax-M3", resolved.Fields.ModelOverride)
	}

	if _, err := server.prepareSessionRuntime(t.Context(), session.BlackboardModeInteractive, sessionRuntimeInput{
		ModelProviderID: hub.ID,
		Model:           "MiniMax-M3",
	}, previous); err != nil {
		t.Fatalf("prepare Session Runtime after Model Provider switch: %v", err)
	}
}

func TestSessionProviderControlsUseTheSameOwnerContractForBuiltInProviders(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi,
	} {
		t.Run(string(provider), func(t *testing.T) {
			providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
				SessionID:    "session-" + string(provider),
				Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
			})
			factory := &recordingProviderSessionFactory{session: providerSession, adapter: &persistentTestAdapter{}}
			server, err := NewServer(Config{
				Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"), RuntimeRoot: t.TempDir(),
				DisableBuiltinSkills: true, ProviderSessionFactory: factory,
			})
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			t.Cleanup(func() { _ = server.Close() })
			modelProvider := createSnapshotTestModelProvider(t, server, "session-model")
			profile, err := server.profiles.Create(string(provider), provider, runtimeprofile.Fields{
				DefaultRunner: "host", BinaryPath: "/bin/sh", ModelProviderID: modelProvider.ID, ModelOverride: "session-model",
			})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(
				`{"input":"provider conformance","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`,
			))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("create Session status = %d, body=%s", response.Code, response.Body.String())
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && len(factory.Requests()) == 0 {
				time.Sleep(time.Millisecond)
			}
			requests := factory.Requests()
			if len(requests) != 1 || requests[0].Provider != provider {
				t.Fatalf("provider launch requests = %#v", requests)
			}
			if requests[0].Owner.Kind != "session" || requests[0].Owner.SessionID == "" || requests[0].Owner.ProjectID != "" {
				t.Fatalf("provider owner contract = %#v", requests[0].Owner)
			}
		})
	}
}

func TestSessionLaunchUsesProjectFreeOwnerAndPersistentProviderControls(t *testing.T) {
	providerSession := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:         "session-provider-1",
		ManualAcknowledge: true,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true,
			InterruptThenReplace: true, PermissionResponse: true,
		},
	})
	factory := &recordingProviderSessionFactory{
		session: providerSession,
		adapter: &persistentTestAdapter{},
	}
	server, err := NewServer(Config{
		Version: "test", DBPath: filepath.Join(t.TempDir(), "pentest.db"),
		RuntimeRoot: t.TempDir(), DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	modelProvider := createSnapshotTestModelProvider(t, server, "gpt-session")
	profile, err := server.profiles.Create("Session Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		DefaultRunner: "host", ModelProviderID: modelProvider.ID, ModelOverride: "gpt-session",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"input":"inspect the session target","runtime_profile_id":"`+profile.ID+`","runner":"host","host_activated":true}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("create Session status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode Session: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created Session has no id")
	}

	var launchRequest ProviderSessionLaunchRequest
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		requests := factory.Requests()
		if len(requests) > 0 {
			launchRequest = requests[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if launchRequest.Owner.ID != created.ID {
		t.Fatalf("provider owner id = %q, want %q", launchRequest.Owner.ID, created.ID)
	}
	if err := launchRequest.Owner.Validate(); err != nil {
		t.Fatalf("Session owner contract: %v", err)
	}
	if launchRequest.Owner.ProjectID != "" || launchRequest.Continuation.OwnerID != created.ID || launchRequest.Owner.Capabilities.ProjectScope {
		t.Fatalf("provider launch crossed Project/Task boundary: owner=%#v continuation=%#v", launchRequest.Owner, launchRequest.Continuation)
	}
	found, err := server.sessions.Get(created.ID)
	if err != nil {
		t.Fatalf("read Session Workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(found.Workdir, "turn-notes.txt"), []byte("runtime-owned"), 0o600); err != nil {
		t.Fatalf("seed colliding Runtime file: %v", err)
	}

	message := httptest.NewRecorder()
	messageRequest := multipartSessionRequest(t, http.MethodPost, "/api/sessions/"+created.ID+"/messages",
		`{"message":"continue with the second pass"}`, "turn-notes.txt", "operator evidence")
	server.ServeHTTP(message, messageRequest)
	if message.Code != http.StatusAccepted {
		t.Fatalf("Session message status = %d, body=%s", message.Code, message.Body.String())
	}

	conversation := httptest.NewRecorder()
	server.ServeHTTP(conversation, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/conversation", nil))
	if conversation.Code != http.StatusOK {
		t.Fatalf("conversation status = %d, body=%s", conversation.Code, conversation.Body.String())
	}
	var conversationPayload struct {
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.NewDecoder(conversation.Body).Decode(&conversationPayload); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if len(conversationPayload.Events) != 2 {
		t.Fatalf("conversation events = %#v, want initial and continuation input", conversationPayload.Events)
	}
	waitForProviderRequests(t, providerSession, 1)
	messageRequests := providerSession.LastRequests()
	wantMessagePath := filepath.Join(found.Workdir, "turn-notes-1.txt")
	if !strings.Contains(messageRequests[len(messageRequests)-1].Message, "ATTACHED FILES:\n- "+wantMessagePath) {
		t.Fatalf("Session message Runtime input omitted resolved attachment path: %q", messageRequests[len(messageRequests)-1].Message)
	}
	eventsResponse := httptest.NewRecorder()
	server.ServeHTTP(eventsResponse, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/events", nil))
	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("Session Events status = %d, body=%s", eventsResponse.Code, eventsResponse.Body.String())
	}
	var eventsPayload struct {
		Events []session.Event `json:"events"`
	}
	if err := json.NewDecoder(eventsResponse.Body).Decode(&eventsPayload); err != nil {
		t.Fatalf("decode Session Events: %v", err)
	}
	var messageAttachment session.Event
	for _, event := range eventsPayload.Events {
		if event.Kind == session.EventKindAttachment && event.Payload["filename"] == "turn-notes-1.txt" {
			messageAttachment = event
			break
		}
	}
	if messageAttachment.ID == "" || messageAttachment.Payload["continuation_id"] != launchRequest.Continuation.ID {
		t.Fatalf("Session message attachment lacks continuation ownership: %#v", messageAttachment)
	}

	var steerBody bytes.Buffer
	steerWriter := multipart.NewWriter(&steerBody)
	payloadField, err := steerWriter.CreateFormField("payload")
	if err != nil {
		t.Fatalf("create Session steer payload field: %v", err)
	}
	if _, err := payloadField.Write([]byte(`{"message":"interrupt and focus on the login flow"}`)); err != nil {
		t.Fatalf("write Session steer payload: %v", err)
	}
	attachmentField, err := steerWriter.CreateFormFile("attachments", "interrupt-notes.txt")
	if err != nil {
		t.Fatalf("create Session steer attachment: %v", err)
	}
	if _, err := attachmentField.Write([]byte("interrupt evidence")); err != nil {
		t.Fatalf("write Session steer attachment: %v", err)
	}
	if err := steerWriter.Close(); err != nil {
		t.Fatalf("close Session steer multipart body: %v", err)
	}
	steer := httptest.NewRecorder()
	steerRequest := httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/steer", &steerBody)
	steerRequest.Header.Set("Content-Type", steerWriter.FormDataContentType())
	server.ServeHTTP(steer, steerRequest)
	if steer.Code != http.StatusAccepted {
		t.Fatalf("Session steer status = %d, body=%s", steer.Code, steer.Body.String())
	}
	waitForProviderRequests(t, providerSession, 2)
	requests := providerSession.LastRequests()
	if !strings.Contains(requests[len(requests)-1].Message, "ATTACHED FILES:\n- "+filepath.Join(found.Workdir, "interrupt-notes.txt")) {
		t.Fatalf("Session steer Runtime message omitted attachment path: %q", requests[len(requests)-1].Message)
	}
	steerRequestID := requests[len(requests)-1].RequestID
	if err := providerSession.Acknowledge(steerRequestID); err != nil {
		t.Fatalf("acknowledge Session replacement: %v", err)
	}
	replacementDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(replacementDeadline) {
		active, activeErr := server.sessions.ActiveContinuation(created.ID)
		if activeErr != nil {
			t.Fatalf("read active Session continuation: %v", activeErr)
		}
		if active != nil && active.Number == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	active, err := server.sessions.ActiveContinuation(created.ID)
	if err != nil || active == nil || active.Number != 2 {
		t.Fatalf("Session replacement continuation = %#v, %v", active, err)
	}
	var events []session.Event
	var sawReplacement, sawAttachment bool
	eventDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(eventDeadline) {
		events, err = server.sessions.Events(created.ID)
		if err != nil {
			t.Fatalf("read Session replacement events: %v", err)
		}
		for _, event := range events {
			if event.Payload["continuation_id"] == active.ID {
				sawReplacement = true
			}
			if event.Kind == session.EventKindAttachment && event.Payload["filename"] == "interrupt-notes.txt" {
				sawAttachment = event.Payload["continuation_id"] == launchRequest.Continuation.ID
			}
		}
		if sawReplacement {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawReplacement {
		t.Fatalf("Session replacement emitted no events on continuation %q: %#v", active.ID, events)
	}
	if !sawAttachment {
		t.Fatalf("Session steer attachment lost its source continuation: %#v", events)
	}

	timeline := httptest.NewRecorder()
	server.ServeHTTP(timeline, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/timeline", nil))
	if timeline.Code != http.StatusOK {
		t.Fatalf("timeline status = %d, body=%s", timeline.Code, timeline.Body.String())
	}
	var timelinePayload struct {
		SessionID string `json:"session_id"`
		Items     []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.NewDecoder(timeline.Body).Decode(&timelinePayload); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if timelinePayload.SessionID != created.ID {
		t.Fatalf("timeline session_id = %q, want %q", timelinePayload.SessionID, created.ID)
	}
	for _, item := range timelinePayload.Items {
		if item.Type == "" {
			t.Fatalf("timeline item missing type: %#v", timelinePayload.Items)
		}
	}
	// The steer attachment must surface as a lifecycle timeline item rather
	// than vanishing; conversation events are never part of the timeline.
	var sawAttachmentInTimeline bool
	for _, item := range timelinePayload.Items {
		if item.Type == "lifecycle" && strings.HasPrefix(item.Content, "Attached interrupt-notes.txt") {
			sawAttachmentInTimeline = true
		}
	}
	if !sawAttachmentInTimeline {
		t.Fatalf("timeline omitted steer attachment item: %#v", timelinePayload.Items)
	}

	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/sessions/"+created.ID+"/stop", nil))
	if stop.Code != http.StatusOK {
		t.Fatalf("Session stop status = %d, body=%s", stop.Code, stop.Body.String())
	}
}
