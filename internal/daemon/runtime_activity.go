package daemon

import (
	"context"
	"strings"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

const (
	runtimeLivenessLive     = "live"
	runtimeLivenessOffline  = "offline"
	runtimeLivenessOrphaned = "orphaned"
	runtimeLivenessUnknown  = "unknown"

	runtimeTurnBusy = "busy"
	runtimeTurnIdle = "idle"
)

// computeRuntimeActivity derives Runtime Activity solely from current
// daemon-owned process/session health. Stored native session IDs, historical
// events, and elapsed time are never liveness authority.
//
// Durable Task status is used only to classify lack of ownership:
//   - active Task without ownership proof → orphaned
//   - terminal Task without ownership → offline
//
// Persistent provider sessions require a bound healthy session for live.
// Losing persistent session ownership is never reported as live (orphaned
// while durable-active). One-shot runs may be live from harness ownership.
func (server *Server) computeRuntimeActivity(found task.Task) task.RuntimeActivity {
	taskID := strings.TrimSpace(found.ID)
	durableActive := durableTaskActive(found.Status)
	session, bound := server.providerSessions.get(taskID)
	harnessActive := server.harness != nil && server.harness.IsActive(taskID)
	expectsSession := server.taskExpectsProviderSession(found)

	if bound {
		if sessionHealthUnknown(session) {
			return task.RuntimeActivity{
				Liveness: runtimeLivenessUnknown,
				Warning:  "runtime health cannot currently be determined",
			}
		}
		if sessionOffline(session) {
			return task.RuntimeActivity{Liveness: runtimeLivenessOffline}
		}
		turn := runtimeTurnIdle
		if sessionBusy(session) {
			turn = runtimeTurnBusy
		}
		return task.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: turn}
	}

	// Persistent Runtime without a bound session cannot be live.
	if expectsSession {
		if durableActive {
			return task.RuntimeActivity{Liveness: runtimeLivenessOrphaned}
		}
		return task.RuntimeActivity{Liveness: runtimeLivenessOffline}
	}

	// One-shot path: harness ownership is the process health authority.
	if harnessActive {
		return task.RuntimeActivity{Liveness: runtimeLivenessLive, TurnActivity: runtimeTurnIdle}
	}
	if durableActive {
		return task.RuntimeActivity{Liveness: runtimeLivenessOrphaned}
	}
	return task.RuntimeActivity{Liveness: runtimeLivenessOffline}
}

func (server *Server) taskExpectsProviderSession(found task.Task) bool {
	// Without a provider-session factory the daemon only has one-shot adapters;
	// harness ownership remains the live Runtime authority. Persistent ownership
	// (and therefore orphaned-on-loss) applies only when the factory path is armed.
	if server.providerSessionFactory == nil {
		return false
	}
	// If this Task currently has a bound session, it is on the persistent path.
	if _, bound := server.providerSessions.get(found.ID); bound {
		return true
	}
	provider := runtimeprofile.Provider(strings.TrimSpace(found.RuntimeControls.RuntimeProvider))
	if provider == "" && found.ActiveContinuation != nil {
		provider = runtimeprofile.Provider(strings.TrimSpace(found.ActiveContinuation.RuntimeProvider))
	}
	if provider == "" && found.LatestContinuation != nil {
		provider = runtimeprofile.Provider(strings.TrimSpace(found.LatestContinuation.RuntimeProvider))
	}
	if provider == "" {
		profile, err := server.resolveTaskRuntimeProfile(found)
		if err != nil {
			return false
		}
		provider = profile.Provider
	}
	return supportsPersistentProviderSession(found.Runner, provider)
}

func durableTaskActive(status task.Status) bool {
	return status == task.StatusRunning || status == task.StatusPaused
}

func sessionBusy(session runtime.ProviderSession) bool {
	if reporter, ok := session.(interface{ ControlBusy() bool }); ok {
		return reporter.ControlBusy()
	}
	return false
}

func sessionOffline(session runtime.ProviderSession) bool {
	if reporter, ok := session.(interface{ SessionOffline() bool }); ok {
		return reporter.SessionOffline()
	}
	if reporter, ok := session.(interface{ SessionClosed() bool }); ok {
		return reporter.SessionClosed()
	}
	return false
}

func sessionUnexpectedOffline(session runtime.ProviderSession) bool {
	if reporter, ok := session.(interface{ SessionUnexpectedOffline() bool }); ok {
		return reporter.SessionUnexpectedOffline()
	}
	return false
}

func sessionHealthUnknown(session runtime.ProviderSession) bool {
	if reporter, ok := session.(interface{ SessionHealthUnknown() bool }); ok {
		return reporter.SessionHealthUnknown()
	}
	return false
}

