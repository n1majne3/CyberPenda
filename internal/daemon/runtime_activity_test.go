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

// #150 primary seam: daemon Task HTTP exposes Runtime Activity from current
// process/session health, with lifecycle side effects and no activity audits.

type activitySessionFactory struct {
	mu       sync.Mutex
	session  runtime.ProviderSession
	adapter  runtime.Adapter
	provider runtimeprofile.Provider
	runner   task.Runner
	opens    int
}

func (f *activitySessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.provider != "" && request.Provider != f.provider {
		return ProviderSessionBinding{}, errString("provider mismatch")
	}
	if f.runner != "" && request.Runner != f.runner {
		return ProviderSessionBinding{}, errString("runner mismatch")
	}
	if binder, ok := f.session.(runtime.ProviderSessionContinuationBinder); ok {
		_ = binder.BindContinuation(request.Continuation.ID)
	}
	if adapter, ok := f.adapter.(*runtime.ProviderSessionRunAdapter); ok {
		adapter.BindContinuation(request.Continuation.ID)
	}
	return ProviderSessionBinding{Session: f.session, Adapter: f.adapter}, nil
}

func (f *activitySessionFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

type runtimeActivityBody struct {
	Status          string `json:"status"`
	RuntimeActivity struct {
		Liveness     string `json:"liveness"`
		TurnActivity string `json:"turn_activity"`
		Warning      string `json:"warning"`
	} `json:"runtime_activity"`
}

func newRuntimeActivityFixture(t *testing.T, provider runtimeprofile.Provider, runner task.Runner, factory ProviderSessionFactory) (*Server, task.Task, modelprovider.Provider) {
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
	mp, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name: "Primary", BaseURL: "https://api.example.test/v1",
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses, modelprovider.ProtocolAnthropicMessages},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test", "claude-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mp.APIKeyEnv, "sk-test")

	binary := filepath.Join(root, string(provider))
	if err := writeExecutable(binary, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	fields := runtimeprofile.Fields{
		ModelProviderID: mp.ID, ModelOverride: "gpt-test", ReasoningEffort: "medium",
		BinaryPath: binary,
	}
	if provider == runtimeprofile.ProviderClaudeCode {
		fields.ModelOverride = "claude-test"
	}
	profile, err := server.profiles.Create(string(provider)+" profile", provider, fields)
	if err != nil {
		t.Fatal(err)
	}
	createReq := task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: runner,
	}
	if runner == task.RunnerHost {
		createReq.RunControls = task.RunControls{HostActivated: true}
	}
	created, err := server.tasks.Create(createReq)
	if err != nil {
		t.Fatal(err)
	}
	return server, created, mp
}

func getTaskActivity(t *testing.T, server *Server, projectID, taskID string) runtimeActivityBody {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("get task status = %d body %s", resp.Code, resp.Body.String())
	}
	var body runtimeActivityBody
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func launchActivityTask(t *testing.T, server *Server, created task.Task) {
	t.Helper()
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
}

