package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
	"pentest/internal/task"
)

// reconcileInterruptedSessions closes the in-memory ownership gap after a
// daemon restart. Provider bridges are intentionally not reopened implicitly;
// Sandbox containers and Host process groups stop before their Continuations become interrupted.
// A cleanup failure leaves the Continuation active for the next startup retry.
// Every recovered open Session continuation is marked interrupted and the next
// user message/resume request creates a fresh continuation while retaining the
// Session identity and prior native-runtime metadata.
func (server *Server) reconcileInterruptedSessions(excludedIDs ...[]string) error {
	if server.sessions == nil {
		return nil
	}
	excluded := map[string]bool{}
	if len(excludedIDs) > 0 {
		for _, id := range excludedIDs[0] {
			excluded[id] = true
		}
	}
	open, err := server.sessions.List(session.LifecycleOpen)
	if err != nil {
		return fmt.Errorf("Session recovery: list open Sessions: %w", err)
	}
	for _, found := range open {
		if excluded[found.ID] {
			continue
		}
		active, activeErr := server.sessions.ActiveContinuation(found.ID)
		if activeErr != nil {
			return fmt.Errorf("Session recovery: inspect Session %s: %w", found.ID, activeErr)
		}
		if active == nil {
			continue
		}
		if active.Runner == session.RunnerHost {
			if pgid, ok := runtime.ParseHostProcessGroupID(active.ContainerID); ok {
				if err := runtime.KillHostProcessGroup(context.Background(), pgid); err != nil {
					return fmt.Errorf("Session recovery: stop Host process group %d for Session %s: %w", pgid, found.ID, err)
				}
			}
		}
		if active.Runner == session.RunnerSandbox && strings.TrimSpace(active.ContainerID) != "" {
			if err := runtime.StopDockerContainer(server.containerCLI, active.ContainerID, 2*time.Second); err != nil {
				return fmt.Errorf("Session recovery: stop container %s for Session %s: %w", active.ContainerID, found.ID, err)
			}
			if err := runtime.RemoveDockerContainer(server.containerCLI, active.ContainerID); err != nil {
				return fmt.Errorf("Session recovery: remove container %s for Session %s: %w", active.ContainerID, found.ID, err)
			}
		}
		if _, statusErr := server.sessions.UpdateContinuationStatus(active.ID, session.RuntimeStatusInterrupted); statusErr != nil {
			return fmt.Errorf("Session recovery: interrupt Continuation %s: %w", active.ID, statusErr)
		}
		payload := session.EventPayload{
			"phase":           "provider_session_recovery_required",
			"reason":          "daemon_restart",
			"recovery_state":  "failed_closed",
			"next_action":     "resume_creates_fresh_continuation",
			"continuation_id": active.ID,
		}
		if active.NativeSessionID != "" {
			payload["native_session_id"] = active.NativeSessionID
		}
		if active.NativeSessionPath != "" {
			payload["native_session_path"] = active.NativeSessionPath
		}
		_, _ = server.sessions.AppendEvent(found.ID, session.EventKindLifecycle, payload)
	}
	return nil
}

func resolveRuntimeStorage(config Config) (string, string, error) {
	runtimeRoot := strings.TrimSpace(config.RuntimeRoot)
	if runtimeRoot == "" {
		runtimeRoot = filepath.Join(filepath.Dir(config.DBPath), "runs")
	}
	runtimeRoot = filepath.Clean(runtimeRoot)
	if strings.TrimSpace(config.TaskVolume) == "" {
		return runtimeRoot, "", nil
	}

	taskVolumeRoot := strings.TrimSpace(config.TaskVolumeRoot)
	if taskVolumeRoot == "" {
		taskVolumeRoot = "/data"
	}
	taskVolumeRoot = filepath.Clean(taskVolumeRoot)
	absRuntimeRoot, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve runtime root: %w", err)
	}
	absTaskVolumeRoot, err := filepath.Abs(taskVolumeRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve task volume root: %w", err)
	}
	relativeRuntimeRoot, err := filepath.Rel(absTaskVolumeRoot, absRuntimeRoot)
	if err != nil || relativeRuntimeRoot == ".." || strings.HasPrefix(relativeRuntimeRoot, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("runtime root %q is outside task volume root %q", runtimeRoot, taskVolumeRoot)
	}
	return runtimeRoot, taskVolumeRoot, nil
}

func sessionRoot(config Config, runtimeRoot string) string {
	if configured := strings.TrimSpace(config.SessionRoot); configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(runtimeRoot, "sessions")
}

