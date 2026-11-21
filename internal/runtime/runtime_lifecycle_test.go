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
	"pentest/internal/task"
)

func TestCommandRuntimeAdapterContinuesAfterOversizedStdoutLine(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{Domains: []string{"example.com"}}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "enumerate example.com", RuntimeProfileID: "codex", Runner: task.RunnerHost})
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
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "long task", Runner: task.RunnerSandbox})

	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:  created.ID,
			Adapter: slowFakeAdapter{},
		})
	}()

	waitForActive(t, harness, created.ID)
	harness.Stop(created.ID)

	select {
	case err := <-done:
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
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "long task", Runner: task.RunnerSandbox})

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
	if harness.IsActive(created.ID) {
		t.Fatal("StopAndWait returned before releasing the active Runtime owner")
	}
	if err := <-done; err == nil {
		t.Fatal("expected stopped run to report a cancellation error")
	}
}

func TestHarnessShutdownAllAndWaitReleasesRuntimeWithoutFinalizingDurableState(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "long task", Runner: task.RunnerSandbox})
	continuation, err := tasks.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID: created.ID, ContinuationID: continuation.ID, Adapter: slowFakeAdapter{},
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		found, getErr := tasks.Get(created.ID)
		if getErr == nil && found.Status == task.StatusRunning {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("Task did not reach running before shutdown: %#v, %v", found, getErr)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !harness.ShutdownAllAndWait(2 * time.Second) {
		t.Fatal("shutdown did not release the active Runtime")
	}
	if harness.IsActive(created.ID) {
		t.Fatal("shutdown returned before releasing the active Runtime owner")
	}
	if err := <-done; !errors.Is(err, runtime.ErrHarnessShutdown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown Launch error = %v, want Harness shutdown cancellation", err)
	}
	found, err := tasks.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != task.StatusRunning {
		t.Fatalf("shutdown finalized durable Task status = %q, want running for restart reconciliation", found.Status)
	}
	latest, err := tasks.Continuation(continuation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != task.StatusRunning {
		t.Fatalf("shutdown finalized durable Continuation status = %q, want running for restart reconciliation", latest.Status)
	}
}

// Finish intent path must not write Task/Continuation terminal status — sole
// owner is handleFinishTask after close + harness exit.
func TestHarnessFinishIntentDoesNotWriteTerminalStatus(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "finish owner", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := tasks.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:         created.ID,
			ContinuationID: cont.ID,
			Adapter:        slowFakeAdapter{},
		})
	}()
	waitForActive(t, harness, created.ID)

	harness.MarkFinishRequested(created.ID)
	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("StopAndWait under finish intent")
	}
	if err := <-done; err != nil {
		t.Fatalf("finish-intent Launch error = %v, want nil (exit without terminal write)", err)
	}

	updated, err := tasks.Continuation(cont.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status == task.StatusCompleted || updated.Status == task.StatusStopped || updated.Status == task.StatusFailed {
		t.Fatalf("harness finish path must not write terminal status, got %q", updated.Status)
	}
	fetched, _ := tasks.Get(created.ID)
	// May still be running (Launch marked running at start) — never completed by harness finish.
	if fetched.Status == task.StatusCompleted || fetched.Status == task.StatusStopped || fetched.Status == task.StatusFailed {
		t.Fatalf("Task terminal status written by harness finish path: %q", fetched.Status)
	}

	events, err := tasks.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawFinishShutdown bool
	for _, event := range events {
		if event.Kind == task.EventKindLifecycle && event.Payload["phase"] == "finish_shutdown" {
			sawFinishShutdown = true
		}
		if event.Kind == task.EventKindLifecycle && event.Payload["phase"] == "completed" {
			t.Fatalf("unexpected completed lifecycle from harness finish: %#v", event.Payload)
		}
	}
	if !sawFinishShutdown {
		t.Fatal("expected finish_shutdown lifecycle without terminal status write")
	}
}

func TestHarnessClearFinishIntentThenCancelSettlesStopped(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, err := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "finish clear", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatal(err)
	}
	cont, err := tasks.CreateContinuation(created.ID, "profile", "fake", task.RunnerSandbox)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID:         created.ID,
			ContinuationID: cont.ID,
			Adapter:        slowFakeAdapter{},
		})
	}()
	waitForActive(t, harness, created.ID)

	harness.MarkFinishRequested(created.ID)
	harness.ClearFinishIntent(created.ID)
	if harness.FinishIntentActive(created.ID) {
		t.Fatal("expected finish intent cleared")
	}
	if !harness.StopAndWait(created.ID, 2*time.Second) {
		t.Fatal("StopAndWait after clear")
	}
	<-done

	updated, err := tasks.Continuation(cont.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != task.StatusStopped {
		t.Fatalf("continuation status = %q, want stopped after clear→cancel", updated.Status)
	}
	if updated.Status == task.StatusCompleted {
		t.Fatal("Continuation completed after clear")
	}
}

func TestHarnessStopAndWaitConfirmsRuntimeResourcesExited(t *testing.T) {
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "long task", Runner: task.RunnerSandbox})

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
	skipUnlessPOSIXProcessDoubles(t)
	harness, tasks, projects := newServices(t)
	proj, _ := projects.Create("P", "", project.Scope{}, project.Defaults{})
	created, _ := tasks.Create(task.CreateRequest{ProjectID: proj.ID, Type: task.TypePentest, Goal: "long command", Runner: task.RunnerHost})

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
