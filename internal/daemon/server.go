package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"pentest/internal/blackboard"
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
	mux                *http.ServeMux
	version            string
	logger             *log.Logger
	db                 *store.DB
	projects           *project.Service
	runtimePlugins     *runtimeplugin.Registry
	runtimeExtensions  *runtimeextension.Registry
	profiles           *runtimeprofile.Service
	modelProviders     *modelprovider.Service
	skills             *skill.Service
	creds              *credential.Service
	modelRefreshClient *http.Client
	preflight          *preflight.Service
	tasks              *task.Service
	harness            *runtime.Harness
	facts              *blackboard.Service
	reads              *blackboard.BlackboardReadService
	graph              *blackboard.GraphService
	// projectInterface is the graph-native Runtime project-interface module
	// (runtime protocol §1). It is wired only while the store epoch has
	// activated the graph Blackboard (graph_v1), so graph data stays dark
	// before the M05 cutover.
	projectInterface       *projectinterface.Service
	projectInterfaceGrants *projectinterface.GrantStore
	projectInterfaceHTTP   *projectinterface.HTTPHandler
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
		facts:          blackboard.NewService(db),
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
	epoch, err := db.CanonicalStore()
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	if epoch == store.CanonicalStoreGraphV1 || epoch == store.CanonicalStoreGraphV1Finalized {
		graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{}).WithArtifactRoot(artifactRoot)
		server.graph = graph
		server.reads = blackboard.NewBlackboardReadService(db).WithArtifactRoot(artifactRoot)
		server.projectInterfaceGrants = projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
		server.tasks.SetContinuationTerminalMarker(server.projectInterfaceGrants)
		server.projectInterface = projectinterface.NewService(projectinterface.Deps{
			DB: db, Graph: graph, Grants: server.projectInterfaceGrants, Tasks: server.tasks,
			ArtifactRoot: artifactRoot, RuntimeRoot: runtimeRoot,
			OperatorRoots: config.EvidenceSourceRoots,
		})
		server.tasks.SetContinuationReconciler(server.projectInterface)
		server.projectInterfaceHTTP = projectinterface.NewHTTPHandler(server.projectInterface).
			WithOperatorAuth(server.authToken, server.authToken == "")
		if err := graph.RepairTaskGoals(context.Background()); err != nil {
			_ = server.Close()
			return nil, fmt.Errorf("repair Task Goals at graph startup: %w", err)
		}
		server.tasks.SetGoalProjector(graph)
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
			// Project-interface HTTP handlers own their structured credential
			// errors. Let those narrow routes classify a missing/invalid grant;
			// every other API and MCP route remains behind the daemon middleware.
			if server.projectInterfaceHTTP == nil || !isProjectInterfaceHTTPTransport(request) {
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
	// the daemon operator token. Accept it only on the trusted project-interface
	// transports; the adapter then enforces the bound Project and capability.
	if server.projectInterface != nil && isProjectInterfaceTransport(request) {
		if token := projectinterface.BearerToken(request); token != "" {
			_, err := server.projectInterface.Authenticate(request.Context(), token, "")
			return err == nil
		}
	}
	return false
}

func isProjectInterfaceTransport(request *http.Request) bool {
	if request.URL.Path == "/mcp" {
		return true
	}
	return isProjectInterfaceHTTPTransport(request)
}

