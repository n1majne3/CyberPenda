package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/project"
	"pentest/internal/store"
)

type Config struct {
	Version string
	DBPath  string
}

type Server struct {
	mux      *http.ServeMux
	version  string
	db       *store.DB
	projects *project.Service
}

func NewServer(config Config) (*Server, error) {
	db, err := store.Open(config.DBPath)
	if err != nil {
		return nil, err
	}

	server := &Server{
		mux:      http.NewServeMux(),
		version:  config.Version,
		db:       db,
		projects: project.NewService(db),
	}
	server.routes()

	return server, nil
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	server.mux.ServeHTTP(response, request)
}

func (server *Server) Close() error {
	return server.db.Close()
}

func (server *Server) routes() {
	server.mux.HandleFunc("GET /health", server.handleHealth)
	server.mux.HandleFunc("GET /api/projects", server.handleListProjects)
	server.mux.HandleFunc("POST /api/projects", server.handleCreateProject)
	server.mux.HandleFunc("GET /api/projects/{id}", server.handleGetProject)
	server.mux.HandleFunc("PATCH /api/projects/{id}", server.handleUpdateProject)
}

func (server *Server) handleHealth(response http.ResponseWriter, request *http.Request) {
	payload := struct {
		Version  string `json:"version"`
		Database struct {
			Status string `json:"status"`
		} `json:"database"`
	}{
		Version: server.version,
	}
	payload.Database.Status = "ok"

	writeJSON(response, http.StatusOK, payload)
}

func (server *Server) handleCreateProject(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name        string           `json:"name"`
		Description string           `json:"description"`
		Scope       project.Scope    `json:"scope"`
		Defaults    project.Defaults `json:"defaults"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	created, err := server.projects.Create(input.Name, input.Description, input.Scope, input.Defaults)
	if err != nil {
		if errors.Is(err, project.ErrMissingName) {
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
		// Preserve existing name when the field is omitted.
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

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, struct {
		Error string `json:"error"`
	}{
		Error: message,
	})
}
