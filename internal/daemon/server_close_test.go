package daemon

import (
	"context"
	"testing"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

type serverCloseBlockingAdapter struct {
	started chan struct{}
	release chan struct{}
}

func (adapter *serverCloseBlockingAdapter) Name() string { return "server-close-blocking" }

func (adapter *serverCloseBlockingAdapter) Run(ctx context.Context, _ string, _ func(task.EventKind, task.EventPayload)) error {
	close(adapter.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-adapter.release:
		return nil
	}
}

func TestServerCloseStopsActiveTaskHarnessBeforeClosingStore(t *testing.T) {
	server, created, _ := newFinishTaskFixture(t, nil)
	continuation, err := server.tasks.CreateContinuation(created.ID, created.RuntimeProfileID, "codex", created.Runner)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &serverCloseBlockingAdapter{started: make(chan struct{}), release: make(chan struct{})}
	launchDone := make(chan error, 1)
	go func() {
		launchDone <- server.harness.Launch(context.Background(), runtime.LaunchRequest{
			TaskID: created.ID, Goal: created.Goal, ContinuationID: continuation.ID, Adapter: adapter,
		})
	}()
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("Task Runtime Harness did not start")
	}

	if err := server.Close(); err != nil {
		close(adapter.release)
		<-launchDone
		t.Fatalf("Server.Close error = %v", err)
	}
	select {
	case <-launchDone:
	case <-time.After(200 * time.Millisecond):
		close(adapter.release)
		<-launchDone
		t.Fatal("Server.Close returned while the Task Runtime Harness was still active")
	}
	if server.harness.IsActive(created.ID) {
		t.Fatal("Server.Close left the Task Runtime Harness active")
	}
}
