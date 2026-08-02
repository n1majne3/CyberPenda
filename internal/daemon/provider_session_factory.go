package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

var errAssistedConclusionUnsupported = errors.New("assisted_conclusion_unsupported: Runtime does not expose the complete persistent SendTurn, normalized Tool/Turn event, and closed AttemptResult contract")

// ProviderSessionLaunchRequest is the launch-assembly seam for a persistent
// provider session. The request is deliberately owner/Continuation scoped;
// credentials and raw provider protocol frames never cross this boundary.
type ProviderSessionLaunchRequest struct {
	// Owner is the required server-derived capability contract. The provider
	// boundary receives only this owner-neutral identity and continuation, so a
	// Session can never be represented as a Task with an absent Project.
	Owner         owner.Contract
	Continuation  owner.Continuation
	Provider      runtimeprofile.Provider
	Runner        task.Runner
	LaunchGoal    string
	RuntimeConfig map[string]any
	LegacyAdapter runtime.Adapter
}

func providerSessionOwnerID(request ProviderSessionLaunchRequest) string {
	return strings.TrimSpace(request.Owner.ID)
}

func validateProviderSessionLaunchRequest(request ProviderSessionLaunchRequest) error {
	if err := request.Owner.Validate(); err != nil {
		return fmt.Errorf("provider session owner contract: %w", err)
	}
	if strings.TrimSpace(request.Continuation.ID) == "" ||
		strings.TrimSpace(request.Continuation.OwnerID) != strings.TrimSpace(request.Owner.ID) {
		return fmt.Errorf("provider session Continuation is not bound to the owner contract")
	}
	if strings.TrimSpace(string(request.Provider)) == "" || strings.TrimSpace(string(request.Runner)) == "" {
		return fmt.Errorf("provider session launch selection is incomplete")
	}
	return nil
}

// ProviderSessionBinding contains the provider session and the long-running
// Adapter that drives its initial turn. A session without an Adapter would be
// bound in the daemon while the legacy one-shot process still ran, which is an
// unsafe split-brain launch and therefore rejected.
type ProviderSessionBinding struct {
	Session runtime.ProviderSession
	Adapter runtime.Adapter
}

// ProviderSessionFactory opens or reuses one owner-bound provider session. An
// implementation must return the same session/adapter identity for later
// Continuations of that owner and must bind the supplied Continuation on its
// private transport before returning.
type ProviderSessionFactory interface {
	Open(context.Context, ProviderSessionLaunchRequest) (ProviderSessionBinding, error)
}

// ProviderSessionRecoveryLiveness is the closed result of a non-spawning
// ownership probe. It deliberately matches Runtime Activity liveness.
type ProviderSessionRecoveryLiveness string

const (
	ProviderSessionRecoveryLive     ProviderSessionRecoveryLiveness = "live"
	ProviderSessionRecoveryOffline  ProviderSessionRecoveryLiveness = "offline"
	ProviderSessionRecoveryOrphaned ProviderSessionRecoveryLiveness = "orphaned"
	ProviderSessionRecoveryUnknown  ProviderSessionRecoveryLiveness = "unknown"
)

// ProviderSessionRecoveryRequest identifies one exact durable Runtime and the
// control request whose callbacks the daemon needs to recover. Metadata is
// copied from the supplied Continuation so a factory never needs to query
// aggregate storage or infer identity from provider output.
type ProviderSessionRecoveryRequest struct {
	Owner             owner.Contract
	Continuation      owner.Continuation
	ReceiptID         string
	SourceSessionID   string
	SourceRequestID   string
	DispatchRequestID string
	ContainerID       string
	NativeSessionID   string
	NativeSessionPath string
}

// ProviderSessionRecoveryResult returns a binding only when ownership of the
// exact existing Runtime is proven live. Non-live results must not carry a
// binding.
type ProviderSessionRecoveryResult struct {
	Liveness ProviderSessionRecoveryLiveness
	Binding  ProviderSessionBinding
}

// ProviderSessionRecoveryFactory is an optional restart capability. Recover
// MUST only probe or attach to the exact Runtime named by the request. It MUST
// NOT call Open, launch a process/container, create a Continuation, or resume a
// provider-native session in a replacement Runtime.
type ProviderSessionRecoveryFactory interface {
	Recover(context.Context, ProviderSessionRecoveryRequest) (ProviderSessionRecoveryResult, error)
}

// ProviderSessionRecoveryOutcome is safe coordinator input. It exposes no raw
// provider data and distinguishes successful adoption from a liveness probe.
type ProviderSessionRecoveryOutcome struct {
	Owner    owner.Contract
	OwnerID  string
	Liveness ProviderSessionRecoveryLiveness
	Adopted  bool
	Warning  string
	Activity task.RuntimeActivity
}

