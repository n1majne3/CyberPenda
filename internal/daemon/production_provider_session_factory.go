package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// ProductionProviderSessionFactory is the daemon's concrete non-PTY bridge
// assembly. Codex App Server, Claude Agent SDK, and Pi headless RPC are wired
// end-to-end on Sandbox and Host. Host Pi keeps the explicit piWire translation
// boundary through pentest-provider-bridge; Host Claude uses the packaged SDK
// Query bridge.
// Claude is launched through the version-pinned SDK bridge rather than the
// interactive CLI, so interrupt and settlement stay provider-native.
type ProductionProviderSessionFactoryConfig struct {
	Docker runtime.SandboxBridgeDocker
	// BridgeCommand is the sandbox-local provider-bridge path used inside
	// container create argv (default /usr/local/bin/pentest-provider-bridge).
	BridgeCommand string
	// ClaudeSDKBridgeCommand is the host/sandbox Claude Agent SDK bridge
	// executable. Empty defaults to /usr/local/bin/pentest-claude-sdk-bridge.
	ClaudeSDKBridgeCommand string
	// HostBridgeCommand is the host-side piWire translation executable for
	// Host Pi. Empty falls back to BridgeCommand. Tests set this explicitly so
	// process-spec assertions do not depend on sandbox image layout.
	HostBridgeCommand string
	Diagnostics       func(string)
	// HostStarter is an optional process seam for Host Runner tests. Production
	// uses the real local process-group starter when nil.
	HostStarter runtime.HostProcessStarter
}

type ProductionProviderSessionFactory struct {
	config      ProductionProviderSessionFactoryConfig
	bridges     *runtime.SandboxSessionBridgeRegistry
	hostBridges *runtime.HostSessionBridgeRegistry

	mu     sync.Mutex
	bounds map[string]ProviderSessionBinding
}

type productionBoundSession struct {
	runtime.ProviderSession
	onClose func(context.Context)
	once    sync.Once
}

func (s *productionBoundSession) BindContinuation(id string) error {
	if binder, ok := s.ProviderSession.(runtime.ProviderSessionContinuationBinder); ok {
		return binder.BindContinuation(id)
	}
	return nil
}

func (s *productionBoundSession) HandleEvent(event runtime.SandboxBridgeEvent, emit runtime.ProviderSessionEmit) {
	if handler, ok := s.ProviderSession.(runtime.ProviderSessionEventHandler); ok {
		handler.HandleEvent(event, emit)
	}
}

func (s *productionBoundSession) Close(ctx context.Context) error {
	err := s.ProviderSession.Close(ctx)
	if err == nil || err == runtime.ErrProviderSessionClosed {
		s.once.Do(func() {
			if s.onClose != nil {
				s.onClose(ctx)
			}
		})
	}
	return err
}

func (s *productionBoundSession) ControlBusy() bool {
	if reporter, ok := s.ProviderSession.(interface{ ControlBusy() bool }); ok {
		return reporter.ControlBusy()
	}
	return false
}

func (s *productionBoundSession) SessionClosed() bool {
	if reporter, ok := s.ProviderSession.(interface{ SessionClosed() bool }); ok {
		return reporter.SessionClosed()
	}
	return false
}

func (s *productionBoundSession) SessionOffline() bool {
	if reporter, ok := s.ProviderSession.(interface{ SessionOffline() bool }); ok {
		return reporter.SessionOffline()
	}
	return s.SessionClosed()
}

func (s *productionBoundSession) SessionUnexpectedOffline() bool {
	if reporter, ok := s.ProviderSession.(interface{ SessionUnexpectedOffline() bool }); ok {
		return reporter.SessionUnexpectedOffline()
	}
	return false
}

// productionBridgeTransport is the shared protocol surface for sandbox and host bridges.
type productionBridgeTransport interface {
	runtime.ProviderSessionTransport
	Closed() <-chan struct{}
	Terminated() <-chan struct{}
}

func NewProductionProviderSessionFactory(config ProductionProviderSessionFactoryConfig) *ProductionProviderSessionFactory {
	if strings.TrimSpace(config.BridgeCommand) == "" {
		config.BridgeCommand = "/usr/local/bin/pentest-provider-bridge"
	}
	if strings.TrimSpace(config.ClaudeSDKBridgeCommand) == "" {
		config.ClaudeSDKBridgeCommand = "/usr/local/bin/pentest-claude-sdk-bridge"
	}
	return &ProductionProviderSessionFactory{
		config:      config,
		bridges:     runtime.NewSandboxSessionBridgeRegistry(),
		hostBridges: runtime.NewHostSessionBridgeRegistry(),
		bounds:      map[string]ProviderSessionBinding{},
	}
}