func isProjectInterfaceHTTPTransport(request *http.Request) bool {
	if !strings.HasPrefix(request.URL.Path, "/api/projects/") {
		return false
	}
	switch {
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/blackboard/mutations"):
		return true
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/blackboard/records:resolve"):
		return true
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/blackboard/evidence:retain"):
		return true
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/blackboard/runtime-graph"):
		return true
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
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/runtime-graph", server.handleBlackboardRuntimeGraph)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/work-view", server.handleBlackboardWorkView)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/current-truth", server.handleBlackboardCurrentTruth)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/frontier", server.handleBlackboardFrontier)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/records", server.handleBlackboardRecords)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/records:resolve", server.handleBlackboardRecordResolve)
	// Graph-native Runtime project-interface routes (runtime protocol §12.1).
	// Grant-authed; only registered while the graph Blackboard is active. The
	// records:resolve POST coexists with the operator GET by HTTP method.
	if server.projectInterfaceHTTP != nil {
		server.mux.HandleFunc("POST /api/projects/{id}/blackboard/mutations", server.projectInterfaceHTTP.Apply)
		server.mux.HandleFunc("POST /api/projects/{id}/blackboard/records:resolve", server.projectInterfaceHTTP.ResolveRecords)
		server.mux.HandleFunc("POST /api/projects/{id}/blackboard/evidence:retain", server.projectInterfaceHTTP.RetainEvidence)
		server.mux.HandleFunc("POST /api/projects/{id}/blackboard/attempts:checkpoint", server.projectInterfaceHTTP.CheckpointAttempt)
		// net/http patterns cannot suffix a wildcard with ":finish". Capture the
		// final segment and let the adapter validate/remove that exact suffix.
		server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/continuations/{continuation_action}", server.projectInterfaceHTTP.FinishContinuation)
	}
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/records/{node_id}", server.handleBlackboardRecordDetail)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/records/{node_id}/history", server.handleBlackboardRecordHistory)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/records/{node_id}/provenance", server.handleBlackboardRecordProvenance)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/records/{node_id}/traversal", server.handleBlackboardGraphTraversal)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/health", server.handleBlackboardHealth)
	server.mux.HandleFunc("POST /api/projects/{id}/blackboard/health-runs", server.handleStartBlackboardHealthRun)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/health-runs/{run_id}", server.handleBlackboardHealthRun)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/health-runs/{run_id}/results", server.handleBlackboardHealthResults)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/graph-explorer", server.handleBlackboardGraphExplorer)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/entities", server.handleBlackboardEntities)
	server.mux.HandleFunc("GET /api/projects/{id}/blackboard/entities/{node_id}", server.handleBlackboardEntityDetail)
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
	// Graph-native deterministic deliverables (read contract §7.2). Available only
	// while the store epoch has activated BlackboardReadService (graph_v1).
	server.mux.HandleFunc("GET /api/projects/{id}/reports/pentest", server.handlePentestReport)
	server.mux.HandleFunc("GET /api/projects/{id}/reports/ctf-solution", server.handleCTFSolution)
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

func (server *Server) handleBlackboardRuntimeGraph(response http.ResponseWriter, request *http.Request) {
	// Once graph_v1 is active this route is one canonical project-interface
	// transport. The handler owns both grant and operator authentication and
	// returns the same structured envelope (including credential failures) for
	// every caller.
	if server.projectInterfaceHTTP != nil {
		server.projectInterfaceHTTP.CurrentGraph(response, request)
		return
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       request.PathValue("id"),
		Kind:            blackboard.ReadKindCanonicalGraphV1,
	})
}

func (server *Server) handleBlackboardRecords(response http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	limit, ok := parseOptionalReadInt(response, query.Get("limit"), "limit")
	if !ok {
		return
	}
	atRevision, ok := parseOptionalReadIntPointer(response, query.Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	nodeTypes := make([]blackboard.NodeType, 0, len(query["node_type"]))
	for _, value := range query["node_type"] {
		nodeTypes = append(nodeTypes, blackboard.NodeType(value))
	}
	dispositions := make([]blackboard.Disposition, 0, len(query["disposition"]))
	for _, value := range query["disposition"] {
		dispositions = append(dispositions, blackboard.Disposition(value))
	}
	hasEvidence, ok := parseOptionalReadBool(response, query.Get("has_evidence"), "has_evidence")
	if !ok {
		return
	}
	hasContradiction, ok := parseOptionalReadBool(response, query.Get("has_contradiction"), "has_contradiction")
	if !ok {
		return
	}
	frontier, ok := parseOptionalReadBool(response, query.Get("frontier"), "frontier")
	if !ok {
		return
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindRecordCollectionV1, AtRevision: atRevision,
		RecordCollection: &blackboard.RecordCollectionRequest{NodeTypes: nodeTypes, Dispositions: dispositions, Lifecycle: query["lifecycle"], ScopeStatus: query["scope_status"], Severity: query["severity"], EntityKind: query["entity_kind"], ActorType: query["actor_type"], TaskID: query.Get("task_id"), ContinuationID: query.Get("continuation_id"), RuntimeProfileID: query.Get("runtime_profile_id"), Runner: query.Get("runner"), AboutEntityID: query.Get("about_entity_id"), EdgeType: blackboard.EdgeType(query.Get("edge_type")), Direction: query.Get("direction"), HasEvidence: hasEvidence, HasContradiction: hasContradiction, Frontier: frontier, HealthSeverity: query["health_severity"], UpdatedBefore: query.Get("updated_before"), UpdatedAfter: query.Get("updated_after"), Query: query.Get("query"), Sort: query.Get("sort"), Limit: limit, Cursor: query.Get("cursor")},
	})
}

