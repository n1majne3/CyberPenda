package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/modelprovider"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// #151 primary seam: Host Claude binds one Task-scoped Query session across
// turns, applies Runtime Turn Selection without replacing Query when the
// provider is unchanged, and cleans up process groups on Stop.

type hostClaudeSessionFactory struct {
	mu       sync.Mutex
	requests []ProviderSessionLaunchRequest
	session  runtime.ProviderSession
	adapter  runtime.Adapter
	opens    int
	failOpen error
}

func (f *hostClaudeSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	f.opens++
	if f.failOpen != nil {
		return ProviderSessionBinding{}, f.failOpen
	}
	if request.Runner != task.RunnerHost {
		return ProviderSessionBinding{}, errString("host Claude factory requires host runner")
	}
	if request.Provider != runtimeprofile.ProviderClaudeCode {
		return ProviderSessionBinding{}, errString("host Claude factory requires claude_code")
	}
	if binder, ok := f.session.(runtime.ProviderSessionContinuationBinder); ok {
		_ = binder.BindContinuation(request.Continuation.ID)
	}
	if adapter, ok := f.adapter.(*runtime.ProviderSessionRunAdapter); ok {
		adapter.BindContinuation(request.Continuation.ID)
	}
	return ProviderSessionBinding{Session: f.session, Adapter: f.adapter}, nil
}

func (f *hostClaudeSessionFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func (f *hostClaudeSessionFactory) allRequests() []ProviderSessionLaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProviderSessionLaunchRequest(nil), f.requests...)
}

