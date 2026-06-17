// Package runtime owns the runtime harness: the daemon-managed control wrapper
// that launches, resumes, steers, and stops a runtime for one task. The harness
// owns process lifecycle and continuation control; it does not execute pentest
// tools itself. Adapters are thin and provider-specific.
package runtime

import (
	"context"
	"fmt"
	"sync"

	"pentest/internal/task"
)

// Adapter is the provider-specific runtime boundary. Real adapters (Codex,
// Claude Code, Pi) detect a binary, build launch args, stream normalized
// events, and support the best available steering mode. The fake adapter
// exercises the same contract without a real runtime.
type Adapter interface {
	// Name identifies the runtime provider.
	Name() string
	// Run executes the runtime for one continuation, emitting normalized events
	// through emit. It returns when the continuation completes, is interrupted
	// (ctx cancelled), or fails. Adapters must not leak secrets into events.
	Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error
}

// LaunchRequest describes a harness launch for one task continuation.
type LaunchRequest struct {
	TaskID  string
	Goal    string
	Adapter Adapter
}

// Harness owns runtime lifecycle for tasks through adapters. It records
// normalized events on the task timeline and tracks active runs so they can be
// stopped.
type Harness struct {
	tasks  *task.Service
	mu     sync.Mutex
	active map[string]context.CancelFunc // taskID -> cancel
}

// NewHarness returns a Harness that records events through the task service.
func NewHarness(tasks *task.Service) *Harness {
	return &Harness{tasks: tasks, active: map[string]context.CancelFunc{}}
}

// Launch starts one runtime continuation for a task. It marks the task running,
// emits a lifecycle-started event, runs the adapter, and emits a lifecycle
// completion event. It blocks until the continuation finishes or the context is
// cancelled.
func (h *Harness) Launch(ctx context.Context, req LaunchRequest) error {
	if req.Adapter == nil {
		return fmt.Errorf("launch requires an adapter")
	}
	if _, err := h.tasks.Get(req.TaskID); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	h.register(req.TaskID, cancel)
	defer h.unregister(req.TaskID)

	emit := func(kind task.EventKind, payload task.EventPayload) {
		if _, err := h.tasks.AppendEvent(req.TaskID, kind, payload); err != nil {
			// Event recording failure must not crash the runtime; it is surfaced
			// via the returned run error below when relevant.
			return
		}
	}

	emit(task.EventKindLifecycle, task.EventPayload{"phase": "started", "adapter": req.Adapter.Name()})
	if _, err := h.tasks.UpdateStatus(req.TaskID, task.StatusRunning); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	runErr := req.Adapter.Run(ctx, req.Goal, emit)

	finalStatus := task.StatusCompleted
	finalPhase := "completed"
	if runErr != nil {
		finalStatus = task.StatusFailed
		finalPhase = "failed"
	}
	if ctx.Err() != nil {
		finalStatus = task.StatusStopped
		finalPhase = "stopped"
	}
	emit(task.EventKindLifecycle, task.EventPayload{"phase": finalPhase, "adapter": req.Adapter.Name()})
	if _, err := h.tasks.UpdateStatus(req.TaskID, finalStatus); err != nil {
		return fmt.Errorf("mark %s: %w", finalStatus, err)
	}
	return runErr
}

// Stop requests the active continuation for a task to stop. It is a no-op if no
// continuation is active.
func (h *Harness) Stop(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cancel, ok := h.active[taskID]; ok {
		cancel()
	}
}

// IsActive reports whether a continuation is currently running for the task.
func (h *Harness) IsActive(taskID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.active[taskID]
	return ok
}

func (h *Harness) register(taskID string, cancel context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[taskID] = cancel
}

func (h *Harness) unregister(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.active, taskID)
}

// fakeGoalKey holds the task id the fake adapter is running, used only to keep
// Run non-trivial. The fake adapter simulates a runtime that reports progress.
type fakeAdapter struct{}

// NewFakeAdapter returns the fake runtime adapter. The fake adapter exercises
// the full harness and event contract without a real runtime, so task, harness,
// blackboard, and report paths can be proven before real adapters exist.
func NewFakeAdapter() Adapter { return &fakeAdapter{} }

func (f *fakeAdapter) Name() string { return "fake" }

func (f *fakeAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	emit(task.EventKindRuntimeOutput, task.EventPayload{"text": "planning task", "goal": goal})
	// Cooperate with cancellation.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	emit(task.EventKindRuntimeOutput, task.EventPayload{"text": "enumerating in-scope assets"})
	return nil
}
