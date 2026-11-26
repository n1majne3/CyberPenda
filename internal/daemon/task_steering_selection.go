package daemon

import (
	"net/http"

	"pentest/internal/runtimeconfig"
	"pentest/internal/task"
)

func (server *Server) recordSelectedRuntimeConfig(response http.ResponseWriter, found task.Task, _ string, input taskContinuationSelectionInput) (task.RuntimeConfigVersion, bool) {
	prepared, ok := server.prepareSelectedRuntimeConfig(response, found, input)
	if !ok {
		return task.RuntimeConfigVersion{}, false
	}
	recorded, err := server.tasks.RecordRuntimeConfig(found.ID, prepared.RuntimeProfileID, prepared.Config)
	if err != nil {
		writeTaskError(response, err)
		return task.RuntimeConfigVersion{}, false
	}
	return recorded, true
}

func (server *Server) prepareSelectedRuntimeConfig(response http.ResponseWriter, found task.Task, input taskContinuationSelectionInput) (task.RuntimeConfigVersion, bool) {
	effort, err := normalizeLaunchReasoningEffort(input.ReasoningEffort)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return task.RuntimeConfigVersion{}, false
	}
	input.ReasoningEffort = effort
	requestedProfile, ok := server.resolveTaskContinuationRuntimeProfile(response, found, input)
	if !ok {
		return task.RuntimeConfigVersion{}, false
	}
	versions, err := server.tasks.RuntimeConfigVersions(found.ID)
	if err != nil || len(versions) == 0 {
		writeError(response, http.StatusInternalServerError, "runtime configuration snapshot is missing")
		return task.RuntimeConfigVersion{}, false
	}
	prior, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return task.RuntimeConfigVersion{}, false
	}
	turn := runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: requestedProfile.Fields.ModelProviderID,
		Model:           firstNonBlank(input.selectedModel(), requestedProfile.Fields.ModelOverride),
		ReasoningEffort: firstNonBlank(input.ReasoningEffort, requestedProfile.Fields.ReasoningEffort),
	}
	cloned, err := server.cloneSnapshotForTurn(prior, found.ProjectID, turn)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return task.RuntimeConfigVersion{}, false
	}
	provenance := ""
	if cloned.RuntimeProfile != nil {
		provenance = cloned.RuntimeProfile.ID
	}
	return task.RuntimeConfigVersion{TaskID: found.ID, RuntimeProfileID: provenance, Config: runtimeSnapshotMap(cloned)}, true
}

func (server *Server) appendTaskSteeringSelection(response http.ResponseWriter, found task.Task, payload task.EventPayload, input taskContinuationSelectionInput) (task.Event, *task.RuntimeConfigVersion, bool) {
	var version *task.RuntimeConfigVersion
	if input.hasSelection() {
		prepared, ok := server.prepareSelectedRuntimeConfig(response, found, input)
		if !ok {
			return task.Event{}, nil, false
		}
		version = &prepared
	}
	event, err := server.tasks.AppendEvent(found.ID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return task.Event{}, nil, false
	}
	if version != nil {
		recorded, err := server.tasks.RecordRuntimeConfig(found.ID, version.RuntimeProfileID, version.Config)
		if err != nil {
			writeTaskError(response, err)
			return task.Event{}, nil, false
		}
		version = &recorded
	}
	return event, version, true
}
