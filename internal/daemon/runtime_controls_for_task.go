package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
	"pentest/internal/timeline"
	"pentest/internal/transcript"
)

func (server *Server) runtimeControlsForTask(found task.Task, latest *task.TaskContinuation) (task.RuntimeControls, error) {
	profile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.RuntimeControls{}, err
	}
	plugin, ok := server.runtimePlugins.Get(string(profile.Provider))
	nativeResumeSupported := ok && plugin.NativeResume.Supported
	active := found.Status == task.StatusRunning || found.Status == task.StatusPaused
	sessionCaptured := latest != nil && strings.TrimSpace(latest.NativeSessionID) != ""
	_, providerSessionBound := server.providerSessions.get(found.ID)

	activity := server.computeRuntimeActivity(found)
	controls := task.RuntimeControls{
		ResumeAvailable:         ok && !active,
		FinishAvailable:         activity.Liveness == runtimeLivenessLive && activity.TurnActivity == runtimeTurnIdle,
		QueueSteerAvailable:     ok,
		NativeSessionCaptured:   sessionCaptured,
		SameRuntimeProviderOnly: true,
		RuntimeProvider:         string(profile.Provider),
	}
	if profile.Provider == runtimeprofile.ProviderPi {
		if ids := server.projectedPiModelProviderIDs(found); len(ids) > 0 {
			controls.ProjectedModelProviderIDs = ids
		}
	}
	if selection, selectionErr := server.currentTurnSelection(found); selectionErr == nil {
		controls.TurnSelection = &task.TurnSelection{
			ModelProviderID: selection.ModelProviderID,
			Model:           selection.Model,
			ReasoningEffort: selection.RequestedReasoningEffort,
		}
	}
	if events, eventsErr := server.tasks.RuntimeControlEvents(found.ID); eventsErr == nil {
		if providerSessionBound {
			controls.ProviderPermissions = providerPermissionRequestsForTask(events)
		}
		for index := len(events) - 1; index >= 0; index-- {
			phase, _ := events[index].Payload["phase"].(string)
			if phase == "started" {
				break
			}
			if phase != "provider_session_recovery_required" {
				continue
			}
			controls.RecoveryState, _ = events[index].Payload["recovery_state"].(string)
			controls.RecoveryReason, _ = events[index].Payload["reason"].(string)
			break
		}
	}
	steeringControl, steeringControlErr := server.latestSteeringControl(owner.KindTask, found.ID)
	if steeringControlErr == nil {
		controls.NativeSteerRequestID = steeringControl.requestID
		controls.NativeSteerState = steeringControl.state
		controls.NativeSteerErrorCode = steeringControl.errorCode
		controls.NativeSteerError = steeringControl.error
	}
	if session, bound := server.providerSessions.get(found.ID); bound {
		if selectedMode, modeErr := nativeSteerModeForSession(session, false); modeErr == nil {
			controls.NativeSteerAvailable = active
			controls.NativeSteerMode = string(selectedMode)
			if steeringControl.nonTerminal {
				controls.NativeSteerAvailable = false
			}
			controls.InterruptSteerAvailable = controls.NativeSteerAvailable
			if !controls.NativeSteerAvailable && active {
				controls.InterruptSteerReason = "native steer request is already in progress"
			}
		} else {
			controls.NativeSteerReason = modeErr.Error()
			controls.InterruptSteerReason = controls.NativeSteerReason
		}
		if controls.NativeSteerState == "" && active {
			controls.NativeSteerState = "idle"
		}
	}
	if nativeResumeSupported {
		controls.NativeResumeAvailable = !active && sessionCaptured
		if !providerSessionBound {
			controls.InterruptSteerAvailable = active && sessionCaptured
		}
	} else {
		controls.NativeResumeReason = fmt.Sprintf("native resume unsupported for provider %s", profile.Provider)
		controls.InterruptSteerReason = controls.NativeResumeReason
	}
	if nativeResumeSupported && !sessionCaptured {
		controls.NativeResumeReason = "native session unavailable"
		controls.InterruptSteerReason = controls.NativeResumeReason
	}
	return controls, nil
}

func providerPermissionRequestsForTask(events []task.Event) []task.ProviderPermissionRequest {
	requests := make(map[string]task.ProviderPermissionRequest)
	for _, event := range events {
		permissionID, _ := event.Payload["permission_request_id"].(string)
		if permissionID == "" {
			continue
		}
		phase, _ := event.Payload["phase"].(string)
		switch phase {
		case "provider_permission_requested":
			request := task.ProviderPermissionRequest{PermissionRequestID: permissionID, CreatedAt: event.CreatedAt}
			request.RequestID, _ = event.Payload["request_id"].(string)
			request.SessionID, _ = event.Payload["session_id"].(string)
			request.ProviderTurnID, _ = event.Payload["provider_turn_id"].(string)
			request.Provider, _ = event.Payload["provider"].(string)
			requests[permissionID] = request
		case "provider_permission_response_applied":
			delete(requests, permissionID)
		}
	}
	result := make([]task.ProviderPermissionRequest, 0, len(requests))
	for _, request := range requests {
		result = append(result, request)
	}
	slices.SortFunc(result, func(left, right task.ProviderPermissionRequest) int {
		return left.CreatedAt.Compare(right.CreatedAt)
	})
	return result
}

