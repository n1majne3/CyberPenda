package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

type startupCancellationAdapter struct{}

func (startupCancellationAdapter) Name() string { return "startup-cancellation" }
func (startupCancellationAdapter) Run(ctx context.Context, _ string, _ func(task.EventKind, task.EventPayload)) error {
	<-ctx.Done()
	return ctx.Err()
}

// Ownership is a cancellation boundary, not a persistence or Provider-ready
// signal. Keep this distinction explicit so a test fix cannot break Stop.
func TestOwnerHarnessCanStopBeforeRunningPersistence(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var persisted atomic.Bool
	harness := runtime.NewOwnerHarness(runtime.OwnerHarnessConfig{
		VerifyOwner: func(string) error { return nil },
		MarkRunning: func(string, string) error {
			close(entered)
			<-release
			persisted.Store(true)
			return nil
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		done <- harness.Launch(ctx, runtime.OwnerLaunchRequest{OwnerID: "startup-owner", Adapter: startupCancellationAdapter{}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("startup Harness did not exit")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("launch did not enter persistence")
	}
	if !harness.IsActive("startup-owner") || persisted.Load() {
		t.Fatal("expected cancellable ownership before running persistence")
	}
	harness.Stop("startup-owner")
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop during startup was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the starting Runtime")
	}
	if harness.IsActive("startup-owner") {
		t.Fatal("finished launch retained ownership")
	}
}