// ProviderSessionRecoveryReport exposes owner-neutral proven-live and
// lifecycle exclusion sets used by startup reconciliation.
type ProviderSessionRecoveryReport struct {
	LiveOwnerIDs                   []string
	LifecycleProtectedOwnerIDs     []string
	ReconciliationExcludedOwnerIDs []string
	Outcomes                       []ProviderSessionRecoveryOutcome
}

// ProviderSessionAssistedConclusionReporter is the additive capability seam
// used before Runtime creation. Persistent SendTurn alone is insufficient: the
// factory must also promise bounded Tool/Turn observations for this provider.
type ProviderSessionAssistedConclusionReporter interface {
	SupportsAssistedConclusion(runtimeprofile.Provider) bool
}

type ProviderSessionFactoryFunc func(context.Context, ProviderSessionLaunchRequest) (ProviderSessionBinding, error)

func (f ProviderSessionFactoryFunc) Open(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	if f == nil {
		return ProviderSessionBinding{}, fmt.Errorf("provider session factory is unavailable")
	}
	if err := validateProviderSessionLaunchRequest(request); err != nil {
		return ProviderSessionBinding{}, err
	}
	return f(ctx, request)
}

// providerSessionFactoryError keeps a stable phase label while surfacing a
// redacted, human-readable cause so operators (backend log) and the frontend
// can see why setup failed. The redacted detail masks credential values; the
// unredacted cause stays in the unwrap chain for server-only diagnostics.
type providerSessionFactoryError struct {
	cause  error
	detail string
}

func (e *providerSessionFactoryError) Error() string {
	if strings.TrimSpace(e.detail) != "" {
		return "provider session setup failed: " + e.detail
	}
	return "provider session setup failed"
}
func (e *providerSessionFactoryError) Unwrap() error { return e.cause }

func supportedProviderSessionFactoryProvider(provider runtimeprofile.Provider) bool {
	switch provider {
	case runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi:
		return true
	default:
		return false
	}
}

// supportsPersistentProviderSession reports whether the runner/provider pair
// uses owner-scoped provider-session assembly. Host supports Codex, Claude Code,
// and Pi; unsupported plugins retain the legacy one-shot path.
func supportsPersistentProviderSession(runner task.Runner, provider runtimeprofile.Provider) bool {
	switch runner {
	case task.RunnerSandbox:
		return supportedProviderSessionFactoryProvider(provider)
	case task.RunnerHost:
		return provider == runtimeprofile.ProviderCodex || provider == runtimeprofile.ProviderClaudeCode || provider == runtimeprofile.ProviderPi
	default:
		return false
	}
}

// wrapPersistentProviderAdapter re-wraps a persistent provider-session Adapter
// with the Pi session-file tailer when the provider writes turn output to a
// session jsonl instead of the bridge RPC channel. Only Pi behaves this way, so
// Codex and Claude Code adapters (which forward output as bridge events) are
// returned unchanged. Without this wrapping a persistent Pi session produces an
// empty transcript because ProviderSessionRunAdapter never tails the jsonl.
func wrapPersistentProviderAdapter(adapter runtime.Adapter, provider runtimeprofile.Provider, providerHome string) runtime.Adapter {
	if adapter == nil || provider != runtimeprofile.ProviderPi || strings.TrimSpace(providerHome) == "" {
		return adapter
	}
	sessionDir := filepath.Join(providerHome, "agent", "sessions")
	return runtime.NewPiSessionTailAdapter(adapter, sessionDir)
}

func validateProviderSessionBinding(binding ProviderSessionBinding) error {
	if binding.Session == nil || strings.TrimSpace(binding.Session.SessionID()) == "" {
		return fmt.Errorf("provider session factory returned no session identity")
	}
	if binding.Adapter == nil {
		return fmt.Errorf("provider session factory returned no session adapter")
	}
	return nil
}

func validateAssistedConclusionBinding(binding ProviderSessionBinding) error {
	if !binding.Session.Capabilities().AssistedConclusion {
		return errAssistedConclusionUnsupported
	}
	if _, ok := binding.Session.(runtime.ProviderSessionObservationSink); !ok {
		return errAssistedConclusionUnsupported
	}
	if _, ok := binding.Session.(runtime.ProviderSessionCompleteTurnLineageResolver); !ok {
		return errAssistedConclusionUnsupported
	}
	if _, ok := binding.Session.(runtime.ProviderSessionAttemptResultSource); !ok {
		return errAssistedConclusionUnsupported
	}
	return nil
}

func (server *Server) supportsAssistedConclusion(provider runtimeprofile.Provider) bool {
	plugin, ok := server.runtimePlugins.Get(string(provider))
	if !ok || !plugin.Capabilities.PersistentSession || !plugin.Capabilities.SendTurn {
		return false
	}
	reporter, ok := server.providerSessionFactory.(ProviderSessionAssistedConclusionReporter)
	return ok && reporter.SupportsAssistedConclusion(provider)
}
