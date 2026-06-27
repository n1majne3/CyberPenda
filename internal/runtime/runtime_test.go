package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestCommandRuntimeAdapterExecutesProviderProcessAndStreamsOutput(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "enumerate example.com", RuntimeProfileID: "codex", Runner: task.RunnerHost})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "codex-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho provider-ready:$*\necho provider-warning >&2\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID: created.ID,
		Goal:   created.Goal,
		Adapter: runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    "codex",
			Program: binary,
			Args:    []string{"run", "--", created.Goal},
		}),
	}); err != nil {
		t.Fatalf("launch provider adapter: %v", err)
	}

	events, err := tasks.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var sawAdapter bool
	var sawStdout bool
	var sawStderr bool
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle && event.Payload["adapter"] == "codex" {
			sawAdapter = true
		}
		if event.Kind == task.EventKindRuntimeOutput {
			text, _ := event.Payload["text"].(string)
			stream, _ := event.Payload["stream"].(string)
			if stream == "stdout" && strings.Contains(text, "provider-ready:run -- enumerate example.com") {
				sawStdout = true
			}
			if stream == "stderr" && strings.Contains(text, "provider-warning") {
				sawStderr = true
			}
		}
	}
	if !sawAdapter {
		t.Fatalf("expected lifecycle adapter codex, got %#v", events)
	}
	if !sawStdout {
		t.Fatalf("expected stdout event from provider, got %#v", events)
	}
	if !sawStderr {
		t.Fatalf("expected stderr event from provider, got %#v", events)
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
func TestCommandRuntimeAdapterContinuesAfterOversizedStdoutLine(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "enumerate example.com", RuntimeProfileID: "codex", Runner: task.RunnerHost})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "huge-line-test")
	script := "#!/bin/sh\n" +
		"python3 -c 'import sys; sys.stdout.write(\"x\"*200000); sys.stdout.write(\"\\n\"); print(\"after-huge-line\")'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := harness.Launch(ctx, runtime.LaunchRequest{
		TaskID: created.ID,
		Goal:   created.Goal,
		Adapter: runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
			Name:    "codex",
			Program: binary,
			Args:    []string{"run"},
		}),
	}); err != nil {
		t.Fatalf("launch provider adapter: %v", err)
	}

	events, err := tasks.Events(created.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var sawHuge bool
	var sawAfter bool
	var sawScannerError bool
	for _, event := range events {
		if event.Kind != task.EventKindRuntimeOutput {
			continue
		}
		text, _ := event.Payload["text"].(string)
		if strings.Contains(text, "token too long") {
			sawScannerError = true
		}
		if len(text) > 100_000 {
			sawHuge = true
		}
		if strings.Contains(text, "after-huge-line") {
			sawAfter = true
		}
	}
	if sawScannerError {
		t.Fatalf("expected no scanner token-too-long error, got events: %#v", events)
	}
	if !sawHuge {
		t.Fatalf("expected truncated huge line event, got %#v", events)
	}
	if !sawAfter {
		t.Fatalf("expected output after huge line, got %#v", events)
	}
}

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
