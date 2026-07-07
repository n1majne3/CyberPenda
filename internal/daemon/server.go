package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/runtime"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/skill"
	"pentest/internal/store"
	"pentest/internal/task"

	"pentest/internal/daemon/webfs"
)

type Config struct {
	Version      string
	DBPath       string
	RuntimeRoot  string
	SkillsRoot   string
	SandboxImage string
	ContainerCLI string
	ListenAddr   string
	// Logger receives request and task-lifecycle log lines. When nil the daemon
	// uses the standard library default logger, so output appears under
	// `make dev` alongside the startup lines.
	Logger *log.Logger
	// RuntimePluginDirs are trusted local directories containing runtime plugin
	// manifest JSON files. Empty means built-ins only.
	RuntimePluginDirs []string
	// RuntimeExtensionDirs are trusted local directories containing runtime
	// extension manifest JSON files. Empty means no external extensions.
	RuntimeExtensionDirs []string
	// SkillImporter is the controlled management-time importer for package-backed
	// skills. Nil means the import endpoint rejects import attempts.
	SkillImporter skill.Importer
	// DisableBuiltinSkills skips packaged built-in Skill seeding. This is used by
	// tests that need an empty Skill library; production leaves built-ins on.
	DisableBuiltinSkills bool
}

type Server struct {
	mux               *http.ServeMux
	version           string
	logger            *log.Logger
	db                *store.DB
	projects          *project.Service
	runtimePlugins    *runtimeplugin.Registry
	runtimeExtensions *runtimeextension.Registry
	profiles          *runtimeprofile.Service
	modelProviders    *modelprovider.Service
	skills            *skill.Service
	creds             *credential.Service
	preflight         *preflight.Service
	tasks             *task.Service
	harness           *runtime.Harness
	facts             *blackboard.Service
	runtimeRoot       string
	sandboxImage      string
	containerCLI      string
	listenAddr        string
	tempSkillsRoot    string
	controlMu         sync.Mutex
	activeControls    map[string]bool
}

func NewServer(config Config) (*Server, error) {
	db, err := store.Open(config.DBPath)
	if err != nil {
		return nil, err
	}

	runtimePlugins, err := runtimePluginRegistry(config.RuntimePluginDirs)
	if err != nil {
		return nil, err
	}
	runtimeExtensions, err := runtimeExtensionRegistry(config.RuntimeExtensionDirs)
	if err != nil {
		return nil, err
	}
	profiles := runtimeprofile.NewService(db, runtimeProfileProviders(runtimePlugins))
	modelProviders := modelprovider.NewService(db)
	skillsRoot := strings.TrimSpace(config.SkillsRoot)
	var tempSkillsRoot string
	if skillsRoot == "" {
		if config.DBPath == "" || config.DBPath == ":memory:" {
			tempSkillsRoot, err = os.MkdirTemp("", "pentest-skills-*")
			if err != nil {
				_ = db.Close()
				return nil, err
			}
			skillsRoot = tempSkillsRoot
		} else {
			skillsRoot = filepath.Join(filepath.Dir(config.DBPath), "skills")
		}
	}
	skillImporter := config.SkillImporter
	if skillImporter == nil {
		skillImporter = skill.NPXImporter{}
	}
	skills := skill.NewService(db, skillsRoot, skillImporter)
	if !config.DisableBuiltinSkills {
		if err := skills.InstallBuiltinSkills(context.Background()); err != nil {
			_ = db.Close()
			if tempSkillsRoot != "" {
				_ = os.RemoveAll(tempSkillsRoot)
			}
			return nil, err
		}
	}
	creds := credential.NewService(db)
	tasks := task.NewService(db, nil)
	runtimeRoot := config.RuntimeRoot
	if runtimeRoot == "" {
		runtimeRoot = filepath.Join(filepath.Dir(config.DBPath), "runs")
	}
	listenAddr := strings.TrimSpace(config.ListenAddr)
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8787"
	}
	server := &Server{
		mux:               http.NewServeMux(),
		version:           config.Version,
		logger:            config.Logger,
		db:                db,
		projects:          project.NewService(db),
		runtimePlugins:    runtimePlugins,
		runtimeExtensions: runtimeExtensions,
		profiles:          profiles,
		modelProviders:    modelProviders,
		skills:            skills,
		creds:             creds,
		preflight: preflight.NewService(profiles, creds, skills).
			WithModelProviders(modelProviders, runtimePlugins).
			WithRuntimeExtensions(runtimeExtensions),
		tasks:          tasks,
		harness:        runtime.NewHarness(tasks),
		facts:          blackboard.NewService(db),
		runtimeRoot:    runtimeRoot,
		sandboxImage:   config.SandboxImage,
		containerCLI:   config.ContainerCLI,
		listenAddr:     listenAddr,
		tempSkillsRoot: tempSkillsRoot,
		activeControls: map[string]bool{},
	}
	if server.logger == nil {
		server.logger = log.Default()
	}
	server.tasks.SetProjectService(server.projects)
	server.routes()
	server.reconcileInterruptedTasks()

	return server, nil
}