func TestRuntimeActivityLiveIdleAndBusyForSandboxProviders(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex,
		runtimeprofile.ProviderClaudeCode,
		runtimeprofile.ProviderPi,
	} {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
				SessionID:         "session-" + string(provider),
				ManualAcknowledge: true,
				Capabilities: runtimeplugin.Capabilities{
					PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
					InTurnSteer: provider == runtimeprofile.ProviderPi,
				},
			})
			closed := make(chan struct{})
			adapter := runtime.NewProviderSessionRunAdapter(session, closed)
			factory := &activitySessionFactory{session: session, adapter: adapter, provider: provider, runner: task.RunnerSandbox}
			server, created, mp := newRuntimeActivityFixture(t, provider, task.RunnerSandbox, factory)
			launchActivityTask(t, server, created)

			// After launch turn settles, health is live/idle independent of Task status.
			deadline := time.Now().Add(2 * time.Second)
			var body runtimeActivityBody
			for time.Now().Before(deadline) {
				body = getTaskActivity(t, server, created.ProjectID, created.ID)
				if body.RuntimeActivity.Liveness == "live" && body.RuntimeActivity.TurnActivity == "idle" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if body.Status != "running" {
				t.Fatalf("status = %q, want running (independent of activity)", body.Status)
			}
			if body.RuntimeActivity.Liveness != "live" || body.RuntimeActivity.TurnActivity != "idle" {
				t.Fatalf("activity after launch = %#v", body.RuntimeActivity)
			}

			model := "gpt-test"
			if provider == runtimeprofile.ProviderClaudeCode {
				model = "claude-test"
			}
			bodyJSON := `{"request_id":"act-steer-1","message":"continue","model_provider_id":` + quoteJSON(mp.ID) + `,"model":` + quoteJSON(model) + `,"reasoning_effort":"high"}`
			req := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", strings.NewReader(bodyJSON))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			server.ServeHTTP(resp, req)
			if resp.Code != http.StatusAccepted {
				t.Fatalf("steer status = %d body %s", resp.Code, resp.Body.String())
			}

			// Manual-ack native steer keeps ControlBusy true until acknowledged.
			deadline = time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				body = getTaskActivity(t, server, created.ProjectID, created.ID)
				if body.RuntimeActivity.Liveness == "live" && body.RuntimeActivity.TurnActivity == "busy" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if body.RuntimeActivity.Liveness != "live" || body.RuntimeActivity.TurnActivity != "busy" {
				t.Fatalf("activity during steer = %#v", body.RuntimeActivity)
			}

			if err := session.Acknowledge("act-steer-1"); err != nil {
				t.Fatalf("acknowledge steer: %v", err)
			}
			deadline = time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				body = getTaskActivity(t, server, created.ProjectID, created.ID)
				if body.RuntimeActivity.TurnActivity == "idle" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if body.RuntimeActivity.Liveness != "live" || body.RuntimeActivity.TurnActivity != "idle" {
				t.Fatalf("activity after steer settle = %#v", body.RuntimeActivity)
			}

			close(closed)
		})
	}
}

func TestRuntimeActivityHostCodexHealthTransitions(t *testing.T) {
	testRuntimeActivityHostHealthTransitions(t, runtimeprofile.ProviderCodex)
}

func TestRuntimeActivityHostClaudeHealthTransitions(t *testing.T) {
	// #151: Host Claude uses the same truthful Runtime Activity contract as Host Codex.
	testRuntimeActivityHostHealthTransitions(t, runtimeprofile.ProviderClaudeCode)
}

func TestRuntimeActivityHostPiHealthTransitions(t *testing.T) {
	testRuntimeActivityHostHealthTransitions(t, runtimeprofile.ProviderPi)
}

func testRuntimeActivityHostHealthTransitions(t *testing.T, provider runtimeprofile.Provider) {
	t.Helper()
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "host-activity-" + string(provider),
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
			InTurnSteer: provider == runtimeprofile.ProviderPi,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &activitySessionFactory{
		session: session, adapter: adapter,
		provider: provider, runner: task.RunnerHost,
	}
	server, created, _ := newRuntimeActivityFixture(t, provider, task.RunnerHost, factory)
	launchActivityTask(t, server, created)

	body := getTaskActivity(t, server, created.ProjectID, created.ID)
	if body.Status != "running" || body.RuntimeActivity.Liveness != "live" {
		t.Fatalf("host live activity = status %q activity %#v", body.Status, body.RuntimeActivity)
	}
	if body.RuntimeActivity.TurnActivity != "idle" && body.RuntimeActivity.TurnActivity != "busy" {
		t.Fatalf("host turn activity = %q", body.RuntimeActivity.TurnActivity)
	}

	// Unexpected exit: terminal done signal + offline session health.
	session.MarkOffline()
	close(closed)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body = getTaskActivity(t, server, created.ProjectID, created.ID)
		if body.Status == "failed" && body.RuntimeActivity.Liveness == "offline" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if body.Status != "failed" {
		t.Fatalf("status after unexpected exit = %q, want failed", body.Status)
	}
	if body.RuntimeActivity.Liveness != "offline" {
		t.Fatalf("activity after unexpected exit = %#v", body.RuntimeActivity)
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("stale ownership retained after unexpected exit")
	}
	if server.harness.IsActive(created.ID) {
		t.Fatal("harness still active after unexpected exit")
	}
}