func (server *Server) handleCanonicalBlackboardRead(response http.ResponseWriter, request *http.Request, readRequest blackboard.ReadRequest) {
	if server.reads == nil {
		writeError(response, http.StatusConflict, "graph Blackboard reads are unavailable before graph cutover")
		return
	}
	envelope, err := server.reads.Read(request.Context(), readRequest)
	if err != nil {
		writeBlackboardReadError(response, err)
		return
	}
	etag := `"` + envelope.ProjectionHash + `"`
	response.Header().Set("ETag", etag)
	if readRequest.AtRevision != nil {
		response.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		response.Header().Set("Cache-Control", "private, no-cache")
	}
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(response, http.StatusOK, envelope)
}

func parseOptionalReadBool(response http.ResponseWriter, value, path string) (*bool, bool) {
	if strings.TrimSpace(value) == "" {
		return nil, true
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		writeBlackboardReadError(response, &blackboard.ValidationError{Code: blackboard.ErrCodeInvalidQuery, Message: path + " must be a boolean", OperationIndex: -1, Path: path})
		return nil, false
	}
	return &parsed, true
}

func parseOptionalReadInt(response http.ResponseWriter, value, path string) (int, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		writeBlackboardReadError(response, &blackboard.ValidationError{Code: blackboard.ErrCodeInvalidQuery, Message: path + " must be an integer", OperationIndex: -1, Path: path})
		return 0, false
	}
	return parsed, true
}

func parseOptionalReadIntPointer(response http.ResponseWriter, value, path string) (*int, bool) {
	if strings.TrimSpace(value) == "" {
		return nil, true
	}
	parsed, ok := parseOptionalReadInt(response, value, path)
	if !ok {
		return nil, false
	}
	return &parsed, true
}

func writeBlackboardReadError(response http.ResponseWriter, err error) {
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) {
		writeError(response, http.StatusInternalServerError, "Blackboard read failed")
		return
	}
	status := http.StatusBadRequest
	switch validation.Code {
	case blackboard.ErrCodeProjectNotFound, blackboard.ErrCodeRevisionNotFound, blackboard.ErrCodeRecordNotFound, blackboard.ErrCodeHealthRunNotFound:
		status = http.StatusNotFound
	case blackboard.ErrCodeLiteralIdentityRequired, blackboard.ErrCodeHealthRunInProgress, blackboard.ErrCodeIdempotencyConflict:
		status = http.StatusConflict
	case blackboard.ErrCodeProjectionTooLarge, blackboard.ErrCodeProjectKindMismatch:
		status = http.StatusUnprocessableEntity
	case blackboard.ErrCodeSnapshotUnavailable:
		status = http.StatusServiceUnavailable
	}
	details := validation.Details
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(response, status, struct {
		Error struct {
			ProtocolVersion int            `json:"protocol_version"`
			Code            string         `json:"code"`
			Message         string         `json:"message"`
			OperationIndex  *int           `json:"operation_index"`
			OpID            *string        `json:"op_id"`
			Path            string         `json:"path"`
			Retryable       bool           `json:"retryable"`
			Details         map[string]any `json:"details"`
			RequestID       string         `json:"request_id"`
		} `json:"error"`
	}{Error: struct {
		ProtocolVersion int            `json:"protocol_version"`
		Code            string         `json:"code"`
		Message         string         `json:"message"`
		OperationIndex  *int           `json:"operation_index"`
		OpID            *string        `json:"op_id"`
		Path            string         `json:"path"`
		Retryable       bool           `json:"retryable"`
		Details         map[string]any `json:"details"`
		RequestID       string         `json:"request_id"`
	}{ProtocolVersion: blackboard.BlackboardReadProtocolVersion, Code: validation.Code, Message: validation.Message, Path: validation.Path, Retryable: validation.Retryable, Details: details}})
}

