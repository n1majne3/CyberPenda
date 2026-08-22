package runtimeplugin_test

import (
	"testing"

	"pentest/internal/runtimeplugin"
)

func TestManagedConfigKeysAreDeclaredPerBuiltinPlugin(t *testing.T) {
	registry := runtimeplugin.MustBuiltinRegistry()
	for _, id := range []string{"codex", "claude_code", "hermes", "pi"} {
		plugin, ok := registry.Get(id)
		if !ok {
			t.Fatalf("builtin plugin %q missing", id)
		}
		if len(plugin.ConfigProjection.ManagedKeys) == 0 {
			t.Fatalf("plugin %q declares no managed config keys", id)
		}
		for _, key := range plugin.ConfigProjection.ManagedKeys {
			if key.Key == "" {
				t.Fatalf("plugin %q declares a managed key with empty path", id)
			}
			if key.Field == "" {
				t.Fatalf("plugin %q managed key %q has no owning structured field", id, key.Key)
			}
		}
	}
	// The fake provider has no config projection; it must declare nothing.
	if fake, ok := registry.Get("fake"); ok && len(fake.ConfigProjection.ManagedKeys) != 0 {
		t.Fatalf("fake plugin must not declare managed keys, got %#v", fake.ConfigProjection.ManagedKeys)
	}
}