func TestRuntimeActivityOrphanedInterruptsWithoutStoredSessionAuthority(t *testing.T) {
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectRecord, err := server.projects.Create("Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "orphan case", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(created.ID, profile.ID, "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	// Stored native session identity must not make the Runtime look live.
	if _, err := server.tasks.UpdateContinuationRuntimeMetadata(continuation.ID, "container-x", "native-session-x", "/sessions/x.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	body := getTaskActivity(t, server, created.ProjectID, created.ID)
	if body.Status != "interrupted" {
		t.Fatalf("status = %q, want interrupted for orphaned ownership loss", body.Status)
	}
	// Task detail must keep exposing orphaned after ownership loss — not live,
	// and not collapsed away solely because lifecycle became interrupted.
	if body.RuntimeActivity.Liveness != "orphaned" {
		t.Fatalf("activity = %#v, want orphaned on Task detail", body.RuntimeActivity)
	}
}

func TestRuntimeActivityLostSessionOwnershipIsOrphanedNotLive(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-lost",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &activitySessionFactory{session: session, adapter: adapter, provider: runtimeprofile.ProviderCodex}
	server, created, _ := newRuntimeActivityFixture(t, runtimeprofile.ProviderCodex, task.RunnerSandbox, factory)
	launchActivityTask(t, server, created)

	// Drop daemon session ownership while the Task is still durable-active.
	// Persistent Runtime must report orphaned, never live, without a bound session.
	_ = server.providerSessions.remove(created.ID)
	body := getTaskActivity(t, server, created.ProjectID, created.ID)
	if body.RuntimeActivity.Liveness == "live" {
		t.Fatalf("lost session ownership reported live: %#v", body.RuntimeActivity)
	}
	if body.Status != "interrupted" {
		t.Fatalf("status = %q, want interrupted", body.Status)
	}
	if body.RuntimeActivity.Liveness != "orphaned" {
		t.Fatalf("activity = %#v, want orphaned", body.RuntimeActivity)
	}
	close(closed)
}

func TestRuntimeActivityExplicitStopIsNotUnexpectedOffline(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-stop",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &activitySessionFactory{session: session, adapter: adapter, provider: runtimeprofile.ProviderCodex}
	server, created, _ := newRuntimeActivityFixture(t, runtimeprofile.ProviderCodex, task.RunnerSandbox, factory)
	launchActivityTask(t, server, created)

	// Operator Stop closes the session explicitly; must not append runtime_offline failure.
	stopReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/stop", nil)
	stopResp := httptest.NewRecorder()
	// Close the adapter done channel as production Close would via bridge.Closed.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(closed)
	}()
	server.ServeHTTP(stopResp, stopReq)
	if stopResp.Code != http.StatusOK && stopResp.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d body %s", stopResp.Code, stopResp.Body.String())
	}

	events, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Payload["reason"] == "runtime_offline" {
			t.Fatalf("explicit Stop recorded unexpected offline lifecycle: %#v", event)
		}
		if event.Kind == "runtime_activity" {
			t.Fatalf("unexpected runtime activity audit: %#v", event)
		}
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status == task.StatusFailed {
		t.Fatalf("explicit Stop left Task failed; want stopped/interrupted, got %q", found.Status)
	}
}

func TestRuntimeActivityUnknownDoesNotMutateLifecycle(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-unknown",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	session.MarkHealthUnknown()
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &activitySessionFactory{session: session, adapter: adapter, provider: runtimeprofile.ProviderCodex}
	server, created, _ := newRuntimeActivityFixture(t, runtimeprofile.ProviderCodex, task.RunnerSandbox, factory)
	launchActivityTask(t, server, created)

	body := getTaskActivity(t, server, created.ProjectID, created.ID)
	if body.Status != "running" {
		t.Fatalf("status = %q, want running (unknown must not mutate lifecycle)", body.Status)
	}
	if body.RuntimeActivity.Liveness != "unknown" {
		t.Fatalf("activity = %#v, want unknown", body.RuntimeActivity)
	}
	if body.RuntimeActivity.Warning == "" {
		t.Fatal("unknown activity should carry a warning")
	}
	close(closed)
}

