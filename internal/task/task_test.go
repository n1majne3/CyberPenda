package task_test

import (
	"errors"
	"path/filepath"
	"testing"

	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

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
			YOLO:   false,
			Notes:  "business hours only",
			Extras: map[string]string{"depth": "shallow"},
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
	if created.RunControls.YOLO {
		t.Fatal("expected YOLO off by default")
	}
	if created.RunControls.Notes != "business hours only" {
		t.Fatalf("expected run-control notes, got %q", created.RunControls.Notes)
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

// TestReconcileInterruptedStatusesMarksActiveTasksInterrupted proves the daemon
// startup reconcile: tasks left running/paused/pending by a previous daemon
// instance become interrupted, while already-terminal tasks are untouched.
func TestReconcileInterruptedStatusesMarksActiveTasksInterrupted(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

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
	if pausedGot.Status != task.StatusInterrupted {
		t.Fatalf("expected paused task -> interrupted, got %q", pausedGot.Status)
	}
	if completedGot.Status != task.StatusCompleted {
		t.Fatalf("expected completed task untouched, got %q", completedGot.Status)
	}
}