func (f *ProductionProviderSessionFactory) Open(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	switch request.Runner {
	case task.RunnerSandbox:
		return f.openSandbox(ctx, request)
	case task.RunnerHost:
		return f.openHost(ctx, request)
	default:
		return ProviderSessionBinding{}, fmt.Errorf("provider session factory does not support runner %q", request.Runner)
	}
}

func (f *ProductionProviderSessionFactory) openHost(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	switch request.Provider {
	case runtimeprofile.ProviderCodex:
		return f.openHostCodex(ctx, request)
	case runtimeprofile.ProviderClaudeCode:
		return f.openHostClaude(ctx, request)
	case runtimeprofile.ProviderPi:
		return f.openHostPi(ctx, request)
	default:
		return ProviderSessionBinding{}, fmt.Errorf("host provider session factory supports codex, claude_code, and pi only")
	}
}

func (f *ProductionProviderSessionFactory) openHostCodex(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	taskID := strings.TrimSpace(request.Task.ID)
	if taskID == "" || strings.TrimSpace(request.Continuation.ID) == "" {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge requires Task and Continuation identity")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if prior, ok := f.bounds[taskID]; ok {
		return f.rebindPrior(prior, request.Continuation.ID)
	}

	launch, ok := runtime.CommandAdapterLaunch(request.LegacyAdapter)
	if !ok {
		return ProviderSessionBinding{}, fmt.Errorf("host provider session bridge requires host command adapter")
	}
	providerBinary := strings.TrimSpace(launch.Program)
	if providerBinary == "" {
		providerBinary = "codex"
	}
	// Host Codex App Server is the durable protocol process. Append
	// non-conflicting Custom Args from the one-shot host launch so advanced
	// provider options survive assembly; reserved conflicts fail before launch.
	program := providerBinary
	args := []string{"app-server"}
	if custom := hostCodexCustomArgs(launch.Args); len(custom) > 0 {
		args = append(args, custom...)
	}
	workdir := strings.TrimSpace(launch.Workdir)
	if workdir == "" {
		return ProviderSessionBinding{}, fmt.Errorf("host provider session bridge requires Task workdir")
	}

	var runAdapter *runtime.ProviderSessionRunAdapter
	var runAdapterMu sync.RWMutex
	bridge, err := f.hostBridges.Bind(ctx, taskID, request.Continuation.ID, func() (*runtime.HostSessionBridge, error) {
		bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{
			TaskID: taskID, Program: program, Args: args, Workdir: workdir, Env: launch.Env,
			Diagnostics: f.config.Diagnostics, Starter: f.config.HostStarter,
			ProtocolEmit: func(event runtime.SandboxBridgeEvent) {
				runAdapterMu.RLock()
				adapter := runAdapter
				runAdapterMu.RUnlock()
				if adapter != nil {
					adapter.HandleBridgeEvent(event)
				}
			},
		})
		if err != nil {
			return nil, err
		}
		if err := bridge.Start(ctx); err != nil {
			_ = bridge.Close(ctx)
			return nil, err
		}
		return bridge, nil
	})
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	// Durable process-group identity for daemon-restart cleanup. Sandbox uses
	// ContainerID; Host reuses the same metadata field with a typed prefix.
	processIdentity := runtime.FormatHostProcessGroupID(bridge.ProcessGroupID())
	return f.finishCodexBinding(ctx, request, taskID, workdir, bridge, &runAdapter, &runAdapterMu, func(closeCtx context.Context) {
		_ = f.hostBridges.CloseTask(closeCtx, taskID)
	}, processIdentity)
}

