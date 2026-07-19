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
// assembly. Codex App Server is wired end-to-end; Claude Code and Pi are
// rejected until their CLI transports expose a provider-native interrupt and
// settlement protocol (their SDK/RPC implementations are not the installed
// one-shot CLI contract).
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
	if request.Provider != runtimeprofile.ProviderCodex {
		return ProviderSessionBinding{}, &runtime.UnsupportedProviderSessionCapabilityError{Capability: runtime.ProviderSessionCapabilityInterruptThenReplace}
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
	for _, arg := range legacyArgs {
		if !strings.Contains(arg, "=") && filepath.Base(arg) == "codex" {
			providerBinary = arg
			break
		}
	}
	createArgs, err := runtime.RewriteDockerCreateCommand(legacyArgs, string(request.Provider), []string{f.config.BridgeCommand, "--provider", "codex", "--", providerBinary, "app-server"})
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
	// Establish the App Server protocol and a real provider thread before the
	// session is exposed to the daemon. The bridge forwards these frames over
	// its private stdin/stdout channel; no provider payload is persisted.
	if _, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: "setup:initialize", Method: "initialize", Params: json.RawMessage(`{"clientInfo":{"name":"cyberpenda","version":"dev"}}`)}); err != nil {
		_ = bridge.Close(ctx)
		return ProviderSessionBinding{}, err
	}
	threadMethod := "thread/start"
	threadRequestID := "setup:thread"
	threadParams := json.RawMessage(`{"cwd":"/task/workdir"}`)
	if durableThreadID := strings.TrimSpace(request.Continuation.NativeSessionID); durableThreadID != "" {
		threadMethod = "thread/resume"
		threadRequestID = "setup:thread-resume"
		threadParams = json.RawMessage(fmt.Sprintf(`{"threadId":%q,"cwd":"/task/workdir"}`, durableThreadID))
	}
	threadResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: threadRequestID, Method: threadMethod, Params: threadParams})
	if err != nil {
		_ = bridge.Close(ctx)
		return ProviderSessionBinding{}, err
	}
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(threadResponse.Result, &threadResult); err != nil {
		_ = bridge.Close(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session thread response invalid")
	}
	threadID := strings.TrimSpace(threadResult.Thread.ID)
	if threadID == "" {
		threadID = strings.TrimSpace(threadResult.ID)
	}
	if durableThreadID := strings.TrimSpace(request.Continuation.NativeSessionID); durableThreadID != "" && threadID != durableThreadID {
		_ = bridge.Close(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session resume identity changed")
	}
	if threadID == "" {
		_ = bridge.Close(ctx)
		return ProviderSessionBinding{}, fmt.Errorf("provider session thread identity unavailable")
	}
	nativeSession := runtime.NewCodexProviderSession(runtime.CodexProviderSessionConfig{
		Transport: bridge, SessionID: threadID,
		Capabilities: runtimeplugin.Capabilities{PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true, PermissionResponse: true, ResumeSession: true},
	})
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
		return runtime.NativeSessionMetadata{ContainerID: bridge.ContainerID(), NativeSessionID: threadID}
	})
	binding := ProviderSessionBinding{Session: session, Adapter: runAdapter}
	f.bounds[taskID] = binding
	return binding, nil
}
