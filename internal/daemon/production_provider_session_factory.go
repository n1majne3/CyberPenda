package daemon

import (
	"context"
	"encoding/json"
	"fmt"
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
// end-to-end on Sandbox. Host Runner currently supports Codex App Server only
// (#149); Claude Code and Pi host persistence are separate slices.
// Claude is launched through the version-pinned SDK bridge rather than the
// interactive CLI, so interrupt and settlement stay provider-native.
type ProductionProviderSessionFactoryConfig struct {
	Docker        runtime.SandboxBridgeDocker
	BridgeCommand string
	Diagnostics   func(string)
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

// productionBridgeTransport is the shared protocol surface for sandbox and host bridges.
type productionBridgeTransport interface {
	runtime.ProviderSessionTransport
	Closed() <-chan struct{}
}

func NewProductionProviderSessionFactory(config ProductionProviderSessionFactoryConfig) *ProductionProviderSessionFactory {
	if strings.TrimSpace(config.BridgeCommand) == "" {
		config.BridgeCommand = "/usr/local/bin/pentest-provider-bridge"
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
	// Host persistence is Codex-only in this slice. Explicit activation and
	// one-shot fallback for other providers remain unchanged.
	if request.Provider != runtimeprofile.ProviderCodex {
		return ProviderSessionBinding{}, fmt.Errorf("host provider session factory supports codex only")
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
		bridgeCommand = []string{"/usr/local/bin/pentest-claude-sdk-bridge", "--cwd", "/task/workdir"}
		model, _ := request.RuntimeConfig["launch_model_override"].(string)
		if strings.TrimSpace(model) == "" {
			model = argValue(legacyArgs, "--model")
		}
		if model = strings.TrimSpace(model); model != "" {
			bridgeCommand = append(bridgeCommand, "--model", model)
		}
		if settings := argValue(legacyArgs, "--settings"); settings != "" {
			bridgeCommand = append(bridgeCommand, "--settings", settings)
		}
		if durableSessionID := strings.TrimSpace(request.Continuation.NativeSessionID); durableSessionID != "" {
			bridgeCommand = append(bridgeCommand, "--resume", durableSessionID)
		}
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
	return f.finishNonCodexSandboxBinding(ctx, request, taskID, bridge, &runAdapter, &runAdapterMu)
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
	*runAdapter = runtime.NewProviderSessionRunAdapter(session, bridge.Closed())
	runAdapterMu.Unlock()
	(*runAdapter).BindContinuation(request.Continuation.ID)
	(*runAdapter).SetSessionMetadata(func() runtime.NativeSessionMetadata {
		return runtime.NativeSessionMetadata{ContainerID: containerID, NativeSessionID: threadID, NativeSessionPath: sessionPath}
	})
	binding := ProviderSessionBinding{Session: session, Adapter: *runAdapter}
	f.bounds[taskID] = binding
	return binding, nil
}

func (f *ProductionProviderSessionFactory) finishNonCodexSandboxBinding(
	ctx context.Context,
	request ProviderSessionLaunchRequest,
	taskID string,
	bridge *runtime.SandboxSessionBridge,
	runAdapter **runtime.ProviderSessionRunAdapter,
	runAdapterMu *sync.RWMutex,
) (ProviderSessionBinding, error) {
	setupMethod, setupID, setupParams := "pi/get_state", "setup:state", json.RawMessage(`{}`)
	if request.Provider == runtimeprofile.ProviderClaudeCode {
		setupMethod, setupID, setupParams = "claude/initialize", "setup:initialize", json.RawMessage(`{}`)
	}
	setupResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: setupID, Method: setupMethod, Params: setupParams})
	if err != nil {
		_ = f.bridges.CloseTask(ctx, taskID)
		return ProviderSessionBinding{}, err
	}
	var sessionID, sessionPath string
	if request.Provider == runtimeprofile.ProviderClaudeCode {
		var state struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(setupResponse.Result, &state); err != nil || strings.TrimSpace(state.SessionID) == "" {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, fmt.Errorf("provider session identity unavailable")
		}
		sessionID = strings.TrimSpace(state.SessionID)
		if durable := strings.TrimSpace(request.Continuation.NativeSessionID); durable != "" && durable != sessionID {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, fmt.Errorf("provider session resume identity changed")
		}
	} else {
		var state struct {
			SessionID   string `json:"session_id"`
			SessionPath string `json:"session_path"`
		}
		if err := json.Unmarshal(setupResponse.Result, &state); err == nil {
			sessionID, sessionPath = strings.TrimSpace(state.SessionID), strings.TrimSpace(state.SessionPath)
		}
		if durable := strings.TrimSpace(request.Continuation.NativeSessionID); durable != "" && sessionID != "" && durable != sessionID {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, fmt.Errorf("provider session resume identity changed")
		}
		if sessionID == "" {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, fmt.Errorf("provider session identity unavailable")
		}
	}
	capabilities := runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true, InTurnSteer: request.Provider == runtimeprofile.ProviderPi}
	var nativeSession runtime.ProviderSession
	if request.Provider == runtimeprofile.ProviderClaudeCode {
		nativeSession = runtime.NewClaudeCodeProviderSession(runtime.ClaudeCodeProviderSessionConfig{Transport: bridge, SessionID: sessionID, Capabilities: capabilities})
	} else {
		nativeSession = runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{Transport: bridge, SessionID: sessionID, Capabilities: capabilities})
	}
	var session *productionBoundSession
	session = &productionBoundSession{ProviderSession: nativeSession, onClose: func(closeCtx context.Context) {
		f.mu.Lock()
		if current, ok := f.bounds[taskID]; ok && current.Session == session {
			delete(f.bounds, taskID)
		}
		f.mu.Unlock()
		_ = f.bridges.CloseTask(closeCtx, taskID)
	}}
	runAdapterMu.Lock()
	*runAdapter = runtime.NewProviderSessionRunAdapter(session, bridge.Closed())
	runAdapterMu.Unlock()
	(*runAdapter).BindContinuation(request.Continuation.ID)
	(*runAdapter).SetSessionMetadata(func() runtime.NativeSessionMetadata {
		return runtime.NativeSessionMetadata{ContainerID: bridge.ContainerID(), NativeSessionID: sessionID, NativeSessionPath: sessionPath}
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

// hostCodexCustomArgs extracts non-conflicting Custom Args from a one-shot host
// Codex launch argv (after the binary). Structured model/effort flags, the
// exec subcommand, non-interactive defaults, and the trailing goal are skipped
// so advanced provider options survive app-server assembly without redefining
// structured controls.
func hostCodexCustomArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	skipNext := map[string]bool{
		"--model": true, "-m": true, "--profile": true, "-c": true,
		"--config": true, "--config-file": true,
	}
	dropExact := map[string]bool{
		// One-shot subcommands and harness-owned non-interactive defaults are
		// not Custom Args; they must not be re-emitted on app-server.
		"exec": true, "resume": true, "app-server": true,
		"--dangerously-bypass-approvals-and-sandbox": true,
		"--skip-git-repo-check":                      true,
		"--full-auto":                                true,
	}
	var custom []string
	// Final positional is the goal when present.
	end := len(args)
	if end > 0 && !strings.HasPrefix(args[end-1], "-") && args[end-1] != "exec" && args[end-1] != "resume" && args[end-1] != "app-server" {
		// Treat last non-flag as goal only when something precedes it.
		if end > 1 {
			end--
		}
	}
	for i := 0; i < end; i++ {
		arg := args[i]
		if dropExact[arg] {
			continue
		}
		if skipNext[arg] {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--model=") || (strings.HasPrefix(arg, "-c") && strings.Contains(arg, "model")) {
			continue
		}
		custom = append(custom, arg)
	}
	// Second pass: drop reserved structured aliases that validation should already reject.
	filtered := custom[:0]
	for i := 0; i < len(custom); i++ {
		arg := custom[i]
		lower := strings.ToLower(arg)
		if lower == "--model" || lower == "-m" || strings.HasPrefix(lower, "--model=") ||
			lower == "--effort" || strings.HasPrefix(lower, "--effort=") ||
			lower == "--thinking" || strings.HasPrefix(lower, "--thinking=") ||
			strings.Contains(lower, "model_reasoning_effort") ||
			lower == "--provider" || strings.HasPrefix(lower, "--provider=") {
			if !strings.Contains(arg, "=") && i+1 < len(custom) {
				i++
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}