func (f *ProductionProviderSessionFactory) openHostClaude(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	taskID := strings.TrimSpace(request.Task.ID)
	if taskID == "" || strings.TrimSpace(request.Continuation.ID) == "" {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge requires Task and Continuation identity")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if prior, ok := f.bounds[taskID]; ok {
		return f.rebindPrior(prior, request.Continuation.ID)
	}

	launch, ok := runtime.CommandAdapterLaunch(request.LegacyAdapter)
	if !ok {
		return ProviderSessionBinding{}, fmt.Errorf("host provider session bridge requires host command adapter")
	}
	workdir := strings.TrimSpace(launch.Workdir)
	if workdir == "" {
		return ProviderSessionBinding{}, fmt.Errorf("host provider session bridge requires Task workdir")
	}

	// Claude's durable process is the version-pinned packaged SDK bridge (one
	// Query), never repo-relative Node sources or an on-demand npm install, and
	// never the interactive CLI. Projected CLAUDE_HOME/settings/MCP/env come
	// from the host launch adapter; non-conflicting Custom Args survive argv
	// assembly. A missing bridge fails closed with no one-shot CLI fallback.
	program := strings.TrimSpace(f.config.ClaudeSDKBridgeCommand)
	if program == "" {
		return ProviderSessionBinding{}, fmt.Errorf("Claude SDK bridge command is not configured")
	}
	args := hostClaudeSDKBridgeArgs(workdir, launch.Args, request)
	var runAdapter *runtime.ProviderSessionRunAdapter
	var runAdapterMu sync.RWMutex
	bridge, err := f.hostBridges.Bind(ctx, taskID, request.Continuation.ID, func() (*runtime.HostSessionBridge, error) {
		bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{
			TaskID: taskID, Program: program, Args: args, Workdir: workdir, Env: launch.Env,
			Diagnostics: f.config.Diagnostics, Starter: f.config.HostStarter,
			ProtocolEmit: func(event runtime.SandboxBridgeEvent) {
				runAdapterMu.RLock()
				adapter := runAdapter
				runAdapterMu.RUnlock()
				if adapter != nil {
					adapter.HandleBridgeEvent(event)
				}
			},
		})
		if err != nil {
			return nil, err
		}
		if err := bridge.Start(ctx); err != nil {
			_ = bridge.Close(ctx)
			return nil, fmt.Errorf("Claude SDK bridge unavailable at %s: %w", program, err)
		}
		return bridge, nil
	})
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	processIdentity := runtime.FormatHostProcessGroupID(bridge.ProcessGroupID())
	return f.finishClaudeBinding(ctx, request, taskID, bridge, &runAdapter, &runAdapterMu, func(closeCtx context.Context) {
		_ = f.hostBridges.CloseTask(closeCtx, taskID)
	}, processIdentity)
}

