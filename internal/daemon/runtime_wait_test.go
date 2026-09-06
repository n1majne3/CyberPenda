package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"pentest/internal/task"
)

func waitForTaskRunning(t *testing.T, server *Server, taskID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := awaitTaskRunning(ctx, server, taskID); err != nil {
		t.Fatal(err)
	}
}

func awaitTaskRunning(ctx context.Context, server *Server, taskID string) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		found, err := server.tasks.Get(taskID)
		if err != nil {
			return fmt.Errorf("read Task %s during startup: %w", taskID, err)
		}
		continuation, err := server.tasks.LatestContinuation(taskID)
		if err != nil {
			return fmt.Errorf("read Task %s Continuation during startup: %w", taskID, err)
		}
		active := server.harness.IsActive(taskID)
		if active && found.Status == task.StatusRunning && continuation != nil && continuation.Status == task.StatusRunning {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Task %s did not reach running: status=%s harness_active=%v continuation=%+v: %w", taskID, found.Status, active, continuation, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Hold the exact observable startup gap open. No sleep or scheduler luck is
// needed: ownership stays active while the durable states are set separately.
func TestTaskStartupWaitRequiresDurableContinuation(t *testing.T) {
	server, created, _ := newFinishTaskFixture(t, nil)
	release := make(chan struct{})
	defer close(release)
	forceTerminalWithActiveHarness(t, server, created, release)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := awaitTaskRunning(ctx, server, created.ID); err == nil {
		t.Fatal("startup wait returned for an active Harness with a completed Task")
	}
	cont, err := server.tasks.CreateContinuation(created.ID, created.RuntimeProfileID, "codex", created.Runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.UpdateStatus(created.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := awaitTaskRunning(ctx, server, created.ID); err == nil {
		t.Fatal("startup wait returned before the Continuation reached running")
	}
	if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := awaitTaskRunning(ctx, server, created.ID); err != nil {
		t.Fatalf("durable running Task and Continuation were not observed: %v", err)
	}
}
