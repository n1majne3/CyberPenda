package daemon

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"pentest/internal/blackboardv2"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/steering"
	"pentest/internal/task"
)

func unconsumedHarnessSteering(events []task.Event) []string {
	consumed := make(map[string]bool)
	for _, event := range events {
		if event.Kind != task.EventKindSteering || event.Payload["phase"] != "steering_applied" {
			continue
		}
		if requestedID, ok := event.Payload["requested_event_id"].(string); ok && requestedID != "" {
			consumed[requestedID] = true
		}
	}
	directives := make([]string, 0)
	for _, event := range events {
		if event.Kind != task.EventKindSteering || event.Payload["phase"] != "steering_requested" || consumed[event.ID] {
			continue
		}
		if directive, ok := event.Payload["directive"].(string); ok && strings.TrimSpace(directive) != "" {
			directives = append(directives, directive)
		}
	}
	return directives
}

func (server *Server) writeResumePreparationError(response http.ResponseWriter, err error) {
	var boardErr *blackboardv2.Error
	if errors.As(err, &boardErr) && boardErr.Code == "reconciliation_incomplete" {
		writeError(response, http.StatusConflict, boardErr.Message)
		return
	}
	switch {
	case errors.Is(err, runtimeprofile.ErrNotFound):
		writeError(response, http.StatusBadRequest, "runtime profile not found")
	case errors.Is(err, errNativeResumeUnavailable):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNativeSessionUnavailable):
		writeError(response, http.StatusConflict, err.Error())
	case errors.Is(err, task.ErrContinuationReconciliationIncomplete), errors.Is(err, task.ErrSteeringSelectionConflict):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeTaskAdapterError(response, err)
	}
}

