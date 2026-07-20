package daemon

import (
	"context"
	"fmt"
	"strings"

	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// ProviderSessionLaunchRequest is the launch-assembly seam for a persistent
// provider session. The request is deliberately Task/Continuation scoped;
// credentials and raw provider protocol frames never cross this boundary.
type ProviderSessionLaunchRequest struct {
	Task          task.Task
	Continuation  task.TaskContinuation
	Provider      runtimeprofile.Provider
	Runner        task.Runner
	LaunchGoal    string
	RuntimeConfig map[string]any
	LegacyAdapter runtime.Adapter
}

// ProviderSessionBinding contains the provider session and the long-running
// Adapter that drives its initial turn. A session without an Adapter would be
// bound in the daemon while the legacy one-shot process still ran, which is an
// unsafe split-brain launch and therefore rejected.
type ProviderSessionBinding struct {
	Session runtime.ProviderSession
	Adapter runtime.Adapter
}

// ProviderSessionFactory opens or reuses a Task-owned provider session. An
// implementation must return the same session/adapter identity for later
// Continuations of the same Task and must bind the supplied Continuation on its
// private transport before returning.
type ProviderSessionFactory interface {
	Open(context.Context, ProviderSessionLaunchRequest) (ProviderSessionBinding, error)
}

type ProviderSessionFactoryFunc func(context.Context, ProviderSessionLaunchRequest) (ProviderSessionBinding, error)

func (f ProviderSessionFactoryFunc) Open(ctx context.Context, request ProviderSessionLaunchRequest) (ProviderSessionBinding, error) {
	if f == nil {
		return ProviderSessionBinding{}, fmt.Errorf("provider session factory is unavailable")
	}
	return f(ctx, request)
}

// providerSessionFactoryError deliberately keeps provider/transport details
// out of HTTP errors and persisted lifecycle payloads while retaining an
// unwrap chain for internal tests and diagnostics.
type providerSessionFactoryError struct{ cause error }

func (e *providerSessionFactoryError) Error() string { return "provider session setup failed" }
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
// uses Task-scoped provider-session assembly. Host currently supports Codex
// only; Claude Code and Pi host persistence are separate slices.
func supportsPersistentProviderSession(runner task.Runner, provider runtimeprofile.Provider) bool {
	switch runner {
	case task.RunnerSandbox:
		return supportedProviderSessionFactoryProvider(provider)
	case task.RunnerHost:
		return provider == runtimeprofile.ProviderCodex
	default:
		return false
	}
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
