package task_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

type recordingTerminalMarker struct {
	continuationIDs []string
}

func (m *recordingTerminalMarker) MarkContinuationTerminal(_ context.Context, continuationID string) error {
	m.continuationIDs = append(m.continuationIDs, continuationID)
	return nil
}

func newStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return db
}

// TestCreateCapturesGoalRunControlsAndScopeSnapshot proves the tracer bullet:
// launching a task captures the goal, run controls, the runtime profile id, the
// selected runner, and an immutable snapshot of the project scope at launch.
func TestCreateCapturesGoalRunControlsAndScopeSnapshot(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	projectID, err := projects.Create(
		"Acme",
		"",
		project.Scope{Domains: []string{"example.com"}, Notes: "live scope"},
		project.Defaults{},
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	created, err := svc.Create(task.CreateRequest{
		ProjectID:        projectID.ID,
		Goal:             "Enumerate example.com subdomains",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxNetwork: "host_proxy_only",
			Notes:          "business hours only",
			Extras:         map[string]string{"depth": "shallow"},
		},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	if created.ID == "" {
		t.Fatal("expected task id")
	}
	if created.Goal != "Enumerate example.com subdomains" {
		t.Fatalf("expected goal, got %q", created.Goal)
	}
	if created.RuntimeProfileID != "profile-1" {
		t.Fatalf("expected runtime profile id, got %q", created.RuntimeProfileID)
	}
	if created.Runner != task.RunnerSandbox {
		t.Fatalf("expected sandbox runner, got %q", created.Runner)
	}
	if created.RunControls.Notes != "business hours only" {
		t.Fatalf("expected run-control notes, got %q", created.RunControls.Notes)
	}
	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if fetched.RunControls.SandboxNetwork != "host_proxy_only" {
		t.Fatalf("expected persisted sandbox network, got %q", fetched.RunControls.SandboxNetwork)
	}
	// Scope snapshot is an immutable copy captured at launch.
	if got := created.ScopeSnapshot.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope snapshot domain, got %#v", got)
	}
	if created.ScopeSnapshot.Notes != "live scope" {
		t.Fatalf("expected scope snapshot notes, got %q", created.ScopeSnapshot.Notes)
	}
	if created.Status != task.StatusPending {
		t.Fatalf("expected initial status pending, got %q", created.Status)
	}
}

func TestCreateRejectsMissingGoal(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "", Runner: task.RunnerSandbox})
	if !errors.Is(err, task.ErrMissingGoal) {
		t.Fatalf("expected ErrMissingGoal, got %v", err)
	}
}

func TestCreateRejectsUnsupportedRunner(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "do something", Runner: "kali-magic"})
	if !errors.Is(err, task.ErrUnsupportedRunner) {
		t.Fatalf("expected ErrUnsupportedRunner, got %v", err)
	}
}

func TestCreateRejectsUnknownProject(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	_, err := svc.Create(task.CreateRequest{ProjectID: "missing", Goal: "x", Runner: task.RunnerSandbox})
	if !errors.Is(err, task.ErrProjectNotFound) {
		t.Fatalf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestGetReturnsPersistedTask(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})

	created, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "enumerate", RuntimeProfileID: "prof", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Goal != "enumerate" {
		t.Fatalf("expected goal, got %q", fetched.Goal)
	}
	if got := fetched.ScopeSnapshot.Domains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("expected scope snapshot persisted, got %#v", got)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	_, err := svc.Get("missing")
	if !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListForProjectReturnsTasksInCreationOrder(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	if _, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "first", Runner: task.RunnerSandbox}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "second", Runner: task.RunnerSandbox}); err != nil {
		t.Fatalf("create second: %v", err)
	}

	tasks, err := svc.ListForProject(proj.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Goal != "first" || tasks[1].Goal != "second" {
		t.Fatalf("expected creation order, got %q then %q", tasks[0].Goal, tasks[1].Goal)
	}
}

// TestAppendEventStoresEventsInOrder proves task events are appended with a
// monotonically increasing sequence and read back in order.
func TestAppendEventStoresEventsInOrder(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "g", Runner: task.RunnerSandbox})

	if _, err := svc.AppendEvent(created.ID, task.EventKindRuntimeOutput, task.EventPayload{"text": "started"}); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if _, err := svc.AppendEvent(created.ID, task.EventKindRuntimeOutput, task.EventPayload{"text": "working"}); err != nil {
		t.Fatalf("append second: %v", err)
	}

	events, err := svc.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("expected seq 1 then 2, got %d then %d", events[0].Seq, events[1].Seq)
	}
	if events[0].Payload["text"] != "started" || events[1].Payload["text"] != "working" {
		t.Fatalf("expected payload preserved in order, got %#v", events)
	}
}

