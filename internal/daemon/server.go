package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardmigration"
	"pentest/internal/blackboardv2"
	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
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
	Version     string
	DBPath      string
	RuntimeRoot string
	// ArtifactRoot contains managed Evidence payloads. Empty defaults to the
	// database directory. EvidenceSourceRoots are the explicit local roots from
	// which authenticated operators may retain payloads.
	ArtifactRoot        string
	EvidenceSourceRoots []string
	SkillsRoot          string
	SandboxImage        string
	ContainerCLI        string
	ListenAddr          string
	// AuthToken gates every mutating route when non-empty. A non-loopback bind
	// refuses to start unless this is set, so a daemon exposed to the network
	// cannot become an unauthenticated control plane. Loopback dev (make dev)
	// leaves it empty, so no enforcement applies.
	AuthToken string
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
	// ModelRefreshClient is the HTTP client used to call upstream /v1/models
	// during Model Catalog Refresh. Nil means http.DefaultClient, which is the
	// production behavior; tests inject a stubbed transport so the refresh API
	// can be exercised end to end without real network traffic.
	ModelRefreshClient *http.Client
}

type Server struct {
	mux                    *http.ServeMux
	version                string
	logger                 *log.Logger
	db                     *store.DB
	projects               *project.Service
	runtimePlugins         *runtimeplugin.Registry
	runtimeExtensions      *runtimeextension.Registry
	profiles               *runtimeprofile.Service
	modelProviders         *modelprovider.Service
	skills                 *skill.Service
	creds                  *credential.Service
	modelRefreshClient     *http.Client
	preflight              *preflight.Service
	tasks                  *task.Service
	harness                *runtime.Harness
	canonicalStore         string
	graph                  *blackboard.GraphService
	blackboardV2           *blackboardv2.Service
	blackboardV2Continuity *blackboardv2.ContinuityService
	// projectInterface is the graph-native Runtime project-interface module
	// (runtime protocol §1). It is wired only while the store epoch has
	// activated the graph Blackboard (graph_v1), so graph data stays dark
	// before the M05 cutover.
	projectInterface       *projectinterface.Service
	projectInterfaceGrants *projectinterface.GrantStore
	runtimeRoot            string
	sandboxImage           string
	containerCLI           string
	listenAddr             string
	authToken              string
	tempSkillsRoot         string
	controlMu              sync.Mutex
	activeControls         map[string]bool
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
	artifactRoot := strings.TrimSpace(config.ArtifactRoot)
	if artifactRoot == "" {
		artifactRoot = filepath.Dir(config.DBPath)
	}
	listenAddr := strings.TrimSpace(config.ListenAddr)
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8787"
	}
	authToken := strings.TrimSpace(config.AuthToken)
	if !isLoopback(listenAddr) && authToken == "" {
		_ = db.Close()
		if tempSkillsRoot != "" {
			_ = os.RemoveAll(tempSkillsRoot)
		}
		return nil, fmt.Errorf("non-loopback bind %q requires an auth token; set -auth-token or PENTEST_AUTH_TOKEN", listenAddr)
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		_ = db.Close()
		if tempSkillsRoot != "" {
			_ = os.RemoveAll(tempSkillsRoot)
		}
		return nil, err
	}
	server := &Server{
		mux:                http.NewServeMux(),
		version:            config.Version,
		logger:             config.Logger,
		db:                 db,
		projects:           project.NewService(db),
		runtimePlugins:     runtimePlugins,
		runtimeExtensions:  runtimeExtensions,
		profiles:           profiles,
		modelProviders:     modelProviders,
		skills:             skills,
		creds:              creds,
		modelRefreshClient: config.ModelRefreshClient,
		preflight: preflight.NewService(profiles, creds, skills).
			WithModelProviders(modelProviders, runtimePlugins).
			WithRuntimeExtensions(runtimeExtensions),
		tasks:          tasks,
		harness:        runtime.NewHarness(tasks),
		canonicalStore: epoch,
		runtimeRoot:    runtimeRoot,
		sandboxImage:   config.SandboxImage,
		containerCLI:   config.ContainerCLI,
		listenAddr:     listenAddr,
		authToken:      authToken,
		tempSkillsRoot: tempSkillsRoot,
		activeControls: map[string]bool{},
	}
	if server.logger == nil {
		server.logger = log.Default()
	}
	server.tasks.SetProjectService(server.projects)
	if epoch == store.CanonicalStoreGraphV1 || epoch == store.CanonicalStoreGraphV1Finalized {
		storeState, stateErr := db.BlackboardStoreState()
		if stateErr != nil {
			_ = server.Close()
			return nil, stateErr
		}
		if epoch == store.CanonicalStoreGraphV1 && storeState.CutoverState != "graph" {
			_ = server.Close()
			return nil, fmt.Errorf("graph Blackboard activation refused while cutover_state=%s; run migration verify or explicit backup recovery for cutover %s", storeState.CutoverState, storeState.CutoverID)
		}
		if epoch == store.CanonicalStoreGraphV1 && storeState.CutoverID != "" && storeState.LatestVerificationHash == "" {
			migration := blackboardmigration.NewService(db, config.DBPath, artifactRoot)
			if _, verifyErr := migration.Execute(context.Background(), blackboardmigration.MigrationRequest{Kind: blackboardmigration.MigrationKindVerify}); verifyErr != nil {
				_ = server.Close()
				return nil, fmt.Errorf("verify committed graph Blackboard cutover before activation: %w", verifyErr)
			}
		}
		graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{}).WithArtifactRoot(artifactRoot)
		server.graph = graph
		server.projectInterfaceGrants = projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
		server.tasks.SetContinuationTerminalMarker(server.projectInterfaceGrants)
		server.projectInterface = projectinterface.NewService(projectinterface.Deps{
			DB: db, Graph: graph, Grants: server.projectInterfaceGrants, Tasks: server.tasks,
			ArtifactRoot: artifactRoot, RuntimeRoot: runtimeRoot,
			OperatorRoots: config.EvidenceSourceRoots,
		})
		server.tasks.SetContinuationReconciler(server.projectInterface)
		if err := server.recoverPinnedContinuationFiles(); err != nil {
			_ = server.Close()
			return nil, err
		}
	} else if epoch == store.CanonicalStoreBlackboardV2 {
		server.projectInterfaceGrants = projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
		server.tasks.SetContinuationTerminalMarker(server.projectInterfaceGrants)
		server.blackboardV2 = blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot, RuntimeRoot: runtimeRoot})
		server.tasks.SetContinuationReconciler(server.blackboardV2)
		server.blackboardV2Continuity = blackboardv2.NewContinuityService(db, server.blackboardV2, server.tasks, runtimeRoot)
		if err := server.recoverBlackboardV2ContinuationFiles(context.Background()); err != nil {
			_ = server.Close()
			return nil, err
		}
	}
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
	if server.projectInterface != nil {
		continuations, listErr := server.tasks.TerminalContinuations()
		if listErr != nil {
			server.logger.Printf("task reconcile: failed to list terminal Continuations: %v", listErr)
			return
		}
		for _, continuation := range continuations {
			if _, reconcileErr := server.projectInterface.ReconcileContinuation(context.Background(), continuation.ID, "daemon_restart"); reconcileErr != nil {
				server.logger.Printf("task reconcile: failed to reconcile Continuation %s: %v", continuation.ID, reconcileErr)
			}
		}
	} else if server.blackboardV2 != nil {
		continuations, listErr := server.tasks.TerminalContinuations()
		if listErr != nil {
			server.logger.Printf("task reconcile: failed to list terminal Continuations: %v", listErr)
			return
		}
		for _, continuation := range continuations {
			if reconcileErr := server.blackboardV2.ReconcileTerminalContinuation(context.Background(), continuation.ID, "daemon_restart"); reconcileErr != nil {
				server.logger.Printf("task reconcile: failed to reconcile Continuation %s: %v", continuation.ID, reconcileErr)
			}
		}
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
	if server.authToken != "" && !server.publicPath(request) {
		if !server.authorized(request) {
			// Project-interface and Blackboard v2 HTTP handlers own their structured
			// credential errors. Let those narrow routes classify a missing/invalid
			// grant; every other API and MCP route remains behind the daemon middleware.
			if !(server.blackboardV2 != nil && isBlackboardV2HTTPTransport(request)) {
				writeError(response, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
	}
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

// authorized reports whether the request carries the configured auth token.
// The token is accepted as either an "Authorization: Bearer <token>" header or
// a "?token=<token>" query parameter; the query form exists so sandbox MCP
// transports that cannot attach per-request headers still authenticate.
func (server *Server) authorized(request *http.Request) bool {
	if server.authToken == "" {
		return true
	}
	if header := strings.TrimSpace(request.Header.Get("Authorization")); header != "" {
		if scheme, token, ok := strings.Cut(header, " "); ok && strings.EqualFold(scheme, "Bearer") {
			if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(server.authToken)) == 1 {
				return true
			}
		}
	}
	if queryToken := request.URL.Query().Get("token"); queryToken != "" {
		if subtle.ConstantTimeCompare([]byte(queryToken), []byte(server.authToken)) == 1 {
			return true
		}
	}
	// A Continuation Interface Grant is a separate, narrower credential from
	// the daemon operator token. Accept it only on Blackboard v2 HTTP and MCP.
	if token := projectinterface.BearerToken(request); token != "" {
		if server.blackboardV2 != nil && server.projectInterfaceGrants != nil &&
			(isBlackboardV2HTTPTransport(request) || request.URL.Path == "/mcp") {
			_, err := server.projectInterfaceGrants.Resolve(request.Context(), token)
			return err == nil
		}
	}
	return false
}

// publicPath reports whether the request targets a route that stays reachable
// without the auth token: the health probe, CORS preflight, and the SPA's static
// assets (which a browser loads before it can attach a header). API and MCP
// routes are never public.
func (server *Server) publicPath(request *http.Request) bool {
	if request.Method == http.MethodOptions {
		return true
	}
	if request.Method == http.MethodGet && request.URL.Path == "/health" {
		return true
	}
	// Every public path below is a static asset the SPA file server serves via
	// GET only, so non-GET requests must go through auth (and then 404/405).
	if request.Method != http.MethodGet {
		return false
	}
	clean := path.Clean(request.URL.Path)
	if strings.HasPrefix(clean, "/assets/") {
		return true
	}
	switch clean {
	case "/", "/index.html":
		return true
	}
	return isStaticAssetPath(clean)
}

// isStaticAssetPath reports whether the cleaned path is a static asset served
// by the SPA file server (favicon, logos, manifest, etc.).
func isStaticAssetPath(clean string) bool {
	ext := strings.ToLower(path.Ext(clean))
	switch ext {
	case ".svg", ".png", ".ico", ".webp", ".woff", ".woff2", ".css", ".js", ".json":
		// Exclude API-shaped JSON (/api/...) and the MCP route: only top-level
		// asset files count as static SPA assets.
		return !strings.HasPrefix(clean, "/api/") && clean != "/mcp"
	}
	return false
}

// isLoopback reports whether the listen address binds only to the local host.
// An empty address defaults to loopback. IPv6 any-addresses ("[::]", "::")
// count as non-loopback so they require an auth token like 0.0.0.0.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = strings.TrimSpace(addr)
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
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
	server.mux.HandleFunc("DELETE /api/projects/{id}/tasks/{task_id}", server.handleDeleteTask)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/events", server.handleTaskEvents)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/transcript", server.handleTaskTranscript)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/timeline", server.handleTaskTimeline)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/stop", server.handleStopTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/resume", server.handleResumeTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/steer/queue", server.handleQueueSteerTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/steer", server.handleSteerTask)
	server.registerBlackboardV2Routes()
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
	var factCount, findingCount, evidenceCount int
	if server.blackboardV2 != nil {
		projection, snapshotErr := server.blackboardV2.ProjectRuntimeSnapshot(request.Context(), found.ID)
		if snapshotErr != nil {
			writeError(response, http.StatusInternalServerError, "read Blackboard snapshot")
			return
		}
		factCount = len(projection.Snapshot.Knowledge.Facts)
		findingCount = len(projection.Snapshot.Knowledge.Findings)
		evidenceCount = len(projection.Snapshot.Knowledge.Evidence)
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
		if strings.HasPrefix(request.URL.Path, "/api/") || request.URL.Path == "/mcp" {
			http.NotFound(response, request)
			return
		}
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
