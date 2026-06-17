package runtime_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/store"
	"pentest/internal/task"
)

func newServices(t *testing.T) (*runtime.Harness, *task.Service, *project.Service) {
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
	projects := project.NewService(db)
	tasks := task.NewService(db, projects)
	harness := runtime.NewHarness(tasks)
	return harness, tasks, projects
}

// TestFakeRuntimeEmitsNormalizedEvents proves the tracer bullet: launching a
// fake runtime through the harness makes the task timeline receive normalized
// lifecycle and runtime-output events.
func TestFakeRuntimeEmitsNormalizedEvents(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "enumerate example.com", RuntimeProfileID: "fake", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID:  created.ID,
		Adapter: runtime.NewFakeAdapter(),
	}); err != nil {
		t.Fatalf("launch: %v", err)
	}

	events, err := tasks.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	// The fake runtime must emit a lifecycle-started event and at least one
	// runtime-output event, all normalized through the task timeline.
	kinds := map[task.EventKind]bool{}
	for _, e := range events {
		kinds[e.Kind] = true
	}
	if !kinds[task.EventKindLifecycle] {
		t.Fatalf("expected a lifecycle event, got %#v", kinds)
	}
	if !kinds[task.EventKindRuntimeOutput] {
		t.Fatalf("expected a runtime_output event, got %#v", kinds)
	}
}

// slowFakeAdapter cooperates with cancellation so Stop can be observed.
type slowFakeAdapter struct{}

func (slowFakeAdapter) Name() string { return "fake-slow" }
func (slowFakeAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	emit(task.EventKindRuntimeOutput, task.EventPayload{"text": "started long work"})
	<-ctx.Done()
	return ctx.Err()
}

// TestHarnessStopEndsActiveRun proves Stop cancels the active continuation and
// the task ends in the stopped status.
func TestHarnessStopEndsActiveRun(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "long task", Runner: task.RunnerSandbox})

	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:  created.ID,
			Adapter: slowFakeAdapter{},
		})
	}()

	// Give the goroutine time to register the active run.
	waitForActive(t, harness, created.ID)
	harness.Stop(created.ID)

	select {
	case err := <-done:
		// A stopped run reports the context cancellation as its error.
		if err == nil {
			t.Fatal("expected stopped run to report a cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("launch did not return after Stop within 2s")
	}

	fetched, _ := tasks.Get(created.ID)
	if fetched.Status != task.StatusStopped {
		t.Fatalf("expected status stopped, got %q", fetched.Status)
	}
}

func waitForActive(t *testing.T, h *runtime.Harness, taskID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.IsActive(taskID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run did not become active")
}
