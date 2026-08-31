package daemon

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
)

type saveRuntimeProfileInput struct {
	Name    string `json:"name"`
	Confirm bool   `json:"confirm"`
}

func (server *Server) handleSaveTaskRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	found, ok := server.requireProjectTask(response, request)
	if !ok {
		return
	}
	versions, err := server.tasks.RuntimeConfigVersions(found.ID)
	if err != nil || len(versions) == 0 {
		writeError(response, http.StatusBadRequest, "runtime configuration snapshot is missing")
		return
	}
	server.saveRuntimeProfileFromSnapshot(response, request, versions[len(versions)-1].Config)
}

func (server *Server) handleSaveSessionRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	sessionID := strings.TrimSpace(request.PathValue("id"))
	if _, err := server.sessions.Get(sessionID); err != nil {
		writeError(response, http.StatusNotFound, "session not found")
		return
	}
	versions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil || len(versions) == 0 {
		writeError(response, http.StatusBadRequest, "runtime configuration snapshot is missing")
		return
	}
	server.saveRuntimeProfileFromSnapshot(response, request, versions[len(versions)-1].Config)
}

func (server *Server) saveRuntimeProfileFromSnapshot(response http.ResponseWriter, request *http.Request, config map[string]any) {
	var input saveRuntimeProfileInput
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || !input.Confirm {
		writeError(response, http.StatusBadRequest, "name and confirm: true are required")
		return
	}
	snapshot, err := decodeRuntimeSnapshot(config)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	profileView, err := runtimeProfileFromSnapshot(config)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	profileView.Fields.ModelProviderID = snapshot.TurnSelection.ModelProviderID
	profileView.Fields.ModelOverride = snapshot.TurnSelection.Model
	profileView.Fields.ReasoningEffort = snapshot.TurnSelection.ReasoningEffort
	profileView.Fields.DefaultRunner = snapshot.Runner
	profileView.Fields.APIKeys = nil
	allSkills, err := server.skills.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "read Runtime Profile Skill selection")
		return
	}
	tx, err := server.db.Begin()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "begin Runtime Profile save")
		return
	}
	defer func() { _ = tx.Rollback() }()
	created, err := server.profiles.CreateTx(tx, input.Name, runtimeprofile.Provider(snapshot.RuntimePluginID), profileView.Fields)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := copySnapshotSkillSelectionToProfileTx(tx, created.ID, snapshot, allSkills); err != nil {
		writeError(response, http.StatusInternalServerError, "save Runtime Profile Skill selection")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(response, http.StatusInternalServerError, "commit Runtime Profile")
		return
	}
	writeJSON(response, http.StatusCreated, runtimeprofile.SanitizeProfile(created))
}

func copySnapshotSkillSelectionToProfileTx(tx *sql.Tx, profileID string, snapshot runtimeconfig.RuntimeConfigurationSnapshot, all []skill.Skill) error {
	enabled := make(map[string]bool, len(snapshot.EnabledSkillIDs))
	for _, id := range snapshot.EnabledSkillIDs {
		enabled[id] = true
	}
	for _, item := range all {
		if !enabled[item.ID] {
			if _, err := tx.Exec(`INSERT INTO skill_profile_opt_outs(profile_id,skill_id,created_at) VALUES(?,?,?)`, profileID, item.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
	}
	return nil
}
