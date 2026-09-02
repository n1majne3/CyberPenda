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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/challengeworkflow"
	"pentest/internal/credential"
	"pentest/internal/finishreadiness"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/scopeexpansion"
	"pentest/internal/session"
	"pentest/internal/skill"
	"pentest/internal/steering"
	"pentest/internal/store"
	"pentest/internal/task"
	"pentest/internal/workinggraph"

	"pentest/internal/daemon/webfs"
)

type Config struct {
	Version     string
	DBPath      string
	RuntimeRoot string
	// SessionRoot is the managed data root beneath which one Workdir is created
	// for each Non-Project Session. Empty defaults to RuntimeRoot/sessions.
	SessionRoot string
	// ArtifactRoot contains managed Evidence payloads. Empty defaults to the
	// database directory. EvidenceSourceRoots are the explicit local roots from
	// which authenticated operators may retain payloads.
	ArtifactRoot        string
	EvidenceSourceRoots []string
	SkillsRoot          string
	SandboxImage        string
	ContainerCLI        string
	TaskVolume          string
	TaskVolumeRoot      string
	ListenAddr          string
	// AuthToken gates every mutating route when non-empty. A non-loopback bind
	// refuses to start unless this is set, so a daemon exposed to the network
	// cannot become an unauthenticated control plane. Loopback dev (make dev)
	// leaves it empty, so ordinary routes stay open. Operator workflows that can
	// mutate Blackboard still require the generated operator capability.
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
	// during Model Catalog Refresh. Nil uses a bounded production client; tests
	// inject a controlled transport.
	ModelRefreshClient *http.Client
	// ProviderSessionFactory is optional. When nil, launches retain the legacy
	// one-shot Adapter path and native session controls remain unavailable until
	// a real bridge factory is configured.
	ProviderSessionFactory ProviderSessionFactory
	// ChallengePlatforms are explicit protocol adapters keyed by the operator-
	// visible Platform name. An empty map disables Challenge Workflow calls.
	ChallengePlatforms map[string]challengeworkflow.PlatformAdapter
}

type Server struct {
	mux                     *http.ServeMux
	version                 string
	logger                  *log.Logger
	db                      *store.DB
	projects                *project.Service
	scopeExpansions         *scopeexpansion.Service
	runtimePlugins          *runtimeplugin.Registry
	runtimeExtensions       *runtimeextension.Registry
	profiles                *runtimeprofile.Service
	modelProviders          *modelprovider.Service
	capabilityCache         *modelprovider.CapabilityCache
	skills                  *skill.Service
	creds                   *credential.Service
	modelRefreshClient      *http.Client
	preflight               *preflight.Service
	tasks                   *task.Service
	sessions                *session.Service
	steering                *steering.Service
	harness                 *runtime.Harness
	sessionHarness          *runtime.SessionHarness
	canonicalStore          string
	blackboardV2            *blackboardv2.Service
	workingGraph            *workinggraph.Service
	workingGraphCompiler    *workinggraph.SemanticCompiler
	challengeWorkflow       *challengeworkflow.Service
	finishReadiness         *finishreadiness.Service
	blackboardV2Continuity  *blackboardv2.ContinuityService
	projectInterfaceGrants  *projectinterface.GrantStore
	runtimeRoot             string
	sessionRoot             string
	sandboxImage            string
	containerCLI            string
	taskVolume              string
	taskVolumeRoot          string
	listenAddr              string
	authToken               string
	operatorToken           string
	generatedOperatorToken  bool
	tempSkillsRoot          string
	controlMu               sync.Mutex
	activeControls          map[string]bool
	providerControlCtx      context.Context
	providerControlCancel   context.CancelFunc
	providerControlWG       sync.WaitGroup
	providerTaskContexts    map[string]context.Context
	providerTaskCancels     map[string]context.CancelFunc
	activeProviderControls  map[string]bool
	queuedProviderControls  map[string]int
	closing                 bool
	providerSessions        *providerSessionRegistry
	sessionProviderSessions *providerSessionRegistry
	providerSessionFactory  ProviderSessionFactory
	runtimeRecoveryMu       sync.RWMutex
	runtimeRecovery         map[string]task.RuntimeActivity
	runtimeStopTimeout      time.Duration
	steeringDispatchTimeout time.Duration
}