// TestAppendEventOnUnknownTaskFails proves events cannot be added to a phantom
// task.
func TestAppendEventOnUnknownTaskFails(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	_, err := svc.AppendEvent("missing", task.EventKindRuntimeOutput, task.EventPayload{})
	if !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestRuntimeConfigVersionsIncrementOnProfileSwitch proves a profile switch
// inside a task creates a new runtime config version rather than a new task.
func TestRuntimeConfigVersionsIncrementOnProfileSwitch(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "g", RuntimeProfileID: "prof-a", Runner: task.RunnerSandbox})

	// First config version is captured at launch.
	first, err := svc.RecordRuntimeConfig(created.ID, "prof-a", map[string]any{"model": "a"})
	if err != nil {
		t.Fatalf("record first config: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("expected first version 1, got %d", first.Version)
	}

	// A profile switch creates version 2, not a new task.
	second, err := svc.RecordRuntimeConfig(created.ID, "prof-b", map[string]any{"model": "b"})
	if err != nil {
		t.Fatalf("record second config: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("expected second version 2, got %d", second.Version)
	}
	if second.RuntimeProfileID != "prof-b" {
		t.Fatalf("expected new profile, got %q", second.RuntimeProfileID)
	}

	versions, err := svc.RuntimeConfigVersions(created.ID)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 config versions, got %d", len(versions))
	}
	// Task identity is unchanged.
	fetched, _ := svc.Get(created.ID)
	if fetched.RuntimeProfileID != "prof-a" {
		t.Fatalf("task original profile must be unchanged; profile switch affects next continuation, got %q", fetched.RuntimeProfileID)
	}
}

func TestContinuationLifecycleTracksLatestAndActiveRun(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "g",
		RuntimeProfileID: "prof-a",
		Runner:           task.RunnerSandbox,
	})

	first, err := svc.CreateContinuation(created.ID, "prof-a", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create first continuation: %v", err)
	}
	if first.Number != 1 {
		t.Fatalf("expected first continuation number 1, got %d", first.Number)
	}
	if first.Status != task.StatusPending {
		t.Fatalf("expected first continuation pending, got %q", first.Status)
	}

	active, err := svc.ActiveContinuation(created.ID)
	if err != nil {
		t.Fatalf("active continuation: %v", err)
	}
	if active == nil || active.ID != first.ID {
		t.Fatalf("expected active continuation %q, got %#v", first.ID, active)
	}

	if _, err := svc.UpdateContinuationStatus(first.ID, task.StatusRunning); err != nil {
		t.Fatalf("mark first running: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(first.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark first completed: %v", err)
	}

	active, err = svc.ActiveContinuation(created.ID)
	if err != nil {
		t.Fatalf("active continuation after completion: %v", err)
	}
	if active != nil {
		t.Fatalf("expected no active continuation after completion, got %#v", active)
	}

	second, err := svc.CreateContinuation(created.ID, "prof-a", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create second continuation: %v", err)
	}
	if second.Number != 2 {
		t.Fatalf("expected second continuation number 2, got %d", second.Number)
	}

	latest, err := svc.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if latest == nil || latest.ID != second.ID {
		t.Fatalf("expected latest continuation %q, got %#v", second.ID, latest)
	}
}

func TestContinuationRuntimeMetadataIsPersisted(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "g",
		RuntimeProfileID: "prof-a",
		Runner:           task.RunnerSandbox,
	})

	continuation, err := svc.CreateContinuation(created.ID, "prof-a", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}

	updated, err := svc.UpdateContinuationRuntimeMetadata(continuation.ID, "ctr-1", "sess-123", "/tmp/session.jsonl")
	if err != nil {
		t.Fatalf("update continuation metadata: %v", err)
	}
	if updated.ContainerID != "ctr-1" {
		t.Fatalf("expected container id ctr-1, got %q", updated.ContainerID)
	}
	if updated.NativeSessionID != "sess-123" {
		t.Fatalf("expected native session id sess-123, got %q", updated.NativeSessionID)
	}
	if updated.NativeSessionPath != "/tmp/session.jsonl" {
		t.Fatalf("expected native session path /tmp/session.jsonl, got %q", updated.NativeSessionPath)
	}

	latest, err := svc.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if latest == nil || latest.NativeSessionID != "sess-123" {
		t.Fatalf("expected persisted native session id sess-123, got %#v", latest)
	}
}

