package runtimeextension_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"pentest/internal/runtimeextension"
)

func TestLoadDirectoryReadsTrustedRuntimeExtensionManifests(t *testing.T) {
	dir := t.TempDir()
	extension := runtimeextension.Extension{
		SchemaVersion: runtimeextension.SchemaVersion,
		ID:            "pi_browser_tools",
		Name:          "Pi Browser Tools",
		CompatibleRuntimePlugins: []string{
			"pi",
		},
		Source:     runtimeextension.Source{Type: "local_dir", Path: filepath.Join(dir, "source")},
		Projection: runtimeextension.Projection{Location: "provider_home", Path: "extensions/browser-tools"},
		Config:     map[string]string{"mode": "readonly"},
	}
	if err := os.MkdirAll(extension.Source.Path, 0o700); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	raw, err := json.Marshal(extension)
	if err != nil {
		t.Fatalf("marshal extension: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi_browser_tools.json"), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	loaded, errs := runtimeextension.LoadDirectory(dir)
	if len(errs) > 0 {
		t.Fatalf("load errors: %v", errs)
	}
	registry, err := runtimeextension.NewRegistry(loaded)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	found, ok := registry.Get("pi_browser_tools")
	if !ok {
		t.Fatal("expected extension in registry")
	}
	if found.Name != "Pi Browser Tools" || !runtimeextension.CompatibleWith(found, "pi") {
		t.Fatalf("unexpected extension: %#v", found)
	}
	if runtimeextension.CompatibleWith(found, "claude_code") {
		t.Fatalf("extension should not be compatible with claude_code: %#v", found)
	}
}

func TestRepositoryRuntimeExtensionManifestsLoad(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(filename), "..", "..", "runtime-extensions")

	loaded, errs := runtimeextension.LoadDirectory(dir)
	if len(errs) > 0 {
		t.Fatalf("load errors: %v", errs)
	}
	registry, err := runtimeextension.NewRegistry(loaded)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	assertCompatibleExtension(t, registry, "pi_pentest_pack", "pi")
	assertCompatibleExtension(t, registry, "claude_code_pentest_pack", "claude_code")
}

func assertCompatibleExtension(
	t *testing.T,
	registry *runtimeextension.Registry,
	id string,
	provider string,
) {
	t.Helper()
	extension, ok := registry.Get(id)
	if !ok {
		t.Fatalf("expected extension %q in registry", id)
	}
	if !runtimeextension.CompatibleWith(extension, provider) {
		t.Fatalf("expected extension %q to be compatible with %q: %#v", id, provider, extension)
	}
}