func NewServer(config Config) (*Server, error) {
	runtimeRoot, taskVolumeRoot, err := resolveRuntimeStorage(config)
	if err != nil {
		return nil, err
	}
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
	// Managed Config Keys live in the Runtime Plugin Manifests; inject them so
	// Profile Config Import policy stays declared data, not import-side
	// branching.
	managedDeclarations := map[runtimeprofile.Provider][]runtimeprofile.ManagedKeyDeclaration{}
	for _, id := range runtimePlugins.IDs() {
		plugin, ok := runtimePlugins.Get(id)
		if !ok || len(plugin.ConfigProjection.ManagedKeys) == 0 {
			continue
		}
		declarations := make([]runtimeprofile.ManagedKeyDeclaration, 0, len(plugin.ConfigProjection.ManagedKeys))
		for _, managed := range plugin.ConfigProjection.ManagedKeys {
			declarations = append(declarations, runtimeprofile.ManagedKeyDeclaration{
				Key:       managed.Key,
				Field:     managed.Field,
				Condition: managed.Condition,
			})
		}
		managedDeclarations[runtimeprofile.Provider(plugin.ID)] = declarations
	}
	profiles.SetManagedKeyDeclarations(managedDeclarations)
	profiles.SetKnownInstallRefs(func() []string {
		var refs []string
		for _, ext := range runtimeExtensions.List() {
			if ref := strings.TrimSpace(ext.Config["install_ref"]); ref != "" {
				refs = append(refs, ref)
			}
		}
		return refs
	})
	modelProviders := modelprovider.NewService(db)
	capabilityOverlayDir := ""
	if config.DBPath != "" && config.DBPath != ":memory:" {
		capabilityOverlayDir = filepath.Dir(config.DBPath)
	}
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
		skillImporter = skill.NPXSkillsImporter{}
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
	projects := project.NewService(db)
	tasks := task.NewService(db, nil)
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
	operatorToken := authToken
	generatedOperatorToken := false
	if operatorToken == "" {
		operatorToken, err = (projectinterface.RandomTokenSource{}).NewToken()
		if err != nil {
			_ = db.Close()
			if tempSkillsRoot != "" {
				_ = os.RemoveAll(tempSkillsRoot)
			}
			return nil, fmt.Errorf("generate loopback operator token: %w", err)
		}
		generatedOperatorToken = true
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		_ = db.Close()
		if tempSkillsRoot != "" {
			_ = os.RemoveAll(tempSkillsRoot)
		}
		return nil, err
	}
	providerControlCtx, providerControlCancel := context.WithCancel(context.Background())
	modelRefreshClient := config.ModelRefreshClient
	if modelRefreshClient == nil {
		modelRefreshClient = modelprovider.NewCatalogHTTPClient()
	}
	capabilityCache := modelprovider.LoadCapabilityCache(capabilityOverlayDir, modelRefreshClient)
	server := &Server{
		mux:                http.NewServeMux(),
		version:            config.Version,
		logger:             config.Logger,
		db:                 db,
		projects:           projects,
		scopeExpansions:    scopeexpansion.NewService(db, projects),
		runtimePlugins:     runtimePlugins,
		runtimeExtensions:  runtimeExtensions,
		profiles:           profiles,
		modelProviders:     modelProviders,
		capabilityCache:    capabilityCache,
		skills:             skills,
		creds:              creds,
		modelRefreshClient: modelRefreshClient,
		preflight: preflight.NewService(profiles, creds, skills).
			WithModelProviders(modelProviders, runtimePlugins).
			WithRuntimeExtensions(runtimeExtensions).
			WithCapabilityCache(capabilityCache),
		tasks:                   tasks,
		steering:                steering.NewService(db),
		sessionRoot:             sessionRoot(config, runtimeRoot),
		harness:                 runtime.NewHarness(tasks),
		canonicalStore:          epoch,
		runtimeRoot:             runtimeRoot,
		sandboxImage:            config.SandboxImage,
		containerCLI:            config.ContainerCLI,
		taskVolume:              strings.TrimSpace(config.TaskVolume),
		taskVolumeRoot:          taskVolumeRoot,
		listenAddr:              listenAddr,
		authToken:               authToken,
		operatorToken:           operatorToken,
		generatedOperatorToken:  generatedOperatorToken,
		tempSkillsRoot:          tempSkillsRoot,
		activeControls:          map[string]bool{},
		providerControlCtx:      providerControlCtx,
		providerControlCancel:   providerControlCancel,
		providerTaskContexts:    map[string]context.Context{},
		providerTaskCancels:     map[string]context.CancelFunc{},
		activeProviderControls:  map[string]bool{},
		queuedProviderControls:  map[string]int{},
		providerSessions:        newProviderSessionRegistry(),
		sessionProviderSessions: newProviderSessionRegistry(),
		providerSessionFactory:  config.ProviderSessionFactory,
		runtimeRecovery:         map[string]task.RuntimeActivity{},
		runtimeStopTimeout:      10 * time.Second,
		steeringDispatchTimeout: defaultAcceptedSteeringDispatchTimeout,
	}
	server.sessions = session.NewService(db, server.sessionRoot)
	server.sessionHarness = runtime.NewSessionHarness(server.sessions)
	if server.logger == nil {
		server.logger = log.Default()
	}
	if err := server.sessions.CleanupDeletedWorkdirs(); err != nil {
		server.logger.Printf("Session cleanup retry: %v", err)
	}
	server.tasks.SetProjectService(server.projects)
	if epoch != store.CanonicalStoreBlackboardV2 {
		_ = server.Close()
		return nil, fmt.Errorf("daemon requires canonical store %q, got %q", store.CanonicalStoreBlackboardV2, epoch)
	}
	server.projectInterfaceGrants = projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
	server.tasks.SetContinuationTerminalMarker(server.projectInterfaceGrants)
	server.sessions.SetContinuationTerminalMarker(server.projectInterfaceGrants)
	server.blackboardV2 = blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot, RuntimeRoot: runtimeRoot})
	server.workingGraph = workinggraph.NewService(db)
	server.workingGraphCompiler = workinggraph.NewSemanticCompiler(server.blackboardV2)
	server.challengeWorkflow = challengeworkflow.NewService(db, server.projects, server.tasks, config.ChallengePlatforms, challengeworkflow.NewBlackboardRecorder(server.blackboardV2, server.tasks, runtimeRoot))
	server.finishReadiness = finishreadiness.NewService(db, server.tasks)
	server.tasks.SetContinuationReconciler(server.blackboardV2)
	server.blackboardV2Continuity = blackboardv2.NewContinuityService(db, server.blackboardV2, server.tasks, runtimeRoot)
	if err := server.recoverBlackboardV2ContinuationFiles(context.Background()); err != nil {
		_ = server.Close()
		return nil, err
	}
	challengeRecoveryContext, cancelChallengeRecovery := context.WithTimeout(context.Background(), 2*time.Second)
	for _, failure := range server.challengeWorkflow.Recover(challengeRecoveryContext) {
		server.logger.Printf("Challenge operation recovery pending: task=%s operation=%s kind=%s error=%s", failure.TaskID, failure.OperationID, failure.Kind, failure.Error)
	}
	cancelChallengeRecovery()
	// Import baseline uses the same resolved projection as the editor seed
	// and merged preview (issue #226: client cannot supply the baseline).
	// The provenance list names the credential-generated paths so import
	// enforces placeholder integrity without guessing from a sentinel.
	profiles.SetImportBaselineProvenance(func(profile runtimeprofile.Profile) (string, []string, error) {
		req := server.previewProjectionRequest(profile)
		text, err := runner.StructuredProjectedConfigTextWith(profile.Provider, profile, req)
		if err != nil {
			return "", nil, err
		}
		var generated []string
		for _, name := range req.CredentialEnvNames {
			generated = append(generated, "env."+name)
		}
		for _, name := range runner.InlineAPIKeyEnvNames(profile) {
			generated = append(generated, "env."+name)
		}
		if req.ModelSnapshot != nil && strings.TrimSpace(req.ModelSnapshot.APIKeyEnv) != "" {
			generated = append(generated, "env."+req.ModelSnapshot.APIKeyEnv)
		}
		return text, generated, nil
	})
	server.routes()
	server.reconcileInterruptedTasks(nil)
	server.reconcileInterruptedSessions(nil)
	server.recoverAcceptedSteering(context.Background())

	return server, nil
}

