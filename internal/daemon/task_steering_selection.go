package daemon

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

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
	return task.RuntimeConfigVersion{TaskID: found.ID, RuntimeProfileID: provenance, Config: runtimeSnapshotMap(cloned)}, true
}

func (server *Server) appendTaskSteeringSelection(response http.ResponseWriter, found task.Task, payload task.EventPayload, input taskContinuationSelectionInput) (task.Event, *task.RuntimeConfigVersion, bool) {
	var version *task.RuntimeConfigVersion
	var configJSON []byte
	if input.hasSelection() {
		prepared, ok := server.prepareSelectedRuntimeConfig(response, found, input)
		if !ok {
			return task.Event{}, nil, false
		}
		var err error
		configJSON, err = json.Marshal(prepared.Config)
		if err != nil {
			writeTaskError(response, err)
			return task.Event{}, nil, false
		}
		prepared.ID = rand.Text()
		version = &prepared
	}
	tx, err := server.db.Begin()
	if err != nil {
		writeTaskError(response, err)
		return task.Event{}, nil, false
	}
	defer func() { _ = tx.Rollback() }()
	event, err := server.tasks.AppendEventTx(tx, found.ID, task.EventKindSteering, payload)
	if err != nil {
		writeTaskError(response, err)
		return task.Event{}, nil, false
	}
	if version != nil {
		var maxVersion sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(version) FROM task_runtime_config_versions WHERE task_id = ?`, found.ID).Scan(&maxVersion); err != nil {
			writeTaskError(response, err)
			return task.Event{}, nil, false
		}
		version.Version = int(maxVersion.Int64) + 1
		version.CreatedAt = time.Now().UTC()
		if _, err := tx.Exec(`INSERT INTO task_runtime_config_versions (id, task_id, version, runtime_profile_id, config_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			version.ID, version.TaskID, version.Version, version.RuntimeProfileID, string(configJSON), version.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			writeTaskError(response, err)
			return task.Event{}, nil, false
		}
	}
	if err := tx.Commit(); err != nil {
		writeTaskError(response, err)
		return task.Event{}, nil, false
	}
	return event, version, true
}