func (f *ProductionProviderSessionFactory) openHostPi(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	taskID := strings.TrimSpace(request.Task.ID)
	if taskID == "" || strings.TrimSpace(request.Continuation.ID) == "" {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge requires Task and Continuation identity")
	}

	// HostSessionBridge speaks CyberPenda JSON-RPC; Pi speaks native headless
	// RPC. Host Pi must retain the piWire translation boundary rather than
	// connect Pi stdin/stdout directly.
	bridgeProgram, err := f.resolveHostBridgeCommand()
	if err != nil {
		return ProviderSessionBinding{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if prior, ok := f.bounds[taskID]; ok {
		return f.rebindPrior(prior, request.Continuation.ID)
	}

	launch, ok := runtime.CommandAdapterLaunch(request.LegacyAdapter)
	if !ok {
		return ProviderSessionBinding{}, fmt.Errorf("host provider session bridge requires host command adapter")
	}
	providerBinary := strings.TrimSpace(launch.Program)
	if providerBinary == "" {
		providerBinary = "pi"
	}
	workdir := strings.TrimSpace(launch.Workdir)
	if workdir == "" {
		return ProviderSessionBinding{}, fmt.Errorf("host provider session bridge requires Task workdir")
	}
	// Preserve projected PI_CODING_AGENT_DIR / session dir and every launch-ready
	// credential env from Config Projection. Missing agent dir fails clearly.
	if err := requireHostPiProjectedEnv(launch.Env); err != nil {
		return ProviderSessionBinding{}, err
	}

	// Program is the explicit host bridge executable; Pi is the child after "--".
	args := []string{"--provider", "pi", "--", providerBinary, "--mode", "rpc"}
	if sessionPath := strings.TrimSpace(request.Continuation.NativeSessionPath); sessionPath != "" {
		args = append(args, "--session", sessionPath)
	} else {
		sessionID := strings.TrimSpace(request.Continuation.NativeSessionID)
		if sessionID == "" {
			sessionID = taskID
		}
		args = append(args, "--session-id", sessionID)
	}
	if custom := hostPiCustomArgs(launch.Args); len(custom) > 0 {
		args = append(args, custom...)
	}

	var runAdapter *runtime.ProviderSessionRunAdapter
	var runAdapterMu sync.RWMutex
	bridge, err := f.hostBridges.Bind(ctx, taskID, request.Continuation.ID, func() (*runtime.HostSessionBridge, error) {
		bridge, err := runtime.NewHostSessionBridge(runtime.HostSessionBridgeConfig{
			TaskID: taskID, Program: bridgeProgram, Args: args, Workdir: workdir, Env: launch.Env,
			Diagnostics: f.config.Diagnostics, Starter: f.config.HostStarter,
			ProtocolEmit: func(event runtime.SandboxBridgeEvent) {
				runAdapterMu.RLock()
				adapter := runAdapter
				runAdapterMu.RUnlock()
				if adapter != nil {
					adapter.HandleBridgeEvent(event)
				}
			},
		})
		if err != nil {
			return nil, err
		}
		if err := bridge.Start(ctx); err != nil {
			_ = bridge.Close(ctx)
			return nil, err
		}
		return bridge, nil
	})
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	processIdentity := runtime.FormatHostProcessGroupID(bridge.ProcessGroupID())
	agentDir := strings.TrimSpace(launch.Env["PI_CODING_AGENT_DIR"])
	return f.finishPiBinding(ctx, request, taskID, bridge, &runAdapter, &runAdapterMu, func(closeCtx context.Context) {
		_ = f.hostBridges.CloseTask(closeCtx, taskID)
		cleanupHostPiArtifacts(agentDir)
	}, processIdentity)
}

// resolveHostBridgeCommand returns the explicit host piWire bridge path and
// fails clearly when it is missing. Production requires a real executable;
// HostStarter tests still resolve the path so process specs stay observable.
func (f *ProductionProviderSessionFactory) resolveHostBridgeCommand() (string, error) {
	path := strings.TrimSpace(f.config.HostBridgeCommand)
	if path == "" {
		path = strings.TrimSpace(f.config.BridgeCommand)
	}
	if path == "" {
		path = "/usr/local/bin/pentest-provider-bridge"
	}
	if f.config.HostStarter != nil {
		// Test seam: HostStarter intercepts Start, so the path need only be
		// explicit and non-empty for process-spec assertions.
		return path, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("host pi provider bridge is unavailable at %q: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("host pi provider bridge is unavailable at %q: path is a directory", path)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("host pi provider bridge is unavailable at %q: not executable", path)
	}
	return path, nil
}

func requireHostPiProjectedEnv(env map[string]string) error {
	if strings.TrimSpace(env["PI_CODING_AGENT_DIR"]) == "" {
		return fmt.Errorf("host pi requires projected PI_CODING_AGENT_DIR")
	}
	if strings.TrimSpace(env["PI_CODING_AGENT_SESSION_DIR"]) == "" {
		return fmt.Errorf("host pi requires projected PI_CODING_AGENT_SESSION_DIR")
	}
	return nil
}

// cleanupHostPiArtifacts removes Task-scoped Pi session files and projected
// credentials after process-group teardown on Stop, failure, and daemon shutdown.
// models.json (non-secret) is retained for diagnostics until the next projection.
func cleanupHostPiArtifacts(agentDir string) {
	agentDir = strings.TrimSpace(agentDir)
	if agentDir == "" {
		return
	}
	_ = os.Remove(filepath.Join(agentDir, "auth.json"))
	_ = os.RemoveAll(filepath.Join(agentDir, "sessions"))
}

func (f *ProductionProviderSessionFactory) openSandbox(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	if request.Provider != runtimeprofile.ProviderClaudeCode && request.Provider != runtimeprofile.ProviderCodex && request.Provider != runtimeprofile.ProviderPi {
		return ProviderSessionBinding{}, fmt.Errorf("provider %q is not supported by production provider session factory", request.Provider)
	}
	if f.config.Docker == nil {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge docker transport is unavailable")
	}
	taskID := strings.TrimSpace(request.Task.ID)
	if taskID == "" || strings.TrimSpace(request.Continuation.ID) == "" {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge requires Task and Continuation identity")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if prior, ok := f.bounds[taskID]; ok {
		return f.rebindPrior(prior, request.Continuation.ID)
	}

	legacyArgs, ok := runtime.DockerSandboxCreateArgs(request.LegacyAdapter)
	if !ok {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge requires Docker sandbox adapter")
	}
	providerBinary := "codex"
	if request.Provider == runtimeprofile.ProviderClaudeCode {
		providerBinary = "claude"
	}
	for _, arg := range legacyArgs {
		if !strings.Contains(arg, "=") && filepath.Base(arg) == filepath.Base(providerBinary) {
			providerBinary = arg
			break
		}
	}
	if request.Provider == runtimeprofile.ProviderPi {
		providerBinary = "pi"
		for _, arg := range legacyArgs {
			if !strings.Contains(arg, "=") && filepath.Base(arg) == "pi" {
				providerBinary = arg
				break
			}
		}
	}
	bridgeCommand := []string{f.config.BridgeCommand, "--provider", string(request.Provider), "--", providerBinary}
	if request.Provider == runtimeprofile.ProviderClaudeCode {
		// The SDK bridge is an executable in the sandbox image. It owns the
		// long-lived Query and does not invoke the Claude CLI's private protocol.
		bridgeCommand = append([]string{f.config.ClaudeSDKBridgeCommand}, hostClaudeSDKBridgeArgs("/task/workdir", legacyArgs, request)...)
	} else if request.Provider == runtimeprofile.ProviderCodex {
		bridgeCommand = append(bridgeCommand, "app-server")
	} else {
		bridgeCommand = append(bridgeCommand, "--mode", "rpc")
		if sessionPath := strings.TrimSpace(request.Continuation.NativeSessionPath); sessionPath != "" {
			bridgeCommand = append(bridgeCommand, "--session", sessionPath)
		} else {
			// Pi creates its durable session lazily. Supplying a stable Task-scoped
			// id makes the pre-launch get_state handshake deterministic and keeps
			// later Continuations on the same native session.
			sessionID := strings.TrimSpace(request.Continuation.NativeSessionID)
			if sessionID == "" {
				sessionID = taskID
			}
			bridgeCommand = append(bridgeCommand, "--session-id", sessionID)
		}
	}
	createArgs, err := runtime.RewriteDockerCreateCommand(legacyArgs, string(request.Provider), bridgeCommand)
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	var runAdapter *runtime.ProviderSessionRunAdapter
	var runAdapterMu sync.RWMutex
	bridge, err := f.bridges.Bind(ctx, taskID, request.Continuation.ID, func() (*runtime.SandboxSessionBridge, error) {
		bridge, err := runtime.NewSandboxSessionBridge(f.config.Docker, runtime.SandboxBridgeConfig{
			TaskID: taskID, CreateArgs: createArgs, Diagnostics: f.config.Diagnostics,
			ProtocolEmit: func(event runtime.SandboxBridgeEvent) {
				runAdapterMu.RLock()
				adapter := runAdapter
				runAdapterMu.RUnlock()
				if adapter != nil {
					adapter.HandleBridgeEvent(event)
				}
			},
		})
		if err != nil {
			return nil, err
		}
		if err := bridge.Start(ctx); err != nil {
			_ = bridge.Close(ctx)
			return nil, err
		}
		return bridge, nil
	})
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	if request.Provider == runtimeprofile.ProviderCodex {
		return f.finishCodexBinding(ctx, request, taskID, "/task/workdir", bridge, &runAdapter, &runAdapterMu, func(closeCtx context.Context) {
			_ = f.bridges.CloseTask(closeCtx, taskID)
		}, bridge.ContainerID())
	}
	if request.Provider == runtimeprofile.ProviderPi {
		return f.finishPiBinding(ctx, request, taskID, bridge, &runAdapter, &runAdapterMu, func(closeCtx context.Context) {
			_ = f.bridges.CloseTask(closeCtx, taskID)
		}, bridge.ContainerID())
	}
	return f.finishClaudeBinding(ctx, request, taskID, bridge, &runAdapter, &runAdapterMu, func(closeCtx context.Context) {
		_ = f.bridges.CloseTask(closeCtx, taskID)
	}, bridge.ContainerID())
}

func (f *ProductionProviderSessionFactory) rebindPrior(prior ProviderSessionBinding, continuationID string) (ProviderSessionBinding, error) {
	if binder, ok := prior.Session.(runtime.ProviderSessionContinuationBinder); ok {
		if err := binder.BindContinuation(continuationID); err != nil {
			return ProviderSessionBinding{}, err
		}
	}
	if adapter, ok := prior.Adapter.(*runtime.ProviderSessionRunAdapter); ok {
		adapter.BindContinuation(continuationID)
	}
	return prior, nil
}

func (f *ProductionProviderSessionFactory) finishCodexBinding(
	ctx context.Context,
	request ProviderSessionLaunchRequest,
	taskID, cwd string,
	bridge productionBridgeTransport,
	runAdapter **runtime.ProviderSessionRunAdapter,
	runAdapterMu *sync.RWMutex,
	closeBridge func(context.Context),
	containerID string,
) (ProviderSessionBinding, error) {
	if _, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: "setup:initialize", Method: "initialize", Params: json.RawMessage(`{"clientInfo":{"name":"cyberpenda","version":"dev"}}`)}); err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, err
	}
	setupMethod, setupID, setupParams := "thread/start", "setup:thread", json.RawMessage(fmt.Sprintf(`{"cwd":%q}`, cwd))
	if durableThreadID := strings.TrimSpace(request.Continuation.NativeSessionID); durableThreadID != "" {
		setupMethod, setupID, setupParams = "thread/resume", "setup:thread-resume", json.RawMessage(fmt.Sprintf(`{"threadId":%q,"cwd":%q}`, durableThreadID, cwd))
	}
	setupResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: setupID, Method: setupMethod, Params: setupParams})
	if err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, err
	}
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(setupResponse.Result, &threadResult); err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session thread response invalid")
	}
	threadID := strings.TrimSpace(threadResult.Thread.ID)
	if threadID == "" {
		threadID = strings.TrimSpace(threadResult.ID)
	}
	if durableThreadID := strings.TrimSpace(request.Continuation.NativeSessionID); durableThreadID != "" && threadID != durableThreadID {
		closeBridge(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session resume identity changed")
	}
	if threadID == "" {
		closeBridge(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session thread identity unavailable")
	}
	sessionPath := strings.TrimSpace(request.Continuation.NativeSessionPath)
	capabilities := runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true}
	nativeSession := runtime.NewCodexProviderSession(runtime.CodexProviderSessionConfig{Transport: bridge, SessionID: threadID, Capabilities: capabilities})
	var session *productionBoundSession
	session = &productionBoundSession{ProviderSession: nativeSession, onClose: func(closeCtx context.Context) {
		f.mu.Lock()
		if current, ok := f.bounds[taskID]; ok && current.Session == session {
			delete(f.bounds, taskID)
		}
		f.mu.Unlock()
		closeBridge(closeCtx)
	}}
	runAdapterMu.Lock()
	// Unexpected process/protocol exit (Terminated) and explicit cleanup
	// (Closed) both end the harness wait; they remain distinct bridge signals.
	*runAdapter = runtime.NewProviderSessionRunAdapter(session, runtime.FirstSignal(bridge.Closed(), bridge.Terminated()))
	runAdapterMu.Unlock()
	(*runAdapter).BindContinuation(request.Continuation.ID)
	(*runAdapter).SetSessionMetadata(func() runtime.NativeSessionMetadata {
		return runtime.NativeSessionMetadata{ContainerID: containerID, NativeSessionID: threadID, NativeSessionPath: sessionPath}
	})
	binding := ProviderSessionBinding{Session: session, Adapter: *runAdapter}
	f.bounds[taskID] = binding
	return binding, nil
}

