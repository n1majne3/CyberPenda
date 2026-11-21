package daemon

import (
	"net/http"

	"pentest/internal/runtimeconfig"
	"pentest/internal/task"
)

func (server *Server) recordSelectedRuntimeConfig(response http.ResponseWriter, found task.Task, steeringEventID string, input taskContinuationSelectionInput) (task.RuntimeConfigVersion, bool) {
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
		Model:           firstNonBlank(input.selectedModel(), requestedProfile.Fields.ModelOverride, requestedProfile.Fields.Model),
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
	recorded, err := server.tasks.RecordRuntimeConfig(found.ID, provenance, runtimeSnapshotMap(cloned))
	if err != nil {
		writeTaskError(response, err)
		return task.RuntimeConfigVersion{}, false
	}
	return recorded, true
}
