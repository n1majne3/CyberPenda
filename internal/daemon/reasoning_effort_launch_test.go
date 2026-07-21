package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// TestCodexLaunchSendsResolvedRequestedReasoningEffort is the primary #144
// acceptance seam: real domain services + SQLite, deterministic FakeProviderSession.
func TestCodexLaunchSendsResolvedRequestedReasoningEffort(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "effort-session-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &effortProviderSessionFactory{session: session, adapter: adapter}

	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	// Profile default is medium; launch override max must win and not mutate the profile.
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test", SandboxImage: "cyberpenda:test", ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "max")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.CapturedRuntimeConfig["requested_reasoning_effort"]; got != "max" {
		t.Fatalf("captured requested effort = %#v, want max", got)
	}
	if got := plan.CapturedRuntimeConfig["launch_reasoning_effort_override"]; got != "max" {
		t.Fatalf("captured launch override = %#v, want max", got)
	}
	if plan.LaunchReasoningEffort != "max" {
		t.Fatalf("plan launch effort = %q, want max", plan.LaunchReasoningEffort)
	}

	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)

	deadline := time.Now().Add(2 * time.Second)
	var requests []runtime.ProviderSessionRequest
	for time.Now().Before(deadline) {
		requests = session.LastRequests()
		if len(requests) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(requests) == 0 {
		t.Fatal("expected FakeProviderSession to receive the initial turn")
	}
	first := requests[0]
	if first.RequestedReasoningEffort != "max" {
		t.Fatalf("initial turn effort = %q, want max", first.RequestedReasoningEffort)
	}
	if first.Model != "gpt-test" {
		t.Fatalf("initial turn model = %q, want gpt-test", first.Model)
	}
	if first.EffectiveReasoningEffort != "" {
		t.Fatalf("effective effort must stay unknown unless reported, got %q", first.EffectiveReasoningEffort)
	}

	versions, err := server.tasks.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) == 0 {
		t.Fatal("expected captured runtime config version")
	}
	if got := versions[0].Config["requested_reasoning_effort"]; got != "max" {
		t.Fatalf("stored requested effort = %#v, want max", got)
	}

	stored, err := server.profiles.Get(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fields.ReasoningEffort != "medium" {
		t.Fatalf("profile effort mutated to %q", stored.Fields.ReasoningEffort)
	}

	server.harness.StopAndWait(created.ID, 2*time.Second)
}

func TestCodexLaunchDefaultsRequestedReasoningEffortToHighWhenMissing(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "effort-session-default",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true,
		},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &effortProviderSessionFactory{session: session, adapter: adapter}

	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test", SandboxImage: "cyberpenda:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.CapturedRuntimeConfig["requested_reasoning_effort"]; got != "high" {
		t.Fatalf("default requested effort = %#v, want high", got)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)

	deadline := time.Now().Add(2 * time.Second)
	var requests []runtime.ProviderSessionRequest
	for time.Now().Before(deadline) {
		requests = session.LastRequests()
		if len(requests) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(requests) == 0 {
		t.Fatal("expected initial turn request")
	}
	if requests[0].RequestedReasoningEffort != "high" {
		t.Fatalf("initial turn effort = %q, want high", requests[0].RequestedReasoningEffort)
	}
	server.harness.StopAndWait(created.ID, 2*time.Second)
}

func TestTaskCreateHTTPAcceptsLaunchReasoningEffortOverrideWithoutMutatingProfile(t *testing.T) {
	// HTTP path for launch override validation; uses fake provider so preflight/provider session stay lightweight.
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "effort-http",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	closed := make(chan struct{})
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	factory := &effortProviderSessionFactory{session: session, adapter: adapter}

	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	projectRecord, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		Model: "gpt-test", SandboxImage: "cyberpenda:test", ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"goal":"inspect example.com","runtime_profile_id":"` + profile.ID + `","reasoning_effort":"xhigh","runner":"sandbox"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectRecord.ID+"/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create task status %d body %s", resp.Code, resp.Body.String())
	}

	waitForHarnessActive(t, server, jsonTaskID(t, resp.Body.Bytes()), true)
	deadline := time.Now().Add(2 * time.Second)
	var requests []runtime.ProviderSessionRequest
	for time.Now().Before(deadline) {
		requests = session.LastRequests()
		if len(requests) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(requests) == 0 {
		t.Fatal("expected initial turn from HTTP launch")
	}
	if requests[0].RequestedReasoningEffort != "xhigh" {
		t.Fatalf("HTTP launch effort = %q, want xhigh", requests[0].RequestedReasoningEffort)
	}

	stored, err := server.profiles.Get(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fields.ReasoningEffort != "low" {
		t.Fatalf("profile mutated to %q", stored.Fields.ReasoningEffort)
	}
	server.harness.StopAndWait(createdTaskIDFromBody(t, resp.Body.Bytes()), 2*time.Second)
}

type effortProviderSessionFactory struct {
	session *runtime.FakeProviderSession
	adapter *runtime.ProviderSessionRunAdapter
}

func (f *effortProviderSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.adapter.BindContinuation(request.Continuation.ID)
	return ProviderSessionBinding{Session: f.session, Adapter: f.adapter}, nil
}

func jsonTaskID(t *testing.T, body []byte) string {
	t.Helper()
	return createdTaskIDFromBody(t, body)
}

func createdTaskIDFromBody(t *testing.T, body []byte) string {
	t.Helper()
	// Minimal extract without full decode dependency in helpers.
	const key = `"id":"`
	idx := strings.Index(string(body), key)
	if idx < 0 {
		t.Fatalf("task id missing from body %s", body)
	}
	rest := string(body)[idx+len(key):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("task id unterminated in body %s", body)
	}
	return rest[:end]
}
