package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"pentest/internal/skill"
)

type skillWriteRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Source      skill.SourceProvenance `json:"source_provenance,omitempty"`
	Files       map[string]string      `json:"files"`
}

type skillResponse struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description,omitempty"`
	Source           skill.SourceProvenance `json:"source_provenance,omitempty"`
	Files            map[string]string      `json:"files,omitempty"`
	Enabled          bool                   `json:"enabled"`
	GloballyOptedOut bool                   `json:"globally_opted_out"`
	ProfileOptedOut  bool                   `json:"profile_opted_out"`
	CreatedAt        any                    `json:"created_at"`
	UpdatedAt        any                    `json:"updated_at"`
}

func (server *Server) handleListSkills(response http.ResponseWriter, request *http.Request) {
	profileID := strings.TrimSpace(request.URL.Query().Get("runtime_profile_id"))
	skills, err := server.skills.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list skills")
		return
	}
	profileOptedOut := map[string]bool{}
	if profileID != "" {
		ids, err := server.skills.ProfileOptedOutSkillIDs(profileID)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "resolve Profile Skill Opt-Outs")
			return
		}
		for _, id := range ids {
			profileOptedOut[id] = true
		}
	}
	out := make([]skillResponse, 0, len(skills))
	for _, got := range skills {
		isProfileOptedOut := profileOptedOut[got.ID]
		isEnabled := !got.GloballyOptedOut && !isProfileOptedOut
		out = append(out, newSkillResponse(got, isEnabled, isProfileOptedOut))
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
	out := newSkillResponse(got, !got.GloballyOptedOut, false)
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
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeJSON(response, status, newSkillResponse(published, !published.GloballyOptedOut, false))
}

func (server *Server) handleImportSkill(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		server.handleImportSkillArchive(response, request)
		return
	}
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
	writeJSON(response, http.StatusCreated, newSkillResponse(imported, !imported.GloballyOptedOut, false))
}

func (server *Server) handleImportSkillArchive(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, 32<<20)
	if err := request.ParseMultipartForm(8 << 20); err != nil {
		writeError(response, http.StatusBadRequest, "invalid skill archive upload")
		return
	}
	defer request.MultipartForm.RemoveAll()
	headers := request.MultipartForm.File["archive"]
	if len(headers) != 1 {
		writeError(response, http.StatusBadRequest, "exactly one skill archive is required")
		return
	}
	imported, err := server.importSkillArchive(request, headers[0])
	if err != nil {
		writeSkillError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, newSkillResponse(imported, !imported.GloballyOptedOut, false))
}

func (server *Server) importSkillArchive(request *http.Request, header *multipart.FileHeader) (skill.Skill, error) {
	file, err := header.Open()
	if err != nil {
		return skill.Skill{}, fmt.Errorf("%w: open uploaded archive", skill.ErrInvalidSkill)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 32<<20+1))
	if err != nil {
		return skill.Skill{}, fmt.Errorf("%w: read uploaded archive", skill.ErrInvalidSkill)
	}
	if len(raw) > 32<<20 {
		return skill.Skill{}, fmt.Errorf("%w: uploaded archive is too large", skill.ErrInvalidSkill)
	}
	return server.skills.ImportArchive(request.Context(), header.Filename, raw)
}

func (server *Server) handleDeleteSkill(response http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("skill_id"))
	forceDisable := parseBoolQuery(request.URL.Query().Get("force_disable"))
	if err := server.skills.Delete(request.Context(), id, forceDisable); err != nil {
		writeSkillError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handlePutGlobalSkillOptOut(response http.ResponseWriter, request *http.Request) {
	server.handleGlobalSkillOptOut(response, request, true)
}

func (server *Server) handleDeleteGlobalSkillOptOut(response http.ResponseWriter, request *http.Request) {
	server.handleGlobalSkillOptOut(response, request, false)
}

func (server *Server) handlePutAllGlobalSkillOptOuts(response http.ResponseWriter, request *http.Request) {
	server.handleAllGlobalSkillOptOuts(response, request, true)
}

func (server *Server) handleDeleteAllGlobalSkillOptOuts(response http.ResponseWriter, request *http.Request) {
	server.handleAllGlobalSkillOptOuts(response, request, false)
}

func (server *Server) handleAllGlobalSkillOptOuts(response http.ResponseWriter, _ *http.Request, optedOut bool) {
	if err := server.skills.SetAllGlobalOptOut(optedOut); err != nil {
		writeSkillError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleGlobalSkillOptOut(response http.ResponseWriter, request *http.Request, optedOut bool) {
	skillID := strings.TrimSpace(request.PathValue("skill_id"))
	if err := server.skills.SetGlobalOptOut(skillID, optedOut); err != nil {
		writeSkillError(response, err)
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

func (server *Server) handlePutAllSkillProfileOptOuts(response http.ResponseWriter, request *http.Request) {
	server.handleAllSkillProfileOptOuts(response, request, true)
}

func (server *Server) handleDeleteAllSkillProfileOptOuts(response http.ResponseWriter, request *http.Request) {
	server.handleAllSkillProfileOptOuts(response, request, false)
}

func (server *Server) handleAllSkillProfileOptOuts(response http.ResponseWriter, request *http.Request, optedOut bool) {
	profileID := strings.TrimSpace(request.PathValue("profile_id"))
	if err := server.skills.SetAllOptOut(profileID, optedOut); err != nil {
		writeSkillError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleSkillProfileOptOut(response http.ResponseWriter, request *http.Request, optedOut bool) {
	skillID := strings.TrimSpace(request.PathValue("skill_id"))
	profileID := strings.TrimSpace(request.PathValue("profile_id"))
	if err := server.skills.SetOptOut(profileID, skillID, optedOut); err != nil {
		writeSkillError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func newSkillResponse(got skill.Skill, enabled, profileOptedOut bool) skillResponse {
	return skillResponse{
		ID:               got.ID,
		Name:             got.Name,
		Description:      got.Description,
		Source:           publicSkillSource(got.Source),
		Enabled:          enabled,
		GloballyOptedOut: got.GloballyOptedOut,
		ProfileOptedOut:  profileOptedOut,
		CreatedAt:        got.CreatedAt,
		UpdatedAt:        got.UpdatedAt,
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
