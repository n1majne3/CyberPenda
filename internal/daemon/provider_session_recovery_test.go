package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type recoverySessionFactory struct {
	mu       sync.Mutex
	result   ProviderSessionRecoveryResult
	err      error
	opens    int
	recovers int
	request  ProviderSessionRecoveryRequest
}

// contractlessRecoverySession intentionally exposes only ProviderSession. It
// may claim the assisted capability but cannot deliver the typed observation,
// lineage, or canonical result callbacks required by conclusion recovery.
type contractlessRecoverySession struct{ inner *runtime.FakeProviderSession }

func (s *contractlessRecoverySession) SessionID() string { return s.inner.SessionID() }
func (s *contractlessRecoverySession) Capabilities() runtimeplugin.Capabilities {
	return s.inner.Capabilities()
}
func (s *contractlessRecoverySession) SendTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.inner.SendTurn(ctx, request, emit)
}
func (s *contractlessRecoverySession) InterruptTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.inner.InterruptTurn(ctx, request, emit)
}
func (s *contractlessRecoverySession) InterruptThenReplace(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.inner.InterruptThenReplace(ctx, request, emit)
}
func (s *contractlessRecoverySession) SteerInTurn(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.inner.SteerInTurn(ctx, request, emit)
}
func (s *contractlessRecoverySession) RespondPermission(ctx context.Context, request runtime.ProviderSessionRequest, emit runtime.ProviderSessionEmit) (runtime.ProviderSessionResult, error) {
	return s.inner.RespondPermission(ctx, request, emit)
}
func (s *contractlessRecoverySession) Close(ctx context.Context) error { return s.inner.Close(ctx) }

func (f *recoverySessionFactory) Open(context.Context, ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	return ProviderSessionBinding{}, errString("Open must not be called during ownership recovery")
}

func (f *recoverySessionFactory) Recover(_ context.Context, request ProviderSessionRecoveryRequest) (ProviderSessionRecoveryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovers++
	f.request = request
	return f.result, f.err
}

func (f *recoverySessionFactory) counts() (opens, recovers int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.recovers
}

func TestRecoverProviderSessionOwnershipClosedStates(t *testing.T) {
	for _, test := range []struct {
		name              string
		liveness          ProviderSessionRecoveryLiveness
		wantFinalStatus   task.Status
		wantFinalActivity string
		wantWarning       bool
	}{
		{name: "offline", liveness: ProviderSessionRecoveryOffline, wantFinalStatus: task.StatusFailed, wantFinalActivity: runtimeLivenessOffline},
		{name: "orphaned", liveness: ProviderSessionRecoveryOrphaned, wantFinalStatus: task.StatusInterrupted, wantFinalActivity: runtimeLivenessOffline},
		{name: "unknown", liveness: ProviderSessionRecoveryUnknown, wantFinalStatus: task.StatusRunning, wantFinalActivity: runtimeLivenessUnknown, wantWarning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := &recoverySessionFactory{result: ProviderSessionRecoveryResult{Liveness: test.liveness}}
			server, found, continuation := newRecoveryOwnershipFixture(t, factory)

			report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{
				recoveryRequest(found, continuation),
			})
			outcomes := report.Outcomes
			if len(outcomes) != 1 || outcomes[0].Liveness != test.liveness || outcomes[0].Adopted {
				t.Fatalf("outcomes = %#v", outcomes)
			}
			beforeLifecycle, err := server.tasks.Get(found.ID)
			if err != nil || beforeLifecycle.Status != task.StatusRunning {
				t.Fatalf("probe changed lifecycle: status=%q err=%v", beforeLifecycle.Status, err)
			}
			applyRecoveryStartupLifecycle(server, report)
			updated, err := server.tasks.Get(found.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != test.wantFinalStatus {
				t.Fatalf("status = %q, want %q", updated.Status, test.wantFinalStatus)
			}
			activity := server.computeRuntimeActivity(updated)
			if activity.Liveness != test.wantFinalActivity {
				t.Fatalf("activity = %#v", activity)
			}
			if test.wantWarning && activity.Warning == "" {
				t.Fatalf("unknown activity has no warning: %#v", activity)
			}
			if test.liveness == ProviderSessionRecoveryUnknown {
				if len(report.LifecycleProtectedOwnerIDs) != 1 || report.LifecycleProtectedOwnerIDs[0] != found.ID {
					t.Fatalf("unknown lifecycle-protected Task IDs = %#v", report.LifecycleProtectedOwnerIDs)
				}
			} else if len(report.LifecycleProtectedOwnerIDs) != 0 {
				t.Fatalf("terminal recovery protected Task IDs = %#v", report.LifecycleProtectedOwnerIDs)
			}
			assertRecoveryDidNotLaunch(t, server, factory, found.ID)
		})
	}
}

