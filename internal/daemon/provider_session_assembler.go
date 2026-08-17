package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

// providerSessionAssembler is the per-Runtime-family seam behind the
// ProductionProviderSessionFactory. One assembler owns everything
// provider-native about opening a persistent provider session: the durable
// process argv for host and sandbox launches, the bridge handshake that
// establishes native session identity, and family-specific teardown. Adding a
// Runtime family means adding one assembler, not editing a switch.
type providerSessionAssembler interface {
	// HostCommand assembles the durable host process program and argv from
	// structured launch facts. It never parses rendered one-shot argv.
	HostCommand(assembly providerSessionAssembly) (program string, args []string, err error)
	// SandboxCommand assembles the in-container bridge argv that replaces the
	// one-shot runtime command after the sandbox image name.
	SandboxCommand(assembly providerSessionAssembly) ([]string, error)
	// Setup performs the provider-native handshake over the bound bridge and
	// returns the native session plus the durable native session path for
	// restart metadata. Identity mismatches must fail closed with an error.
	Setup(ctx context.Context, bridge productionBridgeTransport, assembly providerSessionAssembly) (providerSessionSetup, error)
	// HostStartError decorates a bridge Start failure with the family's
	// operator-facing context.
	HostStartError(program string, err error) error
	// HostTeardown returns optional extra cleanup that runs after the host
	// bridge process group ends. Nil means no family-specific teardown.
	HostTeardown(assembly providerSessionAssembly) func(context.Context)
}

// providerSessionAssembly is the structured launch input every assembler
// consumes: the launch request with its Facts plus the one-shot adapter's
// projected environment.
type providerSessionAssembly struct {
	request ProviderSessionLaunchRequest
	env     map[string]string
}

func (a providerSessionAssembly) facts() ProviderSessionLaunchFacts {
	return a.request.Facts
}

func (a providerSessionAssembly) providerBinary(registry *runtimeplugin.Registry, provider runtimeprofile.Provider) (string, error) {
	if binary := strings.TrimSpace(a.request.Facts.ProviderBinary); binary != "" {
		return binary, nil
	}
	plugin, ok := registry.Get(string(provider))
	if !ok || strings.TrimSpace(plugin.Binary.Default) == "" {
		return "", fmt.Errorf("provider %q has no resolved provider binary", provider)
	}
	return plugin.Binary.Default, nil
}

// providerSessionSetup is the assembler handshake outcome: the constructed
// native provider session bound to the bridge transport, plus the durable
// native session path recorded in restart metadata.
type providerSessionSetup struct {
	Session    runtime.ProviderSession
	NativePath string
}

// manifestSessionCapabilities returns the Runtime Plugin manifest capabilities
// for one provider family. The manifest, not per-call-site structs, is the one
// capability source; Claude is the deliberate exception whose assisted
// conclusion capability is proven by its runtime handshake.
func manifestSessionCapabilities(registry *runtimeplugin.Registry, provider runtimeprofile.Provider) (runtimeplugin.Capabilities, error) {
	plugin, ok := registry.Get(string(provider))
	if !ok {
		return runtimeplugin.Capabilities{}, fmt.Errorf("provider %q is not registered", provider)
	}
	return plugin.Capabilities, nil
}

// openHostWithAssembler is the shared Host Runner flow: prologue, host command
// assembly, bridge bind, family handshake, and the shared finish. Only the
// assembler differs per family.
func (f *ProductionProviderSessionFactory) openHostWithAssembler(ctx context.Context, request ProviderSessionLaunchRequest, assembler providerSessionAssembler) (ProviderSessionBinding, error) {
	taskID := providerSessionOwnerID(request)
	if taskID == "" || strings.TrimSpace(request.Continuation.ID) == "" {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge requires owner and Continuation identity")
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
		return ProviderSessionBinding{}, fmt.Errorf("host provider session bridge requires owner workdir")
	}
	assembly := providerSessionAssembly{request: request, env: launch.Env}
	program, args, err := assembler.HostCommand(assembly)
	if err != nil {
		return ProviderSessionBinding{}, err
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
			return nil, assembler.HostStartError(program, err)
		}
		return bridge, nil
	})
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	processIdentity := runtime.FormatHostProcessGroupID(bridge.ProcessGroupID())
	teardown := assembler.HostTeardown(assembly)
	closeBridge := func(closeCtx context.Context) {
		_ = f.hostBridges.CloseTask(closeCtx, taskID)
		if teardown != nil {
			teardown(closeCtx)
		}
	}
	setup, err := assembler.Setup(ctx, bridge, assembly)
	if err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, err
	}
	return f.finishProviderSessionBinding(ctx, request, taskID, bridge, &runAdapter, &runAdapterMu, closeBridge, processIdentity, setup)
}

