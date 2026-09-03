package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"pentest/internal/task"
)

// ErrHarnessShutdown is the parent-context cause used when a daemon closes.
// It stops in-memory Runtime ownership without writing a user-requested Stop
// into durable Task or Session lifecycle state.
var ErrHarnessShutdown = fmt.Errorf("Runtime Harness shutdown: %w", context.Canceled)

// OwnerHarnessConfig supplies the owner-specific persistence callbacks for the
// shared Runtime Harness. Task and Session use the same lifecycle, stop, and
// continuation-control machinery; only their durable owner projections differ.
type OwnerHarnessConfig struct {
	VerifyOwner                       func(string) error
	AppendEvent                       func(string, string, task.EventKind, task.EventPayload) error
	MarkRunning                       func(string, string) error
	UpdateContinuationStatus          func(string, string) error
	UpdateContinuationRuntimeMetadata func(string, string, string, string) error
	Finalize                          func(string, string, string) error
}

// OwnerLaunchRequest is the owner-neutral launch input shared by Task and
// Non-Project Session Runtime Harnesses.
type OwnerLaunchRequest struct {
	OwnerID          string
	Goal             string
	Adapter          Adapter
	ContinuationID   string
	Metadata         func() (NativeSessionMetadata, error)
	StopConfirmation StopConfirmation
}

type ownerActiveRun struct {
	mu                sync.RWMutex
	cancel            context.CancelFunc
	done              chan struct{}
	stopConfirmation  StopConfirmation
	continuationID    string
	finishRequested   bool
	shutdownRequested bool
}

// OwnerHarness owns process lifecycle and continuation control for one
// aggregate owner. It intentionally knows nothing about Projects, Tasks, or
// Sessions; those semantics are supplied by OwnerHarnessConfig.
type OwnerHarness struct {
	config OwnerHarnessConfig
	mu     sync.Mutex
	active map[string]*ownerActiveRun
}

func NewOwnerHarness(config OwnerHarnessConfig) *OwnerHarness {
	return &OwnerHarness{config: config, active: map[string]*ownerActiveRun{}}
}