func TestRecoveredUnknownActivityDoesNotOverrideOperatorStop(t *testing.T) {
	factory := &recoverySessionFactory{result: ProviderSessionRecoveryResult{Liveness: ProviderSessionRecoveryUnknown}}
	server, found, continuation := newRecoveryOwnershipFixture(t, factory)
	report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{recoveryRequest(found, continuation)})
	if len(report.Outcomes) != 1 || report.Outcomes[0].Liveness != ProviderSessionRecoveryUnknown {
		t.Fatalf("outcomes = %#v", report.Outcomes)
	}
	if activity := server.computeRuntimeActivity(found); activity.Liveness != runtimeLivenessUnknown || activity.Warning == "" {
		t.Fatalf("pre-Stop activity = %#v", activity)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/"+found.ProjectID+"/tasks/"+found.ID+"/stop", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Stop status = %d body %s", response.Code, response.Body.String())
	}
	stopped := getTaskActivity(t, server, found.ProjectID, found.ID)
	if stopped.Status != string(task.StatusStopped) || stopped.RuntimeActivity.Liveness != runtimeLivenessOffline || stopped.RuntimeActivity.Warning != "" {
		t.Fatalf("post-Stop Task = %#v", stopped)
	}
	if _, cached := server.recoveredRuntimeActivity(found.ID); cached {
		t.Fatal("terminal Task retained unknown recovery Activity")
	}
}

func TestRecoverProviderSessionOwnershipAdoptsOnlyExactHealthyLiveBinding(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "source-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	adapter := runtime.NewProviderSessionRunAdapter(session, make(chan struct{}))
	factory := &recoverySessionFactory{result: ProviderSessionRecoveryResult{
		Liveness: ProviderSessionRecoveryLive,
		Binding:  ProviderSessionBinding{Session: session, Adapter: adapter},
	}}
	server, found, continuation := newRecoveryOwnershipFixture(t, factory)
	request := recoveryRequest(found, continuation)
	request.DispatchRequestID = "" // pending receipt has no control dispatch yet

	report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{
		request,
	})
	outcomes := report.Outcomes
	if len(outcomes) != 1 || !outcomes[0].Adopted || outcomes[0].Liveness != ProviderSessionRecoveryLive {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	if len(report.LiveOwnerIDs) != 1 || report.LiveOwnerIDs[0] != found.ID {
		t.Fatalf("live Task IDs = %#v", report.LiveOwnerIDs)
	}
	if len(report.LifecycleProtectedOwnerIDs) != 1 || report.LifecycleProtectedOwnerIDs[0] != found.ID {
		t.Fatalf("lifecycle-protected Task IDs = %#v", report.LifecycleProtectedOwnerIDs)
	}
	applyRecoveryStartupLifecycle(server, report)
	bound, ok := server.providerSessions.get(found.ID)
	if !ok || bound.SessionID() != "source-session" {
		t.Fatalf("bound session = %#v, %v", bound, ok)
	}
	updated, err := server.tasks.Get(found.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != task.StatusRunning {
		t.Fatalf("status = %q", updated.Status)
	}
	if activity := server.computeRuntimeActivity(updated); activity.Liveness != runtimeLivenessLive {
		t.Fatalf("activity = %#v", activity)
	}
	assertRecoveryDidNotLaunch(t, server, factory, found.ID)

	factory.mu.Lock()
	captured := factory.request
	factory.mu.Unlock()
	if captured.Owner.ID != found.ID || captured.Continuation.ID != continuation.ID || captured.ReceiptID != "receipt-1" ||
		captured.SourceSessionID != "source-session" || captured.SourceRequestID != "source-work-request" || captured.DispatchRequestID != "" ||
		captured.ContainerID != "" || captured.NativeSessionID != "source-session" ||
		captured.NativeSessionPath != "/sessions/source.jsonl" {
		t.Fatalf("recovery request = %#v", captured)
	}
}

func TestRecoverProviderSessionOwnershipRejectsSessionMismatch(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "different-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true},
	})
	factory := &recoverySessionFactory{result: ProviderSessionRecoveryResult{
		Liveness: ProviderSessionRecoveryLive,
		Binding:  ProviderSessionBinding{Session: session, Adapter: runtime.NewProviderSessionRunAdapter(session, make(chan struct{}))},
	}}
	server, found, continuation := newRecoveryOwnershipFixture(t, factory)

	report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{recoveryRequest(found, continuation)})
	outcomes := report.Outcomes
	if len(outcomes) != 1 || outcomes[0].Liveness != ProviderSessionRecoveryOrphaned || outcomes[0].Adopted || outcomes[0].Warning == "" {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	if _, ok := server.providerSessions.get(found.ID); ok {
		t.Fatal("mismatched session was bound")
	}
	beforeLifecycle, _ := server.tasks.Get(found.ID)
	if beforeLifecycle.Status != task.StatusRunning {
		t.Fatalf("probe changed lifecycle: status=%q", beforeLifecycle.Status)
	}
	applyRecoveryStartupLifecycle(server, report)
	updated, _ := server.tasks.Get(found.ID)
	if updated.Status != task.StatusInterrupted || server.computeRuntimeActivity(updated).Liveness != runtimeLivenessOffline {
		t.Fatalf("mismatch changed lifecycle/activity: status=%q activity=%#v", updated.Status, server.computeRuntimeActivity(updated))
	}
	assertRecoveryDidNotLaunch(t, server, factory, found.ID)
}

