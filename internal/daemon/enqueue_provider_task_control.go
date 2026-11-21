package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/blackboardv2"

	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/store"
	"pentest/internal/task"
)

// providerControlSettlement is evaluated before and after a provider-control
// owner is acquired. The first pass may wait for Working Graph settlement;
// the second pass must not block while the control boundary is held.
type providerControlSettlement func(context.Context, bool) (bool, error)

func (server *Server) enqueueProviderTaskControlAfterSettlement(ownerID string, settlement providerControlSettlement, operation func(context.Context)) bool {
	return server.enqueueProviderTaskControlWithSettlement(ownerID, false, settlement, operation)
}

func (server *Server) enqueueProviderTaskControlWithSettlement(ownerID string, requireExisting bool, settlement providerControlSettlement, operation func(context.Context)) bool {
	server.controlMu.Lock()
	if server.closing {
		server.controlMu.Unlock()
		return false
	}
	taskCtx := server.providerTaskContexts[ownerID]
	if requireExisting && (taskCtx == nil || taskCtx.Err() != nil) {
		server.controlMu.Unlock()
		return false
	}
	if taskCtx == nil || taskCtx.Err() != nil {
		taskCtx = server.providerTaskContextLocked(ownerID)
	}
	server.queuedProviderControls[ownerID]++
	server.providerControlWG.Add(1)
	server.controlMu.Unlock()

	go func() {
		ownsControl := false
		queued := true
		defer func() {
			server.controlMu.Lock()
			if queued {
				server.decrementQueuedProviderControlLocked(ownerID)
			}
			if ownsControl {
				delete(server.activeControls, ownerID)
				delete(server.activeProviderControls, ownerID)
			}
			server.controlMu.Unlock()
			server.providerControlWG.Done()
		}()
		for {
			if settlement != nil {
				settled, err := settlement(taskCtx, true)
				if err != nil || !settled {
					return
				}
			}
			server.controlMu.Lock()
			if server.closing || taskCtx.Err() != nil {
				server.controlMu.Unlock()
				return
			}
			if !server.activeControls[ownerID] {
				server.activeControls[ownerID] = true
				server.activeProviderControls[ownerID] = true
				server.decrementQueuedProviderControlLocked(ownerID)
				queued = false
				ownsControl = true
				server.controlMu.Unlock()
				if settlement != nil {
					settled, err := settlement(taskCtx, false)
					if err != nil || !settled {
						server.controlMu.Lock()
						delete(server.activeControls, ownerID)
						delete(server.activeProviderControls, ownerID)
						server.queuedProviderControls[ownerID]++
						queued = true
						ownsControl = false
						server.controlMu.Unlock()
						if err != nil {
							return
						}
						continue
					}
				}
				break
			}
			server.controlMu.Unlock()

			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-taskCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		operation(taskCtx)
	}()
	return true
}

func (server *Server) releaseProviderTaskControl(taskID string) {
	server.controlMu.Lock()
	delete(server.activeControls, taskID)
	delete(server.activeProviderControls, taskID)
	server.controlMu.Unlock()
	server.providerControlWG.Done()
}

func (server *Server) releaseTaskControl(taskID string) {
	server.controlMu.Lock()
	defer server.controlMu.Unlock()
	delete(server.activeControls, taskID)
}

func (server *Server) handleResumeTask(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	taskID := request.PathValue("task_id")
	if !server.requireProject(response, projectID) {
		return
	}
	if taskID == "" {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	var input taskContinuationSelectionInput
	if err := decodeOptionalJSON(request, &input); err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(input.RuntimeProfileID) != "" {
		writeError(response, http.StatusBadRequest, "runtime_profile_locked")
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
	if durableTaskActive(found.Status) {
		writeError(response, http.StatusConflict, "task is already running")
		return
	}
	// Terminal Tasks may still show a brief harness window while Launch releases
	// ownership. Wait for absence rather than rejecting a legitimate resume.
	// Budget is server.runtimeStopTimeout (not a hardcoded constant).
	if server.harness != nil && server.harness.IsActive(taskID) {
		if !server.waitRuntimeHarnessInactive(taskID) {
			writeError(response, http.StatusConflict, "runtime harness is still active")
			return
		}
	}
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)
	// Re-validate under the control lock: a concurrent resume may have already
	// launched. Using stale terminal Task state here would call
	// ensureRuntimeAbsentBeforeLaunch and stop the first Runtime.
	found, err = server.tasks.Get(taskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	if found.ProjectID != projectID {
		writeError(response, http.StatusNotFound, "task not found")
		return
	}
	if durableTaskActive(found.Status) {
		writeError(response, http.StatusConflict, "task is already running")
		return
	}
	if server.harness != nil && server.harness.IsActive(taskID) {
		if !server.waitRuntimeHarnessInactive(taskID) {
			writeError(response, http.StatusConflict, "runtime harness is still active")
			return
		}
	}
	if _, bound := server.providerSessions.get(taskID); bound {
		writeError(response, http.StatusConflict, "task already has a live Runtime")
		return
	}
	if input.hasSelection() {
		if _, ok := server.recordSelectedRuntimeConfig(response, found, "", input); !ok {
			return
		}
		refreshed, err := server.tasks.Get(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		found = refreshed
	}
	found, resumeGoal, plan, err := server.prepareResumeContinuation(found, "")
	if err != nil {
		server.writeResumePreparationError(response, err)
		return
	}
	if err := server.launchTaskInBackground(found, plan, resumeGoal); err != nil {
		writeTaskLaunchError(response, err)
		return
	}

	updated, err := server.taskDetail(found.ID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, updated)
}

func (server *Server) prepareNativeResumeContinuation(found task.Task, resumedMessage string) (task.Task, string, taskLaunchPlan, error) {
	found, resumedMessage, nativeResume, err := server.prepareNativeResumeRequest(found, resumedMessage)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	var steeringEventIDs []string
	if found.RunControls.BlackboardMode == task.BlackboardModeDisabled {
		resumedMessage, steeringEventIDs, err = server.taskResumeContextWithoutBlackboard(found)
	} else if server.canonicalStore == store.CanonicalStoreBlackboardV2 && server.isBlackboardV2Task(found) {
		resumedMessage, steeringEventIDs, err = server.blackboardV2ResumeContext(found)
	}
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	modelOverride, reasoningEffort := server.resumeTurnSelectionOverrides(found)
	plan, err := server.buildTaskLaunchPlan(found, resumedMessage, modelOverride, nativeResume.NativeSessionID, reasoningEffort)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan.NativeResumeSessionPath = nativeResume.NativeSessionPath
	plan.BlackboardV2SteeringEventIDs = steeringEventIDs
	return found, resumedMessage, plan, nil
}

// prepareResumeContinuation prefers a provider-native session when one is
// available. Otherwise it creates a fresh continuation from Task-owned Goal,
// interrupted Attempt checkpoints, unconsumed Harness Steering, and the new
// Working Snapshot. No summary or synthetic handoff packet is consulted.
func (server *Server) prepareResumeContinuation(found task.Task, resumedMessage string) (task.Task, string, taskLaunchPlan, error) {
	prepared, goal, plan, err := server.prepareNativeResumeContinuation(found, resumedMessage)
	if err == nil {
		return prepared, goal, plan, nil
	}
	if !errors.Is(err, errNativeSessionUnavailable) && !errors.Is(err, errNativeResumeUnavailable) {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	return server.prepareFreshResumeContinuation(found)
}

func (server *Server) prepareNativeResumeRequest(found task.Task, resumedMessage string) (task.Task, string, runtime.NativeSessionMetadata, error) {
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", runtime.NativeSessionMetadata{}, err
	}
	found.RuntimeProfileID = effectiveProfile.ID
	nativeResume, err := server.discoverNativeResumeSession(found)
	if err != nil {
		return task.Task{}, "", runtime.NativeSessionMetadata{}, err
	}
	return found, resumedMessage, nativeResume, nil
}

func (server *Server) prepareFreshResumeContinuation(found task.Task) (task.Task, string, taskLaunchPlan, error) {
	effectiveProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	found.RuntimeProfileID = effectiveProfile.ID

	resumeGoal := ""
	var steeringEventIDs []string
	if found.RunControls.BlackboardMode == task.BlackboardModeDisabled {
		resumeGoal, steeringEventIDs, err = server.taskResumeContextWithoutBlackboard(found)
	} else if server.canonicalStore == store.CanonicalStoreBlackboardV2 && runner.BlackboardV2SupportsProvider(effectiveProfile.Provider) {
		resumeGoal, steeringEventIDs, err = server.blackboardV2ResumeContext(found)
	} else {
		resumeGoal, err = server.buildResumeGoal(found)
	}
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	modelOverride, reasoningEffort := server.resumeTurnSelectionOverrides(found)
	plan, err := server.buildTaskLaunchPlan(found, resumeGoal, modelOverride, "", reasoningEffort)
	if err != nil {
		return task.Task{}, "", taskLaunchPlan{}, err
	}
	plan.BlackboardV2SteeringEventIDs = steeringEventIDs
	return found, resumeGoal, plan, nil
}

// resumeTurnSelectionOverrides carries the preceding Runtime Turn Selection into
// a restart/resume launch so provider-switch paths do not drop model or effort.
func (server *Server) resumeTurnSelectionOverrides(found task.Task) (modelOverride, reasoningEffort string) {
	selection, err := server.currentTurnSelection(found)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(selection.Model), strings.TrimSpace(selection.RequestedReasoningEffort)
}

func (server *Server) buildResumeGoal(found task.Task) (string, error) {
	if found.RunControls.BlackboardMode == task.BlackboardModeDisabled {
		goal, _, err := server.taskResumeContextWithoutBlackboard(found)
		return goal, err
	}
	events, err := server.tasks.Events(found.ID)
	if err != nil {
		return "", err
	}
	if server.canonicalStore == store.CanonicalStoreBlackboardV2 {
		if server.isBlackboardV2Task(found) {
			goal, _, err := server.blackboardV2ResumeContext(found)
			return goal, err
		}
		directives := unconsumedHarnessSteering(events)
		return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{TaskGoal: found.Goal, Steering: directives}), nil
	}

	return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: found.Goal, Steering: unconsumedHarnessSteering(events),
	}), nil
}

func (server *Server) taskResumeContextWithoutBlackboard(found task.Task) (string, []string, error) {
	steering, err := server.tasks.UnconsumedHarnessSteering(context.Background(), found.ID)
	if err != nil {
		return "", nil, err
	}
	directives := make([]string, len(steering))
	eventIDs := make([]string, len(steering))
	for index, directive := range steering {
		directives[index] = directive.Directive
		eventIDs[index] = directive.EventID
	}
	return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: found.Goal,
		Steering: directives,
	}), eventIDs, nil
}