func TestRuntimeActivityListDecoratesSeparatelyFromStatus(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-list",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &activitySessionFactory{session: session, adapter: adapter, provider: runtimeprofile.ProviderCodex}
	server, created, _ := newRuntimeActivityFixture(t, runtimeprofile.ProviderCodex, task.RunnerSandbox, factory)
	launchActivityTask(t, server, created)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+created.ProjectID+"/tasks", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("list status = %d body %s", resp.Code, resp.Body.String())
	}
	var listed struct {
		Tasks []runtimeActivityBody `json:"tasks"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("tasks = %d", len(listed.Tasks))
	}
	got := listed.Tasks[0]
	if got.Status != "running" {
		t.Fatalf("list status = %q", got.Status)
	}
	if got.RuntimeActivity.Liveness != "live" {
		t.Fatalf("list activity = %#v", got.RuntimeActivity)
	}
	close(closed)
}

func TestRuntimeActivityOrphanCleanupBeforeReplacementLaunch(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-replace",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true, ResumeSession: true},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &activitySessionFactory{session: session, adapter: adapter, provider: runtimeprofile.ProviderCodex}
	server, created, _ := newRuntimeActivityFixture(t, runtimeprofile.ProviderCodex, task.RunnerSandbox, factory)
	launchActivityTask(t, server, created)
	if factory.openCount() != 1 {
		t.Fatalf("opens = %d", factory.openCount())
	}

	// Simulate a prior Runtime still bound while Task is terminal; replacement
	// must clean it up before opening a new session.
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusStopped); err != nil {
		t.Fatal(err)
	}
	if cont, err := server.tasks.ActiveContinuation(created.ID); err == nil && cont != nil {
		_, _ = server.tasks.UpdateContinuationStatus(cont.ID, task.StatusStopped)
	}
	// Leave session bound and harness stopped to force orphan cleanup path.
	server.harness.StopAndWait(created.ID, 2*time.Second)

	// Replacement launch via resume.
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/resume", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	// Resume may succeed or conflict depending on continuation reconciliation;
	// ownership must not double-bind either way.
	if factory.openCount() > 2 {
		t.Fatalf("replacement opened too many sessions: %d", factory.openCount())
	}
	close(closed)
}

func TestRuntimeActivityNoExtraAuditRecords(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "session-audit",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &activitySessionFactory{session: session, adapter: adapter, provider: runtimeprofile.ProviderCodex}
	server, created, _ := newRuntimeActivityFixture(t, runtimeprofile.ProviderCodex, task.RunnerSandbox, factory)
	launchActivityTask(t, server, created)

	// Wait until the Runtime is live/idle and launch event writes have settled so
	// concurrent initial-turn timeline records are not mistaken for GET side effects.
	deadline := time.Now().Add(3 * time.Second)
	var settled int
	for time.Now().Before(deadline) {
		body := getTaskActivity(t, server, created.ProjectID, created.ID)
		events, err := server.tasks.Events(created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if body.RuntimeActivity.Liveness == "live" && body.RuntimeActivity.TurnActivity == "idle" &&
			body.Status == "running" && len(events) == settled && settled > 0 {
			break
		}
		settled = len(events)
		time.Sleep(10 * time.Millisecond)
	}

	before, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := getTaskActivity(t, server, created.ProjectID, created.ID)
	if body.RuntimeActivity.Liveness != "live" {
		t.Fatalf("setup activity = %#v status %q, want live running", body.RuntimeActivity, body.Status)
	}
	after, err := server.tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("GET activity created events: before %d after %d", len(before), len(after))
	}
	for _, event := range after {
		if phase, _ := event.Payload["phase"].(string); strings.Contains(phase, "runtime_activity") {
			t.Fatalf("unexpected runtime activity audit event: %#v", event)
		}
		if event.Kind == "runtime_activity" {
			t.Fatalf("unexpected runtime_activity event kind: %#v", event)
		}
	}
	close(closed)
}