// TestReconcileInterruptedStatusesMarksActiveTasksInterrupted proves the daemon
// startup reconcile: tasks left running/paused/pending by a previous daemon
// instance become interrupted, while already-terminal tasks are untouched.
func TestReconcileInterruptedStatusesMarksActiveTasksInterrupted(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	marker := &recordingTerminalMarker{}
	svc.SetContinuationTerminalMarker(marker)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	createTaskWithStatus := func(goal string) task.Task {
		created, err := svc.Create(task.CreateRequest{
			ProjectID:        proj.ID,
			Goal:             goal,
			RuntimeProfileID: "profile-1",
			Runner:           task.RunnerSandbox,
		})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		return created
	}

	running := createTaskWithStatus("running ghost")
	paused := createTaskWithStatus("paused ghost")
	completed := createTaskWithStatus("done task")
	runningContinuation, err := svc.CreateContinuation(running.ID, "profile-1", "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create running continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(runningContinuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("set running continuation: %v", err)
	}
	if _, err := svc.UpdateStatus(running.ID, task.StatusRunning); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := svc.UpdateStatus(paused.ID, task.StatusPaused); err != nil {
		t.Fatalf("set paused: %v", err)
	}
	if _, err := svc.UpdateStatus(completed.ID, task.StatusCompleted); err != nil {
		t.Fatalf("set completed: %v", err)
	}

	changed, err := svc.ReconcileInterruptedStatuses()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("expected 2 interrupted tasks, got %d", len(changed))
	}

	runningGot, _ := svc.Get(running.ID)
	pausedGot, _ := svc.Get(paused.ID)
	completedGot, _ := svc.Get(completed.ID)
	if runningGot.Status != task.StatusInterrupted {
		t.Fatalf("expected running task -> interrupted, got %q", runningGot.Status)
	}
	runningContinuationGot, err := svc.LatestContinuation(running.ID)
	if err != nil {
		t.Fatalf("latest running continuation: %v", err)
	}
	if runningContinuationGot == nil || runningContinuationGot.Status != task.StatusInterrupted {
		t.Fatalf("expected running continuation -> interrupted, got %#v", runningContinuationGot)
	}
	if len(marker.continuationIDs) != 1 || marker.continuationIDs[0] != runningContinuation.ID {
		t.Fatalf("startup reconciliation terminal marker calls = %v", marker.continuationIDs)
	}
	if pausedGot.Status != task.StatusInterrupted {
		t.Fatalf("expected paused task -> interrupted, got %q", pausedGot.Status)
	}
	if completedGot.Status != task.StatusCompleted {
		t.Fatalf("expected completed task untouched, got %q", completedGot.Status)
	}
}

func TestReconcileInterruptedStatusesClearsStaleActiveContinuations(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	marker := &recordingTerminalMarker{}
	svc.SetContinuationTerminalMarker(marker)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "already interrupted task",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile-1", "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("set continuation running: %v", err)
	}
	if _, err := svc.UpdateStatus(created.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("set task interrupted: %v", err)
	}

	changed, err := svc.ReconcileInterruptedStatuses()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("expected no task status changes, got %d", len(changed))
	}
	latest, err := svc.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("latest continuation: %v", err)
	}
	if latest == nil || latest.Status != task.StatusInterrupted {
		t.Fatalf("expected stale active continuation -> interrupted, got %#v", latest)
	}
	if len(marker.continuationIDs) != 1 || marker.continuationIDs[0] != continuation.ID {
		t.Fatalf("stale reconciliation terminal marker calls = %v", marker.continuationIDs)
	}
}

func TestReconcileInterruptedStateIgnoresTerminalSandboxContainers(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Goal:             "completed sandbox task",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile-1", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationRuntimeMetadata(continuation.ID, "ctr-completed", "", ""); err != nil {
		t.Fatalf("set continuation container: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("set continuation completed: %v", err)
	}
	if _, err := svc.UpdateStatus(created.ID, task.StatusCompleted); err != nil {
		t.Fatalf("set task completed: %v", err)
	}

	reconciled, err := svc.ReconcileInterruptedState()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(reconciled.Tasks) != 0 {
		t.Fatalf("expected no task status changes, got %d", len(reconciled.Tasks))
	}
	if len(reconciled.Continuations) != 0 {
		t.Fatalf("expected no terminal continuation cleanup candidates, got %#v", reconciled.Continuations)
	}
}

type recordingGoalProjector struct {
	calls []string
	err   error
}

func (p *recordingGoalProjector) ProjectTaskGoal(taskID string) error {
	p.calls = append(p.calls, taskID)
	return p.err
}

