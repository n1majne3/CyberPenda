package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
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
	if !durableActive {
		server.clearRecoveredRuntimeActivity(taskID)
	}
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
	if durableActive {
		if recovered, ok := server.recoveredRuntimeActivity(taskID); ok {
			return recovered
		}
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

func (server *Server) recoveredRuntimeActivity(taskID string) (task.RuntimeActivity, bool) {
	server.runtimeRecoveryMu.RLock()
	defer server.runtimeRecoveryMu.RUnlock()
	activity, ok := server.runtimeRecovery[strings.TrimSpace(taskID)]
	return activity, ok
}

func (server *Server) setRecoveredRuntimeActivity(taskID string, activity task.RuntimeActivity) {
	server.runtimeRecoveryMu.Lock()
	defer server.runtimeRecoveryMu.Unlock()
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	server.runtimeRecovery[taskID] = activity
}

func (server *Server) clearRecoveredRuntimeActivity(taskID string) {
	server.runtimeRecoveryMu.Lock()
	delete(server.runtimeRecovery, strings.TrimSpace(taskID))
	server.runtimeRecoveryMu.Unlock()
}

// recoverProviderSessionOwnership probes and adopts exact pre-existing
// provider sessions during startup. It never calls ProviderSessionFactory.Open,
// Harness.Launch, or any Task/Continuation creation API. The returned live Task
// IDs may be excluded from stale-runtime interruption by the startup
// coordinator; all other outcomes retain their closed Runtime Activity meaning.
func (server *Server) recoverProviderSessionOwnership(ctx context.Context, requests []ProviderSessionRecoveryRequest) ProviderSessionRecoveryReport {
	report := ProviderSessionRecoveryReport{
		LiveOwnerIDs: []string{}, LifecycleProtectedOwnerIDs: []string{}, ReconciliationExcludedOwnerIDs: []string{},
		Outcomes: make([]ProviderSessionRecoveryOutcome, 0, len(requests)),
	}
	recoveryFactory, supported := server.providerSessionFactory.(ProviderSessionRecoveryFactory)
	for _, request := range requests {
		outcome := ProviderSessionRecoveryOutcome{Owner: request.Owner, OwnerID: strings.TrimSpace(request.Owner.ID)}
		if !supported {
			server.orphanProviderSessionRecovery(request.Owner, &outcome, "runtime ownership recovery is unsupported")
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		if err := server.validateProviderSessionRecoveryRequest(request); err != nil {
			server.orphanProviderSessionRecovery(request.Owner, &outcome, "runtime ownership recovery request is invalid")
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		result, err := recoveryFactory.Recover(ctx, request)
		if err != nil {
			server.orphanProviderSessionRecovery(request.Owner, &outcome, "runtime ownership could not be proven")
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		outcome.Liveness = result.Liveness
		if result.Liveness == ProviderSessionRecoveryLive {
			if err := server.validateAndBindRecoveredProviderSession(request, result.Binding); err != nil {
				server.orphanProviderSessionRecovery(request.Owner, &outcome, "runtime ownership could not be proven")
				report.Outcomes = append(report.Outcomes, outcome)
				continue
			}
			if request.Owner.IsTask() {
				server.clearRecoveredRuntimeActivity(outcome.OwnerID)
			}
			outcome.Adopted = true
			outcome.Activity = server.recoveredOwnerActivity(request.Owner)
			report.LiveOwnerIDs = append(report.LiveOwnerIDs, outcome.OwnerID)
			report.LifecycleProtectedOwnerIDs = append(report.LifecycleProtectedOwnerIDs, outcome.OwnerID)
			report.ReconciliationExcludedOwnerIDs = append(report.ReconciliationExcludedOwnerIDs, outcome.OwnerID)
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		if !validProviderSessionRecoveryLiveness(result.Liveness) || result.Binding.Session != nil || result.Binding.Adapter != nil {
			server.orphanProviderSessionRecovery(request.Owner, &outcome, "runtime ownership recovery result is invalid")
			report.Outcomes = append(report.Outcomes, outcome)
			continue
		}
		activity := recoveryRuntimeActivity(result.Liveness)
		outcome.Warning = activity.Warning
		outcome.Activity = activity
		if request.Owner.IsTask() {
			server.setRecoveredRuntimeActivity(outcome.OwnerID, activity)
		}
		if result.Liveness == ProviderSessionRecoveryUnknown {
			report.LifecycleProtectedOwnerIDs = append(report.LifecycleProtectedOwnerIDs, outcome.OwnerID)
			report.ReconciliationExcludedOwnerIDs = append(report.ReconciliationExcludedOwnerIDs, outcome.OwnerID)
		} else if result.Liveness == ProviderSessionRecoveryOffline {
			report.ReconciliationExcludedOwnerIDs = append(report.ReconciliationExcludedOwnerIDs, outcome.OwnerID)
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	return report
}

func (server *Server) validateProviderSessionRecoveryRequest(request ProviderSessionRecoveryRequest) error {
	if request.Owner.Validate() != nil || strings.TrimSpace(request.Continuation.ID) == "" ||
		request.Continuation.OwnerID != request.Owner.ID || strings.TrimSpace(request.ReceiptID) == "" ||
		strings.TrimSpace(request.SourceSessionID) == "" ||
		strings.TrimSpace(request.SourceRequestID) == "" {
		return fmt.Errorf("recovery identity is incomplete")
	}
	if strings.TrimSpace(request.ContainerID) != strings.TrimSpace(request.Continuation.ContainerID) ||
		strings.TrimSpace(request.NativeSessionID) != strings.TrimSpace(request.Continuation.NativeSessionID) ||
		strings.TrimSpace(request.NativeSessionPath) != strings.TrimSpace(request.Continuation.NativeSessionPath) {
		return fmt.Errorf("recovery metadata does not match Continuation")
	}
	if request.Owner.IsTask() {
		storedTask, err := server.tasks.Get(request.Owner.ID)
		if err != nil || !durableTaskActive(storedTask.Status) {
			return fmt.Errorf("recovery Task is stale")
		}
		storedContinuation, err := server.tasks.Continuation(request.Continuation.ID)
		if err != nil || !recoveryContinuationMetadataMatches(request, ownerContinuationFromTask(storedContinuation)) {
			return fmt.Errorf("recovery Continuation is stale")
		}
		active, err := server.tasks.ActiveContinuation(request.Owner.ID)
		if err != nil || active == nil || active.ID != request.Continuation.ID {
			return fmt.Errorf("recovery Continuation is not the active pin")
		}
		return nil
	}
	storedSession, err := server.sessions.Get(request.Owner.ID)
	if err != nil || storedSession.Lifecycle != session.LifecycleOpen {
		return fmt.Errorf("recovery Session is stale")
	}
	storedContinuation, err := server.sessions.Continuation(request.Continuation.ID)
	if err != nil || !recoveryContinuationMetadataMatches(request, ownerContinuationFromSession(storedContinuation)) {
		return fmt.Errorf("recovery Continuation is stale")
	}
	active, err := server.sessions.ActiveContinuation(request.Owner.ID)
	if err != nil || active == nil || active.ID != request.Continuation.ID {
		return fmt.Errorf("recovery Continuation is not the active pin")
	}
	return nil
}

func recoveryContinuationMetadataMatches(request ProviderSessionRecoveryRequest, stored owner.Continuation) bool {
	return stored.OwnerID == request.Owner.ID && stored.ContainerID == request.ContainerID &&
		stored.NativeSessionID == request.NativeSessionID && stored.NativeSessionPath == request.NativeSessionPath
}

func validProviderSessionRecoveryLiveness(liveness ProviderSessionRecoveryLiveness) bool {
	return liveness == ProviderSessionRecoveryLive || liveness == ProviderSessionRecoveryOffline ||
		liveness == ProviderSessionRecoveryOrphaned || liveness == ProviderSessionRecoveryUnknown
}

func recoveryRuntimeActivity(liveness ProviderSessionRecoveryLiveness) task.RuntimeActivity {
	activity := task.RuntimeActivity{Liveness: string(liveness)}
	if liveness == ProviderSessionRecoveryUnknown {
		activity.Warning = "runtime health cannot currently be determined"
	}
	return activity
}

func (server *Server) orphanProviderSessionRecovery(found owner.Contract, outcome *ProviderSessionRecoveryOutcome, warning string) {
	outcome.Liveness = ProviderSessionRecoveryOrphaned
	outcome.Adopted = false
	outcome.Warning = warning
	activity := recoveryRuntimeActivity(ProviderSessionRecoveryOrphaned)
	outcome.Activity = activity
	if found.IsTask() {
		stored, err := server.tasks.Get(found.ID)
		if err == nil && durableTaskActive(stored.Status) {
			server.setRecoveredRuntimeActivity(outcome.OwnerID, activity)
		} else {
			server.clearRecoveredRuntimeActivity(outcome.OwnerID)
		}
	}
}

// applyProviderSessionRecoveryLifecycle runs only after generic restart
// reconciliation has enumerated and cleaned orphaned Runtime identities.
// Offline Tasks were temporarily excluded from that pass so their exact active
// Continuation remains available for explicit cleanup before failure. Live and
// unknown outcomes are intentionally lifecycle-neutral.
func (server *Server) applyProviderSessionRecoveryLifecycle(outcomes []ProviderSessionRecoveryOutcome) {
	for _, outcome := range outcomes {
		if !outcome.Owner.IsTask() {
			continue
		}
		if outcome.Liveness == ProviderSessionRecoveryOrphaned {
			server.clearRecoveredRuntimeActivity(outcome.OwnerID)
			continue
		}
		if outcome.Liveness != ProviderSessionRecoveryOffline {
			continue
		}
		found, err := server.tasks.Get(outcome.OwnerID)
		if err != nil || !durableTaskActive(found.Status) {
			server.clearRecoveredRuntimeActivity(outcome.OwnerID)
			continue
		}
		continuation, continuationErr := server.tasks.ActiveContinuation(outcome.OwnerID)
		if continuationErr == nil && continuation != nil {
			server.cleanupStaleContinuationContainer(*continuation)
			_, _ = server.tasks.UpdateContinuationStatus(continuation.ID, task.StatusFailed)
		}
		if _, err := server.tasks.UpdateStatus(outcome.OwnerID, task.StatusFailed); err != nil {
			server.logger.Printf("task reconcile: failed to mark offline Task %s failed: %v", outcome.OwnerID, err)
			continue
		}
		_, _ = server.tasks.AppendEvent(outcome.OwnerID, task.EventKindLifecycle, task.EventPayload{
			"phase": "failed", "reason": "runtime_offline",
		})
		server.settleTaskAcceptedSteering(outcome.OwnerID, owner.SteeringReasonOwnerRuntimeLost, "Task Runtime was lost before the accepted steering dispatched")
		server.clearRecoveredRuntimeActivity(outcome.OwnerID)
	}
}

func (server *Server) validateAndBindRecoveredProviderSession(request ProviderSessionRecoveryRequest, binding ProviderSessionBinding) error {
	if err := validateProviderSessionBinding(binding); err != nil {
		return err
	}
	if binding.Session.SessionID() != strings.TrimSpace(request.SourceSessionID) {
		return fmt.Errorf("recovered provider session identity changed")
	}
	capabilities := binding.Session.Capabilities()
	if !capabilities.PersistentSession || !capabilities.SendTurn || sessionOffline(binding.Session) || sessionHealthUnknown(binding.Session) {
		return fmt.Errorf("recovered provider session is not healthy and controllable")
	}
	if request.Owner.IsSession() {
		return server.BindSessionProviderSession(request.Owner.ID, binding.Session)
	}
	return server.BindProviderSession(request.Owner.ID, binding.Session)
}

func (server *Server) recoveredOwnerActivity(contract owner.Contract) task.RuntimeActivity {
	if contract.IsTask() {
		if found, err := server.tasks.Get(contract.ID); err == nil {
			return server.computeRuntimeActivity(found)
		}
		return task.RuntimeActivity{}
	}
	found, err := server.sessions.Get(contract.ID)
	if err != nil {
		return task.RuntimeActivity{}
	}
	decorated, err := server.decorateSession(found)
	if err != nil {
		return task.RuntimeActivity{}
	}
	return task.RuntimeActivity{
		Liveness: decorated.RuntimeActivity.Liveness, TurnActivity: decorated.RuntimeActivity.TurnActivity,
		Warning: decorated.RuntimeActivity.Warning,
	}
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
	if reporter, ok := session.(runtime.ProviderSessionTurnBusyReporter); ok {
		return reporter.TurnBusy()
	}
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
			server.settleTaskAcceptedSteering(found.ID, owner.SteeringReasonOwnerRuntimeLost, "Task Runtime was lost before the accepted steering dispatched")
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

// runtimeHarnessWaitTimeout is the operator-facing wait budget for harness
// release (Resume, etc.). Tests set server.runtimeStopTimeout to a short value.
func (server *Server) runtimeHarnessWaitTimeout() time.Duration {
	if server != nil && server.runtimeStopTimeout > 0 {
		return server.runtimeStopTimeout
	}
	return 10 * time.Second
}

// waitRuntimeHarnessInactive waits up to runtimeStopTimeout for IsActive to
// clear without cancelling the run.
func (server *Server) waitRuntimeHarnessInactive(taskID string) bool {
	return server.waitHarnessInactive(taskID, server.runtimeHarnessWaitTimeout())
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
	// Poll frequently enough that short test timeouts (tens of ms) still observe release.
	interval := 5 * time.Millisecond
	if timeout < interval {
		interval = timeout / 2
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
	}
	for time.Now().Before(deadline) {
		if !server.harness.IsActive(taskID) {
			return true
		}
		time.Sleep(interval)
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