func (server *Server) handleDashboard(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if server.reads != nil {
		envelope, err := server.reads.Read(request.Context(), blackboard.ReadRequest{ProtocolVersion: blackboard.BlackboardReadProtocolVersion, ProjectID: projectID, Kind: blackboard.ReadKindProjectBlackboardSummaryV1, ProjectSummary: &blackboard.ProjectBlackboardSummaryRequest{}})
		if err != nil {
			writeBlackboardReadError(response, err)
			return
		}
		raw, err := json.Marshal(envelope.Result)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "encode dashboard summary")
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			writeError(response, http.StatusInternalServerError, "encode dashboard summary")
			return
		}
		body["_read"] = map[string]any{"protocol_version": envelope.ProtocolVersion, "projection": envelope.Projection, "observed_graph_revision": envelope.ObservedGraphRevision, "observed_state_hash": envelope.ObservedStateHash, "source_pins": envelope.SourcePins, "projection_hash": envelope.ProjectionHash}
		response.Header().Set("ETag", `"`+envelope.ProjectionHash+`"`)
		response.Header().Set("Cache-Control", "private, no-cache")
		if request.Header.Get("If-None-Match") == response.Header().Get("ETag") {
			response.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(response, http.StatusOK, body)
		return
	}
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

func (server *Server) handleBlackboardWorkView(response http.ResponseWriter, request *http.Request) {
	at, ok := parseOptionalReadIntPointer(response, request.URL.Query().Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindBlackboardWorkV1, AtRevision: at, BlackboardWork: &blackboard.BlackboardWorkRequest{}})
}
func (server *Server) handleBlackboardCurrentTruth(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	limit, ok := parseOptionalReadInt(response, q.Get("limit"), "limit")
	if !ok {
		return
	}
	at, ok := parseOptionalReadIntPointer(response, q.Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindCurrentTruthV1, AtRevision: at, CurrentTruth: &blackboard.CurrentTruthRequest{Confidence: q["confidence"], ScopeStatus: q["scope_status"], Category: q.Get("category"), EntityID: q.Get("entity_id"), Query: q.Get("query"), Limit: limit, Cursor: q.Get("cursor")}})
}
func (server *Server) handleBlackboardFrontier(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	limit, ok := parseOptionalReadInt(response, q.Get("limit"), "limit")
	if !ok {
		return
	}
	at, ok := parseOptionalReadIntPointer(response, q.Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindExplorationFrontierV1, AtRevision: at, ExplorationFrontier: &blackboard.ExplorationFrontierRequest{ParentGoalID: q.Get("parent_goal_id"), EntityID: q.Get("entity_id"), Query: q.Get("query"), Limit: limit, Cursor: q.Get("cursor")}})
}
func (server *Server) handleBlackboardRecordResolve(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindRecordResolveV1, RecordResolve: &blackboard.RecordResolveRequest{NodeType: blackboard.NodeType(q.Get("node_type")), StableKey: q.Get("stable_key"), NodeID: q.Get("node_id")}})
}
func (server *Server) handleBlackboardRecordDetail(response http.ResponseWriter, request *http.Request) {
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindRecordDetailV1, RecordDetail: &blackboard.RecordDetailRequest{NodeID: request.PathValue("node_id"), Literal: request.URL.Query().Get("literal") == "true"}})
}
func (server *Server) handleBlackboardRecordHistory(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	limit, ok := parseOptionalReadInt(response, q.Get("limit"), "limit")
	if !ok {
		return
	}
	before, ok := parseOptionalReadInt(response, q.Get("before_version"), "before_version")
	if !ok {
		return
	}
	at, ok := parseOptionalReadIntPointer(response, q.Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindRecordHistoryV1, AtRevision: at, RecordHistory: &blackboard.RecordHistoryRequest{NodeID: request.PathValue("node_id"), Literal: q.Get("literal") == "true", BeforeVersion: before, Limit: limit, Cursor: q.Get("cursor")}})
}
func (server *Server) handleBlackboardEntities(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	limit, ok := parseOptionalReadInt(response, q.Get("limit"), "limit")
	if !ok {
		return
	}
	at, ok := parseOptionalReadIntPointer(response, q.Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	include := q.Get("include_counts") != "false"
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindEntityCollectionV1, AtRevision: at, EntityCollection: &blackboard.EntityCollectionRequest{ParentID: q.Get("parent_id"), AncestorID: q.Get("ancestor_id"), Kind: q.Get("kind"), Status: q.Get("status"), ScopeStatus: q.Get("scope_status"), Query: q.Get("query"), IncludeCounts: &include, Limit: limit, Cursor: q.Get("cursor")}})
}
func (server *Server) handleBlackboardEntityDetail(response http.ResponseWriter, request *http.Request) {
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindEntityDetailV1, EntityDetail: &blackboard.EntityDetailRequest{NodeID: request.PathValue("node_id")}})
}