// reconcileInterruptedSessions closes the in-memory ownership gap after a
// daemon restart. Provider bridges are intentionally not reopened implicitly;
// every durable open Session continuation is marked interrupted and the next
// user message/resume request creates a fresh continuation while retaining the
// Session identity and prior native-runtime metadata.
func (server *Server) reconcileInterruptedSessions(excludedIDs ...[]string) {
	if server.sessions == nil {
		return
	}
	excluded := map[string]bool{}
	if len(excludedIDs) > 0 {
		for _, id := range excludedIDs[0] {
			excluded[id] = true
		}
	}
	open, err := server.sessions.List(session.LifecycleOpen)
	if err != nil {
		server.logger.Printf("Session reconcile: failed to list open Sessions: %v", err)
		return
	}
	for _, found := range open {
		if excluded[found.ID] {
			continue
		}
		active, activeErr := server.sessions.ActiveContinuation(found.ID)
		if activeErr != nil {
			server.logger.Printf("Session reconcile: failed to inspect Session %s: %v", found.ID, activeErr)
			continue
		}
		if active == nil {
			continue
		}
		if _, statusErr := server.sessions.UpdateContinuationStatus(active.ID, session.RuntimeStatusInterrupted); statusErr != nil {
			server.logger.Printf("Session reconcile: failed to interrupt continuation %s: %v", active.ID, statusErr)
			continue
		}
		payload := session.EventPayload{
			"phase":           "provider_session_recovery_required",
			"reason":          "daemon_restart",
			"recovery_state":  "failed_closed",
			"next_action":     "resume_creates_fresh_continuation",
			"continuation_id": active.ID,
		}
		if active.NativeSessionID != "" {
			payload["native_session_id"] = active.NativeSessionID
		}
		if active.NativeSessionPath != "" {
			payload["native_session_path"] = active.NativeSessionPath
		}
		_, _ = server.sessions.AppendEvent(found.ID, session.EventKindLifecycle, payload)
	}
}

func resolveRuntimeStorage(config Config) (string, string, error) {
	runtimeRoot := strings.TrimSpace(config.RuntimeRoot)
	if runtimeRoot == "" {
		runtimeRoot = filepath.Join(filepath.Dir(config.DBPath), "runs")
	}
	runtimeRoot = filepath.Clean(runtimeRoot)
	if strings.TrimSpace(config.TaskVolume) == "" {
		return runtimeRoot, "", nil
	}

	taskVolumeRoot := strings.TrimSpace(config.TaskVolumeRoot)
	if taskVolumeRoot == "" {
		taskVolumeRoot = "/data"
	}
	taskVolumeRoot = filepath.Clean(taskVolumeRoot)
	absRuntimeRoot, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve runtime root: %w", err)
	}
	absTaskVolumeRoot, err := filepath.Abs(taskVolumeRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve task volume root: %w", err)
	}
	relativeRuntimeRoot, err := filepath.Rel(absTaskVolumeRoot, absRuntimeRoot)
	if err != nil || relativeRuntimeRoot == ".." || strings.HasPrefix(relativeRuntimeRoot, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("runtime root %q is outside task volume root %q", runtimeRoot, taskVolumeRoot)
	}
	return runtimeRoot, taskVolumeRoot, nil
}

func sessionRoot(config Config, runtimeRoot string) string {
	if configured := strings.TrimSpace(config.SessionRoot); configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(runtimeRoot, "sessions")
}

