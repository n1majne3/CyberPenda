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

type restartOrderingReconciler struct {
	tasks        *task.Service
	reason       string
	eventVisible bool
}

func (r *restartOrderingReconciler) ReconcileTerminalContinuation(_ context.Context, continuationID, reason string) error {
	r.reason = reason
	continuation, err := r.tasks.Continuation(continuationID)
	if err != nil {
		return err
	}
	events, err := r.tasks.Events(continuation.TaskID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.ContinuationID == continuationID && event.Kind == task.EventKindLifecycle &&
			event.Payload["phase"] == "interrupted" && event.Payload["reason"] == "daemon_restart" {
			r.eventVisible = true
		}
	}
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
		ProjectID: projectID.ID,

		Type: task.TypePentest, Goal: "Enumerate example.com subdomains",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxNetwork: "host_proxy_only",
			Notes:          "business hours only",
			Extras:         map[string]string{"depth": "shallow"},
			Policy: task.TaskPolicy{
				MaxAttempts:            9,
				MaxWrongSubmissions:    4,
				MaxWallTimeSeconds:     3600,
				MaxConsecutiveFailures: 3,
				MaxRatingDrawdown:      40,
				MaxNoProgressSeconds:   900,
			},
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
	if created.Type != task.TypePentest {
		t.Fatalf("expected Pentest Task Type snapshot, got %q", created.Type)
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
	if fetched.RunControls.SandboxVPNTun {
		t.Fatal("expected sandbox VPN TUN off by default")
	}
	if fetched.RunControls.Policy.MaxWrongSubmissions != 4 || fetched.RunControls.Policy.MaxRatingDrawdown != 40 {
		t.Fatalf("expected immutable task policy snapshot, got %#v", fetched.RunControls.Policy)
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

func TestCreateCapturesExplicitCTFTaskTypeAndRejectsProjectKindMismatch(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	ctfProject, err := projects.CreateWithKind("Arena", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Challenge Project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID: ctfProject.ID,
		Type:      task.TypeCTFChallenge,
		Goal:      "Solve the selected challenge",
		Runner:    task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create CTF Challenge Task: %v", err)
	}
	if created.Type != task.TypeCTFChallenge {
		t.Fatalf("expected CTF Challenge Task Type snapshot, got %q", created.Type)
	}
	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get CTF Challenge Task: %v", err)
	}
	if fetched.Type != task.TypeCTFChallenge {
		t.Fatalf("expected stored CTF Challenge Task Type snapshot, got %q", fetched.Type)
	}

	_, err = svc.Create(task.CreateRequest{
		ProjectID: ctfProject.ID,
		Type:      task.TypePentest,
		Goal:      "Run a Pentest Task",
		Runner:    task.RunnerSandbox,
	})
	if !errors.Is(err, task.ErrTaskTypeProjectKindMismatch) {
		t.Fatalf("expected Task Type and Project Kind mismatch, got %v", err)
	}
}

func TestCreateRejectsMissingTaskType(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	createdProject, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	_, err = svc.Create(task.CreateRequest{
		ProjectID: createdProject.ID,
		Goal:      "Inspect the target",
		Runner:    task.RunnerSandbox,
	})
	if !errors.Is(err, task.ErrInvalidTaskType) {
		t.Fatalf("expected explicit Task Type requirement, got %v", err)
	}
}

func TestCreateRejectsInvalidTaskPolicy(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("Acme", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err = svc.Create(task.CreateRequest{
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "solve challenges",
		Runner: task.RunnerSandbox,
		RunControls: task.RunControls{Policy: task.TaskPolicy{
			MaxAttempts: -1,
		}},
	})
	if !errors.Is(err, task.ErrInvalidTaskPolicy) {
		t.Fatalf("expected invalid task policy, got %v", err)
	}
}

func TestCreateRejectsMissingGoal(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "", Runner: task.RunnerSandbox})
	if !errors.Is(err, task.ErrMissingGoal) {
		t.Fatalf("expected ErrMissingGoal, got %v", err)
	}
}

func TestCreateRejectsUnsupportedRunner(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "do something", Runner: "kali-magic"})
	if !errors.Is(err, task.ErrUnsupportedRunner) {
		t.Fatalf("expected ErrUnsupportedRunner, got %v", err)
	}
}

