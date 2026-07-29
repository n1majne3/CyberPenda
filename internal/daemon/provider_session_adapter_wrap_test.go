package daemon

import (
	"context"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type stubProviderAdapter struct{ name string }

func (s *stubProviderAdapter) Name() string { return s.name }

func (s *stubProviderAdapter) Run(context.Context, string, func(task.EventKind, task.EventPayload)) error {
	return nil
}

func TestWrapPersistentProviderAdapterTailsPiSessions(t *testing.T) {
	inner := &stubProviderAdapter{name: "provider-session:abc123"}

	wrapped := wrapPersistentProviderAdapter(inner, runtimeprofile.ProviderPi, "/task/runtime-home/pi")

	if wrapped == runtime.Adapter(inner) {
		t.Fatal("expected Pi persistent adapter to be wrapped by the session tailer")
	}
	if got := wrapped.Name(); got != inner.name {
		t.Fatalf("wrapped adapter Name() = %q, want passthrough %q", got, inner.name)
	}
}

func TestWrapPersistentProviderAdapterLeavesNonPiUnchanged(t *testing.T) {
	inner := &stubProviderAdapter{name: "provider-session:xyz"}

	for _, provider := range []runtimeprofile.Provider{runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode} {
		if got := wrapPersistentProviderAdapter(inner, provider, "/task/runtime-home/codex"); got != runtime.Adapter(inner) {
			t.Fatalf("provider %q adapter should be returned unchanged, got %#v", provider, got)
		}
	}
}

func TestWrapPersistentProviderAdapterRequiresProviderHome(t *testing.T) {
	inner := &stubProviderAdapter{name: "provider-session:abc123"}

	if got := wrapPersistentProviderAdapter(inner, runtimeprofile.ProviderPi, ""); got != runtime.Adapter(inner) {
		t.Fatalf("missing provider home must return the adapter unchanged, got %#v", got)
	}
}
