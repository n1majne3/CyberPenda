package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// ProductionProviderSessionFactory is the daemon's concrete non-PTY bridge
// assembly. It is a thin shell over the per-family providerSessionAssembler
// seam: Codex App Server, Claude Agent SDK, Pi headless RPC, and Hermes ACP
// each own their durable process argv and bridge handshake, while this module
// owns owner-scoped binding, bridge registries, and the shared finish.
// Capabilities come from the Runtime Plugin manifest and provider bridge
// handshake.
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
	// Plugins is the Runtime Plugin Registry that supplies per-family
	// capabilities and binary defaults. Nil uses the built-in registry.
	Plugins *runtimeplugin.Registry
}

type ProductionProviderSessionFactory struct {
	config      ProductionProviderSessionFactoryConfig
	bridges     *runtime.SandboxSessionBridgeRegistry
	hostBridges *runtime.HostSessionBridgeRegistry
	assemblers  map[runtimeprofile.Provider]providerSessionAssembler

	mu     sync.Mutex
	bounds map[string]ProviderSessionBinding
}

type claudeBridgeCapabilities struct {
	PersistentSession bool `json:"persistent_session"`
	SendTurn          bool `json:"send_turn"`
	InterruptTurn     bool `json:"interrupt_turn"`
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

func (s *productionBoundSession) TurnState() runtime.ProviderSessionTurnState {
	if reporter, ok := s.ProviderSession.(runtime.ProviderSessionTurnStateReporter); ok {
		return reporter.TurnState()
	}
	return runtime.ProviderSessionTurnState{SessionID: s.SessionID(), ControlBusy: s.ControlBusy()}
}

func (s *productionBoundSession) TurnBusy() bool {
	if reporter, ok := s.ProviderSession.(runtime.ProviderSessionTurnBusyReporter); ok {
		return reporter.TurnBusy()
	}
	return s.ControlBusy()
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
	plugins := config.Plugins
	if plugins == nil {
		plugins = runtimeplugin.MustBuiltinRegistry()
	}
	factory := &ProductionProviderSessionFactory{
		config:      config,
		bridges:     runtime.NewSandboxSessionBridgeRegistry(),
		hostBridges: runtime.NewHostSessionBridgeRegistry(),
		assemblers:  map[runtimeprofile.Provider]providerSessionAssembler{},
		bounds:      map[string]ProviderSessionBinding{},
	}
	factory.assemblers[runtimeprofile.ProviderCodex] = codexAssembler{plugins: plugins, bridge: config.BridgeCommand}
	factory.assemblers[runtimeprofile.ProviderClaudeCode] = claudeAssembler{plugins: plugins, sdkCommand: config.ClaudeSDKBridgeCommand}
	factory.assemblers[runtimeprofile.ProviderPi] = piAssembler{
		plugins: plugins, sandboxBridge: config.BridgeCommand, resolveHostBridge: factory.resolveHostBridgeCommand,
	}
	factory.assemblers[runtimeprofile.ProviderHermes] = hermesAssembler{plugins: plugins}
	return factory
}

// assemblerFor resolves the per-family assembler for one launch.
func (f *ProductionProviderSessionFactory) assemblerFor(provider runtimeprofile.Provider) (providerSessionAssembler, error) {
	assembler, ok := f.assemblers[provider]
	if !ok {
		return nil, fmt.Errorf("provider session factory supports codex, claude_code, pi, and hermes only")
	}
	return assembler, nil
}

func (f *ProductionProviderSessionFactory) Open(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	if err := validateProviderSessionLaunchRequest(request); err != nil {
		return ProviderSessionBinding{}, err
	}
	assembler, err := f.assemblerFor(request.Provider)
	if err != nil {
		return ProviderSessionBinding{}, err
	}
	switch request.Runner {
	case task.RunnerSandbox:
		return f.openSandboxWithAssembler(ctx, request, assembler)
	case task.RunnerHost:
		return f.openHostWithAssembler(ctx, request, assembler)
	default:
		return ProviderSessionBinding{}, fmt.Errorf("provider session factory does not support runner %q", request.Runner)
	}
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

// sandboxBridgeDockerForLaunch prefers the container CLI from the launch
// Docker sandbox adapter (Task/Session Run Controls) when the factory transport
// is the production Docker CLI seam. Mock transports stay unchanged so tests
// keep full control of Create/Start without a real CLI.
func sandboxBridgeDockerForLaunch(base runtime.SandboxBridgeDocker, adapter runtime.Adapter) runtime.SandboxBridgeDocker {
	cli, ok := runtime.DockerSandboxContainerCLI(adapter)
	if !ok || strings.TrimSpace(cli) == "" {
		return base
	}
	switch docker := base.(type) {
	case runtime.DockerCLISandboxBridgeDocker:
		docker.ContainerCLI = strings.TrimSpace(cli)
		return docker
	default:
		return base
	}
}
