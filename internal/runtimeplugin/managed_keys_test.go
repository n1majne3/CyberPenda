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

func TestCodexPluginDeclaresMultiAgentManagedKeysAndField(t *testing.T) {
	registry := runtimeplugin.MustBuiltinRegistry()
	plugin, ok := registry.Get("codex")
	if !ok {
		t.Fatalf("builtin codex plugin missing")
	}

	managed := map[string]string{}
	for _, key := range plugin.ConfigProjection.ManagedKeys {
		managed[key.Key] = key.Field
	}
	for _, key := range []string{
		"features.multi_agent",
		"features.multi_agent_v2",
		"agents.enabled",
		"agents.max_concurrent_threads_per_session",
		"agents.max_depth",
	} {
		if field, present := managed[key]; !present || field != "codex_multi_agent" {
			t.Fatalf("expected managed key %q owned by codex_multi_agent, got %q", key, field)
		}
	}

	foundField := false
	for _, field := range plugin.ProfileSchema.Fields {
		if field.Name == "codex_multi_agent" {
			foundField = true
			if field.Type != "codex_multi_agent" {
				t.Fatalf("codex_multi_agent field type = %q", field.Type)
			}
		}
	}
	if !foundField {
		t.Fatalf("codex plugin schema lacks the codex_multi_agent profile field")
	}

	// The multi-agent control is Codex-specific: no other plugin exposes it.
	for _, id := range []string{"claude_code", "pi", "hermes", "fake"} {
		other, ok := registry.Get(id)
		if !ok {
			continue
		}
		for _, field := range other.ProfileSchema.Fields {
			if field.Name == "codex_multi_agent" {
				t.Fatalf("plugin %q must not expose the codex_multi_agent field", id)
			}
		}
	}
}