// reconcileInterruptedTasks clears ghost tasks left running by a previous
// daemon instance. The harness tracks active runs in memory, so after a
// restart no task can actually be executing; mark them interrupted and emit a
// lifecycle event so the timeline and logs explain the gap. Failures are
// logged but never block startup.
func (server *Server) reconcileInterruptedTasks(lifecycleProtectedTaskIDs []string) {
	reconciled, err := server.tasks.ReconcileInterruptedStateExcept(lifecycleProtectedTaskIDs)
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
		// The old in-memory bridge cannot be safely reopened after daemon
		// restart. Keep durable provider metadata on the interrupted
		// Continuation and make the required explicit recovery state visible;
		// Resume will create the fresh Continuation pin.
		latest, latestErr := server.tasks.LatestContinuation(t.ID)
		if latestErr == nil && latest != nil && supportedProviderSessionFactoryProvider(runtimeprofile.Provider(latest.RuntimeProvider)) {
			recovery := task.EventPayload{
				"phase": "provider_session_recovery_required", "reason": "daemon_restart",
				"recovery_state": "failed_closed", "next_action": "resume_creates_fresh_continuation",
			}
			if latest.NativeSessionID != "" {
				recovery["native_session_id"] = latest.NativeSessionID
			}
			if latest.NativeSessionPath != "" {
				recovery["native_session_path"] = latest.NativeSessionPath
			}
			_, _ = server.tasks.AppendEvent(t.ID, task.EventKindLifecycle, recovery)
		}
		server.logTask(t, "interrupted", "daemon restart orphaned this task")
	}
	if len(reconciled.Tasks) > 0 {
		server.logger.Printf("task reconcile: %d task(s) interrupted on daemon restart", len(reconciled.Tasks))
	}
	if server.blackboardV2 != nil {
		continuations, listErr := server.tasks.TerminalContinuations()
		if listErr != nil {
			server.logger.Printf("task reconcile: failed to list terminal Continuations: %v", listErr)
			return
		}
		for _, continuation := range continuations {
			owner, ownerErr := server.tasks.Get(continuation.TaskID)
			if ownerErr != nil {
				server.logger.Printf("task reconcile: failed to load Task %s for terminal Continuation %s: %v", continuation.TaskID, continuation.ID, ownerErr)
				continue
			}
			if owner.RunControls.BlackboardMode == task.BlackboardModeDisabled {
				continue
			}
			if reconcileErr := server.blackboardV2.ReconcileTerminalContinuation(context.Background(), continuation.ID, "daemon_restart"); reconcileErr != nil {
				server.logger.Printf("task reconcile: failed to reconcile Continuation %s: %v", continuation.ID, reconcileErr)
			}
		}
	}
}