// reconcileInterruptedTasks clears ghost tasks left running by a previous
// daemon instance. The harness tracks active runs in memory, so after a
// restart no task can actually be executing; mark them interrupted and emit a
// lifecycle event so the timeline and logs explain the gap. Failures are
// logged but never block startup.
func (server *Server) reconcileInterruptedTasks() {
	reconciled, err := server.tasks.ReconcileInterruptedState()
	if err != nil {
		server.logger.Printf("task reconcile: failed to interrupt stale tasks: %v", err)
		return
	}
	for _, continuation := range reconciled.Continuations {
		server.cleanupStaleContinuationContainer(continuation)
	}
	for _, t := range reconciled.Tasks {
		_, _ = server.tasks.AppendEvent(t.ID, task.EventKindLifecycle, task.EventPayload{
			"phase":  "interrupted",
			"reason": "daemon_restart",
		})
		server.logTask(t, "interrupted", "daemon restart orphaned this task")
	}
	if len(reconciled.Tasks) > 0 {
		server.logger.Printf("task reconcile: %d task(s) interrupted on daemon restart", len(reconciled.Tasks))
	}
}

func (server *Server) cleanupStaleContinuationContainer(continuation task.TaskContinuation) {
	if continuation.Runner != task.RunnerSandbox || strings.TrimSpace(continuation.ContainerID) == "" {
		return
	}
	containerID := strings.TrimSpace(continuation.ContainerID)
	if err := runtime.StopDockerContainer(server.containerCLI, containerID, 2*time.Second); err != nil {
		server.logger.Printf("task reconcile: failed to stop stale container %s for task %s: %v", containerID, continuation.TaskID, err)
		return
	}
	if err := runtime.RemoveDockerContainer(server.containerCLI, containerID); err != nil {
		server.logger.Printf("task reconcile: failed to remove stale container %s for task %s: %v", containerID, continuation.TaskID, err)
		return
	}
	_, _ = server.tasks.AppendEvent(continuation.TaskID, task.EventKindLifecycle, task.EventPayload{
		"phase":        "container_cleaned",
		"reason":       "daemon_restart",
		"container_id": containerID,
	})
}

