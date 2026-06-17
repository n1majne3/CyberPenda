package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/blackboard"
	"pentest/internal/credential"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
	"pentest/internal/task"
)

type Config struct {
	Version string
	DBPath  string
}

type Server struct {
	mux       *http.ServeMux
	version   string
	db        *store.DB
	projects  *project.Service
	profiles  *runtimeprofile.Service
	creds     *credential.Service
	preflight *preflight.Service
	tasks     *task.Service
	harness   *runtime.Harness
	facts     *blackboard.Service
}

func NewServer(config Config) (*Server, error) {
	db, err := store.Open(config.DBPath)
	if err != nil {
		return nil, err
	}

	profiles := runtimeprofile.NewService(db)
	creds := credential.NewService(db)
	tasks := task.NewService(db, nil)
	server := &Server{
		mux:       http.NewServeMux(),
		version:   config.Version,
		db:        db,
		projects:  project.NewService(db),
		profiles:  profiles,
		creds:     creds,
		preflight: preflight.NewService(profiles, creds),
		tasks:     tasks,
		harness:   runtime.NewHarness(tasks),
		facts:     blackboard.NewService(db),
	}
	server.tasks.SetProjectService(server.projects)
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
	server.mux.HandleFunc("POST /api/runtime-profiles", server.handleCreateRuntimeProfile)
	server.mux.HandleFunc("GET /api/runtime-profiles", server.handleListRuntimeProfiles)
	server.mux.HandleFunc("GET /api/runtime-profiles/{id}", server.handleGetRuntimeProfile)
	server.mux.HandleFunc("PATCH /api/runtime-profiles/{id}", server.handleUpdateRuntimeProfile)
	server.mux.HandleFunc("DELETE /api/runtime-profiles/{id}", server.handleDeleteRuntimeProfile)
	server.mux.HandleFunc("PUT /api/credential-bindings", server.handleUpsertGlobalCredentialBinding)
	server.mux.HandleFunc("GET /api/credential-bindings", server.handleListGlobalCredentialBindings)
	server.mux.HandleFunc("DELETE /api/credential-bindings/{binding_id}", server.handleDeleteCredentialBinding)
	server.mux.HandleFunc("POST /api/projects/{id}/preflight", server.handlePreflight)
	server.mux.HandleFunc("GET /api/projects/{id}/dashboard", server.handleDashboard)
	server.mux.HandleFunc("PUT /api/projects/{id}/credential-bindings", server.handleUpsertProjectCredentialBinding)
	server.mux.HandleFunc("GET /api/projects/{id}/credential-bindings", server.handleListProjectCredentialBindings)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks", server.handleCreateTask)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks", server.handleListTasks)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}", server.handleGetTask)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/events", server.handleTaskEvents)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/stop", server.handleStopTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/steer", server.handleSteerTask)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/continuation", server.handleTaskContinuation)
	server.mux.HandleFunc("PUT /api/projects/{id}/tasks/{task_id}/summary", server.handlePutTaskSummary)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/summary", server.handleGetTaskSummary)
	server.mux.HandleFunc("PUT /api/projects/{id}/facts/{fact_key}", server.handleUpsertProjectFact)
	server.mux.HandleFunc("GET /api/projects/{id}/facts/{fact_key}/versions", server.handleProjectFactVersions)
	server.mux.HandleFunc("PUT /api/projects/{id}/facts/{fact_key}/relations", server.handleUpsertFactRelation)
	server.mux.HandleFunc("GET /api/projects/{id}/facts/{fact_key}/relations", server.handleFactRelations)
	server.mux.HandleFunc("GET /api/projects/{id}/facts/{fact_key}", server.handleGetProjectFact)
	server.mux.HandleFunc("GET /api/projects/{id}/facts/index", server.handleFactIndex)
	server.mux.HandleFunc("PUT /api/projects/{id}/findings/{finding_key}", server.handleUpsertFinding)
	server.mux.HandleFunc("GET /api/projects/{id}/findings/{finding_key}/versions", server.handleFindingVersions)
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

func (server *Server) handleListRuntimeProfiles(response http.ResponseWriter, request *http.Request) {
	profiles, err := server.profiles.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list runtime profiles")
		return
	}
	if profiles == nil {
		profiles = []runtimeprofile.Profile{}
	}
	writeJSON(response, http.StatusOK, struct {
		Profiles []runtimeprofile.Profile `json:"profiles"`
	}{
		Profiles: profiles,
	})
}

func (server *Server) handleCreateRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name     string                  `json:"name"`
		Provider runtimeprofile.Provider `json:"provider"`
		Fields   runtimeprofile.Fields   `json:"fields"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	created, err := server.profiles.Create(input.Name, input.Provider, input.Fields)
	if err != nil {
		switch {
		case errors.Is(err, runtimeprofile.ErrMissingName),
			errors.Is(err, runtimeprofile.ErrMissingProvider),
			errors.Is(err, runtimeprofile.ErrUnknownProvider):
			writeError(response, http.StatusBadRequest, err.Error())
		default:
			writeError(response, http.StatusInternalServerError, "store runtime profile")
		}
		return
	}

	writeJSON(response, http.StatusCreated, created)
}

func (server *Server) handleGetRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	found, err := server.profiles.Get(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return
	}

	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handleUpdateRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	var input struct {
		Name     *string                  `json:"name"`
		Provider *runtimeprofile.Provider `json:"provider"`
		Fields   *runtimeprofile.Fields   `json:"fields"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	name := ""
	if input.Name != nil {
		name = *input.Name
	}
	provider := runtimeprofile.Provider("")
	if input.Provider != nil {
		provider = *input.Provider
	}
	var fields runtimeprofile.Fields
	fieldsTouched := false
	if input.Fields != nil {
		fields = *input.Fields
		fieldsTouched = true
	}

	updated, err := server.profiles.Update(id, name, provider, fields, fieldsTouched)
	if err != nil {
		switch {
		case errors.Is(err, runtimeprofile.ErrNotFound):
			writeError(response, http.StatusNotFound, err.Error())
		case errors.Is(err, runtimeprofile.ErrUnknownProvider):
			writeError(response, http.StatusBadRequest, err.Error())
		default:
			writeError(response, http.StatusInternalServerError, "store runtime profile update")
		}
		return
	}

	writeJSON(response, http.StatusOK, updated)
}