func (server *Server) handleQueueSteerTask(response http.ResponseWriter, request *http.Request) {
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
	var input struct {
		Directive string `json:"directive"`
		taskContinuationSelectionInput
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(input.RuntimeProfileID) != "" {
		writeError(response, http.StatusBadRequest, "runtime_profile_locked")
		return
	}
	if strings.TrimSpace(input.Directive) == "" {
		writeError(response, http.StatusBadRequest, "steering directive is required")
		return
	}
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)
	payload := task.EventPayload{
		"directive": input.Directive,
		"phase":     "steering_requested",
		"mode":      "queue",
	}
	if input.SubmittedBy != "" {
		payload["submitted_by"] = input.SubmittedBy
	}
	if input.RuntimeProfileID != "" {
		payload["runtime_profile_id"] = input.RuntimeProfileID
	}
	if input.ModelProviderID != "" {
		payload["model_provider_id"] = input.ModelProviderID
	}
	if model := input.selectedModel(); model != "" {
		payload["model"] = model
		payload["model_override"] = model
	}
	if input.ReasoningEffort != "" {
		payload["reasoning_effort"] = input.ReasoningEffort
	}
	event, err := server.tasks.AppendEvent(taskID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	// Config Projection / Runtime Config Version is only for profile or Model
	// Provider changes. Same-provider model/effort stay on the event payload
	// (and on native turns) without minting a new version.
	var configVersion *task.RuntimeConfigVersion
	if input.hasSelection() {
		recorded, ok := server.recordSelectedRuntimeConfig(response, found, event.ID, input.taskContinuationSelectionInput)
		if !ok {
			return
		}
		configVersion = &recorded
	}
	writeJSON(response, http.StatusOK, struct {
		Event                task.Event                 `json:"event"`
		RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
	}{Event: event, RuntimeConfigVersion: configVersion})
}

func (server *Server) handleSteerTask(response http.ResponseWriter, request *http.Request) {
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
	var input nativeSteerRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(input.RuntimeProfileID) != "" {
		writeError(response, http.StatusBadRequest, "runtime_profile_locked")
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		input.RequestID = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if input.RequestID == "" {
		input.RequestID = newNativeSteerRequestID()
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		input.Message = strings.TrimSpace(input.Directive)
	}
	if input.Message == "" {
		writeError(response, http.StatusBadRequest, "steer message is required")
		return
	}
	adapter := taskSteeringAdapter(server, found.ID, server.taskConclusionSettlementForID(found.ID))
	clientSelectionIdentity := canonicalSteeringClientSelectionIdentity(input.ModelProviderID, input.selectedModel(), input.ReasoningEffort)
	replayIdentity := steeringReplayIdentity{
		requestID: input.RequestID, operatorMessage: input.Message,
		clientSelectionIdentity: clientSelectionIdentity,
		modelProviderID:         input.ModelProviderID, model: input.selectedModel(),
		requestedReasoningEffort: input.ReasoningEffort,
	}
	if record, replayed, replayErr := server.findAcceptedSteeringReplayByIdentity(adapter, replayIdentity); replayErr != nil || replayed {
		if replayErr != nil {
			var conflict *steeringRequestConflictError
			if errors.As(replayErr, &conflict) {
				writeError(response, http.StatusConflict, conflict.Error())
			} else {
				writeTaskError(response, replayErr)
			}
			return
		}
		writeTaskAcceptedSteering(response, record, "")
		return
	}
	if providerSession, ok := server.providerSessions.get(taskID); ok {
		server.handleProviderSessionSteer(response, request, found, providerSession, input)
		return
	}

	input.Directive = input.Message

	activeSteer := found.Status == task.StatusRunning || found.Status == task.StatusPaused
	if !server.acquireTaskControl(taskID) {
		writeError(response, http.StatusConflict, "task control operation already active")
		return
	}
	defer server.releaseTaskControl(taskID)

	payload := task.EventPayload{
		"directive": input.Directive,
		"phase":     "steering_requested",
	}
	if input.SubmittedBy != "" {
		payload["submitted_by"] = input.SubmittedBy
	}
	if input.RuntimeProfileID != "" {
		payload["runtime_profile_id"] = input.RuntimeProfileID
	}
	if input.ModelProviderID != "" {
		payload["model_provider_id"] = input.ModelProviderID
	}
	if model := input.selectedModel(); model != "" {
		payload["model"] = model
		payload["model_override"] = model
	}
	if input.ReasoningEffort != "" {
		payload["reasoning_effort"] = input.ReasoningEffort
	}

	event, err := server.tasks.AppendEvent(taskID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return
	}

	var configVersion *task.RuntimeConfigVersion
	if input.hasSelection() {
		recorded, ok := server.recordSelectedRuntimeConfig(response, found, event.ID, input.taskContinuationSelectionInput)
		if !ok {
			return
		}
		configVersion = &recorded
	}

	if activeSteer {
		if _, _, _, err := server.prepareNativeResumeRequest(found, input.Directive); err != nil {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "resume_failed",
				"error": err.Error(),
			})
			server.writeResumePreparationError(response, err)
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": "interrupting",
		})
		if ok := server.harness.StopAndWait(taskID, 10*time.Second); !ok {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "stop_failed",
			})
			writeError(response, http.StatusConflict, "runtime did not stop in time")
			return
		}

		resumedTask, resumeGoal, plan, err := server.prepareNativeResumeContinuation(found, input.Directive)
		if err != nil {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
				"phase": "resume_failed",
				"error": err.Error(),
			})
			server.writeResumePreparationError(response, err)
			return
		}
		_, _ = server.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{
			"phase": "resuming_native",
		})
		if err := server.launchTaskInBackground(resumedTask, plan, resumeGoal); err != nil {
			writeTaskLaunchError(response, err)
			return
		}
		if !plan.BlackboardV2 {
			_, _ = server.tasks.AppendEvent(taskID, task.EventKindSteering, task.EventPayload{
				"phase":              "steering_applied",
				"directive":          input.Directive,
				"requested_event_id": event.ID,
			})
		}
		detailed, err := server.taskDetail(taskID)
		if err != nil {
			writeTaskError(response, err)
			return
		}
		writeJSON(response, http.StatusAccepted, struct {
			Event                task.Event                 `json:"event"`
			RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
			Task                 task.Task                  `json:"task"`
		}{
			Event:                event,
			RuntimeConfigVersion: configVersion,
			Task:                 detailed,
		})
		return
	}

	writeJSON(response, http.StatusOK, struct {
		Event                task.Event                 `json:"event"`
		RuntimeConfigVersion *task.RuntimeConfigVersion `json:"runtime_config_version,omitempty"`
	}{
		Event:                event,
		RuntimeConfigVersion: configVersion,
	})
}