// finishClaudeBinding establishes Claude Agent SDK session identity on a
// sandbox or host bridge, then binds the long-lived adapter. processIdentity
// is the durable ContainerID/host-pgid metadata used for restart cleanup.
func (f *ProductionProviderSessionFactory) finishClaudeBinding(
	ctx context.Context,
	request ProviderSessionLaunchRequest,
	taskID string,
	bridge productionBridgeTransport,
	runAdapter **runtime.ProviderSessionRunAdapter,
	runAdapterMu *sync.RWMutex,
	closeBridge func(context.Context),
	processIdentity string,
) (ProviderSessionBinding, error) {
	setupResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: "setup:initialize", Method: "claude/initialize", Params: json.RawMessage(`{}`)})
	if err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, err
	}
	var state struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(setupResponse.Result, &state); err != nil || strings.TrimSpace(state.SessionID) == "" {
		closeBridge(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session identity unavailable")
	}
	sessionID := strings.TrimSpace(state.SessionID)
	if durable := strings.TrimSpace(request.Continuation.NativeSessionID); durable != "" && durable != sessionID {
		closeBridge(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session resume identity changed")
	}
	capabilities := runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true}
	nativeSession := runtime.NewClaudeCodeProviderSession(runtime.ClaudeCodeProviderSessionConfig{Transport: bridge, SessionID: sessionID, Capabilities: capabilities})
	var session *productionBoundSession
	session = &productionBoundSession{ProviderSession: nativeSession, onClose: func(closeCtx context.Context) {
		f.mu.Lock()
		if current, ok := f.bounds[taskID]; ok && current.Session == session {
			delete(f.bounds, taskID)
		}
		f.mu.Unlock()
		closeBridge(closeCtx)
	}}
	runAdapterMu.Lock()
	*runAdapter = runtime.NewProviderSessionRunAdapter(session, runtime.FirstSignal(bridge.Closed(), bridge.Terminated()))
	runAdapterMu.Unlock()
	(*runAdapter).BindContinuation(request.Continuation.ID)
	(*runAdapter).SetSessionMetadata(func() runtime.NativeSessionMetadata {
		return runtime.NativeSessionMetadata{ContainerID: processIdentity, NativeSessionID: sessionID}
	})
	binding := ProviderSessionBinding{Session: session, Adapter: *runAdapter}
	f.bounds[taskID] = binding
	return binding, nil
}