func (server *Server) cleanupStaleContinuationContainer(continuation task.TaskContinuation) {
	identity := strings.TrimSpace(continuation.ContainerID)
	if identity == "" {
		return
	}
	if continuation.Runner == task.RunnerHost {
		pgid, ok := runtime.ParseHostProcessGroupID(identity)
		if !ok {
			return
		}
		if err := runtime.KillHostProcessGroup(context.Background(), pgid); err != nil {
			server.logger.Printf("task reconcile: failed to kill host process group %d for task %s: %v", pgid, continuation.TaskID, err)
			return
		}
		_, _ = server.tasks.AppendEvent(continuation.TaskID, task.EventKindLifecycle, task.EventPayload{
			"phase":  "host_process_group_cleaned",
			"reason": "daemon_restart",
			"pgid":   pgid,
		})
		return
	}
	if continuation.Runner != task.RunnerSandbox {
		return
	}
	containerID := identity
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
	// Reject DNS-rebinding before anything else. A malicious web page can rebind
	// its own domain to 127.0.0.1 and then send same-origin-looking requests to
	// the unauthenticated loopback daemon, but the browser still stamps those
	// requests with the page's real (foreign) Origin. Any request carrying a
	// non-loopback Origin is therefore a cross-site/rebinding attempt and is
	// refused before routing. Requests with no Origin (the local UI's same-origin
	// GETs, the sandbox runtime, CLI clients, synthetic test requests) are
	// unaffected, so this guard is invisible to legitimate local callers.
	if !server.allowedOrigin(request) {
		writeError(response, http.StatusForbidden, "forbidden origin")
		return
	}
	if server.authToken != "" && !server.publicPath(request) {
		if !server.authorized(request) {
			// Blackboard v2 handlers own their narrower Continuation capability
			// checks. Every other API remains behind this middleware.
			handlerOwnsCapability := server.blackboardV2 != nil && isBlackboardV2HTTPTransport(request)
			if !handlerOwnsCapability {
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

// ListenAddr is the bind address used to project Runtime MCP and API URLs.
func (server *Server) ListenAddr() string {
	return server.listenAddr
}

// GeneratedOperatorAccessURL returns the one startup URL that transfers a
// generated loopback operator bearer capability to the browser. An explicitly
// configured daemon token is never returned or logged by this seam.
func (server *Server) GeneratedOperatorAccessURL() string {
	if !server.generatedOperatorToken || server.operatorToken == "" {
		return ""
	}
	return "http://" + server.listenAddr + "/?token=" + url.QueryEscape(server.operatorToken)
}

func (server *Server) Close() error {
	server.controlMu.Lock()
	server.closing = true
	server.providerControlCancel()
	server.controlMu.Unlock()
	server.providerControlWG.Wait()
	var sessionRuns []session.Session
	if server.sessions != nil && server.sessionHarness != nil {
		var listErr error
		sessionRuns, listErr = server.sessions.List(session.LifecycleOpen)
		if listErr != nil && !strings.Contains(strings.ToLower(listErr.Error()), "database is closed") {
			server.logger.Printf("Session Runtime shutdown: failed to list open Sessions: %v", listErr)
		}
		for _, found := range sessionRuns {
			server.sessionHarness.Stop(found.ID)
		}
	}
	err := server.providerSessions.closeAll(context.Background())
	if sessionErr := server.sessionProviderSessions.closeAll(context.Background()); err == nil {
		err = sessionErr
	}
	if server.sessionHarness != nil {
		deadline := time.Now().Add(server.runtimeStopTimeout)
		for _, found := range sessionRuns {
			if !server.sessionHarness.StopAndWait(found.ID, time.Until(deadline)) && err == nil {
				err = fmt.Errorf("Session Runtime shutdown timed out for %s", found.ID)
			}
		}
	}
	if dbErr := server.db.Close(); err == nil {
		err = dbErr
	}
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
	// the daemon operator token. Accept it only on Blackboard v2 HTTP.
	if token := projectinterface.BearerToken(request); token != "" {
		if server.blackboardV2 != nil && server.projectInterfaceGrants != nil &&
			isBlackboardV2HTTPTransport(request) {
			_, err := server.projectInterfaceGrants.Resolve(request.Context(), token)
			return err == nil
		}
	}
	return false
}

// requireOperatorAuthority protects local operator workflows that can mutate
// Blackboard outside the versioned Project Interface. The Actor header is
// provenance only; it never authenticates the caller.
func (server *Server) requireOperatorAuthority(response http.ResponseWriter, request *http.Request) bool {
	token := projectinterface.BearerToken(request)
	if token == "" || server.operatorToken == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(server.operatorToken)) != 1 {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
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

// dockerInternalHost is the host gateway the sandbox runtime uses to reach the
// daemon from inside its container. It is a fixed, non-attacker-controllable
// name (a browser stamps Origin/Host from the page origin, so a rebinding page
// can never forge it), which is why the MCP SDK's built-in localhost protection
// is disabled and this allowlist admits it explicitly.
const dockerInternalHost = "host.docker.internal"

// allowedOrigin reports whether the request's Origin header is acceptable. The
// daemon serves only the local operator's UI (same-origin, loopback) plus the
// sandbox runtime and CLI clients, which send no Origin at all. A present Origin
// that is not a loopback origin signals a DNS-rebinding or cross-site request: a
// malicious page rebinds its domain to 127.0.0.1, but the browser still stamps
// the request with the page's real foreign Origin. An absent Origin is allowed
// because browsers always attach one to the cross-site/rebinding requests this
// guard targets, while local non-browser clients and same-origin GETs omit it.
func (server *Server) allowedOrigin(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	host := originHost(origin)
	if host == "" {
		return false
	}
	if isLoopback(host) {
		return true
	}
	if strings.EqualFold(hostOnly(host), dockerInternalHost) {
		return true
	}
	return hostsSame(host, server.listenAddr)
}

// originHost extracts the host[:port] from an Origin header value such as
// "http://127.0.0.1:8787" or "http://[::1]:8787". It returns "" for the opaque
// "null" origin or any value that cannot be parsed, both of which are rejected.
func originHost(origin string) string {
	if strings.EqualFold(origin, "null") {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// hostOnly strips an optional port and IPv6 brackets from a host[:port] string.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return strings.TrimSpace(strings.Trim(host, "[]"))
}

// hostsSame reports whether two host[:port] strings name the same host,
// ignoring port and case.
func hostsSame(a, b string) bool {
	a, b = hostOnly(a), hostOnly(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
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
	server.mux.HandleFunc("GET /api/workspace/navigation", server.handleWorkspaceNavigation)
	server.mux.HandleFunc("GET /api/projects", server.handleListProjects)
	server.mux.HandleFunc("POST /api/projects", server.handleCreateProject)
	server.mux.HandleFunc("GET /api/projects/{id}/scope-expansions", server.handleListScopeExpansions)
	server.mux.HandleFunc("POST /api/projects/{id}/scope-expansions", server.handleProposeScopeExpansion)
	server.mux.HandleFunc("POST /api/projects/{id}/scope-expansions/{expansion_id}/approve", server.handleApproveScopeExpansion)
	server.mux.HandleFunc("POST /api/projects/{id}/scope-expansions/{expansion_id}/reject", server.handleRejectScopeExpansion)
	server.mux.HandleFunc("GET /api/projects/{id}", server.handleGetProject)
	server.mux.HandleFunc("PATCH /api/projects/{id}", server.handleUpdateProject)
	server.mux.HandleFunc("POST /api/projects/{id}/kind-conversion/preview", server.handlePreviewProjectKindConversion)
	server.mux.HandleFunc("POST /api/projects/{id}/kind-conversion", server.handleConvertProjectKind)
	server.mux.HandleFunc("GET /api/sessions", server.handleListSessions)
	server.mux.HandleFunc("POST /api/sessions", server.handleCreateSession)
	server.mux.HandleFunc("POST /api/sessions/preflight", server.handleSessionPreflight)
	server.mux.HandleFunc("GET /api/sessions/archived", server.handleListSessions)
	server.mux.HandleFunc("GET /api/sessions/{id}", server.handleGetSession)
	server.mux.HandleFunc("GET /api/sessions/{id}/events", server.handleSessionEvents)
	server.mux.HandleFunc("GET /api/sessions/{id}/conversation", server.handleSessionConversation)
	server.mux.HandleFunc("GET /api/sessions/{id}/timeline", server.handleSessionTimeline)
	server.mux.HandleFunc("GET /api/sessions/{id}/timeline/items/{seq}", server.handleSessionTimelineItem)
	server.mux.HandleFunc("GET /api/sessions/{id}/transcript", server.handleSessionTranscript)
	server.mux.HandleFunc("GET /api/sessions/{id}/transcript/entries/{entry_id}", server.handleSessionTranscriptEntry)
	server.mux.HandleFunc("POST /api/sessions/{id}/messages", server.handleSessionMessage)
	server.mux.HandleFunc("POST /api/sessions/{id}/runtime-profile", server.handleSaveSessionRuntimeProfile)
	server.mux.HandleFunc("POST /api/sessions/{id}/resume", server.handleSessionMessage)
	server.mux.HandleFunc("POST /api/sessions/{id}/steer", server.handleSessionSteer)
	server.mux.HandleFunc("POST /api/sessions/{id}/steer/queue", server.handleSessionQueueSteer)
	server.mux.HandleFunc("POST /api/sessions/{id}/permissions/{permission_id}/respond", server.handleSessionProviderPermissionResponse)
	server.mux.HandleFunc("POST /api/sessions/{id}/stop", server.handleSessionStop)
	server.mux.HandleFunc("PATCH /api/sessions/{id}", server.handleRenameSession)
	server.mux.HandleFunc("POST /api/sessions/{id}/archive", server.handleArchiveSession)
	server.mux.HandleFunc("POST /api/sessions/{id}/restore", server.handleRestoreSession)
	server.mux.HandleFunc("DELETE /api/sessions/{id}", server.handleDeleteSession)
	server.mux.HandleFunc("POST /api/runtime-profiles", server.handleCreateRuntimeProfile)
	server.mux.HandleFunc("GET /api/runtime-profiles", server.handleListRuntimeProfiles)
	server.mux.HandleFunc("GET /api/runtime-profiles/{id}", server.handleGetRuntimeProfile)
	server.mux.HandleFunc("PATCH /api/runtime-profiles/{id}", server.handleUpdateRuntimeProfile)
	server.mux.HandleFunc("DELETE /api/runtime-profiles/{id}", server.handleDeleteRuntimeProfile)
	server.mux.HandleFunc("GET /api/runtime-profiles/{id}/model-provider-migration-preview", server.handlePreviewModelProviderMigration)
	server.mux.HandleFunc("POST /api/runtime-profiles/{id}/model-provider-migration", server.handleApplyModelProviderMigration)
	server.mux.HandleFunc("POST /api/runtime-profiles/{id}/import-config", server.handleImportRuntimeProfileConfig)
	server.mux.HandleFunc("GET /api/runtime-profiles/{id}/merged-config-preview", server.handleMergedConfigPreview)
	server.mux.HandleFunc("GET /api/runtime-profiles/{id}/projected-config", server.handleProjectedConfig)
	server.mux.HandleFunc("GET /api/model-providers", server.handleListModelProviders)
	server.mux.HandleFunc("POST /api/model-providers", server.handleCreateModelProvider)
	server.mux.HandleFunc("GET /api/model-providers/{id}", server.handleGetModelProvider)
	server.mux.HandleFunc("PATCH /api/model-providers/{id}", server.handleUpdateModelProvider)
	server.mux.HandleFunc("DELETE /api/model-providers/{id}", server.handleDeleteModelProvider)
	server.mux.HandleFunc("POST /api/model-providers/{id}/refresh-models", server.handleRefreshModelProviderModels)
	server.mux.HandleFunc("GET /api/model-capability-cache", server.handleGetModelCapabilityCache)
	server.mux.HandleFunc("POST /api/model-capability-cache/lookup", server.handleLookupModelCapabilityCache)
	server.mux.HandleFunc("POST /api/model-capability-cache/refresh", server.handleRefreshModelCapabilityCache)
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
	server.mux.HandleFunc("PUT /api/skills/opt-outs/global", server.handlePutAllGlobalSkillOptOuts)
	server.mux.HandleFunc("DELETE /api/skills/opt-outs/global", server.handleDeleteAllGlobalSkillOptOuts)
	server.mux.HandleFunc("PUT /api/skills/{skill_id}/opt-out", server.handlePutGlobalSkillOptOut)
	server.mux.HandleFunc("DELETE /api/skills/{skill_id}/opt-out", server.handleDeleteGlobalSkillOptOut)
	server.mux.HandleFunc("PUT /api/skills/profiles/{profile_id}/opt-out", server.handlePutAllSkillProfileOptOuts)
	server.mux.HandleFunc("DELETE /api/skills/profiles/{profile_id}/opt-out", server.handleDeleteAllSkillProfileOptOuts)
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
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/runtime-profile", server.handleSaveTaskRuntimeProfile)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks", server.handleListTasks)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}", server.handleGetTask)
	server.mux.HandleFunc("DELETE /api/projects/{id}/tasks/{task_id}", server.handleDeleteTask)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/events", server.handleTaskEvents)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/transcript", server.handleTaskTranscript)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/transcript/entries/{entry_id}", server.handleTaskTranscriptEntry)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/timeline", server.handleTaskTimeline)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/timeline/items/{seq}", server.handleTaskTimelineItem)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/stop", server.handleStopTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/finish", server.handleFinishTask)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/finish-readiness", server.handleFinishReadiness)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/challenges/claim", server.handleChallengeClaim)
	server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/challenges", server.handleChallengeAttempts)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/challenges/submit", server.handleChallengeSubmit)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/challenges/abandon", server.handleChallengeAbandon)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/challenges/finalize", server.handleChallengeFinalize)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/resume", server.handleResumeTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/steer/queue", server.handleQueueSteerTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/steer", server.handleSteerTask)
	server.mux.HandleFunc("POST /api/projects/{id}/tasks/{task_id}/permissions/{permission_id}/respond", server.handleProviderPermissionResponse)
	server.registerBlackboardV2Routes()
	server.registerSPA()
}

