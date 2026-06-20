package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"pentest/internal/approval"
	"pentest/internal/skill"
)

const globalAuditProjectID = "global"

type skillWriteRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Source      skill.SourceProvenance `json:"source_provenance,omitempty"`
	Files       map[string]string      `json:"files"`
}

type skillResponse struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Source      skill.SourceProvenance `json:"source_provenance,omitempty"`
	Files       map[string]string      `json:"files,omitempty"`
	Enabled     bool                   `json:"enabled"`
	CreatedAt   any                    `json:"created_at"`
	UpdatedAt   any                    `json:"updated_at"`
}

func (server *Server) handleListSkills(response http.ResponseWriter, request *http.Request) {
	profileID := strings.TrimSpace(request.URL.Query().Get("runtime_profile_id"))
	skills, err := server.skills.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list skills")
		return
	}
	enabled := map[string]bool{}
	if profileID != "" {
		enabledSkills, err := server.skills.EnabledSkills(profileID)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "resolve enabled skills")
			return
		}
		for _, got := range enabledSkills {
			enabled[got.ID] = true
		}
	}
	out := make([]skillResponse, 0, len(skills))
	for _, got := range skills {
		isEnabled := true
		if profileID != "" {
			isEnabled = enabled[got.ID]
		}
		out = append(out, newSkillResponse(got, isEnabled))
	}
	writeJSON(response, http.StatusOK, struct {
		Skills []skillResponse `json:"skills"`
	}{Skills: out})
}

func (server *Server) handleGetSkill(response http.ResponseWriter, request *http.Request) {
	got, err := server.skills.Get(request.PathValue("skill_id"))
	if err != nil {
		writeSkillError(response, err)
		return
	}
	files, err := server.skills.Files(got.ID)
	if err != nil {
		writeSkillError(response, err)
		return
	}
	out := newSkillResponse(got, true)
	out.Files = publicSkillFiles(got, files)
	writeJSON(response, http.StatusOK, out)
}

func (server *Server) handlePutSkill(response http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("skill_id"))
	if id == "" {
		writeError(response, http.StatusNotFound, "skill not found")
		return
	}
	_, getErr := server.skills.Get(id)
	existed := getErr == nil
	var input skillWriteRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	published, err := server.skills.Publish(request.Context(), skill.PublishRequest{
		Metadata: skill.Metadata{
			ID:          id,
			Name:        input.Name,
			Description: input.Description,
			Source:      input.Source,
		},
		Files: input.Files,
	})
	if err != nil {
		writeSkillError(response, err)
		return
	}
	kind := "skill_published"
	if existed {
		kind = "skill_updated"
	}
	if err := server.recordSkillAudit(kind, published.ID, map[string]any{"skill": published}); err != nil {
		writeError(response, http.StatusInternalServerError, "record skill audit")
		return
	}
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeJSON(response, status, newSkillResponse(published, true))
}

func (server *Server) handleImportSkill(response http.ResponseWriter, request *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		writeError(response, http.StatusBadRequest, "read import body")
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if _, hasCommand := fields["command"]; hasCommand {
		writeError(response, http.StatusBadRequest, "skill import accepts structured package/ref input, not raw commands")
		return
	}
	var input skill.ImportRequest
	if err := json.Unmarshal(raw, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	imported, err := server.skills.Import(request.Context(), input)
	if err != nil {
		writeSkillError(response, err)
		return
	}
	if err := server.recordSkillAudit("skill_imported", imported.ID, map[string]any{"skill": imported, "request": input}); err != nil {
		writeError(response, http.StatusInternalServerError, "record skill audit")
		return
	}
	writeJSON(response, http.StatusCreated, newSkillResponse(imported, true))
}

func (server *Server) handleDeleteSkill(response http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("skill_id"))
	forceDisable := parseBoolQuery(request.URL.Query().Get("force_disable"))
	if err := server.skills.Delete(request.Context(), id, forceDisable); err != nil {
		writeSkillError(response, err)
		return
	}
	if err := server.recordSkillAudit("skill_deleted", id, map[string]any{"skill_id": id, "force_disable": forceDisable}); err != nil {
		writeError(response, http.StatusInternalServerError, "record skill audit")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handlePutSkillProfileOptOut(response http.ResponseWriter, request *http.Request) {
	server.handleSkillProfileOptOut(response, request, true)
}

func (server *Server) handleDeleteSkillProfileOptOut(response http.ResponseWriter, request *http.Request) {
	server.handleSkillProfileOptOut(response, request, false)
}

func (server *Server) handleSkillProfileOptOut(response http.ResponseWriter, request *http.Request, optedOut bool) {
	skillID := strings.TrimSpace(request.PathValue("skill_id"))
	profileID := strings.TrimSpace(request.PathValue("profile_id"))
	if err := server.skills.SetOptOut(profileID, skillID, optedOut); err != nil {
		writeSkillError(response, err)
		return
	}
	if err := server.recordSkillAudit("skill_opt_out_changed", skillID, map[string]any{
		"skill_id":   skillID,
		"profile_id": profileID,
		"opted_out":  optedOut,
	}); err != nil {
		writeError(response, http.StatusInternalServerError, "record skill audit")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleListGlobalAuditLog(response http.ResponseWriter, request *http.Request) {
	entries, err := server.approvals.ListAudit(globalAuditProjectID, 200)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list audit log")
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Entries []approval.AuditEntry `json:"entries"`
	}{Entries: entries})
}

func (server *Server) recordSkillAudit(kind, skillID string, payload map[string]any) error {
	summary := strings.ReplaceAll(kind, "_", " ") + ": " + skillID
	_, err := server.approvals.RecordAudit(approval.AuditEntry{
		ProjectID: globalAuditProjectID,
		Kind:      kind,
		Summary:   summary,
		Payload:   payload,
	})
	return err
}

func newSkillResponse(got skill.Skill, enabled bool) skillResponse {
	return skillResponse{
		ID:          got.ID,
		Name:        got.Name,
		Description: got.Description,
		Source:      publicSkillSource(got.Source),
		Enabled:     enabled,
		CreatedAt:   got.CreatedAt,
		UpdatedAt:   got.UpdatedAt,
	}
}

func publicSkillSource(source skill.SourceProvenance) skill.SourceProvenance {
	if source.Kind == "builtin" {
		return skill.SourceProvenance{Kind: "builtin"}
	}
	return source
}

func publicSkillFiles(got skill.Skill, files map[string]string) map[string]string {
	if got.Source.Kind != "builtin" {
		return files
	}
	filtered := make(map[string]string, len(files))
	for path, content := range files {
		if strings.EqualFold(path, "UPSTREAM.md") {
			continue
		}
		filtered[path] = content
	}
	return filtered
}

func writeSkillError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, skill.ErrInvalidSkill):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, skill.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, skill.ErrEnabled):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, err.Error())
	}
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}