func runtimePluginRegistry(dirs []string) (*runtimeplugin.Registry, error) {
	plugins := runtimeplugin.BuiltinPlugins()
	var errs []error
	for _, dir := range dirs {
		loaded, loadErrs := runtimeplugin.LoadDirectory(dir)
		plugins = append(plugins, loaded...)
		errs = append(errs, loadErrs...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return runtimeplugin.NewRegistry(plugins)
}

func runtimeExtensionRegistry(dirs []string) (*runtimeextension.Registry, error) {
	var extensions []runtimeextension.Extension
	var errs []error
	for _, dir := range dirs {
		loaded, loadErrs := runtimeextension.LoadDirectory(dir)
		extensions = append(extensions, loaded...)
		errs = append(errs, loadErrs...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return runtimeextension.NewRegistry(extensions)
}

func runtimeProfileProviders(registry *runtimeplugin.Registry) []runtimeprofile.Provider {
	ids := registry.IDs()
	providers := make([]runtimeprofile.Provider, 0, len(ids))
	for _, id := range ids {
		providers = append(providers, runtimeprofile.Provider(id))
	}
	return providers
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	start := time.Now()
	recorder := newStatusRecorder(response)
	server.mux.ServeHTTP(recorder, request)
	server.logRequest(start, request, recorder.status)
}

func (server *Server) Close() error {
	err := server.db.Close()
	if server.tempSkillsRoot != "" {
		if removeErr := os.RemoveAll(server.tempSkillsRoot); err == nil {
			err = removeErr
		}
	}
	return err
}

func (server *Server) routes() {
	server.mux.HandleFunc("GET /health", server.handleHealth)
	server.mux.HandleFunc("GET /api/projects", server.handleListProjects)
	server.mux.HandleFunc("POST /api/projects", server.handleCreateProject)
	server.mux.HandleFunc("GET /api/projects/{id}", server.handleGetProject)
	server.mux.HandleFunc("PATCH /api/projects/{id}", server.handleUpdateProject)
	server.mux.HandleFunc("POST /api/runtime-profiles", server.handleCreateRuntimeProfile)
	server.mux.HandleFunc("POST /api/runtime-profiles/resolve-launch", server.handleResolveLaunchRuntimeProfile)
	server.mux.HandleFunc("GET /api/runtime-profiles", server.handleListRuntimeProfiles)
	server.mux.HandleFunc("GET /api/runtime-profiles/{id}", server.handleGetRuntimeProfile)
	server.mux.HandleFunc("PATCH /api/runtime-profiles/{id}", server.handleUpdateRuntimeProfile)
	server.mux.HandleFunc("POST /api/runtime-profiles/{id}/promote", server.handlePromoteRuntimeProfile)
	server.mux.HandleFunc("DELETE /api/runtime-profiles/{id}", server.handleDeleteRuntimeProfile)
	server.mux.HandleFunc("GET /api/runtime-profiles/{id}/model-provider-migration-preview", server.handlePreviewModelProviderMigration)
	server.mux.HandleFunc("POST /api/runtime-profiles/{id}/model-provider-migration", server.handleApplyModelProviderMigration)
	server.mux.HandleFunc("GET /api/model-providers", server.handleListModelProviders)
	server.mux.HandleFunc("POST /api/model-providers", server.handleCreateModelProvider)
	server.mux.HandleFunc("GET /api/model-providers/{id}", server.handleGetModelProvider)
	server.mux.HandleFunc("PATCH /api/model-providers/{id}", server.handleUpdateModelProvider)
	server.mux.HandleFunc("DELETE /api/model-providers/{id}", server.handleDeleteModelProvider)
	server.mux.HandleFunc("POST /api/model-providers/{id}/refresh-models", server.handleRefreshModelProviderModels)
	server.mux.HandleFunc("GET /api/runtime-plugins", server.handleListRuntimePlugins)
	server.mux.HandleFunc("GET /api/runtime-plugins/{plugin_id}", server.handleGetRuntimePlugin)
	server.mux.HandleFunc("GET /api/runtime-extensions", server.handleListRuntimeExtensions)
	server.mux.HandleFunc("GET /api/runtime-extension-catalog", server.handleListRuntimeExtensionCatalog)
	server.mux.HandleFunc("GET /api/runtime-extensions/{extension_id}", server.handleGetRuntimeExtension)
	server.mux.HandleFunc("GET /api/skills", server.handleListSkills)
	server.mux.HandleFunc("POST /api/skills/import", server.handleImportSkill)
	server.mux.HandleFunc("GET /api/skills/{skill_id}", server.handleGetSkill)
	server.mux.HandleFunc("PUT /api/skills/{skill_id}", server.handlePutSkill)
	server.mux.HandleFunc("DELETE /api/skills/{skill_id}", server.handleDeleteSkill)
	server.mux.HandleFunc("PUT /api/skills/{skill_id}/profiles/{profile_id}/opt-out", server.handlePutSkillProfileOptOut)
	server.mux.HandleFunc("DELETE /api/skills/{skill_id}/profiles/{profile_id}/opt-out", server.handleDeleteSkillProfileOptOut)
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
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/transcript", server.handleTaskTranscript)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/timeline", server.handleTaskTimeline)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/stop", server.handleStopTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/resume", server.handleResumeTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/resume/handoff", server.handleResumeHandoffTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/steer/queue", server.handleQueueSteerTask)
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
	server.mux.HandleFunc("POST /api/projects/{id}/facts/merge", server.handleMergeFacts)
	server.mux.HandleFunc("PUT /api/projects/{id}/findings/{finding_key}", server.handleUpsertFinding)
	server.mux.HandleFunc("GET /api/projects/{id}/findings", server.handleListFindings)
	server.mux.HandleFunc("POST /api/projects/{id}/findings/merge", server.handleMergeFindings)
	server.mux.HandleFunc("GET /api/projects/{id}/findings/{finding_key}/versions", server.handleFindingVersions)
	server.mux.HandleFunc("POST /api/projects/{id}/evidence", server.handleAttachEvidence)
	server.mux.HandleFunc("GET /api/projects/{id}/evidence", server.handleListEvidence)
	server.mux.HandleFunc("POST /api/projects/{id}/report", server.handleReportTrigger)
	server.registerMCP()
	server.registerSPA()
}

func (server *Server) handleHealth(response http.ResponseWriter, request *http.Request) {
	payload := struct {
		Version  string `json:"version"`
		Database struct {
			Status string `json:"status"`
		} `json:"database"`
		MCP struct {
			Status string `json:"status"`
			Path   string `json:"path"`
		} `json:"mcp"`
		Runner struct {
			RuntimeRoot  string `json:"runtime_root"`
			SandboxImage string `json:"sandbox_image"`
			ContainerCLI string `json:"container_cli"`
		} `json:"runner"`
	}{
		Version: server.version,
	}
	payload.Database.Status = "ok"
	payload.MCP.Status = "ok"
	payload.MCP.Path = "/mcp"
	payload.Runner.RuntimeRoot = server.runtimeRoot
	payload.Runner.SandboxImage = server.sandboxImage
	payload.Runner.ContainerCLI = server.containerCLI
	if payload.Runner.ContainerCLI == "" {
		payload.Runner.ContainerCLI = "docker"
	}

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
	sanitized := make([]runtimeprofile.Profile, len(profiles))
	for i, profile := range profiles {
		sanitized[i] = runtimeprofile.SanitizeProfile(profile)
	}
	writeJSON(response, http.StatusOK, struct {
		Profiles []runtimeprofile.Profile `json:"profiles"`
	}{
		Profiles: sanitized,
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

	writeJSON(response, http.StatusCreated, runtimeprofile.SanitizeProfile(created))
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

	writeJSON(response, http.StatusOK, runtimeprofile.SanitizeProfile(found))
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

	writeJSON(response, http.StatusOK, runtimeprofile.SanitizeProfile(updated))
}

func (server *Server) handlePromoteRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	promoted, err := server.profiles.PromoteToPreset(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "promote runtime profile")
		return
	}

	writeJSON(response, http.StatusOK, runtimeprofile.SanitizeProfile(promoted))
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
	writeJSON(response, http.StatusOK, credential.SanitizeBinding(binding))
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
		Bindings: credential.SanitizeBindings(bindings),
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
	writeJSON(response, http.StatusOK, credential.SanitizeBinding(binding))
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
		Bindings: credential.SanitizeBindings(bindings),
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
		RuntimeProfileID        string           `json:"runtime_profile_id"`
		ModelOverride           string           `json:"model_override,omitempty"`
		Runner                  string           `json:"runner"`
		RunControls             task.RunControls `json:"run_controls"`
		CredentialRefsToResolve []string         `json:"credential_refs"`
		HostActivated           bool             `json:"host_activated"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	defaulted, err := server.applyTaskLaunchDefaults(projectID, input.RuntimeProfileID, task.Runner(input.Runner))
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project defaults")
		return
	}

	hostActivated := input.RunControls.HostActivated || input.HostActivated
	result := server.preflight.Run(request.Context(), preflight.Request{
		RuntimeProfileID:        defaulted.runtimeProfileID,
		LaunchModelOverride:     strings.TrimSpace(input.ModelOverride),
		ProjectID:               projectID,
		CredentialRefsToResolve: input.CredentialRefsToResolve,
		Runner:                  string(defaulted.runner),
		HostActivated:           hostActivated,
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
	findingCount, err := server.facts.CountFindings(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count findings")
		return
	}
	evidenceCount, err := server.facts.CountEvidence(found.ID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count evidence")
		return
	}
	summary.Counts.Tasks = len(tasks)
	summary.Counts.Facts = factCount
	summary.Counts.Findings = findingCount
	summary.Counts.Evidence = evidenceCount

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

// registerSPA serves the embedded React build for any non-API, non-health path.
// During development (Vite), the embedded dist is still present but unused; the
// proxy in vite.config.ts routes /api to the daemon instead.
func (server *Server) registerSPA() {
	assets, err := fs.Sub(webfs.Dist, "dist")
	if err != nil {
		// Should not happen: dist is embedded.
		return
	}
	fileServer := http.FileServer(http.FS(assets))
	server.mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		// Serve static assets directly; everything else falls back to
		// index.html so client-side routing works on refresh.
		clean := path.Clean(request.URL.Path)
		if strings.HasPrefix(clean, "/assets/") {
			fileServer.ServeHTTP(response, request)
			return
		}
		// Check if the path maps to a real file (favicon, icons, etc.).
		if f, err := assets.Open(strings.TrimPrefix(clean, "/")); err == nil {
			f.Close()
			fileServer.ServeHTTP(response, request)
			return
		}
		// SPA fallback.
		request.URL.Path = "/"
		fileServer.ServeHTTP(response, request)
	})
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