// openSandboxWithAssembler is the shared Sandbox Runner flow: the family's
// in-container bridge command replaces the one-shot runtime command inside the
// projected docker create argv, then the same handshake and finish as Host.
func (f *ProductionProviderSessionFactory) openSandboxWithAssembler(ctx context.Context, request ProviderSessionLaunchRequest, assembler providerSessionAssembler) (ProviderSessionBinding, error) {
	if f.config.Docker == nil {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge docker transport is unavailable")
	}
	taskID := providerSessionOwnerID(request)
	if taskID == "" || strings.TrimSpace(request.Continuation.ID) == "" {
		return ProviderSessionBinding{}, fmt.Errorf("provider session bridge requires owner and Continuation identity")
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
	assembly := providerSessionAssembly{request: request}
	bridgeCommand, err := assembler.SandboxCommand(assembly)
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	createArgs, err := runtime.RewriteDockerCreateCommand(legacyArgs, string(request.Provider), bridgeCommand)
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	var runAdapter *runtime.ProviderSessionRunAdapter
	var runAdapterMu sync.RWMutex
	bridgeDocker := sandboxBridgeDockerForLaunch(f.config.Docker, request.LegacyAdapter)
	bridge, err := f.bridges.Bind(ctx, taskID, request.Continuation.ID, func() (*runtime.SandboxSessionBridge, error) {
		bridge, err := runtime.NewSandboxSessionBridge(bridgeDocker, runtime.SandboxBridgeConfig{
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
	closeBridge := func(closeCtx context.Context) {
		_ = f.bridges.CloseTask(closeCtx, taskID)
	}
	setup, err := assembler.Setup(ctx, bridge, assembly)
	if err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, err
	}
	return f.finishProviderSessionBinding(ctx, request, taskID, bridge, &runAdapter, &runAdapterMu, closeBridge, bridge.ContainerID(), setup)
}

// finishProviderSessionBinding is the one shared binding tail for every
// family and runner: bound-session wrapping with registry cleanup, the long
// run adapter, continuation binding, restart metadata, and the durable
// owner-scoped binding record. Called with f.mu held by the open flows.
func (f *ProductionProviderSessionFactory) finishProviderSessionBinding(
	ctx context.Context,
	request ProviderSessionLaunchRequest,
	taskID string,
	bridge productionBridgeTransport,
	runAdapter **runtime.ProviderSessionRunAdapter,
	runAdapterMu *sync.RWMutex,
	closeBridge func(context.Context),
	processIdentity string,
	setup providerSessionSetup,
) (ProviderSessionBinding, error) {
	var session runtime.ProviderSession
	session, err := newProductionBoundProviderSession(setup.Session, func(closeCtx context.Context) {
		f.mu.Lock()
		if current, ok := f.bounds[taskID]; ok && current.Session == session {
			delete(f.bounds, taskID)
		}
		f.mu.Unlock()
		closeBridge(closeCtx)
	})
	if err != nil {
		closeBridge(ctx)
		return ProviderSessionBinding{}, err
	}
	runAdapterMu.Lock()
	// Unexpected process/protocol exit (Terminated) and explicit cleanup
	// (Closed) both end the harness wait; they remain distinct bridge signals.
	*runAdapter = runtime.NewProviderSessionRunAdapter(session, runtime.FirstSignal(bridge.Closed(), bridge.Terminated()))
	runAdapterMu.Unlock()
	(*runAdapter).BindContinuation(request.Continuation.ID)
	(*runAdapter).SetSessionMetadata(func() runtime.NativeSessionMetadata {
		return runtime.NativeSessionMetadata{
			ContainerID: processIdentity, NativeSessionID: session.SessionID(), NativeSessionPath: setup.NativePath,
		}
	})
	binding := ProviderSessionBinding{Session: session, Adapter: *runAdapter}
	f.bounds[taskID] = binding
	return binding, nil
}