func (server *Server) taskControlActive(taskID string) bool {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	return server.activeControls[strings.TrimSpace(taskID)]
}

// reconcileRuntimeActivity applies Task lifecycle consequences of current
// Runtime Activity without creating Runtime Activity audit records.
//   - unexpected offline + active Task → failed, ownership released, bridge cleaned
//   - orphaned + active Task → interrupted
//   - unknown → warning only (no lifecycle mutation)
//   - explicit Close/Stop offline → activity only, no unexpected-exit failure
//
// While a Task control operation (Stop/Resume/...) holds the control lock,
// lifecycle is left to that operator path so mid-Stop polls cannot interrupt
// or fail the Task as orphaned/offline.
func (server *Server) reconcileRuntimeActivity(found task.Task, activity task.RuntimeActivity) (task.Task, task.RuntimeActivity) {
	if !durableTaskActive(found.Status) {
		return found, activity
	}
	if server.taskControlActive(found.ID) {
		return found, activity
	}
	switch activity.Liveness {
	case runtimeLivenessOffline:
		session, bound := server.providerSessions.get(found.ID)
		// Explicit Close/Stop leaves the session offline but not unexpected.
		// Only process/protocol death fails the active Task here.
		if bound && !sessionUnexpectedOffline(session) {
			return found, activity
		}
		_ = server.closeProviderSession(found.ID)
		server.waitForHarnessInactive(found.ID, 2*time.Second)
		refreshed, err := server.tasks.Get(found.ID)
		if err != nil {
			return found, activity
		}
		if durableTaskActive(refreshed.Status) {
			if _, err := server.tasks.UpdateStatus(found.ID, task.StatusFailed); err != nil {
				return found, activity
			}
			if cont, err := server.tasks.ActiveContinuation(found.ID); err == nil && cont != nil {
				_, _ = server.tasks.UpdateContinuationStatus(cont.ID, task.StatusFailed)
			}
			// Task lifecycle only — never a Runtime Activity audit/history record.
			_, _ = server.tasks.AppendEvent(found.ID, task.EventKindLifecycle, task.EventPayload{
				"phase": "failed", "reason": "runtime_offline",
			})
			refreshed, err = server.tasks.Get(found.ID)
			if err != nil {
				return found, activity
			}
		}
		return refreshed, task.RuntimeActivity{Liveness: runtimeLivenessOffline}
	case runtimeLivenessOrphaned:
		_ = server.closeProviderSession(found.ID)
		if _, err := server.tasks.UpdateStatus(found.ID, task.StatusInterrupted); err != nil {
			return found, activity
		}
		if cont, err := server.tasks.ActiveContinuation(found.ID); err == nil && cont != nil {
			_, _ = server.tasks.UpdateContinuationStatus(cont.ID, task.StatusInterrupted)
		}
		_, _ = server.tasks.AppendEvent(found.ID, task.EventKindLifecycle, task.EventPayload{
			"phase": "interrupted", "reason": "runtime_orphaned",
		})
		refreshed, err := server.tasks.Get(found.ID)
		if err != nil {
			return found, activity
		}
		// Keep orphaned visible on Task detail after ownership loss.
		return refreshed, task.RuntimeActivity{Liveness: runtimeLivenessOrphaned}
	default:
		return found, activity
	}
}

func (server *Server) waitForHarnessInactive(taskID string, timeout time.Duration) {
	_ = server.waitHarnessInactive(taskID, timeout)
}

// waitHarnessInactive polls until the harness is inactive or timeout elapses.
// It does not cancel the run (unlike StopAndWait).
func (server *Server) waitHarnessInactive(taskID string, timeout time.Duration) bool {
	if server.harness == nil {
		return true
	}
	if !server.harness.IsActive(taskID) {
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !server.harness.IsActive(taskID) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !server.harness.IsActive(taskID)
}

// ensureRuntimeAbsentBeforeLaunch cleans up or proves absence of a prior
// Runtime before a replacement launch, preventing two live Runtimes per Task.
func (server *Server) ensureRuntimeAbsentBeforeLaunch(taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	if server.harness != nil && server.harness.IsActive(taskID) {
		server.harness.Stop(taskID)
		if ok := server.harness.StopAndWait(taskID, 10*time.Second); !ok {
			return context.DeadlineExceeded
		}
	}
	if err := server.closeProviderSession(taskID); err != nil && err != runtime.ErrProviderSessionClosed {
		return err
	}
	return nil
}

func (server *Server) attachRuntimeActivity(found task.Task) task.Task {
	activity := server.computeRuntimeActivity(found)
	found, activity = server.reconcileRuntimeActivity(found, activity)
	found.RuntimeActivity = activity
	return found
}
