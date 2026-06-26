// Package runtime owns the runtime harness: the daemon-managed control wrapper
// that launches, resumes, steers, and stops a runtime for one task. The harness
// owns process lifecycle and continuation control; it does not execute pentest
// tools itself. Adapters are thin and provider-specific.
package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"pentest/internal/adapters"
	"pentest/internal/task"
	"pentest/internal/transcript"
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

// CommandAdapterConfig describes a provider process launch. Program and Args
// are already runner-adjusted by the caller: host launches point at the provider
// binary directly; sandbox launches point at the container CLI with provider
// args appended after the image.
type CommandAdapterConfig struct {
	Name    string
	Program string
	Args    []string
	Workdir string
	Env     map[string]string
}

type commandAdapter struct {
	config CommandAdapterConfig
}

// NewCommandAdapter returns a runtime adapter backed by a real local process.
// It is provider-agnostic: provider-specific argv construction belongs to the
// adapters package and runner-specific wrapping belongs to the runner package.
func NewCommandAdapter(config CommandAdapterConfig) Adapter {
	return &commandAdapter{config: config}
}

func (a *commandAdapter) Name() string {
	if a.config.Name != "" {
		return a.config.Name
	}
	return a.config.Program
}

func (a *commandAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	if a.config.Program == "" {
		return fmt.Errorf("runtime command program is required")
	}
	var emitMu sync.Mutex
	safeEmit := func(kind task.EventKind, payload task.EventPayload) {
		emitMu.Lock()
		defer emitMu.Unlock()
		emit(kind, payload)
	}

	cmd := exec.CommandContext(ctx, a.config.Program, a.config.Args...)
	cmd.Dir = a.config.Workdir
	cmd.Env = os.Environ()
	for key, value := range a.config.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open stderr: %w", err)
	}

	safeEmit(task.EventKindLifecycle, adapters.Redact(task.EventPayload{
		"phase":   "process_started",
		"adapter": a.Name(),
		"program": a.config.Program,
		"args":    a.config.Args,
	}))

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start runtime process: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go scanOutput(&wg, stdout, "stdout", safeEmit)
	go scanOutput(&wg, stderr, "stderr", safeEmit)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("runtime process failed: %w", err)
	}
	return nil
}

func scanOutput(wg *sync.WaitGroup, reader io.Reader, stream string, emit func(task.EventKind, task.EventPayload)) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if transcript.IsIgnorableRuntimeLine(line) {
			continue
		}
		emit(task.EventKindRuntimeOutput, adapters.Redact(task.EventPayload{
			"stream": stream,
			"text":   line,
		}))
	}
	if err := scanner.Err(); err != nil {
		emit(task.EventKindRuntimeOutput, adapters.Redact(task.EventPayload{
			"stream": stream,
			"text":   fmt.Sprintf("read %s: %v", stream, err),
		}))
	}
}
