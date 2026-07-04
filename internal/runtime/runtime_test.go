package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestHarnessStopAndWaitEndsActiveRun(t *testing.T) {
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

	waitForActive(t, harness, created.ID)
	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("expected StopAndWait to observe launch exit")
	}
	if err := <-done; err == nil {
		t.Fatal("expected stopped run to report a cancellation error")
	}
}

func TestHarnessStopAndWaitConfirmsRuntimeResourcesExited(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "long task", Runner: task.RunnerSandbox})

	var confirmed atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:  created.ID,
			Adapter: slowFakeAdapter{},
			StopConfirmation: func(timeout time.Duration) error {
				confirmed.Store(timeout > 0)
				return nil
			},
		})
	}()

	waitForActive(t, harness, created.ID)
	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("expected StopAndWait to confirm runtime resource exit")
	}
	if !confirmed.Load() {
		t.Fatal("expected stop confirmation to run after adapter exit")
	}
	if err := <-done; err == nil {
		t.Fatal("expected stopped run to report a cancellation error")
	}
}

func TestCommandRuntimeAdapterCancellationReturnsContextCanceled(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Goal: "long command", Runner: task.RunnerHost})

	binary := filepath.Join(t.TempDir(), "slow-command")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho started\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatalf("write provider binary: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID: created.ID,
			Adapter: runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
				Name:    "codex",
				Program: binary,
			}),
		})
	}()

	waitForActive(t, harness, created.ID)
	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("expected StopAndWait to observe command exit")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	fetched, _ := tasks.Get(created.ID)
	if fetched.Status != task.StatusStopped {
		t.Fatalf("expected task status stopped, got %q", fetched.Status)
	}
}

func TestDockerContainerStopConfirmationTreatsRemovedContainerAsExited(t *testing.T) {
	dir := t.TempDir()
	cidFile := filepath.Join(dir, "container.cid")
	if err := os.WriteFile(cidFile, []byte("ctr-123\n"), 0o600); err != nil {
		t.Fatalf("write cidfile: %v", err)
	}
	logPath := filepath.Join(dir, "inspect.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	if err := runtime.ConfirmDockerContainerExited(docker, cidFile, time.Second); err != nil {
		t.Fatalf("confirm docker container exited: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read inspect log: %v", err)
	}
	if !strings.Contains(string(raw), "inspect -f {{.State.Running}} ctr-123") {
		t.Fatalf("expected docker inspect for cidfile container, got %s", string(raw))
	}
}

func TestDockerContainerStopConfirmationTimesOutWhileContainerRuns(t *testing.T) {
	dir := t.TempDir()
	cidFile := filepath.Join(dir, "container.cid")
	if err := os.WriteFile(cidFile, []byte("ctr-running\n"), 0o600); err != nil {
		t.Fatalf("write cidfile: %v", err)
	}
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\necho true\n"), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	if err := runtime.ConfirmDockerContainerExited(docker, cidFile, 80*time.Millisecond); err == nil {
		t.Fatal("expected timeout while container is still running")
	}
}

func TestDiscoverCodexSessionReturnsNewestSavedSession(t *testing.T) {
	providerHome := filepath.Join(t.TempDir(), "codex")
	oldPath := filepath.Join(providerHome, "sessions", "2026", "07", "03", "older.jsonl")
	newPath := filepath.Join(providerHome, "sessions", "2026", "07", "04", "newer.jsonl")
	for path, sessionID := range map[string]string{
		oldPath: "sess-old",
		newPath: "sess-new",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir session dir: %v", err)
		}
		line := `{"type":"session_meta","payload":{"session_id":"` + sessionID + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatalf("write session file: %v", err)
		}
	}
	if err := os.Chtimes(newPath, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("chtimes newer session: %v", err)
	}

	metadata, err := runtime.DiscoverCodexSession(providerHome)
	if err != nil {
		t.Fatalf("discover codex session: %v", err)
	}
	if metadata.NativeSessionID != "sess-new" {
		t.Fatalf("expected newest session id sess-new, got %q", metadata.NativeSessionID)
	}
	if metadata.NativeSessionPath != newPath {
		t.Fatalf("expected newest session path %q, got %q", newPath, metadata.NativeSessionPath)
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