func (server *Server) handleBlackboardRecordProvenance(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindRecordProvenanceV1, RecordProvenance: &blackboard.RecordProvenanceRequest{NodeID: request.PathValue("node_id"), Version: q.Get("version"), Provenance: q.Get("provenance"), Literal: q.Get("literal") == "true"}})
}

func (server *Server) handleBlackboardGraphTraversal(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	maxDepth, ok := parseOptionalReadInt(response, q.Get("max_depth"), "max_depth")
	if !ok {
		return
	}
	maxNodes, ok := parseOptionalReadInt(response, q.Get("max_nodes"), "max_nodes")
	if !ok {
		return
	}
	at, ok := parseOptionalReadIntPointer(response, q.Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	edgeTypes := make([]blackboard.EdgeType, len(q["edge_type"]))
	for i, value := range q["edge_type"] {
		edgeTypes[i] = blackboard.EdgeType(value)
	}
	nodeTypes := make([]blackboard.NodeType, len(q["node_type"]))
	for i, value := range q["node_type"] {
		nodeTypes[i] = blackboard.NodeType(value)
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindGraphTraversalV1, AtRevision: at, GraphTraversal: &blackboard.GraphTraversalRequest{NodeID: request.PathValue("node_id"), Direction: q.Get("direction"), EdgeTypes: edgeTypes, NodeTypes: nodeTypes, MaxDepth: maxDepth, MaxNodes: maxNodes, IncludeArchived: q.Get("include_archived") == "true", IncludeRetiredEdges: q.Get("include_retired_edges") == "true"}})
}

func (server *Server) handleBlackboardHealth(response http.ResponseWriter, request *http.Request) {
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindBlackboardHealthV1, BlackboardHealth: &blackboard.BlackboardHealthRequest{}})
}

func (server *Server) handleStartBlackboardHealthRun(response http.ResponseWriter, request *http.Request) {
	if server.graph == nil {
		writeError(response, http.StatusConflict, "graph Blackboard is not active")
		return
	}
	type healthRunBody struct {
		SQLiteIntegrity string `json:"sqlite_integrity"`
	}
	body := &healthRunBody{}
	if request.Body != nil {
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var decoded *healthRunBody
		if err := decoder.Decode(&decoded); err != nil && !errors.Is(err, io.EOF) {
			writeBlackboardReadError(response, &blackboard.ValidationError{Code: blackboard.ErrCodeInvalidQuery, Message: "invalid Health run request", OperationIndex: -1, Path: "body"})
			return
		} else if err == nil {
			if decoded == nil {
				writeBlackboardReadError(response, &blackboard.ValidationError{Code: blackboard.ErrCodeInvalidQuery, Message: "Health run request must be a JSON object", OperationIndex: -1, Path: "body"})
				return
			}
			body = decoded
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			writeBlackboardReadError(response, &blackboard.ValidationError{Code: blackboard.ErrCodeInvalidQuery, Message: "Health run request must contain one JSON object", OperationIndex: -1, Path: "body"})
			return
		}
	}
	action, err := server.graph.StartHealthRun(request.Context(), request.PathValue("id"), request.Header.Get("Idempotency-Key"), body.SQLiteIntegrity)
	if err != nil {
		writeBlackboardReadError(response, err)
		return
	}
	if action.Created {
		go func(projectID, runID, integrity string) {
			if _, err := server.graph.CompleteHealthRun(context.Background(), projectID, runID, integrity); err != nil && server.logger != nil {
				server.logger.Printf("Blackboard Health run %s failed: %v", runID, err)
			}
		}(request.PathValue("id"), action.RunID, body.SQLiteIntegrity)
	}
	writeJSON(response, http.StatusAccepted, struct {
		ProtocolVersion int    `json:"protocol_version"`
		RunID           string `json:"run_id"`
		Status          string `json:"status"`
		StatusURL       string `json:"status_url"`
	}{ProtocolVersion: 1, RunID: action.RunID, Status: action.Status, StatusURL: "/api/projects/" + request.PathValue("id") + "/blackboard/health-runs/" + action.RunID})
}

