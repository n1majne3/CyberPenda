package runtimeextension_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadDirectoryRejectsUnknownRequirementKeys(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{
		"schema_version":1,
		"id":"ctf_platform",
		"name":"CTF Platform",
		"compatible_runtime_plugins":["pi"],
		"source":{"type":"local_dir","path":"/tmp/ctf"},
		"projection":{"location":"provider_home","path":"extensions/ctf"},
		"requirements":{"project_kinds":["ctf_challenge"],"authorizes_scope":true}
	}`)
	if err := os.WriteFile(filepath.Join(dir, "ctf.json"), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	loaded, errs := runtimeextension.LoadDirectory(dir)
	if len(loaded) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "unknown field") {
		t.Fatalf("expected unknown requirement key rejection, loaded=%#v errors=%v", loaded, errs)
	}
}