// finishPiBinding completes Pi RPC setup for sandbox or host after the piWire
// translation process is running. Host and sandbox share get_state + Pi session.
func (f *ProductionProviderSessionFactory) finishPiBinding(
	ctx context.Context,
	request ProviderSessionLaunchRequest,
	taskID string,
	bridge productionBridgeTransport,
	runAdapter **runtime.ProviderSessionRunAdapter,
	runAdapterMu *sync.RWMutex,
	closeBridge func(context.Context),
	processIdentity string,
) (ProviderSessionBinding, error) {
	setupResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: "setup:state", Method: "pi/get_state", Params: json.RawMessage(`{}`)})
	if err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, err
	}
	var state struct {
		SessionID   string `json:"session_id"`
		SessionPath string `json:"session_path"`
	}
	if err := json.Unmarshal(setupResponse.Result, &state); err == nil {
		// ok
	}
	sessionID, sessionPath := strings.TrimSpace(state.SessionID), strings.TrimSpace(state.SessionPath)
	if durable := strings.TrimSpace(request.Continuation.NativeSessionID); durable != "" && sessionID != "" && durable != sessionID {
		closeBridge(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session resume identity changed")
	}
	if sessionID == "" {
		closeBridge(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session identity unavailable")
	}
	capabilities := runtimeplugin.Capabilities{
		PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true,
		PermissionResponse: true, ResumeSession: true, InTurnSteer: true,
	}
	nativeSession := runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{Transport: bridge, SessionID: sessionID, Capabilities: capabilities})
	var session *productionBoundSession
	session = &productionBoundSession{ProviderSession: nativeSession, onClose: func(closeCtx context.Context) {
		f.mu.Lock()
		if current, ok := f.bounds[taskID]; ok && current.Session == session {
			delete(f.bounds, taskID)
		}
		f.mu.Unlock()
		closeBridge(closeCtx)
	}}
	runAdapterMu.Lock()
	*runAdapter = runtime.NewProviderSessionRunAdapter(session, runtime.FirstSignal(bridge.Closed(), bridge.Terminated()))
	runAdapterMu.Unlock()
	(*runAdapter).BindContinuation(request.Continuation.ID)
	(*runAdapter).SetSessionMetadata(func() runtime.NativeSessionMetadata {
		return runtime.NativeSessionMetadata{ContainerID: processIdentity, NativeSessionID: sessionID, NativeSessionPath: sessionPath}
	})
	binding := ProviderSessionBinding{Session: session, Adapter: *runAdapter}
	f.bounds[taskID] = binding
	return binding, nil
}