func (server *Server) handleBlackboardHealthRun(response http.ResponseWriter, request *http.Request) {
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindHealthRunV1, HealthRun: &blackboard.HealthRunRequest{RunID: request.PathValue("run_id")}})
}

func (server *Server) handleBlackboardHealthResults(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	limit, ok := parseOptionalReadInt(response, q.Get("limit"), "limit")
	if !ok {
		return
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindHealthResultsV1, HealthResults: &blackboard.HealthResultsRequest{RunID: request.PathValue("run_id"), Severity: q["severity"], Code: q["code"], SubjectKind: q.Get("subject_kind"), SubjectID: q.Get("subject_id"), Limit: limit, Cursor: q.Get("cursor")}})
}

func (server *Server) handleBlackboardGraphExplorer(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	maxDepth, ok := parseOptionalReadInt(response, q.Get("max_depth"), "max_depth")
	if !ok {
		return
	}
	maxNodes, ok := parseOptionalReadInt(response, q.Get("max_nodes"), "max_nodes")
	if !ok {
		return
	}
	maxEdges, ok := parseOptionalReadInt(response, q.Get("max_edges"), "max_edges")
	if !ok {
		return
	}
	at, ok := parseOptionalReadIntPointer(response, q.Get("at_revision"), "at_revision")
	if !ok {
		return
	}
	edgeTypes := make([]blackboard.EdgeType, len(q["edge_type"]))
	for i, value := range q["edge_type"] {
		edgeTypes[i] = blackboard.EdgeType(value)
	}
	nodeTypes := make([]blackboard.NodeType, len(q["node_type"]))
	for i, value := range q["node_type"] {
		nodeTypes[i] = blackboard.NodeType(value)
	}
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: request.PathValue("id"), Kind: blackboard.ReadKindGraphExplorerV1, AtRevision: at, GraphExplorer: &blackboard.GraphExplorerRequest{SeedNodeIDs: q["seed_node_id"], NodeTypes: nodeTypes, EdgeTypes: edgeTypes, Lifecycle: q["lifecycle"], ScopeStatus: q["scope_status"], EntityKind: q["entity_kind"], Direction: q.Get("direction"), MaxDepth: maxDepth, Query: q.Get("query"), IncludeArchived: q.Get("include_archived") == "true", IncludeRetiredEdges: q.Get("include_retired_edges") == "true", MaxNodes: maxNodes, MaxEdges: maxEdges}})
}

// handlePentestReport serves GET /api/projects/{id}/reports/pentest from the
// shared BlackboardReadService. Activation remains behind the store epoch:
// handleCanonicalBlackboardRead returns conflict while reads is nil.
func (server *Server) handlePentestReport(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	// Empty format follows the projection default (json). The bundled UI always
	// requests format=markdown explicitly for operator previews.
	format := q.Get("format")
	scopeContext := q.Get("scope_context")
	if scopeContext == "" {
		scopeContext = "current"
	}
	includeTrue := true
	includeUnresolved := false
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       request.PathValue("id"),
		Kind:            blackboard.ReadKindPentestReportV1,
		PentestReport: &blackboard.PentestReportRequest{
			Format:                format,
			ScopeContext:          scopeContext,
			IncludeUnconfirmed:    &includeTrue,
			IncludeTentativeFacts: &includeTrue,
			IncludeUnresolvedWork: &includeUnresolved,
		},
	})
}

// handleCTFSolution serves GET /api/projects/{id}/reports/ctf-solution.
func (server *Server) handleCTFSolution(response http.ResponseWriter, request *http.Request) {
	q := request.URL.Query()
	// Empty format follows the projection default (json).
	format := q.Get("format")
	includeTrue := true
	server.handleCanonicalBlackboardRead(response, request, blackboard.ReadRequest{
		ProtocolVersion: blackboard.BlackboardReadProtocolVersion,
		ProjectID:       request.PathValue("id"),
		Kind:            blackboard.ReadKindCTFSolutionV1,
		CTFSolution: &blackboard.CTFSolutionRequest{
			Format:            format,
			IncludeCandidates: &includeTrue,
			IncludeProcedure:  &includeTrue,
		},
	})
}