func (server *Server) handleHealth(response http.ResponseWriter, request *http.Request) {
	payload := struct {
		Version  string `json:"version"`
		Database struct {
			Status string `json:"status"`
		} `json:"database"`
		Runner struct {
			RuntimeRoot  string `json:"runtime_root"`
			SandboxImage string `json:"sandbox_image"`
			ContainerCLI string `json:"container_cli"`
			EngineKind   string `json:"engine_kind,omitempty"`
			EngineName   string `json:"engine_name,omitempty"`
		} `json:"runner"`
	}{
		Version: server.version,
	}
	payload.Database.Status = "ok"
	payload.Runner.RuntimeRoot = server.runtimeRoot
	payload.Runner.SandboxImage = server.sandboxImage
	payload.Runner.ContainerCLI = server.containerCLI
	if payload.Runner.ContainerCLI == "" {
		payload.Runner.ContainerCLI = "docker"
	}
	if info, err := runner.DetectEngine(request.Context(), payload.Runner.ContainerCLI, nil); err == nil {
		payload.Runner.EngineKind = string(info.Kind)
		payload.Runner.EngineName = info.Name
	} else {
		// If the configured CLI is missing, surface the first available engine so
		// the launch UI can default to Podman when only Podman is installed.
		for _, candidate := range []string{"podman", "docker"} {
			if candidate == payload.Runner.ContainerCLI {
				continue
			}
			if info, err := runner.DetectEngine(request.Context(), candidate, nil); err == nil {
				payload.Runner.EngineKind = string(info.Kind)
				payload.Runner.EngineName = info.Name
				payload.Runner.ContainerCLI = candidate
				break
			}
		}
	}

	writeJSON(response, http.StatusOK, payload)
}

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
			errors.Is(err, runtimeprofile.ErrUnknownProvider),
			errors.Is(err, runtimeprofile.ErrInvalidReasoningEffort),
			errors.Is(err, runtimeprofile.ErrInvalidCodexMultiAgent),
			errors.Is(err, runtimeprofile.ErrCustomArgConflict):
			server.logCustomArgConflict(input.Provider, input.Fields.CustomArgs, err)
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
		// ConfirmProviderSwitchClearsOverlay confirms discarding a non-empty
		// Custom Config File when switching provider.
		ConfirmProviderSwitchClearsOverlay *bool `json:"confirm_provider_switch_clears_overlay,omitempty"`
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
	confirmProviderSwitchClearsOverlay := input.ConfirmProviderSwitchClearsOverlay != nil && *input.ConfirmProviderSwitchClearsOverlay

	updated, err := server.profiles.Update(id, name, provider, fields, fieldsTouched, confirmProviderSwitchClearsOverlay)
	if err != nil {
		var switchErr *runtimeprofile.ProviderSwitchNeedsOverlayClearError
		switch {
		case errors.Is(err, runtimeprofile.ErrNotFound):
			writeError(response, http.StatusNotFound, err.Error())
		case errors.As(err, &switchErr):
			writeJSON(response, http.StatusConflict, map[string]any{
				"error": switchErr.Error(),
				"code":  "provider_switch_needs_overlay_clear",
			})
		case errors.Is(err, runtimeprofile.ErrUnknownProvider),
			errors.Is(err, runtimeprofile.ErrInvalidReasoningEffort),
			errors.Is(err, runtimeprofile.ErrInvalidCodexMultiAgent),
			errors.Is(err, runtimeprofile.ErrCustomArgConflict):
			logProvider := provider
			if logProvider == "" {
				if existing, getErr := server.profiles.Get(id); getErr == nil {
					logProvider = existing.Provider
				}
			}
			logArgs := fields.CustomArgs
			if !fieldsTouched {
				if existing, getErr := server.profiles.Get(id); getErr == nil {
					logArgs = existing.Fields.CustomArgs
				}
			}
			server.logCustomArgConflict(logProvider, logArgs, err)
			writeError(response, http.StatusBadRequest, err.Error())
		default:
			writeError(response, http.StatusInternalServerError, "store runtime profile update")
		}
		return
	}

	writeJSON(response, http.StatusOK, runtimeprofile.SanitizeProfile(updated))
}