func TestRecoverProviderSessionOwnershipRejectsUnhealthyOrIncapableLiveBinding(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*runtime.FakeProviderSession)
		caps    runtimeplugin.Capabilities
	}{
		{name: "missing SendTurn", caps: runtimeplugin.Capabilities{PersistentSession: true, AssistedConclusion: true}},
		{name: "missing assisted conclusion", caps: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true}},
		{name: "offline", caps: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true}, prepare: func(session *runtime.FakeProviderSession) { session.MarkOffline() }},
		{name: "unknown health", caps: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true}, prepare: func(session *runtime.FakeProviderSession) { session.MarkHealthUnknown() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{SessionID: "source-session", Capabilities: test.caps})
			if test.prepare != nil {
				test.prepare(session)
			}
			factory := &recoverySessionFactory{result: ProviderSessionRecoveryResult{
				Liveness: ProviderSessionRecoveryLive,
				Binding:  ProviderSessionBinding{Session: session, Adapter: runtime.NewProviderSessionRunAdapter(session, make(chan struct{}))},
			}}
			server, found, continuation := newRecoveryOwnershipFixture(t, factory)
			report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{recoveryRequest(found, continuation)})
			outcomes := report.Outcomes
			if len(outcomes) != 1 || outcomes[0].Liveness != ProviderSessionRecoveryOrphaned || outcomes[0].Adopted {
				t.Fatalf("outcomes = %#v", outcomes)
			}
			if _, bound := server.providerSessions.get(found.ID); bound {
				t.Fatal("unhealthy or incapable session was bound")
			}
			beforeLifecycle, _ := server.tasks.Get(found.ID)
			if beforeLifecycle.Status != task.StatusRunning {
				t.Fatalf("probe changed lifecycle: status=%q", beforeLifecycle.Status)
			}
			applyRecoveryStartupLifecycle(server, report)
			updated, _ := server.tasks.Get(found.ID)
			if updated.Status != task.StatusInterrupted {
				t.Fatalf("status = %q", updated.Status)
			}
			assertRecoveryDidNotLaunch(t, server, factory, found.ID)
		})
	}
}

func TestRecoverProviderSessionOwnershipRejectsLiveBindingWithoutTypedAssistedContract(t *testing.T) {
	inner := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "source-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	session := &contractlessRecoverySession{inner: inner}
	factory := &recoverySessionFactory{result: ProviderSessionRecoveryResult{
		Liveness: ProviderSessionRecoveryLive,
		Binding:  ProviderSessionBinding{Session: session, Adapter: runtime.NewProviderSessionRunAdapter(session, make(chan struct{}))},
	}}
	server, found, continuation := newRecoveryOwnershipFixture(t, factory)
	report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{recoveryRequest(found, continuation)})
	if len(report.Outcomes) != 1 || report.Outcomes[0].Liveness != ProviderSessionRecoveryOrphaned || report.Outcomes[0].Adopted {
		t.Fatalf("outcomes = %#v", report.Outcomes)
	}
	if _, bound := server.providerSessions.get(found.ID); bound {
		t.Fatal("contractless assisted session was bound")
	}
	assertRecoveryDidNotLaunch(t, server, factory, found.ID)
}