func newHostClaudePersistentFixture(t *testing.T, factory ProviderSessionFactory) (*Server, task.Task, modelprovider.Provider, runtimeprofile.Profile) {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true, ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"claude-test", "claude-strong"}, DefaultModel: "claude-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.APIKeyEnv, "sk-test")
	profile, err := server.profiles.Create("Claude Host", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		ModelProviderID: provider.ID, ModelOverride: "claude-test", ReasoningEffort: "medium",
		BinaryPath: filepath.Join(root, "claude"), CustomArgs: []string{"--add-dir", "/tmp/extra"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Legacy launch plan still needs a binary path on disk for projection assembly
	// even when the provider-session factory supplies the persistent adapter.
	if err := writeExecutable(filepath.Join(root, "claude"), "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, created, provider, profile
}

func TestHostClaudeLaunchBindsPersistentQueryAcrossTurns(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "host-claude-session-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &hostClaudeSessionFactory{session: session, adapter: adapter}
	server, created, provider, _ := newHostClaudePersistentFixture(t, factory)

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
	waitForProviderRequests(t, session, 1)

	first := session.LastRequests()[0]
	if first.ModelProviderID != provider.ID || first.Model != "claude-test" || first.RequestedReasoningEffort != "medium" {
		t.Fatalf("initial turn selection = %#v", first)
	}
	bound, ok := server.providerSessions.get(created.ID)
	if !ok || bound.SessionID() != "host-claude-session-1" {
		t.Fatalf("bound session = ok=%v id=%q", ok, sessionIDOf(bound))
	}
	if factory.openCount() != 1 {
		t.Fatalf("factory opens = %d, want 1", factory.openCount())
	}
	req0 := factory.allRequests()[0]
	if req0.Runner != task.RunnerHost || req0.Provider != runtimeprofile.ProviderClaudeCode {
		t.Fatalf("factory request identity = %#v", req0)
	}

	// Same-provider second turn reuses the Query session without factory re-open.
	body := `{
		"request_id":"host-claude-turn-2",
		"message":"continue deeper",
		"model_provider_id":` + quoteJSON(provider.ID) + `,
		"model":"claude-strong",
		"reasoning_effort":"xhigh"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("steer status = %d body %s", resp.Code, resp.Body.String())
	}
	waitForProviderRequests(t, session, 2)
	second := session.LastRequests()[1]
	if second.Model != "claude-strong" || second.RequestedReasoningEffort != "xhigh" || second.ModelProviderID != provider.ID {
		t.Fatalf("second turn selection = %#v", second)
	}
	if factory.openCount() != 1 {
		t.Fatalf("same-provider turn opened a new session: opens=%d", factory.openCount())
	}
	if session.SessionID() != "host-claude-session-1" {
		t.Fatalf("Query session identity changed to %q", session.SessionID())
	}
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Payload["request_id"] == "host-claude-turn-2" && (event.Payload["phase"] == "steering_applied" || event.Payload["outcome"] == "settled" || event.Kind == task.EventKindConversation) {
				return true
			}
		}
		return false
	})

	// Stop closes the bound session and leaves no duplicate ownership.
	var stopStatus int
	var stopBody string
	deadlineStop := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadlineStop) {
		stopReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/stop", nil)
		stopResp := httptest.NewRecorder()
		server.ServeHTTP(stopResp, stopReq)
		stopStatus, stopBody = stopResp.Code, stopResp.Body.String()
		if stopStatus == http.StatusOK || stopStatus == http.StatusAccepted {
			break
		}
		if stopStatus != http.StatusConflict {
			t.Fatalf("stop status = %d body %s", stopStatus, stopBody)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stopStatus != http.StatusOK && stopStatus != http.StatusAccepted {
		t.Fatalf("stop status = %d body %s", stopStatus, stopBody)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := server.providerSessions.get(created.ID); !ok && !server.harness.IsActive(created.ID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("provider session still bound after Stop")
	}
}

func TestHostClaudeProviderChangeQueuesMessageAndCreatesConfigVersion(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "host-claude-session-a",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &hostClaudeSessionFactory{session: session, adapter: adapter}
	server, created, provider, _ := newHostClaudePersistentFixture(t, factory)

	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(alternate.APIKeyEnv, "sk-alt")

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "high")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
	waitForProviderRequests(t, session, 1)

	body := `{
		"directive":"switch endpoint",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2",
		"reasoning_effort":"max"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer/queue", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("queue status = %d body %s", resp.Code, resp.Body.String())
	}
	var queued struct {
		Event struct {
			Payload map[string]any `json:"payload"`
		} `json:"event"`
		RuntimeConfigVersion *struct {
			Config map[string]any `json:"config"`
		} `json:"runtime_config_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if queued.Event.Payload["directive"] != "switch endpoint" {
		t.Fatalf("queued message dropped: %#v", queued.Event.Payload)
	}
	if queued.RuntimeConfigVersion == nil {
		t.Fatal("provider change must create Runtime Config Version for host Claude")
	}
	if queued.RuntimeConfigVersion.Config["model_provider_id"] != alternate.ID {
		t.Fatalf("config = %#v", queued.RuntimeConfigVersion.Config)
	}
	if factory.openCount() != 1 {
		t.Fatalf("queue path opened unexpected sessions: %d", factory.openCount())
	}
	_ = provider
}

func TestHostClaudeMissingNativeMetadataFallsBackToFreshContinuation(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "claude")
	if err := writeExecutable(binary, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		DisableBuiltinSkills: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Claude", runtimeprofile.ProviderClaudeCode, runtimeprofile.Fields{
		Model: "claude-test", BinaryPath: binary, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect", RuntimeProfileID: profile.ID, Runner: task.RunnerHost,
		RunControls: task.RunControls{HostActivated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusStopped); err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "claude_code", task.RunnerHost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusStopped); err != nil {
		t.Fatal(err)
	}

	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, goal, plan, err := server.prepareResumeContinuation(found, "continue after stop")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ID != created.ID {
		t.Fatalf("resume replaced Task identity: %#v", prepared)
	}
	if strings.TrimSpace(goal) == "" {
		t.Fatal("resume dropped the continuation goal/message")
	}
	if plan.NativeResumeSessionID != "" {
		t.Fatalf("missing metadata must not force native resume id, got %q", plan.NativeResumeSessionID)
	}
}

func TestHostClaudeBridgeSetupFailureDoesNotOneShotFallback(t *testing.T) {
	// Once the persistent path is selected, a missing packaged bridge must fail
	// the launch with a clear factory error and must not run the legacy CLI adapter.
	factory := &hostClaudeSessionFactory{failOpen: errString("Claude SDK bridge unavailable at /opt/missing-bridge: executable file not found")}
	server, created, _, _ := newHostClaudePersistentFixture(t, factory)

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	err = server.launchTaskInBackground(created, plan, created.Goal)
	if err == nil {
		t.Fatal("expected launch failure when Claude SDK bridge is unavailable")
	}
	if !strings.Contains(err.Error(), "provider session setup failed") {
		t.Fatalf("error = %v, want provider session setup failed", err)
	}
	if factory.openCount() != 1 {
		t.Fatalf("factory opens = %d, want 1 (persistent path selected)", factory.openCount())
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("failed launch must not leave a bound session")
	}
	if server.harness.IsActive(created.ID) {
		t.Fatal("failed launch must not leave an active harness (no one-shot fallback)")
	}
}
