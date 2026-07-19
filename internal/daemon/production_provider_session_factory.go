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
// end-to-end. Claude is launched through the version-pinned SDK bridge rather
// than the interactive CLI, so interrupt and settlement stay provider-native.
type ProductionProviderSessionFactoryConfig struct {
	Docker        runtime.SandboxBridgeDocker
	BridgeCommand string
	Diagnostics   func(string)
}

type ProductionProviderSessionFactory struct {
	config  ProductionProviderSessionFactoryConfig
	bridges *runtime.SandboxSessionBridgeRegistry

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

func NewProductionProviderSessionFactory(config ProductionProviderSessionFactoryConfig) *ProductionProviderSessionFactory {
	if strings.TrimSpace(config.BridgeCommand) == "" {
		config.BridgeCommand = "/usr/local/bin/pentest-provider-bridge"
	}
	return &ProductionProviderSessionFactory{config: config, bridges: runtime.NewSandboxSessionBridgeRegistry(), bounds: map[string]ProviderSessionBinding{}}
}

func (f *ProductionProviderSessionFactory) Open(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	if request.Runner != task.RunnerSandbox {
		return ProviderSessionBinding{}, fmt.Errorf("provider session factory requires sandbox runner")
	}
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
		if binder, ok := prior.Session.(runtime.ProviderSessionContinuationBinder); ok {
			if err := binder.BindContinuation(request.Continuation.ID); err != nil {
				return ProviderSessionBinding{}, err
			}
		}
		if adapter, ok := prior.Adapter.(*runtime.ProviderSessionRunAdapter); ok {
			adapter.BindContinuation(request.Continuation.ID)
		}
		return prior, nil
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
	// Establish the provider protocol and a real provider session before the
	// session is exposed to the daemon. The bridge forwards these frames over
	// its private stdin/stdout channel; no provider payload is persisted.
	var sessionID, sessionPath string
	if request.Provider == runtimeprofile.ProviderCodex {
		if _, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: "setup:initialize", Method: "initialize", Params: json.RawMessage(`{"clientInfo":{"name":"cyberpenda","version":"dev"}}`)}); err != nil {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, err
		}
	}
	setupMethod, setupID, setupParams := "pi/get_state", "setup:state", json.RawMessage(`{}`)
	if request.Provider == runtimeprofile.ProviderClaudeCode {
		setupMethod, setupID, setupParams = "claude/initialize", "setup:initialize", json.RawMessage(`{}`)
	}
	if request.Provider == runtimeprofile.ProviderCodex {
		setupMethod, setupID, setupParams = "thread/start", "setup:thread", json.RawMessage(`{"cwd":"/task/workdir"}`)
		if durableThreadID := strings.TrimSpace(request.Continuation.NativeSessionID); durableThreadID != "" {
			setupMethod, setupID, setupParams = "thread/resume", "setup:thread-resume", json.RawMessage(fmt.Sprintf(`{"threadId":%q,"cwd":"/task/workdir"}`, durableThreadID))
		}
	}
	setupResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: setupID, Method: setupMethod, Params: setupParams})
	if err != nil {
		_ = f.bridges.CloseTask(ctx, taskID)
		return ProviderSessionBinding{}, err
	}
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
	} else if request.Provider == runtimeprofile.ProviderPi {
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
	} else {
		threadResponse := setupResponse
		var threadResult struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			ID string `json:"id"`
		}
		if err := json.Unmarshal(threadResponse.Result, &threadResult); err != nil {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, fmt.Errorf("provider session thread response invalid")
		}
		threadID := strings.TrimSpace(threadResult.Thread.ID)
		if threadID == "" {
			threadID = strings.TrimSpace(threadResult.ID)
		}
		if durableThreadID := strings.TrimSpace(request.Continuation.NativeSessionID); durableThreadID != "" && threadID != durableThreadID {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, fmt.Errorf("provider session resume identity changed")
		}
		if threadID == "" {
			_ = f.bridges.CloseTask(ctx, taskID)
			return ProviderSessionBinding{}, fmt.Errorf("provider session thread identity unavailable")
		}
		sessionID = threadID
	}
	capabilities := runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true, InTurnSteer: request.Provider == runtimeprofile.ProviderPi}
	var nativeSession runtime.ProviderSession
	if request.Provider == runtimeprofile.ProviderClaudeCode {
		nativeSession = runtime.NewClaudeCodeProviderSession(runtime.ClaudeCodeProviderSessionConfig{Transport: bridge, SessionID: sessionID, Capabilities: capabilities})
	} else if request.Provider == runtimeprofile.ProviderPi {
		nativeSession = runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{Transport: bridge, SessionID: sessionID, Capabilities: capabilities})
	} else {
		nativeSession = runtime.NewCodexProviderSession(runtime.CodexProviderSessionConfig{Transport: bridge, SessionID: sessionID, Capabilities: capabilities})
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
	runAdapter = runtime.NewProviderSessionRunAdapter(session, bridge.Closed())
	runAdapterMu.Unlock()
	runAdapter.BindContinuation(request.Continuation.ID)
	runAdapter.SetSessionMetadata(func() runtime.NativeSessionMetadata {
		return runtime.NativeSessionMetadata{ContainerID: bridge.ContainerID(), NativeSessionID: sessionID, NativeSessionPath: sessionPath}
	})
	binding := ProviderSessionBinding{Session: session, Adapter: runAdapter}
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