func (h *OwnerHarness) Launch(ctx context.Context, req OwnerLaunchRequest) error {
	if h == nil {
		return fmt.Errorf("Runtime Harness is unavailable")
	}
	if req.Adapter == nil {
		return fmt.Errorf("Runtime launch requires an adapter")
	}
	if h.config.VerifyOwner == nil {
		return fmt.Errorf("Runtime Harness owner contract is unavailable")
	}
	if err := h.config.VerifyOwner(req.OwnerID); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	run := h.register(req.OwnerID, cancel, req.ContinuationID, req.StopConfirmation)
	defer func() {
		// Release the active-owner slot before notifying waiters. A waiter may
		// launch the replacement Runtime as soon as done closes; the old run must
		// not remain visible or later delete that replacement from the registry.
		h.unregister(req.OwnerID, run)
		close(run.done)
	}()

	emit := func(kind task.EventKind, payload task.EventPayload) {
		if h.config.AppendEvent == nil {
			return
		}
		_ = h.config.AppendEvent(req.OwnerID, run.currentContinuationID(), kind, payload)
	}
	emit(task.EventKindLifecycle, task.EventPayload{"phase": "started", "adapter": req.Adapter.Name()})
	if h.config.MarkRunning != nil {
		if err := h.config.MarkRunning(req.OwnerID, req.ContinuationID); err != nil {
			return fmt.Errorf("mark Runtime running: %w", err)
		}
	}
	if recorder, ok := req.Adapter.(metadataRecordingAdapter); ok && h.config.UpdateContinuationRuntimeMetadata != nil {
		recorder.SetMetadataRecorder(func(metadata NativeSessionMetadata) error {
			continuationID := run.currentContinuationID()
			if continuationID == "" || (metadata.ContainerID == "" && metadata.NativeSessionID == "" && metadata.NativeSessionPath == "") {
				return nil
			}
			return h.config.UpdateContinuationRuntimeMetadata(continuationID, metadata.ContainerID, metadata.NativeSessionID, metadata.NativeSessionPath)
		})
	}

	runErr := req.Adapter.Run(ctx, req.Goal, emit)
	continuationID := run.currentContinuationID()
	if continuationID != "" && req.Metadata != nil && h.config.UpdateContinuationRuntimeMetadata != nil {
		if metadata, err := req.Metadata(); err == nil && (metadata.ContainerID != "" || metadata.NativeSessionID != "" || metadata.NativeSessionPath != "") {
			if err := h.config.UpdateContinuationRuntimeMetadata(continuationID, metadata.ContainerID, metadata.NativeSessionID, metadata.NativeSessionPath); err != nil {
				return fmt.Errorf("record Runtime continuation metadata: %w", err)
			}
		}
	}
	if run.shutdownWasRequested() || errors.Is(context.Cause(ctx), ErrHarnessShutdown) {
		return ErrHarnessShutdown
	}

	if run.finishWasRequested() {
		emit(task.EventKindLifecycle, task.EventPayload{"phase": "finish_shutdown", "adapter": req.Adapter.Name()})
		return nil
	}

	status, phase := "completed", "completed"
	if runErr != nil {
		status, phase = "failed", "failed"
	}
	if ctx.Err() != nil {
		status, phase = "stopped", "stopped"
	}
	emit(task.EventKindLifecycle, task.EventPayload{"phase": phase, "adapter": req.Adapter.Name()})
	if h.config.Finalize != nil {
		if err := h.config.Finalize(req.OwnerID, continuationID, status); err != nil {
			return fmt.Errorf("mark Runtime %s: %w", status, err)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return runErr
}

func (h *OwnerHarness) register(ownerID string, cancel context.CancelFunc, continuationID string, stopConfirmation StopConfirmation) *ownerActiveRun {
	run := &ownerActiveRun{cancel: cancel, done: make(chan struct{}), stopConfirmation: stopConfirmation, continuationID: continuationID}
	h.mu.Lock()
	h.active[ownerID] = run
	h.mu.Unlock()
	return run
}

func (h *OwnerHarness) unregister(ownerID string, run *ownerActiveRun) {
	h.mu.Lock()
	if h.active[ownerID] == run {
		delete(h.active, ownerID)
	}
	h.mu.Unlock()
}

func (h *OwnerHarness) Stop(ownerID string) {
	h.mu.Lock()
	run := h.active[ownerID]
	h.mu.Unlock()
	if run != nil {
		run.cancel()
	}
}

func (h *OwnerHarness) MarkFinishRequested(ownerID string) {
	h.mu.Lock()
	run := h.active[ownerID]
	h.mu.Unlock()
	if run == nil {
		return
	}
	run.mu.Lock()
	run.finishRequested = true
	run.mu.Unlock()
}

func (h *OwnerHarness) ClearFinishIntent(ownerID string) {
	h.mu.Lock()
	run := h.active[ownerID]
	h.mu.Unlock()
	if run == nil {
		return
	}
	run.mu.Lock()
	run.finishRequested = false
	run.mu.Unlock()
}

func (h *OwnerHarness) FinishIntentActive(ownerID string) bool {
	h.mu.Lock()
	run := h.active[ownerID]
	h.mu.Unlock()
	return run != nil && run.finishWasRequested()
}

func (h *OwnerHarness) IsActive(ownerID string) bool {
	h.mu.Lock()
	_, ok := h.active[ownerID]
	h.mu.Unlock()
	return ok
}

func (h *OwnerHarness) StopAndWait(ownerID string, timeout time.Duration) bool {
	h.mu.Lock()
	run := h.active[ownerID]
	h.mu.Unlock()
	if run == nil {
		return true
	}
	run.cancel()
	return waitForOwnerRun(run, time.Now().Add(timeout))
}

// ShutdownAllAndWait cancels every Runtime active at the start of the call and
// waits for each Harness run to release its owner without finalizing durable
// lifecycle state. The next daemon reconciles those owners as interrupted.
// New launches must be blocked by the caller before this method starts.
func (h *OwnerHarness) ShutdownAllAndWait(timeout time.Duration) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	runs := make([]*ownerActiveRun, 0, len(h.active))
	for _, run := range h.active {
		runs = append(runs, run)
	}
	h.mu.Unlock()

	for _, run := range runs {
		run.requestShutdown()
		run.cancel()
	}
	deadline := time.Now().Add(timeout)
	for _, run := range runs {
		if !waitForOwnerRunRelease(run, deadline) {
			return false
		}
	}
	return true
}

func waitForOwnerRun(run *ownerActiveRun, deadline time.Time) bool {
	if !waitForOwnerRunRelease(run, deadline) {
		return false
	}
	return confirmOwnerRunStopped(run, deadline)
}

func waitForOwnerRunRelease(run *ownerActiveRun, deadline time.Time) bool {
	select {
	case <-run.done:
		return true
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-run.done:
		return true
	case <-timer.C:
		return false
	}
}

func confirmOwnerRunStopped(run *ownerActiveRun, deadline time.Time) bool {
	if run.stopConfirmation == nil {
		return true
	}
	remaining := time.Until(deadline)
	return remaining > 0 && run.stopConfirmation(remaining) == nil
}

func (h *OwnerHarness) RebindContinuation(ownerID, continuationID string) error {
	h.mu.Lock()
	run := h.active[ownerID]
	h.mu.Unlock()
	if run == nil || continuationID == "" {
		return fmt.Errorf("active Runtime continuation is unavailable")
	}
	run.mu.Lock()
	run.continuationID = continuationID
	run.mu.Unlock()
	return nil
}

func (run *ownerActiveRun) currentContinuationID() string {
	run.mu.RLock()
	defer run.mu.RUnlock()
	return run.continuationID
}

func (run *ownerActiveRun) finishWasRequested() bool {
	run.mu.RLock()
	defer run.mu.RUnlock()
	return run.finishRequested
}

func (run *ownerActiveRun) requestShutdown() {
	run.mu.Lock()
	run.shutdownRequested = true
	run.mu.Unlock()
}

func (run *ownerActiveRun) shutdownWasRequested() bool {
	run.mu.RLock()
	defer run.mu.RUnlock()
	return run.shutdownRequested
}