func TestCreateDefaultsToWorkingGraphAndPersistsBlackboardMode(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	workingGraph, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if workingGraph.RunControls.BlackboardMode != task.BlackboardModeWorkingGraph ||
		workingGraph.BlackboardConclusion.Mode != task.BlackboardModeWorkingGraph ||
		workingGraph.BlackboardConclusion.State != task.BlackboardConclusionStateClean {
		t.Fatalf("working-graph Task = %#v", workingGraph)
	}
	fetched, err := svc.Get(workingGraph.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.RunControls.BlackboardMode != task.BlackboardModeWorkingGraph {
		t.Fatalf("persisted mode = %q", fetched.RunControls.BlackboardMode)
	}

	interactive, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect interactively", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardMode: task.BlackboardModeInteractive},
	})
	if err != nil {
		t.Fatal(err)
	}
	if interactive.RunControls.BlackboardMode != task.BlackboardModeWorkingGraph || interactive.BlackboardConclusion.Mode != task.BlackboardModeWorkingGraph {
		t.Fatalf("interactive Task = %#v", interactive)
	}
}

func TestCreatePersistsDisabledBlackboardMode(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect without Blackboard", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardMode: task.BlackboardModeDisabled},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.RunControls.BlackboardMode != task.BlackboardModeDisabled ||
		created.BlackboardConclusion.Mode != task.BlackboardModeDisabled {
		t.Fatalf("disabled Task = %#v", created)
	}

	reloaded, err := svc.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RunControls.BlackboardMode != task.BlackboardModeDisabled ||
		reloaded.BlackboardConclusion.Mode != task.BlackboardModeDisabled {
		t.Fatalf("reloaded disabled Task = %#v", reloaded)
	}
}

func TestGetNormalizesMissingStoredBlackboardModeToWorkingGraph(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID, Type: task.TypePentest, Goal: "legacy Task", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET run_controls_json='{}' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}

	reloaded, err := svc.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RunControls.BlackboardMode != task.BlackboardModeWorkingGraph ||
		reloaded.BlackboardConclusion.Mode != task.BlackboardModeWorkingGraph {
		t.Fatalf("normalized legacy Task = %#v", reloaded)
	}
}

func TestCreateRejectsInvalidBlackboardMode(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})

	_, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,
		Type:      task.TypePentest, Goal: "inspect", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{BlackboardMode: "automatic"},
	})
	if !errors.Is(err, task.ErrInvalidBlackboardMode) {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateRejectsUnknownProject(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	_, err := svc.Create(task.CreateRequest{ProjectID: "missing", Type: task.TypePentest, Goal: "x", Runner: task.RunnerSandbox})
	if !errors.Is(err, task.ErrProjectNotFound) {
		t.Fatalf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestGetReturnsPersistedTask(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})

	created, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate", RuntimeProfileID: "prof", Runner: task.RunnerSandbox})
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

	if _, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "first", Runner: task.RunnerSandbox}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "second", Runner: task.RunnerSandbox}); err != nil {
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
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox})

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