func TestGoalProjectorCallSitesPreserveTaskDurabilityAndGuardContinuation(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projector := &recordingGoalProjector{}
	svc.SetGoalProjector(projector)

	created, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "Project me", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if len(projector.calls) != 1 || projector.calls[0] != created.ID {
		t.Fatalf("create projection calls: %+v", projector.calls)
	}

	projector.err = errors.New("graph unavailable")
	updated, err := svc.UpdateStatus(created.ID, task.StatusRunning)
	if err == nil || updated.Status != task.StatusRunning {
		t.Fatalf("update should return persisted task plus projection error: task=%+v err=%v", updated, err)
	}
	persisted, getErr := svc.Get(created.ID)
	if getErr != nil || persisted.Status != task.StatusRunning {
		t.Fatalf("task status must remain durable after projection error: task=%+v err=%v", persisted, getErr)
	}
	if len(projector.calls) != 2 {
		t.Fatalf("update projection calls: %+v", projector.calls)
	}

	_, err = svc.CreateContinuation(created.ID, "profile-1", "codex", task.RunnerSandbox)
	if err == nil {
		t.Fatal("expected continuation projection error")
	}
	latest, latestErr := svc.LatestContinuation(created.ID)
	if latestErr != nil {
		t.Fatalf("read latest continuation: %v", latestErr)
	}
	if latest != nil {
		t.Fatalf("continuation must not persist when projection fails: %+v", latest)
	}
	if len(projector.calls) != 3 {
		t.Fatalf("continuation projection calls: %+v", projector.calls)
	}
}

func TestCreateReturnsPersistedTaskWhenGoalProjectionFails(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projector := &recordingGoalProjector{err: errors.New("graph unavailable")}
	svc.SetGoalProjector(projector)

	created, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "Persist first", Runner: task.RunnerSandbox})
	if err == nil || created.ID == "" {
		t.Fatalf("expected persisted task and projection error: task=%+v err=%v", created, err)
	}
	persisted, getErr := svc.Get(created.ID)
	if getErr != nil || persisted.Goal != "Persist first" {
		t.Fatalf("created task must remain durable: task=%+v err=%v", persisted, getErr)
	}
	if len(projector.calls) != 1 || projector.calls[0] != created.ID {
		t.Fatalf("projection calls: %+v", projector.calls)
	}
}

func TestContinuationSnapshotPinIsPersistedAndSurvivesLifecycleUpdates(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "g", RuntimeProfileID: "prof-a", Runner: task.RunnerSandbox})

	runtimeConfig, err := svc.RecordRuntimeConfig(created.ID, "prof-a", map[string]any{"model": "test"})
	if err != nil {
		t.Fatalf("record runtime config: %v", err)
	}
	pin := task.ContinuationSnapshotPin{
		RuntimeConfigVersionID:              runtimeConfig.ID,
		BlackboardGraphRevision:             42,
		BlackboardRendererVersion:           "canonical_main_graph_v1",
		BlackboardEstimatorVersion:          "utf8_bytes_div_4_v1",
		BlackboardProjectionHash:            "abc123",
		BlackboardProjectionBytes:           2048,
		BlackboardProjectionEstimatedTokens: 512,
	}
	continuation, err := svc.CreateContinuationWithSnapshotPin(created.ID, "prof-a", "codex", task.RunnerSandbox, pin)
	if err != nil {
		t.Fatalf("create pinned continuation: %v", err)
	}
	if continuation.ContinuationSnapshotPin != pin {
		t.Fatalf("created pin: got %+v want %+v", continuation.ContinuationSnapshotPin, pin)
	}

	updated, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning)
	if err != nil {
		t.Fatalf("update pinned continuation: %v", err)
	}
	if updated.ContinuationSnapshotPin != pin {
		t.Fatalf("lifecycle update changed pin: got %+v want %+v", updated.ContinuationSnapshotPin, pin)
	}
	latest, err := svc.LatestContinuation(created.ID)
	if err != nil {
		t.Fatalf("read pinned continuation: %v", err)
	}
	if latest == nil || latest.ContinuationSnapshotPin != pin {
		t.Fatalf("persisted pin: got %+v want %+v", latest, pin)
	}

	otherConfig, err := svc.RecordRuntimeConfig(created.ID, "prof-b", map[string]any{"model": "other"})
	if err != nil {
		t.Fatalf("record mismatched runtime config: %v", err)
	}
	mismatched := pin
	mismatched.RuntimeConfigVersionID = otherConfig.ID
	if _, err := svc.CreateContinuationWithSnapshotPin(created.ID, "prof-a", "codex", task.RunnerSandbox, mismatched); err == nil {
		t.Fatal("expected runtime config profile mismatch to fail")
	}
}

func TestTerminalContinuationClosesBoundCapabilities(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	marker := &recordingTerminalMarker{}
	svc.SetContinuationTerminalMarker(marker)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "g", Runner: task.RunnerSandbox})
	continuation, err := svc.CreateContinuation(created.ID, "prof-a", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if len(marker.continuationIDs) != 0 {
		t.Fatalf("non-terminal status closed capabilities: %v", marker.continuationIDs)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	if len(marker.continuationIDs) != 1 || marker.continuationIDs[0] != continuation.ID {
		t.Fatalf("terminal marker calls = %v", marker.continuationIDs)
	}
}
