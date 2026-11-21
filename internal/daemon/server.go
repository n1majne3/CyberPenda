package daemon

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/challengeworkflow"
	"pentest/internal/credential"
	"pentest/internal/fgs"
	"pentest/internal/finishreadiness"
	"pentest/internal/modelprovider"
	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/runner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/scopeexpansion"
	"pentest/internal/session"
	"pentest/internal/skill"
	"pentest/internal/steering"
	"pentest/internal/store"
	"pentest/internal/task"
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
	ArtifactRoot          string
	EvidenceSourceRoots   []string
	SkillsRoot            string
	SandboxImage          string
	ContainerCLI          string
	TaskVolume            string
	TaskVolumeRoot        string
	HostBrowserMCPBinary  string
	HostBrowserBrowser    string
	HostBrowserExecutable string
	ListenAddr            string
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
	DisableBuiltinSkills   bool
	ModelRefreshClient     *http.Client
	ProviderSessionFactory ProviderSessionFactory
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
	fgs                     *fgs.Service
	fgsCancel               context.CancelFunc
	fgsWG                   sync.WaitGroup
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
	hostBrowserMCPBinary    string
	hostBrowserBrowser      string
	hostBrowserExecutable   string
	hostBrowserMu           sync.Mutex
	hostBrowserProcesses    map[string]*hostBrowserProcess
	controlMu               sync.Mutex
	activeControls          map[string]bool
	providerControlCtx      context.Context
	providerControlCancel   context.CancelFunc
	providerControlWG       sync.WaitGroup
	runtimeLaunchCtx        context.Context
	runtimeLaunchCancel     context.CancelCauseFunc
	runtimeLaunchWG         sync.WaitGroup
	providerTaskContexts    map[string]context.Context
	providerTaskCancels     map[string]context.CancelFunc
	activeProviderControls  map[string]bool
	queuedProviderControls  map[string]int
	closing                 bool
	providerSessions        *providerSessionRegistry
	sessionProviderSessions *providerSessionRegistry
	providerSessionFactory  ProviderSessionFactory
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
	operatorToken := authToken
	generatedOperatorToken := false
	if operatorToken == "" {
		operatorToken, err = (projectinterface.RandomTokenSource{}).NewToken()
		if err != nil {
			_ = db.Close()
			if tempSkillsRoot != "" {
				_ = os.RemoveAll(tempSkillsRoot)
			}
			return nil, fmt.Errorf("generate operator token: %w", err)
		}
		generatedOperatorToken = true
	}
	if !isLoopback(listenAddr) {
		authToken = operatorToken
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
	runtimeLaunchCtx, runtimeLaunchCancel := context.WithCancelCause(context.Background())
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
		hostBrowserMCPBinary:    config.HostBrowserMCPBinary,
		hostBrowserBrowser:      config.HostBrowserBrowser,
		hostBrowserExecutable:   config.HostBrowserExecutable,
		hostBrowserProcesses:    map[string]*hostBrowserProcess{},
		activeControls:          map[string]bool{},
		providerControlCtx:      providerControlCtx,
		providerControlCancel:   providerControlCancel,
		runtimeLaunchCtx:        runtimeLaunchCtx,
		runtimeLaunchCancel:     runtimeLaunchCancel,
		providerTaskContexts:    map[string]context.Context{},
		providerTaskCancels:     map[string]context.CancelFunc{},
		activeProviderControls:  map[string]bool{},
		queuedProviderControls:  map[string]int{},
		providerSessions:        newProviderSessionRegistry(),
		sessionProviderSessions: newProviderSessionRegistry(),
		providerSessionFactory:  config.ProviderSessionFactory,
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
	server.fgs = fgs.NewService(db)
	server.challengeWorkflow = challengeworkflow.NewService(db, server.tasks)
	server.finishReadiness = finishreadiness.NewService(db, server.tasks)
	server.tasks.SetContinuationReconciler(server.blackboardV2)
	server.blackboardV2Continuity = blackboardv2.NewContinuityService(db, server.blackboardV2, server.tasks, runtimeRoot)
	if err := server.recoverBlackboardV2ContinuationFiles(context.Background()); err != nil {
		_ = server.Close()
		return nil, err
	}
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
	if err := server.reconcileInterruptedSessions(nil); err != nil {
		_ = server.Close()
		return nil, err
	}
	server.recoverAcceptedSteering(context.Background())
	server.startFGSReceiver()

	return server, nil
}