func (server *Server) handleProviderSessionSteer(response http.ResponseWriter, request *http.Request, found task.Task, session runtime.ProviderSession, input nativeSteerRequest) {
	clientSelectionIdentity := canonicalSteeringClientSelectionIdentity(input.ModelProviderID, input.selectedModel(), input.ReasoningEffort)
	// Runtime Plugin / Runtime Profile switches need Config Projection and a
	// restart. Model Provider changes stay native for Pi when the provider was
	// projected at startup (ADR 0015); Codex/Claude still restart.
	if input.hasRuntimeProfileSelection() {
		writeError(response, http.StatusConflict, "native steer cannot change runtime profile; restart the continuation")
		return
	}
	selection, ok := server.resolveNativeTurnSelection(response, found, input.taskContinuationSelectionInput)
	if !ok {
		return
	}
	if found.Status != task.StatusRunning && found.Status != task.StatusPaused {
		writeError(response, http.StatusConflict, "native steer requires an active Task")
		return
	}

	currentSelection, err := server.currentTurnSelection(found)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "resolve current turn selection")
		return
	}
	runtimeProfile, err := server.resolveTaskRuntimeProfile(found)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "resolve Runtime Provider")
		return
	}
	settlement := server.taskConclusionSettlementForID(found.ID)
	prepared, err := prepareAcceptedNativeSteer(
		request.Context(), session,
		providerSelectionRequiresReplacement(runtimeProfile.Provider, currentSelection, selection),
		input.ForceReplace, settlement,
	)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}
	if nativeSteerOperation(session, prepared.Mode) == nil {
		writeError(response, http.StatusConflict, "provider session does not support native steer")
		return
	}
	mode := prepared.Mode
	expectedProviderTurnID := prepared.ExpectedProviderTurnID
	releaseSteeringRequest := server.steering.AcquireRequest(owner.KindTask, found.ID, input.RequestID)
	defer releaseSteeringRequest()

	conversationPayload := task.EventPayload{
		"role": "user", "text": input.Message, "request_id": input.RequestID,
		"delivery": "native_steer", "outcome": "pending", "mode": string(mode),
		"session_id":        session.SessionID(),
		"model_provider_id": selection.ModelProviderID, "model": selection.Model,
		"requested_reasoning_effort": selection.RequestedReasoningEffort,
	}
	if expectedProviderTurnID != "" {
		conversationPayload["provider_turn_id"] = expectedProviderTurnID
	}
	record, _, err := server.acceptSteeringOrReplay(request.Context(), taskSteeringAdapter(server, found.ID, settlement), steering.AcceptRequest{
		RequestID:                input.RequestID,
		ClientSelectionIdentity:  clientSelectionIdentity,
		OperatorMessage:          input.Message,
		Message:                  input.Message,
		Mode:                     owner.SteeringMode(mode),
		ModelProviderID:          selection.ModelProviderID,
		Model:                    selection.Model,
		RequestedReasoningEffort: selection.RequestedReasoningEffort,
		SessionID:                session.SessionID(),
		ExpectedProviderTurnID:   expectedProviderTurnID,
	}, func(tx *sql.Tx) (string, error) {
		event, eventErr := server.tasks.AppendEventTx(tx, found.ID, task.EventKindConversation, conversationPayload)
		if eventErr != nil {
			return "", eventErr
		}
		return event.ID, nil
	})
	if err != nil {
		var conflict *steeringRequestConflictError
		if errors.As(err, &conflict) {
			writeError(response, http.StatusConflict, conflict.Error())
			return
		}
		writeTaskError(response, err)
		return
	}
	writeTaskAcceptedSteering(response, record, session.SessionID())
}
