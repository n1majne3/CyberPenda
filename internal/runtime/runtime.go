// Package runtime owns the runtime harness: the daemon-managed control wrapper
// that launches, resumes, steers, and stops a runtime for one task. The harness
// owns process lifecycle and continuation control; it does not execute pentest
// tools itself. Adapters are thin and provider-specific.
package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"pentest/internal/adapters"
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

type metadataRecordingAdapter interface {
	SetMetadataRecorder(func(NativeSessionMetadata) error)
}

// LaunchRequest describes a harness launch for one task continuation.
type LaunchRequest struct {
	TaskID           string
	Goal             string
	Adapter          Adapter
	ContinuationID   string
	Metadata         func() (NativeSessionMetadata, error)
	StopConfirmation StopConfirmation
}

// Harness owns runtime lifecycle for tasks through adapters. It records
// normalized events on the task timeline and tracks active runs so they can be
// stopped.
type Harness struct {
	tasks  *task.Service
	mu     sync.Mutex
	active map[string]*activeRun // taskID -> cancel + completion
}

type activeRun struct {
	mu               sync.RWMutex
	cancel           context.CancelFunc
	done             chan struct{}
	stopConfirmation StopConfirmation
	continuationID   string
	// finishRequested is set by operator Task Finish so Launch finalizes as
	// completed rather than stopped when the context is cancelled.
	finishRequested bool
}

// NewHarness returns a Harness that records events through the task service.
func NewHarness(tasks *task.Service) *Harness {
	return &Harness{tasks: tasks, active: map[string]*activeRun{}}
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
	done := make(chan struct{})
	run := h.register(req.TaskID, cancel, done, req.StopConfirmation, req.ContinuationID)
	defer func() {
		close(done)
		h.unregister(req.TaskID)
	}()

	emit := func(kind task.EventKind, payload task.EventPayload) {
		var err error
		continuationID := run.currentContinuationID()
		if continuationID != "" {
			_, err = h.tasks.AppendContinuationEvent(req.TaskID, continuationID, kind, payload)
		} else {
			_, err = h.tasks.AppendEvent(req.TaskID, kind, payload)
		}
		if err != nil {
			// Event recording failure must not crash the runtime; it is surfaced
			// via the returned run error below when relevant.
			return
		}
	}

	emit(task.EventKindLifecycle, task.EventPayload{"phase": "started", "adapter": req.Adapter.Name()})
	if _, err := h.tasks.UpdateStatus(req.TaskID, task.StatusRunning); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	if req.ContinuationID != "" {
		if _, err := h.tasks.UpdateContinuationStatus(req.ContinuationID, task.StatusRunning); err != nil {
			return fmt.Errorf("mark continuation running: %w", err)
		}
	}
	if req.ContinuationID != "" {
		if recorder, ok := req.Adapter.(metadataRecordingAdapter); ok {
			recorder.SetMetadataRecorder(func(metadata NativeSessionMetadata) error {
				if metadata.ContainerID == "" && metadata.NativeSessionID == "" && metadata.NativeSessionPath == "" {
					return nil
				}
				continuationID := run.currentContinuationID()
				if continuationID == "" {
					return nil
				}
				_, err := h.tasks.UpdateContinuationRuntimeMetadata(continuationID, metadata.ContainerID, metadata.NativeSessionID, metadata.NativeSessionPath)
				return err
			})
		}
	}

	runErr := req.Adapter.Run(ctx, req.Goal, emit)

	finalStatus := task.StatusCompleted
	finalPhase := "completed"
	if runErr != nil {
		finalStatus = task.StatusFailed
		finalPhase = "failed"
	}
	if ctx.Err() != nil {
		// Operator Finish cancels the harness context after RequestFinish; that
		// must finalize completed, not the stopped interrupt path used by Stop.
		if run.finishWasRequested() {
			finalStatus = task.StatusCompleted
			finalPhase = "completed"
		} else {
			finalStatus = task.StatusStopped
			finalPhase = "stopped"
		}
	}
	emit(task.EventKindLifecycle, task.EventPayload{"phase": finalPhase, "adapter": req.Adapter.Name()})
	finalContinuationID := run.currentContinuationID()
	if finalContinuationID != "" && req.Metadata != nil {
		metadata, err := req.Metadata()
		if err == nil {
			if metadata.ContainerID != "" || metadata.NativeSessionID != "" || metadata.NativeSessionPath != "" {
				if _, err := h.tasks.UpdateContinuationRuntimeMetadata(finalContinuationID, metadata.ContainerID, metadata.NativeSessionID, metadata.NativeSessionPath); err != nil {
					return fmt.Errorf("record continuation metadata: %w", err)
				}
			}
		}
	}
	if _, err := h.tasks.UpdateStatus(req.TaskID, finalStatus); err != nil {
		return fmt.Errorf("mark %s: %w", finalStatus, err)
	}
	if finalContinuationID != "" {
		if _, err := h.tasks.UpdateContinuationStatus(finalContinuationID, finalStatus); err != nil {
			return fmt.Errorf("mark continuation %s: %w", finalStatus, err)
		}
	}
	if run.finishWasRequested() {
		// Operator Finish cancelled the context on purpose; treat the exit as
		// successful completion so callers log completed rather than stopped.
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return runErr
}

// Stop requests the active continuation for a task to stop. It is a no-op if no
// continuation is active.
func (h *Harness) Stop(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if run, ok := h.active[taskID]; ok {
		run.cancel()
	}
}

// RequestFinish marks the active run for operator Task Finish and cancels it.
// Launch finalizes the Task/Continuation as completed instead of stopped.
// It is a no-op when no continuation is active.
func (h *Harness) RequestFinish(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	run, ok := h.active[taskID]
	if !ok {
		return
	}
	run.mu.Lock()
	run.finishRequested = true
	run.mu.Unlock()
	run.cancel()
}

// IsActive reports whether a continuation is currently running for the task.
func (h *Harness) IsActive(taskID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.active[taskID]
	return ok
}

// StopAndWait requests a stop and waits for the active continuation to exit.
// It returns true when no continuation is active or the runtime exits before
// the timeout.
func (h *Harness) StopAndWait(taskID string, timeout time.Duration) bool {
	h.mu.Lock()
	run, ok := h.active[taskID]
	h.mu.Unlock()
	if !ok {
		return true
	}
	run.cancel()
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-run.done:
		if run.stopConfirmation == nil {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		return run.stopConfirmation(remaining) == nil
	case <-timer.C:
		return false
	}
}

// RebindContinuation moves the active run's event/metadata/finalization pin to
// a replacement provider turn without restarting the Task-owned process.
func (h *Harness) RebindContinuation(taskID, continuationID string) error {
	h.mu.Lock()
	run := h.active[taskID]
	h.mu.Unlock()
	if run == nil || continuationID == "" {
		return fmt.Errorf("active runtime continuation is unavailable")
	}
	run.mu.Lock()
	run.continuationID = continuationID
	run.mu.Unlock()
	return nil
}

func (h *Harness) register(taskID string, cancel context.CancelFunc, done chan struct{}, stopConfirmation StopConfirmation, continuationID string) *activeRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	run := &activeRun{cancel: cancel, done: done, stopConfirmation: stopConfirmation, continuationID: continuationID}
	h.active[taskID] = run
	return run
}