func (server *Server) handleDeleteRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	err := server.profiles.Delete(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "delete runtime profile")
		return
	}

	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleUpsertGlobalCredentialBinding(response http.ResponseWriter, request *http.Request) {
	var input credentialBindingInput
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	binding, err := server.creds.Upsert(input.CredentialRef, credential.ScopeGlobal, "", input.Source, input.Disabled)
	if err != nil {
		writeCredentialError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, binding)
}

func (server *Server) handleListGlobalCredentialBindings(response http.ResponseWriter, request *http.Request) {
	bindings, err := server.creds.ListGlobal()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list credential bindings")
		return
	}
	if bindings == nil {
		bindings = []credential.Binding{}
	}
	writeJSON(response, http.StatusOK, struct {
		Bindings []credential.Binding `json:"bindings"`
	}{
		Bindings: bindings,
	})
}

func (server *Server) handleUpsertProjectCredentialBinding(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	// A project-scoped binding must reference a real project.
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	var input credentialBindingInput
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	binding, err := server.creds.Upsert(input.CredentialRef, credential.ScopeProject, projectID, input.Source, input.Disabled)
	if err != nil {
		writeCredentialError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, binding)
}

func (server *Server) handleListProjectCredentialBindings(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	bindings, err := server.creds.ListForProject(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list credential bindings")
		return
	}
	if bindings == nil {
		bindings = []credential.Binding{}
	}
	writeJSON(response, http.StatusOK, struct {
		Bindings []credential.Binding `json:"bindings"`
	}{
		Bindings: bindings,
	})
}

func (server *Server) handleDeleteCredentialBinding(response http.ResponseWriter, request *http.Request) {
	bindingID := request.PathValue("binding_id")
	if bindingID == "" {
		writeError(response, http.StatusNotFound, "credential binding not found")
		return
	}

	err := server.creds.Delete(bindingID)
	if errors.Is(err, credential.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "delete credential binding")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handlePreflight(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	var input struct {
		RuntimeProfileID        string   `json:"runtime_profile_id"`
		Runner                  string   `json:"runner"`
		CredentialRefsToResolve []string `json:"credential_refs"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	result := server.preflight.Run(request.Context(), preflight.Request{
		RuntimeProfileID:        input.RuntimeProfileID,
		ProjectID:               projectID,
		CredentialRefsToResolve: input.CredentialRefsToResolve,
		Runner:                  input.Runner,
	})

	// A preflight result is always 200: the body reports pass/fail per check.
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleDashboard(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	found, err := server.projects.Get(projectID)
	if errors.Is(err, project.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	scope := found.Scope
	// ready means the scope declares at least one named target asset, so
	// meaningful testing can proceed.
	namedAssets := len(scope.Domains) + len(scope.IPs) + len(scope.CIDRs) + len(scope.URLs) + len(scope.Ports)
	summary := struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Scope     struct {
			Domains          int  `json:"domains"`
			IPs              int  `json:"ips"`
			CIDRs            int  `json:"cidrs"`
			URLs             int  `json:"urls"`
			Ports            int  `json:"ports"`
			Excluded         int  `json:"excluded"`
			HasTestingLimits bool `json:"has_testing_limits"`
			HasNotes         bool `json:"has_notes"`
			Ready            bool `json:"ready"`
		} `json:"scope"`
		Counts struct {
			Tasks    int `json:"tasks"`
			Facts    int `json:"facts"`
			Findings int `json:"findings"`
			Evidence int `json:"evidence"`
		} `json:"counts"`
	}{
		ProjectID: found.ID,
		Name:      found.Name,
	}
	summary.Scope.Domains = len(scope.Domains)
	summary.Scope.IPs = len(scope.IPs)
	summary.Scope.CIDRs = len(scope.CIDRs)
	summary.Scope.URLs = len(scope.URLs)
	summary.Scope.Ports = len(scope.Ports)
	summary.Scope.Excluded = len(scope.Excluded)
	summary.Scope.HasTestingLimits = len(scope.TestingLimits) > 0
	summary.Scope.HasNotes = scope.Notes != ""
	summary.Scope.Ready = namedAssets > 0

	tasks, err := server.tasks.ListForProject(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count tasks")
		return
	}
	factCount, err := server.facts.CountFacts(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count facts")
		return
	}
	summary.Counts.Tasks = len(tasks)
	summary.Counts.Facts = factCount

	writeJSON(response, http.StatusOK, summary)
}

// credentialBindingInput decodes the shared shape used by both global and
// project-scoped PUT handlers.
type credentialBindingInput struct {
	CredentialRef string            `json:"credential_ref"`
	Source        credential.Source `json:"source"`
	Disabled      bool              `json:"disabled"`
}

// writeCredentialError maps credential service errors to HTTP statuses. Today
// every documented service error is a client/validation problem, so all map to
// 400. The helper exists so later non-validation errors can be distinguished
// without touching every handler.
func writeCredentialError(response http.ResponseWriter, err error) {
	writeError(response, http.StatusBadRequest, err.Error())
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
