package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// ProviderSessionLaunchFacts carries the structured launch inputs a
// provider-session assembler consumes: the resolved provider binary, the
// profile's Runtime Custom Arguments, the effective model, the projected
// settings path, and the bridge-visible owner workdir. Passing facts replaces
// recovering them by re-parsing the rendered one-shot adapter argv.
type ProviderSessionLaunchFacts struct {
	ProviderBinary      string
	CustomArgs          []string
	Model               string
	SettingsPath        string
	Workdir             string
	HostTaskRoot        string
	Env                 map[string]string
	SandboxContainerCLI string
	SandboxCreateArgs   []string
	SandboxCreateEnv    map[string]string
}

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
	Facts         ProviderSessionLaunchFacts
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
func wrapPersistentProviderAdapter(adapter runtime.Adapter, provider runtimeprofile.Provider, providerHome string, retained ...string) runtime.Adapter {
	if adapter == nil || provider != runtimeprofile.ProviderPi || strings.TrimSpace(providerHome) == "" {
		return adapter
	}
	sessionDir := filepath.Join(providerHome, "agent", "sessions")
	return runtime.NewPiSessionTailAdapter(adapter, sessionDir, retained...)
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