// TestHistoryEventWindowReadsOnlyTheRequestedProjectionWindow proves History
// reads use a keyset-bounded database query. Unrelated conversation Events do
// not consume the Timeline limit or move its projection cursor.
func TestHistoryEventWindowReadsOnlyTheRequestedProjectionWindow(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox})

	for index := 1; index <= 5; index++ {
		if _, err := svc.AppendEvent(created.ID, task.EventKindLifecycle, task.EventPayload{"phase": index}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.AppendEvent(created.ID, task.EventKindConversation, task.EventPayload{"role": "assistant", "text": "irrelevant"}); err != nil {
		t.Fatal(err)
	}

	window, err := svc.HistoryEventWindow(created.ID, task.EventWindowQuery{
		Projection: task.EventProjectionTimeline,
		Limit:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Events) != 3 || window.Events[0].Seq != 3 || window.Events[2].Seq != 5 {
		t.Fatalf("initial Timeline Event window = %#v, want seqs 3..5", window.Events)
	}
	if window.Cursor != 5 || !window.HasOlder {
		t.Fatalf("initial Timeline Event window cursor=%d has_older=%t, want 5/true", window.Cursor, window.HasOlder)
	}

	delta, err := svc.HistoryEventWindow(created.ID, task.EventWindowQuery{
		Projection: task.EventProjectionTimeline,
		AfterSet:   true,
		After:      3,
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Events) != 1 || delta.Events[0].Seq != 4 || !delta.HasNewer || delta.ScanCursor != 4 {
		t.Fatalf("Timeline Event delta = %#v, want seq 4 with more new Events", delta)
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
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type:

	// First config version is captured at launch.
	task.TypePentest, Goal: "g", RuntimeProfileID: "prof-a", Runner: task.RunnerSandbox})

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
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "g",
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

func TestTerminalContinuationStatusCannotBeOverwrittenByLateReconciliation(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	createdProject, err := projects.Create("Terminal monotonicity", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "finish once", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusCompleted); err != nil {
		t.Fatalf("complete Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusInterrupted); !errors.Is(err, task.ErrContinuationStatusConflict) {
		t.Fatalf("late reconciliation error = %v, want status conflict", err)
	}
	stored, err := svc.Continuation(continuation.ID)
	if err != nil {
		t.Fatalf("read terminal Continuation: %v", err)
	}
	if stored.Status != task.StatusCompleted {
		t.Fatalf("late reconciliation overwrote terminal status with %q", stored.Status)
	}
}

func TestContinuationRuntimeMetadataIsPersisted(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "g",
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
			ProjectID: proj.ID,

			Type: task.TypePentest, Goal: goal,
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
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "already interrupted task",
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

func TestRestartInterruptionPersistsTerminalEventBeforeReconciliation(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)

	proj, err := projects.Create("Acme", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	created, err := svc.Create(task.CreateRequest{
		ProjectID: proj.ID, Type: task.TypePentest, Goal: "restart ordering", RuntimeProfileID: "profile-1", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := svc.CreateContinuation(created.ID, "profile-1", "pi", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	if _, err := svc.UpdateContinuationStatus(continuation.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Continuation: %v", err)
	}
	if _, err := svc.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatalf("start Task: %v", err)
	}

	reconciler := &restartOrderingReconciler{tasks: svc}
	svc.SetContinuationReconciler(reconciler)
	if _, err := svc.ReconcileInterruptedState(); err != nil {
		t.Fatalf("reconcile restart state: %v", err)
	}
	if reconciler.reason != "daemon_restart" {
		t.Fatalf("reconciliation reason = %q, want daemon_restart", reconciler.reason)
	}
	if !reconciler.eventVisible {
		t.Fatal("daemon_restart terminal Event was not visible before reconciliation")
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
		ProjectID: proj.ID,

		Type: task.TypePentest, Goal: "completed sandbox task",
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

func TestReconcileInterruptedStateExceptPreservesOwnedTaskAndReturnsHostIdentity(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, _ := projects.Create("Acme", "", project.Scope{}, project.Defaults{})

	owned, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "owned", RuntimeProfileID: "profile", Runner: task.RunnerHost})
	ownedContinuation, _ := svc.CreateContinuation(owned.ID, "profile", "codex", task.RunnerHost)
	_, _ = svc.UpdateContinuationRuntimeMetadata(ownedContinuation.ID, "41001", "native-owned", "")
	_, _ = svc.UpdateContinuationStatus(ownedContinuation.ID, task.StatusRunning)
	_, _ = svc.UpdateStatus(owned.ID, task.StatusRunning)

	stale, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "stale", RuntimeProfileID: "profile", Runner: task.RunnerHost})
	staleContinuation, _ := svc.CreateContinuation(stale.ID, "profile", "codex", task.RunnerHost)
	_, _ = svc.UpdateContinuationRuntimeMetadata(staleContinuation.ID, "41002", "native-stale", "")
	_, _ = svc.UpdateContinuationStatus(staleContinuation.ID, task.StatusRunning)
	_, _ = svc.UpdateStatus(stale.ID, task.StatusRunning)

	result, err := svc.ReconcileInterruptedStateExcept([]string{"", owned.ID, owned.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID != stale.ID {
		t.Fatalf("interrupted Tasks = %#v", result.Tasks)
	}
	if len(result.Continuations) != 1 || result.Continuations[0].ID != staleContinuation.ID ||
		result.Continuations[0].Runner != task.RunnerHost || result.Continuations[0].ContainerID != "41002" {
		t.Fatalf("runtime cleanup candidates = %#v", result.Continuations)
	}
	ownedAfter, _ := svc.Get(owned.ID)
	ownedContinuationAfter, _ := svc.LatestContinuation(owned.ID)
	if ownedAfter.Status != task.StatusRunning || ownedContinuationAfter == nil || ownedContinuationAfter.Status != task.StatusRunning {
		t.Fatalf("owned Runtime was interrupted: Task=%#v Continuation=%#v", ownedAfter, ownedContinuationAfter)
	}
}

func TestTerminalContinuationClosesBoundCapabilities(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	marker := &recordingTerminalMarker{}
	svc.SetContinuationTerminalMarker(marker)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := svc.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox})
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

// TestCreatePersistsSandboxVPNTunRunControl captures the opt-in TUN device
// capability so CTF OpenVPN clients can create /dev/net/tun inside the sandbox.
func TestCreatePersistsSandboxVPNTunRunControl(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("VPN CTF", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	created, err := svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Type:             task.TypePentest,
		Goal:             "Connect VPN then solve",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxVPNTun: true,
			ContainerCLI:  "podman",
		},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if !created.RunControls.SandboxVPNTun {
		t.Fatal("expected sandbox VPN TUN enabled on create")
	}
	if created.RunControls.ContainerCLI != "podman" {
		t.Fatalf("container_cli = %q, want podman", created.RunControls.ContainerCLI)
	}
	fetched, err := svc.Get(created.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !fetched.RunControls.SandboxVPNTun {
		t.Fatal("expected sandbox VPN TUN to persist")
	}
	if fetched.RunControls.ContainerCLI != "podman" {
		t.Fatalf("persisted container_cli = %q", fetched.RunControls.ContainerCLI)
	}
}

func TestCreateRejectsInvalidContainerCLI(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("P", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	_, err = svc.Create(task.CreateRequest{
		ProjectID: proj.ID, Type: task.TypePentest, Goal: "g", Runner: task.RunnerSandbox,
		RunControls: task.RunControls{ContainerCLI: "containerd"},
	})
	if !errors.Is(err, task.ErrInvalidContainerCLI) {
		t.Fatalf("err = %v, want %v", err, task.ErrInvalidContainerCLI)
	}
}

// TestCreateRejectsSandboxVPNTunWithHostProxyOnly proves the two network
// shapes are mutually exclusive: host_proxy_only drops NET_ADMIN after the
// firewall is installed, which cannot host an in-sandbox OpenVPN client.
func TestCreateRejectsSandboxVPNTunWithHostProxyOnly(t *testing.T) {
	db := newStore(t)
	projects := project.NewService(db)
	svc := task.NewService(db, projects)
	proj, err := projects.Create("VPN CTF", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err = svc.Create(task.CreateRequest{
		ProjectID:        proj.ID,
		Type:             task.TypePentest,
		Goal:             "Connect VPN then solve",
		RuntimeProfileID: "profile-1",
		Runner:           task.RunnerSandbox,
		RunControls: task.RunControls{
			SandboxNetwork: "host_proxy_only",
			SandboxVPNTun:  true,
		},
	})
	if !errors.Is(err, task.ErrSandboxVPNTunHostProxyConflict) {
		t.Fatalf("create error = %v, want %v", err, task.ErrSandboxVPNTunHostProxyConflict)
	}
}
