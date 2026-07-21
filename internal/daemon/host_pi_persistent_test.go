package daemon

import (
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

// #152 primary seam: Host Pi binds one Task-scoped RPC session across turns via
// the piWire translation boundary (pentest-provider-bridge), applies projected
// provider/model/effort natively, and cleans process groups + session files +
// credentials on Stop without falling back to one-shot.

type hostPiSessionFactory struct {
	mu       sync.Mutex
	requests []ProviderSessionLaunchRequest
	session  runtime.ProviderSession
	adapter  runtime.Adapter
	opens    int
	err      error
}

func (f *hostPiSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	f.opens++
	if f.err != nil {
		return ProviderSessionBinding{}, f.err
	}
	if request.Runner != task.RunnerHost {
		return ProviderSessionBinding{}, errString("host pi factory requires host runner")
	}
	if request.Provider != runtimeprofile.ProviderPi {
		return ProviderSessionBinding{}, errString("host pi factory requires pi")
	}
	if binder, ok := f.session.(runtime.ProviderSessionContinuationBinder); ok {
		_ = binder.BindContinuation(request.Continuation.ID)
	}
	if adapter, ok := f.adapter.(*runtime.ProviderSessionRunAdapter); ok {
		adapter.BindContinuation(request.Continuation.ID)
	}
	return ProviderSessionBinding{Session: f.session, Adapter: f.adapter}, nil
}

func (f *hostPiSessionFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func newHostPiPersistentFixture(t *testing.T, factory ProviderSessionFactory) (*Server, task.Task, modelprovider.Provider, modelprovider.Provider) {
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
	primary, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"m1", "m1-strong"}, DefaultModel: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Alternate", BaseURL: "https://b.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"m2"}, DefaultModel: "m2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(primary.APIKeyEnv, "sk-primary")
	t.Setenv(alternate.APIKeyEnv, "sk-alt")
	profile, err := server.profiles.Create("Pi Host", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: primary.ID, ModelOverride: "m1", ReasoningEffort: "medium",
		BinaryPath: filepath.Join(root, "pi"), CustomArgs: []string{"--debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(filepath.Join(root, "pi"), "#!/bin/sh\necho ok\n"); err != nil {
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
	return server, created, primary, alternate
}

// cleanupHostPersistentRuntime ends a Task-scoped persistent session after
// assertions. A successful persistent launch is not expected to return on its
// own; tests must close the adapter done signal, release the provider session,
// and stop the harness so the package does not hang.
func cleanupHostPersistentRuntime(t *testing.T, server *Server, taskID string, done chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		if done != nil {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		if server == nil || taskID == "" {
			return
		}
		server.harness.Stop(taskID)
		_ = server.closeProviderSession(taskID)
		_ = server.harness.StopAndWait(taskID, 2*time.Second)
	})
}

func assertRuntimeConfigHasNoSecrets(t *testing.T, config map[string]any, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal runtime config: %v", err)
	}
	body := string(raw)
	for _, secret := range forbidden {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatalf("runtime config leaked secret material %q: %s", secret, body)
		}
	}
	for _, key := range []string{"auth_json", "auth_path", "api_key", "api_keys", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		if _, ok := config[key]; ok {
			t.Fatalf("runtime config must not store credential field %q: %#v", key, config[key])
		}
	}
}