func (server *Server) isBlackboardV2Task(found task.Task) bool {
	profile, err := server.resolveTaskRuntimeProfile(found)
	return err == nil && runner.BlackboardV2SupportsProvider(profile.Provider)
}

func (server *Server) blackboardV2ResumeContext(found task.Task) (string, []string, error) {
	steering, err := server.tasks.UnconsumedHarnessSteering(context.Background(), found.ID)
	if err != nil {
		return "", nil, err
	}
	var checkpoints []blackboardv2.InterruptedAttemptCheckpoint
	latest, err := server.tasks.LatestContinuation(found.ID)
	if err != nil {
		return "", nil, err
	}
	if latest != nil {
		checkpoints, err = server.blackboardV2.InterruptedAttemptCheckpoints(context.Background(), found.ProjectID, latest.ID)
		if err != nil {
			return "", nil, err
		}
	}
	directives := make([]string, len(steering))
	eventIDs := make([]string, len(steering))
	for index, directive := range steering {
		directives[index] = directive.Directive
		eventIDs[index] = directive.EventID
	}
	return adapters.BuildBlackboardV2ResumePrompt(adapters.BlackboardV2ResumeRequest{
		TaskGoal: found.Goal, Steering: directives, InterruptedAttempts: checkpoints,
	}), eventIDs, nil
}