func TestRecoverProviderSessionOwnershipRejectsTerminalTaskBeforeFactoryProbe(t *testing.T) {
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID:    "source-session",
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, AssistedConclusion: true},
	})
	factory := &recoverySessionFactory{result: ProviderSessionRecoveryResult{
		Liveness: ProviderSessionRecoveryLive,
		Binding:  ProviderSessionBinding{Session: session, Adapter: runtime.NewProviderSessionRunAdapter(session, make(chan struct{}))},
	}}
	server, found, continuation := newRecoveryOwnershipFixture(t, factory)
	continuation, _ = server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusCompleted)
	found, _ = server.tasks.UpdateStatus(found.ID, task.StatusCompleted)

	report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{recoveryRequest(found, continuation)})
	if len(report.Outcomes) != 1 || report.Outcomes[0].Adopted || report.Outcomes[0].Liveness != ProviderSessionRecoveryOrphaned {
		t.Fatalf("outcomes = %#v", report.Outcomes)
	}
	opens, recovers := factory.counts()
	if opens != 0 || recovers != 0 {
		t.Fatalf("terminal Task reached factory: opens=%d recovers=%d", opens, recovers)
	}
	if _, bound := server.providerSessions.get(found.ID); bound {
		t.Fatal("terminal Task adopted a session")
	}
	if _, cached := server.recoveredRuntimeActivity(found.ID); cached {
		t.Fatal("terminal Task retained a recovery Activity cache entry")
	}
	if activity := server.computeRuntimeActivity(found); activity.Liveness != runtimeLivenessOffline {
		t.Fatalf("terminal Task activity = %#v", activity)
	}
}

func TestRecoverProviderSessionOwnershipUnsupportedFactoryFailsClosed(t *testing.T) {
	factory := &activitySessionFactory{}
	server, found, continuation := newRecoveryOwnershipFixture(t, factory)
	report := server.recoverProviderSessionOwnership(context.Background(), []ProviderSessionRecoveryRequest{recoveryRequest(found, continuation)})
	outcomes := report.Outcomes
	if len(outcomes) != 1 || outcomes[0].Liveness != ProviderSessionRecoveryOrphaned || outcomes[0].Warning == "" {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	if factory.openCount() != 0 || server.harness.IsActive(found.ID) {
		t.Fatalf("unsupported recovery launched Runtime: opens=%d active=%v", factory.openCount(), server.harness.IsActive(found.ID))
	}
	beforeLifecycle, _ := server.tasks.Get(found.ID)
	if beforeLifecycle.Status != task.StatusRunning {
		t.Fatalf("probe changed lifecycle: status=%q", beforeLifecycle.Status)
	}
	applyRecoveryStartupLifecycle(server, report)
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil || latest == nil || latest.ID != continuation.ID {
		t.Fatalf("latest Continuation = %#v, %v", latest, err)
	}
}

func newRecoveryOwnershipFixture(t *testing.T, factory ProviderSessionFactory) (*Server, task.Task, task.TaskContinuation) {
	t.Helper()
	server, err := NewServer(Config{DBPath: filepath.Join(t.TempDir(), "pentest.db"), DisableBuiltinSkills: true, ProviderSessionFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	projectRecord, err := server.projects.Create("Recovery", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := server.profiles.Create("Codex", runtimeprofile.ProviderCodex, runtimeprofile.Fields{})
	if err != nil {
		t.Fatal(err)
	}
	found, err := server.tasks.Create(task.CreateRequest{ProjectID: projectRecord.ID, Goal: "recover", RuntimeProfileID: profile.ID, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := server.tasks.CreateContinuation(found.ID, profile.ID, string(runtimeprofile.ProviderCodex), task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}
	continuation, err = server.tasks.UpdateContinuationRuntimeMetadata(continuation.ID, "", "source-session", "/sessions/source.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(found.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if continuation, err = server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	found, _ = server.tasks.Get(found.ID)
	return server, found, continuation
}

func recoveryRequest(found task.Task, continuation task.TaskContinuation) ProviderSessionRecoveryRequest {
	return ProviderSessionRecoveryRequest{
		Owner: found.OwnerContract(""), Continuation: ownerContinuationFromTask(continuation), ReceiptID: "receipt-1", SourceSessionID: "source-session", SourceRequestID: "source-work-request", DispatchRequestID: "conclude-request",
		ContainerID: continuation.ContainerID, NativeSessionID: continuation.NativeSessionID, NativeSessionPath: continuation.NativeSessionPath,
	}
}

func assertRecoveryDidNotLaunch(t *testing.T, server *Server, factory *recoverySessionFactory, taskID string) {
	t.Helper()
	opens, recovers := factory.counts()
	if opens != 0 || recovers != 1 || server.harness.IsActive(taskID) {
		t.Fatalf("ownership recovery launched Runtime: opens=%d recovers=%d active=%v", opens, recovers, server.harness.IsActive(taskID))
	}
	latest, err := server.tasks.LatestContinuation(taskID)
	if err != nil || latest == nil {
		t.Fatalf("latest Continuation = %#v, %v", latest, err)
	}
}

func applyRecoveryStartupLifecycle(server *Server, report ProviderSessionRecoveryReport) {
	server.reconcileInterruptedTasks(report.ReconciliationExcludedOwnerIDs)
	server.applyProviderSessionRecoveryLifecycle(report.Outcomes)
}
