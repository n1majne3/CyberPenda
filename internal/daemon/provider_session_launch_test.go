package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type persistentTestAdapter struct {
	mu     sync.Mutex
	record func(runtime.NativeSessionMetadata) error
}

func (*persistentTestAdapter) Name() string { return "persistent-test" }

func (a *persistentTestAdapter) SetMetadataRecorder(record func(runtime.NativeSessionMetadata) error) {
	a.mu.Lock()
	a.record = record
	a.mu.Unlock()
}

func (a *persistentTestAdapter) Run(ctx context.Context, _ string, _ func(task.EventKind, task.EventPayload)) error {
	a.mu.Lock()
	record := a.record
	a.mu.Unlock()
	if record != nil {
		if err := record(runtime.NativeSessionMetadata{ContainerID: "container-1", NativeSessionID: "native-session-1", NativeSessionPath: "/sessions/one.jsonl"}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

type recordingProviderSessionFactory struct {
	mu       sync.Mutex
	requests []ProviderSessionLaunchRequest
	session  runtime.ProviderSession
	adapter  runtime.Adapter
	err      error
}

func (f *recordingProviderSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.err != nil {
		return ProviderSessionBinding{}, f.err
	}
	return ProviderSessionBinding{Session: f.session, Adapter: f.adapter}, nil
}

func (f *recordingProviderSessionFactory) Requests() []ProviderSessionLaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProviderSessionLaunchRequest(nil), f.requests...)
}

func TestLaunchAssemblyBindsAndReusesTaskOwnedProviderSessionAcrossContinuations(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "task-session-1",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
		},
	})
	factory := &recordingProviderSessionFactory{session: session, adapter: &persistentTestAdapter{}}
	server, created := newProviderSessionLaunchFixture(t, factory)
	defer server.Close()

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
	first, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || first == nil {
		t.Fatalf("first active Continuation = %#v, err=%v", first, err)
	}
	bound, ok := server.providerSessions.get(created.ID)
	if !ok || bound != session {
		t.Fatalf("bound session = %#v, ok=%v", bound, ok)
	}

	if !server.harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("first persistent adapter did not stop")
	}
	waitForHarnessActive(t, server, created.ID, false)
	updated, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := server.buildTaskLaunchPlan(updated, "continue", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(updated, secondPlan, "continue"); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
	var second *task.TaskContinuation
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		second, err = server.tasks.ActiveContinuation(created.ID)
		if err == nil && second != nil && second.ContainerID == "container-1" && second.NativeSessionID == "native-session-1" && second.NativeSessionPath == "/sessions/one.jsonl" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil || second == nil {
		t.Fatalf("second active Continuation = %#v, err=%v", second, err)
	}
	if second.ID == first.ID || second.Number != first.Number+1 {
		t.Fatalf("replacement Continuation = %#v, first=%#v", second, first)
	}
	if second.ContainerID != "container-1" || second.NativeSessionID != "native-session-1" || second.NativeSessionPath != "/sessions/one.jsonl" {
		t.Fatalf("replacement lost Task session/container identity: %#v", second)
	}

	requests := factory.Requests()
	if len(requests) != 2 {
		t.Fatalf("factory requests = %d, want 2", len(requests))
	}
	for _, request := range requests {
		if request.Task.ID != created.ID || request.Runner != task.RunnerSandbox || request.Provider != runtimeprofile.ProviderCodex {
			t.Fatalf("factory request lost launch identity: %#v", request)
		}
	}
	if requests[0].Continuation.ID == requests[1].Continuation.ID {
		t.Fatalf("factory did not receive fresh Continuation pins: %#v", requests)
	}
	if latestBound, ok := server.providerSessions.get(created.ID); !ok || latestBound != session {
		t.Fatalf("second launch replaced Task session: %#v, ok=%v", latestBound, ok)
	}
	server.harness.StopAndWait(created.ID, 2*time.Second)
}

func TestLaunchAssemblyFactoryFailureFailsClosedAfterContinuationCreation(t *testing.T) {
	factoryErr := errors.New("bridge setup rejected")
	factory := &recordingProviderSessionFactory{err: factoryErr}
	server, created := newProviderSessionLaunchFixture(t, factory)
	defer server.Close()

	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	err = server.launchTaskInBackground(created, plan, created.Goal)
	if !errors.Is(err, factoryErr) {
		t.Fatalf("launch error = %v, want factory error", err)
	}
	if server.harness.IsActive(created.ID) {
		t.Fatal("factory failure started legacy harness")
	}
	found, err := server.tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusFailed {
		t.Fatalf("Task status = %q, want failed", found.Status)
	}
	latest, err := server.tasks.LatestContinuation(created.ID)
	if err != nil || latest == nil {
		t.Fatalf("latest Continuation = %#v, err=%v", latest, err)
	}
	if latest.Status != task.StatusFailed {
		t.Fatalf("Continuation status = %q, want failed", latest.Status)
	}
	if _, ok := server.providerSessions.get(created.ID); ok {
		t.Fatal("factory failure left a bound provider session")
	}
}

func TestLaunchBoundNativeSteerRebindsHarnessAndFinalizesReplacement(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "task-session-steer", ActiveTurnID: "turn-1",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptThenReplace: true},
	})
	factory := &recordingProviderSessionFactory{session: session, adapter: &persistentTestAdapter{}}
	server, created := newProviderSessionLaunchFixture(t, factory)
	defer server.Close()
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForHarnessActive(t, server, created.ID, true)
	first, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || first == nil {
		t.Fatalf("first Continuation = %#v, err=%v", first, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+created.ProjectID+"/tasks/"+created.ID+"/steer", bytes.NewBufferString(`{"request_id":"launch-steer-1","message":"focus on auth"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer status = %d, body=%s", response.Code, response.Body.String())
	}
	waitForTaskEvent(t, server, created.ID, func(events []task.Event) bool {
		for _, event := range events {
			if event.Payload["request_id"] == "launch-steer-1" && event.Payload["phase"] == "steering_applied" {
				return true
			}
		}
		return false
	})
	replacement, err := server.tasks.ActiveContinuation(created.ID)
	if err != nil || replacement == nil {
		t.Fatalf("replacement Continuation = %#v, err=%v", replacement, err)
	}
	if replacement.ID == first.ID || replacement.Status != task.StatusRunning {
		t.Fatalf("replacement Continuation = %#v", replacement)
	}
	if replacement.ContainerID != "container-1" || replacement.NativeSessionID != "native-session-1" {
		t.Fatalf("replacement lost Task session/container identity: %#v", replacement)
	}
	if !server.harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("persistent harness did not stop")
	}
	stopped, err := server.tasks.Continuation(replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != task.StatusStopped {
		t.Fatalf("replacement status after stop = %q, want stopped", stopped.Status)
	}
	if requests := factory.Requests(); len(requests) != 1 {
		t.Fatalf("native steer opened %d factory sessions, want 1", len(requests))
	}
}

func newProviderSessionLaunchFixture(t *testing.T, factory ProviderSessionFactory) (*Server, task.Task) {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(Config{
		DBPath: filepath.Join(root, "pentest.db"), RuntimeRoot: filepath.Join(root, "runs"),
		SandboxImage: "cyberpenda:test", DisableBuiltinSkills: true,
		ProviderSessionFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdProject, err := server.projects.Create("Project", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{Model: "gpt-test", SandboxImage: "cyberpenda:test"})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: createdProject.ID, Goal: "inspect example.com", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return server, created
}

func waitForHarnessActive(t *testing.T, server *Server, taskID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if server.harness.IsActive(taskID) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("harness active = %v, want %v", server.harness.IsActive(taskID), want)
}