func TestHostPiLaunchBindsPersistentRPCSessionAcrossTurns(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "host-pi-session-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
			InTurnSteer: true, ResumeSession: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &hostPiSessionFactory{session: session, adapter: adapter}
	server, created, primary, alternate := newHostPiPersistentFixture(t, factory)

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	cleanupHostPersistentRuntime(t, server, created.ID, closed)
	waitForHarnessActive(t, server, created.ID, true)
	waitForProviderRequests(t, session, 1)

	// Fixed startup projection (after Config Projection) includes every
	// launch-ready Pi provider; drafts never block this set. Stored config is
	// non-secret: projected IDs only, never API keys or auth previews.
	versions, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil || len(versions) == 0 {
		t.Fatalf("runtime config versions: %v len=%d", err, len(versions))
	}
	stored := versions[len(versions)-1].Config
	ids := stringSliceFromConfig(stored["projected_model_provider_ids"])
	if len(ids) < 2 || !containsString(ids, primary.ID) || !containsString(ids, alternate.ID) {
		t.Fatalf("projected set = %#v, want both launch-ready providers", ids)
	}
	assertRuntimeConfigHasNoSecrets(t, stored, "sk-primary", "sk-alt")

	first := session.LastRequests()[0]
	if first.ModelProviderID != primary.ID || first.Model != "m1" || first.RequestedReasoningEffort != "medium" {
		t.Fatalf("initial turn selection = %#v", first)
	}
	bound, ok := server.providerSessions.get(created.ID)
	if !ok || bound.SessionID() != "host-pi-session-1" {
		t.Fatalf("bound session = ok=%v id=%q", ok, sessionIDOf(bound))
	}
	if factory.openCount() != 1 {
		t.Fatalf("factory opens = %d, want 1", factory.openCount())
	}

	// Projected provider/model/effort switch is native — no Runtime restart.
	body := `{
		"request_id":"host-pi-turn-2",
		"message":"switch projected provider",
		"model_provider_id":` + quoteJSON(alternate.ID) + `,
		"model":"m2",
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
	if second.ModelProviderID != alternate.ID || second.Model != "m2" || second.RequestedReasoningEffort != "xhigh" {
		t.Fatalf("second turn selection = %#v", second)
	}
	if factory.openCount() != 1 {
		t.Fatalf("projected provider switch opened a new session: opens=%d", factory.openCount())
	}
	if session.SessionID() != "host-pi-session-1" {
		t.Fatalf("RPC session identity changed to %q", session.SessionID())
	}

	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Payload["request_id"] == "host-pi-turn-2" && (event.Payload["phase"] == "steering_applied" || event.Payload["outcome"] == "settled" || event.Kind == task.EventKindConversation) {
				return true
			}
		}
		return false
	})

	// Explicit Stop closes the bound session and leaves no duplicate ownership.
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

func TestHostPiOutsideProjectedSetRequiresRestartPath(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "host-pi-session-a",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true, InTurnSteer: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &hostPiSessionFactory{session: session, adapter: adapter}
	server, created, primary, _ := newHostPiPersistentFixture(t, factory)

	outside, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Outside", BaseURL: "https://out.example/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIChatCompletions},
		Catalog:   modelprovider.Catalog{Manual: []string{"out"}, DefaultModel: "out"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// No API key: remains non-launch-ready and must not enter the projection.

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "high")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	cleanupHostPersistentRuntime(t, server, created.ID, closed)
	waitForHarnessActive(t, server, created.ID, true)
	waitForProviderRequests(t, session, 1)

	versions, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil || len(versions) == 0 {
		t.Fatalf("runtime config versions: %v len=%d", err, len(versions))
	}
	stored := versions[len(versions)-1].Config
	ids := stringSliceFromConfig(stored["projected_model_provider_ids"])
	if containsString(ids, outside.ID) {
		t.Fatalf("draft/non-ready provider leaked into projection: %#v", ids)
	}
	if !containsString(ids, primary.ID) {
		t.Fatalf("launch-ready primary missing from projection: %#v", ids)
	}
	assertRuntimeConfigHasNoSecrets(t, stored, "sk-primary", "sk-alt")

	// Outside the fixed projected set: native steer fails closed (restart required).
	t.Setenv(outside.APIKeyEnv, "sk-out")
	body := `{
		"request_id":"host-pi-outside",
		"message":"use outside",
		"model_provider_id":` + quoteJSON(outside.ID) + `,
		"model":"out",
		"reasoning_effort":"max"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("outside projected set status = %d body %s", resp.Code, resp.Body.String())
	}
	if factory.openCount() != 1 {
		t.Fatalf("outside steer must not open sessions: %d", factory.openCount())
	}
}

func TestHostPiFactoryFailureDoesNotFallBackToOneShot(t *testing.T) {
	factory := &hostPiSessionFactory{err: errString("pi bridge unavailable")}
	server, created, _, _ := newHostPiPersistentFixture(t, factory)

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	err = server.launchTaskInBackground(created, plan, created.Goal)
	if err == nil {
		t.Fatal("expected factory failure to fail launch")
	}
	if !strings.Contains(err.Error(), "provider session setup failed") {
		t.Fatalf("error = %v, want redacted factory failure", err)
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("failed launch must not bind a session")
	}
	// Harness must not run the legacy one-shot adapter after persistent selection.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if server.harness.IsActive(created.ID) {
			t.Fatal("one-shot harness must not start after persistent factory failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	found, getErr := server.tasks.Get(created.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if found.Status != task.StatusFailed {
		t.Fatalf("status = %q, want failed", found.Status)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