func (server *Server) handleTaskEvents(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	events, err := server.tasks.Events(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	if events == nil {
		events = []task.Event{}
	}
	writeJSON(response, http.StatusOK, struct {
		Events []task.Event `json:"events"`
	}{
		Events: events,
	})
}

func (server *Server) handleTaskTimeline(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	page, err := server.taskOwnerHistory(found).TimelinePage(parseHistoryRequest(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	writeJSON(response, http.StatusOK, struct {
		TaskID   string          `json:"task_id"`
		Items    []timeline.Item `json:"items"`
		Cursor   int             `json:"cursor"`
		HasOlder bool            `json:"has_older"`
	}{
		TaskID:   found.ID,
		Items:    page.items,
		Cursor:   page.cursor,
		HasOlder: page.hasOlder,
	})
}

func (server *Server) handleTaskTimelineItem(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	item, foundItem, err := server.taskOwnerHistory(found).TimelineItem(request.PathValue("seq"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	if !foundItem {
		writeError(response, http.StatusNotFound, "timeline item not found")
		return
	}
	writeJSON(response, http.StatusOK, item)
}

func (server *Server) handleTaskTranscript(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}

	page, err := server.taskOwnerHistory(found).TranscriptPage(parseHistoryRequest(request))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	writeJSON(response, http.StatusOK, struct {
		TaskID   string             `json:"task_id"`
		Entries  []transcript.Entry `json:"entries"`
		Cursor   int                `json:"cursor"`
		Before   int                `json:"before"`
		HasOlder bool               `json:"has_older"`
	}{
		TaskID:   found.ID,
		Entries:  page.items,
		Cursor:   page.cursor,
		HasOlder: page.hasOlder,
		Before:   page.before,
	})
}

// handleTaskTranscriptEntry returns one complete retained transcript entry by
// ID, including the full payload that the history window preview truncated.
func (server *Server) handleTaskTranscriptEntry(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	entry, foundEntry, err := server.taskOwnerHistory(found).TranscriptEntry(request.PathValue("entry_id"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list task events")
		return
	}
	if !foundEntry {
		writeError(response, http.StatusNotFound, "transcript entry not found")
		return
	}
	writeJSON(response, http.StatusOK, entry)
}

func (server *Server) handleStopTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	deadline := time.Now().Add(server.runtimeStopTimeout)
	stopContext, cancelStop := context.WithDeadline(context.Background(), deadline)
	defer cancelStop()
	if !server.acquireTaskControl(taskID) {
		// Stop is the preemptive control. Cancel every queued or active provider
		// operation for this Task, then take ownership after it unwinds.
		if !server.cancelProviderTaskControls(taskID) {
			writeError(response, http.StatusConflict, "task control operation already active")
			return
		}
		if server.harness != nil {
			server.harness.Stop(taskID)
		}
		if !server.waitAcquireTaskControl(stopContext, taskID) {
			writeError(response, http.StatusConflict, "task control operation did not stop in time")
			return
		}
	} else {
		// A provider operation may be queued with no active owner yet. Cancel it
		// after Stop acquires the Task boundary so it cannot dispatch once Stop
		// releases the boundary.
		server.cancelProviderTaskControls(taskID)
	}
	defer server.releaseTaskControl(taskID)

	// Re-read under control lock so concurrent Finish/resume cannot race settle.
	found, err = server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}

	if found.Status == task.StatusRunning || found.Status == task.StatusPaused {
		if server.harness != nil {
			server.harness.Stop(taskID)
		}
		if err := server.closeProviderSessionForStop(stopContext, taskID); err != nil {
			writeError(response, http.StatusConflict, "provider session did not close")
			return
		}
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		if server.harness != nil && server.harness.IsActive(taskID) {
			if ok := server.harness.StopAndWait(taskID, remaining); !ok {
				writeError(response, http.StatusConflict, "runtime did not stop in time")
				return
			}
		}
		// Durable Task may still be running when harness/session already gone
		// (finish abort, orphan cleanup). Always settle stopped after cleanup.
		if err := server.settleTaskStopped(taskID); err != nil {
			writeTaskError(response, err)
			return
		}
		server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerStopped, "Task stopped with queued accepted steering")
		stopped, err := server.taskDetail(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, stopped)
		return
	}
	if err := server.closeProviderSession(taskID); err != nil && !errors.Is(err, runtime.ErrProviderSessionClosed) {
		writeError(response, http.StatusConflict, "provider session did not close")
		return
	}
	if err := server.settleTaskStopped(taskID); err != nil {
		writeTaskError(response, err)
		return
	}
	server.settleTaskAcceptedSteering(taskID, owner.SteeringReasonOwnerStopped, "Task stopped with queued accepted steering")
	stopped, err := server.taskDetail(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, stopped)
}

// markStoppedBlackboardConclusionsRecoveryRequired closes the durable semantic
// coordinator after Stop has canceled every provider control. A later Resume
// therefore sees explicit operator-recoverable debt instead of waiting forever
// on a Conclude Turn whose provider session was intentionally closed.
// settleTaskStopped marks Task and any non-terminal Continuation as stopped.
// Does not overwrite an existing different terminal Continuation status.
func (server *Server) settleTaskStopped(taskID string) error {
	if cont, err := server.tasks.LatestContinuation(taskID); err == nil && cont != nil {
		if cont.Status == task.StatusRunning || cont.Status == task.StatusPaused || cont.Status == task.StatusPending {
			if _, err := server.tasks.UpdateContinuationStatus(cont.ID, task.StatusStopped); err != nil && !errors.Is(err, task.ErrContinuationStatusConflict) {
				return err
			}
		}
	}
	found, err := server.tasks.Get(taskID)
	if err != nil {
		return err
	}
	if found.Status == task.StatusRunning || found.Status == task.StatusPaused || found.Status == task.StatusPending {
		if _, err := server.tasks.UpdateStatus(taskID, task.StatusStopped); err != nil {
			return err
		}
	}
	return nil
}