func argValue(args []string, option string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == option {
			return strings.TrimSpace(args[index+1])
		}
	}
	return ""
}

// hostClaudeSDKBridgeArgs builds Claude Agent SDK bridge argv for sandbox or
// host. Structured model/settings/resume are projected explicitly; non-conflicting
// Custom Args from the one-shot launch survive assembly.
func hostClaudeSDKBridgeArgs(workdir string, launchArgs []string, request ProviderSessionLaunchRequest) []string {
	args := []string{"--cwd", workdir}
	model, _ := request.RuntimeConfig["launch_model_override"].(string)
	if strings.TrimSpace(model) == "" {
		model = argValue(launchArgs, "--model")
	}
	if model = strings.TrimSpace(model); model != "" {
		args = append(args, "--model", model)
	}
	if settings := argValue(launchArgs, "--settings"); settings != "" {
		args = append(args, "--settings", settings)
	}
	if durableSessionID := strings.TrimSpace(request.Continuation.NativeSessionID); durableSessionID != "" {
		args = append(args, "--resume", durableSessionID)
	}
	if custom := hostClaudeCustomArgs(launchArgs); len(custom) > 0 {
		args = append(args, custom...)
	}
	return args
}

// hostClaudeCustomArgs extracts Custom Args from a one-shot Claude launch argv
// (after the binary) for SDK bridge assembly.
//
// Only these tokens are dropped:
//   - structured launch-template model/settings/resume flags and values
//   - MCP config injection (--strict-mcp-config, --mcp-config)
//   - one-shot print/stream helpers (-p/--print, --output-format, --verbose)
//   - Runtime Non-Interactive Defaults (--dangerously-skip-permissions,
//     --permission-mode)
//   - the goal separator (--) and trailing Task goal
//
// User Custom Args are never silently stripped here. Reserved model/effort
// aliases are rejected by runtimeprofile.ValidateCustomArgs before launch
// (#148); this helper does not reimplement or weaken that seam.
func hostClaudeCustomArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	skipNext := map[string]bool{
		"--model": true, "--settings": true, "--mcp-config": true,
		"--output-format": true, "--permission-mode": true, "--resume": true,
		"--cwd": true, "--effort": true,
	}
	dropExact := map[string]bool{
		"-p": true, "--print": true, "--verbose": true,
		"--strict-mcp-config": true, "--dangerously-skip-permissions": true,
		"--": true,
	}
	end := len(args)
	if end > 0 && !strings.HasPrefix(args[end-1], "-") && args[end-1] != "--" {
		// Drop trailing goal when present (final non-flag positional).
		if end > 1 {
			end--
		}
	}
	var custom []string
	for i := 0; i < end; i++ {
		arg := args[i]
		if dropExact[arg] {
			continue
		}
		if skipNext[arg] {
			i++ // drop structured value
			continue
		}
		if strings.HasPrefix(arg, "--model=") || strings.HasPrefix(arg, "--settings=") ||
			strings.HasPrefix(arg, "--mcp-config=") || strings.HasPrefix(arg, "--output-format=") ||
			strings.HasPrefix(arg, "--permission-mode=") || strings.HasPrefix(arg, "--resume=") ||
			strings.HasPrefix(arg, "--cwd=") || strings.HasPrefix(arg, "--effort=") {
			continue
		}
		custom = append(custom, arg)
	}
	return custom
}