// reconcileInterruptedTasks clears ghost tasks left running by a previous
// daemon instance. The harness tracks active runs in memory, so after a
// restart no task can actually be executing; mark them interrupted and emit a
// lifecycle event so the timeline and logs explain the gap. Failures are
// logged but never block startup.
func (server *Server) reconcileInterruptedTasks(lifecycleProtectedTaskIDs []string) {
	reconciled, err := server.tasks.ReconcileInterruptedStateExcept(lifecycleProtectedTaskIDs)
	if err != nil {
		server.logger.Printf("task reconcile: failed to interrupt stale tasks: %v", err)
		return
	}
	for _, continuation := range reconciled.Continuations {
		server.cleanupStaleContinuationContainer(continuation)
	}
	for _, t := range reconciled.Tasks {
		_, _ = server.tasks.AppendEvent(t.ID, task.EventKindLifecycle, task.EventPayload{
			"phase":  "interrupted",
			"reason": "daemon_restart",
		})
		// The old in-memory bridge cannot be safely reopened after daemon
		// restart. Keep durable provider metadata on the interrupted
		// Continuation and make the required explicit recovery state visible;
		// Resume will create the fresh Continuation pin.
		latest, latestErr := server.tasks.LatestContinuation(t.ID)
		if latestErr == nil && latest != nil && supportedProviderSessionFactoryProvider(runtimeprofile.Provider(latest.RuntimeProvider)) {
			recovery := task.EventPayload{
				"phase": "provider_session_recovery_required", "reason": "daemon_restart",
				"recovery_state": "failed_closed", "next_action": "resume_creates_fresh_continuation",
			}
			if latest.NativeSessionID != "" {
				recovery["native_session_id"] = latest.NativeSessionID
			}
			if latest.NativeSessionPath != "" {
				recovery["native_session_path"] = latest.NativeSessionPath
			}
			_, _ = server.tasks.AppendEvent(t.ID, task.EventKindLifecycle, recovery)
		}
		server.logTask(t, "interrupted", "daemon restart orphaned this task")
	}
	if len(reconciled.Tasks) > 0 {
		server.logger.Printf("task reconcile: %d task(s) interrupted on daemon restart", len(reconciled.Tasks))
	}
	if server.blackboardV2 != nil {
		continuations, listErr := server.tasks.TerminalContinuations()
		if listErr != nil {
			server.logger.Printf("task reconcile: failed to list terminal Continuations: %v", listErr)
			return
		}
		for _, continuation := range continuations {
			owner, ownerErr := server.tasks.Get(continuation.TaskID)
			if ownerErr != nil {
				server.logger.Printf("task reconcile: failed to load Task %s for terminal Continuation %s: %v", continuation.TaskID, continuation.ID, ownerErr)
				continue
			}
			if owner.RunControls.BlackboardMode == task.BlackboardModeDisabled {
				continue
			}
			if reconcileErr := server.blackboardV2.ReconcileTerminalContinuation(context.Background(), continuation.ID, "daemon_restart"); reconcileErr != nil {
				server.logger.Printf("task reconcile: failed to reconcile Continuation %s: %v", continuation.ID, reconcileErr)
			}
		}
	}
}

func (server *Server) cleanupStaleContinuationContainer(continuation task.TaskContinuation) {
	identity := strings.TrimSpace(continuation.ContainerID)
	if identity == "" {
		return
	}
	if continuation.Runner == task.RunnerHost {
		pgid, ok := runtime.ParseHostProcessGroupID(identity)
		if !ok {
			return
		}
		if err := runtime.KillHostProcessGroup(context.Background(), pgid); err != nil {
			server.logger.Printf("task reconcile: failed to kill host process group %d for task %s: %v", pgid, continuation.TaskID, err)
			return
		}
		_, _ = server.tasks.AppendEvent(continuation.TaskID, task.EventKindLifecycle, task.EventPayload{
			"phase":  "host_process_group_cleaned",
			"reason": "daemon_restart",
			"pgid":   pgid,
		})
		return
	}
	if continuation.Runner != task.RunnerSandbox {
		return
	}
	containerID := identity
	if err := runtime.StopDockerContainer(server.containerCLI, containerID, 2*time.Second); err != nil {
		server.logger.Printf("task reconcile: failed to stop stale container %s for task %s: %v", containerID, continuation.TaskID, err)
		return
	}
	if err := runtime.RemoveDockerContainer(server.containerCLI, containerID); err != nil {
		server.logger.Printf("task reconcile: failed to remove stale container %s for task %s: %v", containerID, continuation.TaskID, err)
		return
	}
	_, _ = server.tasks.AppendEvent(continuation.TaskID, task.EventKindLifecycle, task.EventPayload{
		"phase":        "container_cleaned",
		"reason":       "daemon_restart",
		"container_id": containerID,
	})
}
