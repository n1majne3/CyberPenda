package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/project"
)

func (server *Server) handleCreateProject(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name        string           `json:"name"`
		Description string           `json:"description"`
		Kind        string           `json:"kind"`
		Scope       project.Scope    `json:"scope"`
		Defaults    project.Defaults `json:"defaults"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	created, err := server.projects.CreateWithKind(input.Name, input.Description, input.Kind, input.Scope, input.Defaults)
	if err != nil {
		if errors.Is(err, project.ErrMissingName) || errors.Is(err, project.ErrInvalidKind) {
			writeError(response, http.StatusBadRequest, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "store project")
		return
	}

	writeJSON(response, http.StatusCreated, created)
}

func (server *Server) handleListProjects(response http.ResponseWriter, request *http.Request) {
	projects, err := server.projects.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list projects")
		return
	}
	if projects == nil {
		projects = []project.Project{}
	}
	writeJSON(response, http.StatusOK, struct {
		Projects []project.Project `json:"projects"`
	}{
		Projects: projects,
	})
}

func (server *Server) handleGetProject(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	found, err := server.projects.Get(id)
	if errors.Is(err, project.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handlePreviewProjectKindConversion(response http.ResponseWriter, request *http.Request) {
	var input struct {
		TargetKind string `json:"target_kind"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	preview, err := server.projects.PreviewKindConversion(request.PathValue("id"), input.TargetKind)
	if errors.Is(err, project.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, project.ErrInvalidKind) {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "preview Project Kind Conversion")
		return
	}
	writeJSON(response, http.StatusOK, preview)
}

func (server *Server) handleConvertProjectKind(response http.ResponseWriter, request *http.Request) {
	var input struct {
		TargetKind string `json:"target_kind"`
		Confirm    bool   `json:"confirm"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !input.Confirm {
		writeError(response, http.StatusBadRequest, "Project Kind Conversion requires confirmation")
		return
	}
	converted, err := server.projects.ConvertKind(request.PathValue("id"), input.TargetKind)
	if errors.Is(err, project.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, project.ErrInvalidKind) {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, project.ErrKindConversionBlocked) {
		preview, previewErr := server.projects.PreviewKindConversion(request.PathValue("id"), input.TargetKind)
		if previewErr != nil {
			writeError(response, http.StatusConflict, err.Error())
			return
		}
		writeJSON(response, http.StatusConflict, preview)
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "convert Project Kind")
		return
	}
	writeJSON(response, http.StatusOK, converted)
}

func (server *Server) handleUpdateProject(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	var input struct {
		Name        *string           `json:"name"`
		Description *string           `json:"description"`
		Scope       *project.Scope    `json:"scope"`
		Defaults    *project.Defaults `json:"defaults"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	name := ""
	description := ""
	var scope project.Scope
	var defaults project.Defaults
	scopeTouched := false
	defaultsTouched := false

	if input.Name != nil {
		name = *input.Name
	} else {
		existing, err := server.projects.Get(id)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				writeError(response, http.StatusNotFound, err.Error())
				return
			}
			writeError(response, http.StatusInternalServerError, "load project")
			return
		}
		name = existing.Name
	}
	if input.Description != nil {
		description = *input.Description
	}
	if input.Scope != nil {
		scope = *input.Scope
		scopeTouched = true
	}
	if input.Defaults != nil {
		defaults = *input.Defaults
		defaultsTouched = true
	}

	updated, err := server.projects.Update(id, name, description, scope, scopeTouched, defaults, defaultsTouched)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, project.ErrMissingName) {
			writeError(response, http.StatusBadRequest, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "store project update")
		return
	}

	writeJSON(response, http.StatusOK, updated)
}