// hostPiCustomArgs extracts Custom Args from a one-shot host Pi launch argv
// (after the binary) for RPC assembly via the piWire bridge.
//
// Dropped tokens are structured launch-template fields and one-shot mode/session
// flags that persistent RPC replaces. Non-conflicting Custom Args are preserved.
func hostPiCustomArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	skipNext := map[string]bool{
		"--model": true, "--provider": true, "--mode": true,
		"--session": true, "--session-id": true,
	}
	end := len(args)
	if end > 0 && !strings.HasPrefix(args[end-1], "-") {
		// Trailing goal positional from one-shot launch template.
		if end > 1 {
			end--
		}
	}
	var custom []string
	for i := 0; i < end; i++ {
		arg := args[i]
		if skipNext[arg] {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--model=") || strings.HasPrefix(arg, "--provider=") ||
			strings.HasPrefix(arg, "--mode=") || strings.HasPrefix(arg, "--session=") ||
			strings.HasPrefix(arg, "--session-id=") {
			continue
		}
		custom = append(custom, arg)
	}
	return custom
}

// hostCodexCustomArgs extracts Custom Args from a one-shot host Codex launch
// argv (after the binary) for app-server assembly.
//
// Only these tokens are dropped:
//   - one-shot subcommands (exec/resume)
//   - structured launch-template model flags (--model/-m and values)
//   - Runtime Non-Interactive Defaults and harness-injected exec helpers
//     (--dangerously-bypass-approvals-and-sandbox, --skip-git-repo-check)
//   - the trailing Task goal
//
// User Custom Args are never silently stripped here — including -c/--config
// KEY=VALUE, --config-file, and --profile. Reserved model/provider/effort
// aliases are rejected by runtimeprofile.ValidateCustomArgs before launch
// (#148); this helper does not reimplement or weaken that seam.
func hostCodexCustomArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	// Value-bearing flags injected by structured launch, not Custom Args.
	skipNext := map[string]bool{
		"--model": true,
		"-m":      true,
	}
	dropExact := map[string]bool{
		"exec": true, "resume": true, "app-server": true,
		"--dangerously-bypass-approvals-and-sandbox": true,
		"--skip-git-repo-check":                      true,
	}
	// Final positional is the goal when present.
	end := len(args)
	if end > 0 && !strings.HasPrefix(args[end-1], "-") && args[end-1] != "exec" && args[end-1] != "resume" && args[end-1] != "app-server" {
		if end > 1 {
			end--
		}
	}
	var custom []string
	for i := 0; i < end; i++ {
		arg := args[i]
		if dropExact[arg] {
			continue
		}
		if skipNext[arg] {
			i++ // drop structured model value
			continue
		}
		if strings.HasPrefix(arg, "--model=") || strings.HasPrefix(arg, "-m=") {
			continue
		}
		// Preserve everything else: -c/--config/--config-file/--profile/--json/…
		custom = append(custom, arg)
	}
	return custom
}