func (h *Harness) unregister(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.active, taskID)
}

func (r *activeRun) currentContinuationID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.continuationID
}

func (r *activeRun) finishWasRequested() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.finishRequested
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
	mu     sync.Mutex
	record func(NativeSessionMetadata) error
}

// NewCommandAdapter returns a runtime adapter backed by a real local process.
// It is provider-agnostic: provider-specific argv construction belongs to the
// adapters package and runner-specific wrapping belongs to the runner package.
func NewCommandAdapter(config CommandAdapterConfig) Adapter {
	return &commandAdapter{config: config}
}

// CommandAdapterLaunch returns the host launch config for adapters built by
// NewCommandAdapter. Provider-session assembly uses this to start a Task-owned
// host bridge without re-deriving program, workdir, or env.
func CommandAdapterLaunch(adapter Adapter) (CommandAdapterConfig, bool) {
	if adapter == nil {
		return CommandAdapterConfig{}, false
	}
	a, ok := adapter.(*commandAdapter)
	if !ok {
		return CommandAdapterConfig{}, false
	}
	out := CommandAdapterConfig{
		Name:    a.config.Name,
		Program: a.config.Program,
		Args:    append([]string(nil), a.config.Args...),
		Workdir: a.config.Workdir,
	}
	if len(a.config.Env) > 0 {
		out.Env = make(map[string]string, len(a.config.Env))
		for key, value := range a.config.Env {
			out.Env[key] = value
		}
	}
	return out, true
}

func (a *commandAdapter) Name() string {
	if a.config.Name != "" {
		return a.config.Name
	}
	return a.config.Program
}

func (a *commandAdapter) SetMetadataRecorder(record func(NativeSessionMetadata) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record = record
}

func (a *commandAdapter) recordRuntimeLineMetadata(line string) {
	metadata := NativeSessionMetadataFromRuntimeLine(line)
	if metadata.NativeSessionID == "" && metadata.NativeSessionPath == "" && metadata.ContainerID == "" {
		return
	}
	a.mu.Lock()
	record := a.record
	a.mu.Unlock()
	if record != nil {
		_ = record(metadata)
	}
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
	go func() {
		defer wg.Done()
		ScanOutputWithObserver(stdout, "stdout", maxRuntimeOutputLineBytes, a.recordRuntimeLineMetadata, safeEmit)
	}()
	go func() {
		defer wg.Done()
		ScanOutputWithObserver(stderr, "stderr", maxRuntimeOutputLineBytes, a.recordRuntimeLineMetadata, safeEmit)
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("runtime process failed: %w", err)
	}
	return nil
}
