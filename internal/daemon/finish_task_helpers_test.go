package daemon

import (
	"context"
	"errors"
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
	"pentest/internal/testsupport/fakeprovider"
)

// failingFinishReconciler injects Continuation reconciliation failure so Finish
// cannot claim Task completed without a durable recon marker.
type failingFinishReconciler struct {
	err error
}

func (f failingFinishReconciler) ReconcileTerminalContinuation(context.Context, string, string) error {
	return f.err
}

// countingFinishReconciler records successful recon for order assertions.
type countingFinishReconciler struct {
	mu    sync.Mutex
	calls int
	inner task.ContinuationReconciler
}

func (c *countingFinishReconciler) ReconcileTerminalContinuation(ctx context.Context, continuationID, reason string) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.inner != nil {
		return c.inner.ReconcileTerminalContinuation(ctx, continuationID, reason)
	}
	return nil
}

func (c *countingFinishReconciler) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type finishSessionFactory struct {
	mu       sync.Mutex
	session  runtime.ProviderSession
	adapter  runtime.Adapter
	opens    int
	requests []ProviderSessionLaunchRequest
	// newSession builds a fresh session/adapter per Open so resume after Finish
	// cannot rebind a closed handle (mirrors production factory behavior).
	newSession func(open int) (runtime.ProviderSession, runtime.Adapter)
}

func (f *finishSessionFactory) Open(_ context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	f.requests = append(f.requests, request)
	session, adapter := f.session, f.adapter
	if f.newSession != nil {
		session, adapter = f.newSession(f.opens)
		f.session, f.adapter = session, adapter
	}
	if binder, ok := session.(runtime.ProviderSessionContinuationBinder); ok {
		_ = binder.BindContinuation(request.Continuation.ID)
	}
	if runAdapter, ok := adapter.(*runtime.ProviderSessionRunAdapter); ok {
		runAdapter.BindContinuation(request.Continuation.ID)
	}
	return ProviderSessionBinding{Session: session, Adapter: adapter}, nil
}

func (f *finishSessionFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func (f *finishSessionFactory) Requests() []ProviderSessionLaunchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProviderSessionLaunchRequest(nil), f.requests...)
}

// finishBoundSession mirrors production bridges: session Close signals the run
// adapter closed channel so Finish can waitHarnessInactive without cancel.
type finishBoundSession struct {
	*fakeprovider.Session
	closed chan struct{}
	once   sync.Once
}

func (s *finishBoundSession) Close(ctx context.Context) error {
	err := s.Session.Close(ctx)
	if err == nil || errors.Is(err, runtime.ErrProviderSessionClosed) {
		s.signalClosed()
	}
	return err
}

func (s *finishBoundSession) signalClosed() {
	if s.closed == nil {
		return
	}
	s.once.Do(func() {
		select {
		case <-s.closed:
		default:
			close(s.closed)
		}
	})
}

func newFinishSessionPair(sessionID string) (runtime.ProviderSession, runtime.Adapter) {
	return newFinishSessionPairWithCaps(sessionID, runtimeplugin.Capabilities{
		PersistentSession: true, SendTurn: true, InterruptThenReplace: true,
	})
}

func newFinishSessionPairWithCaps(sessionID string, caps runtimeplugin.Capabilities) (runtime.ProviderSession, runtime.Adapter) {
	closed := make(chan struct{})
	fake := fakeprovider.New(fakeprovider.Config{
		SessionID: sessionID, Capabilities: caps,
	})
	session := &finishBoundSession{Session: fake, closed: closed}
	adapter := runtime.NewProviderSessionRunAdapter(session, closed)
	return session, adapter
}

// forceBusySession reports busy turn activity without holding the daemon
// control lock (native steer holds the lock for the whole operation).
type forceBusySession struct {
	*finishBoundSession
	busy bool
}

func (s *forceBusySession) ControlBusy() bool {
	if s.busy {
		return true
	}
	return s.finishBoundSession.ControlBusy()
}

func (s *forceBusySession) TurnBusy() bool {
	if s.busy {
		return true
	}
	return s.finishBoundSession.TurnBusy()
}

func newFinishTaskFixture(t *testing.T, factory ProviderSessionFactory) (*Server, task.Task, modelprovider.Provider) {
	t.Helper()
	return newFinishTaskFixtureAt(t, t.TempDir(), factory)
}

func newFinishTaskFixtureAt(t *testing.T, root string, factory ProviderSessionFactory) (*Server, task.Task, modelprovider.Provider) {
	t.Helper()
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
		Protocols: []modelprovider.Protocol{modelprovider.ProtocolOpenAIResponses},
		Catalog:   modelprovider.Catalog{Manual: []string{"gpt-test"}, DefaultModel: "gpt-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mp.APIKeyEnv, "sk-test")

	binary := filepath.Join(root, "codex")
	if err := writeExecutable(binary, "#!/bin/sh\necho ok\n"); err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{
		ModelProviderID: mp.ID, ModelOverride: "gpt-test", ReasoningEffort: "medium",
		BinaryPath: binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.tasks.Create(task.CreateRequest{
		ProjectID: projectRecord.ID,
		Type:      task.TypePentest, Goal: "inspect example.com",
		RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox,
		RuntimeConfig: testTaskRuntimeSnapshot(t, server, profile, task.RunnerSandbox),
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, created, mp
}

func launchFinishTask(t *testing.T, server *Server, created task.Task) {
	t.Helper()
	plan, err := server.buildTaskLaunchPlan(created, created.Goal, "", "", "medium")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.launchTaskInBackground(created, plan, created.Goal); err != nil {
		t.Fatal(err)
	}
	waitForTaskRunning(t, server, created.ID)
	bound, ok := server.providerSessions.get(created.ID)
	if !ok {
		t.Fatal("provider session was not bound after launch")
	}
	emitter, ok := bound.(interface {
		EmitObservation(runtime.ProviderSessionObservation) error
	})
	if !ok {
		t.Fatalf("finish fixture provider session %T cannot complete its initial Turn", bound)
	}
	turnID := waitForProviderTurn(t, server, created.ID, bound)
	if err := emitter.EmitObservation(runtime.ProviderSessionObservation{
		Kind:           runtime.ProviderSessionObservationTurnCompleted,
		ProviderTurnID: turnID,
		Status:         "completed",
	}); err != nil {
		t.Fatalf("complete initial provider Turn: %v", err)
	}
	waitForLiveIdle(t, server, created)
}

func waitForProviderTurn(t *testing.T, server *Server, taskID string, bound runtime.ProviderSession) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := providerSessionTurnState(bound)
		if turnID := strings.TrimSpace(state.ActiveTurnID); turnID != "" {
			return turnID
		}
		if !server.harness.IsActive(taskID) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("finish fixture launch did not create an active provider Turn; harness_active=%v state=%#v", server.harness.IsActive(taskID), providerSessionTurnState(bound))
	return ""
}

func waitForLiveIdle(t *testing.T, server *Server, created task.Task) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body := getTaskActivity(t, server, created.ProjectID, created.ID)
		if body.Status == "running" && body.RuntimeActivity.Liveness == "live" && body.RuntimeActivity.TurnActivity == "idle" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	body := getTaskActivity(t, server, created.ProjectID, created.ID)
	t.Fatalf("want running live idle, got status=%q activity=%#v", body.Status, body.RuntimeActivity)
}

func postFinish(server *Server, projectID, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/finish", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	return resp
}

func postStop(server *Server, projectID, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/stop", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	return resp
}