// handleImportRuntimeProfileConfig runs one Profile Config Import: parse the
// edited provider-native config text, sync structured-expressible keys, and
// store the remainder as the Custom Config File. Refusals return per-key
// errors with a 400.
func (server *Server) handleImportRuntimeProfileConfig(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	var input struct {
		ConfigText string `json:"config_text"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	result, err := server.profiles.ImportConfig(id, runtimeprofile.ImportConfigRequest{
		ConfigText: input.ConfigText,
	})
	if err != nil {
		var refusal *runtimeprofile.ImportConfigError
		if errors.As(err, &refusal) {
			writeJSON(response, http.StatusBadRequest, map[string]any{
				"error": refusal.Error(),
				"keys":  refusal.Errors,
			})
			return
		}
		if errors.Is(err, runtimeprofile.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "import runtime profile config")
		return
	}

	writeJSON(response, http.StatusOK, map[string]any{
		"profile":     runtimeprofile.SanitizeProfile(result.Profile),
		"mapped_keys": result.MappedKeys,
	})
}

func (server *Server) previewProjectionRequest(profiles ...runtimeprofile.Profile) runner.ProjectionRequest {
	snapshot, err := server.snapshotGlobalModelProviders()
	if err != nil {
		snapshot = runner.CloneGlobalModelProviderSnapshot(nil)
	}
	// Story 3: the daemon operator token must never reach editor text, so
	// the preview request carries no AuthToken and no credential values.
	// Credential-derived env keys render as redacted placeholders derived
	// from binding metadata only.
	req := runner.ProjectionRequest{
		ModelProviders:              server.modelProviders,
		RuntimePlugins:              server.runtimePlugins,
		GlobalModelProviderSnapshot: snapshot,
		DaemonAddr:                  server.listenAddr,
		CredentialEnvNames:          server.credentialEnvNames(),
		CapabilityCache:             server.capabilityCache,
	}
	// Resolve the preview ModelSnapshot so the editor shows every env the
	// launch projection materializes, including the Model Provider API-key
	// env (Story 2/16).
	if len(profiles) > 0 {
		profile := profiles[0]
		if strings.TrimSpace(profile.Fields.ModelProviderID) != "" && server.modelProviders != nil {
			if resolved, err := modelprovider.Resolve(modelprovider.ResolveRequest{
				Profile:         profile,
				Providers:       server.modelProviders,
				Plugins:         server.runtimePlugins,
				CapabilityCache: server.capabilityCache,
			}); err == nil && resolved.ModelProviderID != "" {
				req.ModelSnapshot = &resolved
			}
		}
	}
	return req
}

// credentialEnvNames lists the env var names global credential bindings
// project under. Metadata only — never secret values.
func (server *Server) credentialEnvNames() []string {
	bindings, err := server.creds.ListGlobal()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Disabled {
			continue
		}
		envName, err := credential.DestinationEnv(binding.Source)
		if err != nil || envName == "" {
			continue
		}
		names = append(names, envName)
	}
	return names
}

// handleMergedConfigPreview answers the final merged result the runtime
// receives: the provider-native projected config deep-merged with the
// profile's Custom Config File overlay (structured fields win conflicts).
func (server *Server) handleMergedConfigPreview(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}
	profile, err := server.profiles.Get(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return
	}
	merged, err := runner.MergedProjectedConfigWith(profile.Provider, profile, server.previewProjectionRequest(profile))
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"provider": string(profile.Provider),
		"merged":   merged,
	})
}

// handleProjectedConfig answers the provider-native seed the config editor
// opens on: a complete, realistic file derived from structured fields,
// redacted, with the stored Custom Config File carried alongside.
func (server *Server) handleProjectedConfig(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}
	profile, err := server.profiles.Get(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return
	}
	text, err := runner.ProjectedConfigTextWith(profile.Provider, profile, server.previewProjectionRequest(profile))
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"provider":           string(profile.Provider),
		"format":             runner.OverlayFormat(profile.Provider),
		"text":               text,
		"custom_config_file": profile.Fields.CustomConfigFile,
	})
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
		RuntimePluginID         string           `json:"runtime_plugin_id,omitempty"`
		ModelProviderID         string           `json:"model_provider_id,omitempty"`
		Model                   string           `json:"model,omitempty"`
		ModelOverride           string           `json:"model_override,omitempty"`
		ReasoningEffort         string           `json:"reasoning_effort,omitempty"`
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
	if _, err := normalizeLaunchReasoningEffort(input.ReasoningEffort); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = strings.TrimSpace(input.ModelOverride)
	}
	resolvedConfiguration, err := server.resolveLaunchConfiguration(runtimeconfig.LaunchSelection{
		RuntimeProfileID: input.RuntimeProfileID, RuntimePluginID: input.RuntimePluginID,
		ModelProviderID: input.ModelProviderID, Model: model,
		ReasoningEffort: input.ReasoningEffort, Runner: string(defaulted.runner),
	}, projectID)
	var resolvedProfile *runtimeprofile.Profile
	if err == nil {
		resolvedProfile = &resolvedConfiguration.Profile
	} else if strings.TrimSpace(defaulted.runtimeProfileID) == "" {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	result := server.preflight.Run(request.Context(), preflight.Request{
		RuntimeProfileID:        defaulted.runtimeProfileID,
		Profile:                 resolvedProfile,
		LaunchModelOverride:     model,
		ProjectID:               projectID,
		CredentialRefsToResolve: input.CredentialRefsToResolve,
		Runner:                  string(defaulted.runner),
		HostActivated:           hostActivated,
		ProjectKind:             defaulted.project.Kind,
		ScopeCapabilities:       append([]string(nil), defaulted.project.Scope.Capabilities...),
		ContainerCLI:            task.ResolveContainerCLI(input.RunControls.ContainerCLI, server.containerCLI),
		SandboxVPNTun:           input.RunControls.SandboxVPNTun,
		SandboxNetwork:          input.RunControls.SandboxNetwork,
		RuntimeRoot:             server.runtimeRoot,
	})
	server.logPreflightCustomArgConflict(defaulted.runtimeProfileID, result)

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
