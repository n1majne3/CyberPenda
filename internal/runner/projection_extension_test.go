package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeprofile"
)

func TestProjectRuntimeConfigProjectsRuntimeExtensions(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "extension-source")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "plugin.json"), []byte(`{"name":"browser-tools"}`), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	registry, err := runtimeextension.NewRegistry([]runtimeextension.Extension{{
		SchemaVersion: runtimeextension.SchemaVersion,
		ID:            "codex_browser_tools",
		Name:          "Codex Browser Tools",
		CompatibleRuntimePlugins: []string{
			"codex",
		},
		Source:     runtimeextension.Source{Type: "local_dir", Path: sourceDir},
		Projection: runtimeextension.Projection{Location: "provider_home", Path: "extensions/browser-tools"},
	}})
	if err != nil {
		t.Fatalf("extension registry: %v", err)
	}
	enabled := true
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			Model: "gpt-5",
			RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{{
				ID:      "codex_browser_tools",
				Enabled: &enabled,
				Config:  map[string]string{"mode": "readonly"},
			}},
		},
	}
	layout, err := runner.PrepareTaskLayout(root, "task-1", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		RuntimeExtensions: registry,
	})
	if err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	projectedFile := filepath.Join(layout.ProviderHome, "extensions", "browser-tools", "plugin.json")
	if _, err := os.Stat(projectedFile); err != nil {
		t.Fatalf("expected extension file projected to %s: %v", projectedFile, err)
	}
	extensions, ok := projection.Config["runtime_extensions"].([]map[string]any)
	if !ok || len(extensions) != 1 || extensions[0]["id"] != "codex_browser_tools" {
		t.Fatalf("expected runtime_extensions preview, got %#v", projection.Config["runtime_extensions"])
	}
}

func TestProjectRuntimeConfigKeepsCatalogRuntimeExtensionRefs(t *testing.T) {
	root := t.TempDir()
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Model: "test-model",
			RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{{
				ID: "npm:pi-mcp-adapter",
				Config: map[string]string{
					"registry":    "pi.dev/packages",
					"install_ref": "npm:pi-mcp-adapter",
				},
			}},
		},
	}
	layout, err := runner.PrepareTaskLayout(root, "task-1", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	registry, err := runtimeextension.NewRegistry(nil)
	if err != nil {
		t.Fatalf("extension registry: %v", err)
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		RuntimeExtensions: registry,
	})
	if err != nil {
		t.Fatalf("project runtime config: %v", err)
	}

	extensions, ok := projection.Config["runtime_extensions"].([]map[string]any)
	if !ok || len(extensions) != 1 {
		t.Fatalf("expected runtime_extensions preview, got %#v", projection.Config["runtime_extensions"])
	}
	if extensions[0]["id"] != "npm:pi-mcp-adapter" || extensions[0]["install_ref"] != "npm:pi-mcp-adapter" {
		t.Fatalf("expected catalog extension preview, got %#v", extensions[0])
	}
}

func TestProjectRuntimeConfigRejectsUnknownLocalRuntimeExtensionRefs(t *testing.T) {
	root := t.TempDir()
	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			Model: "gpt-5",
			RuntimeExtensions: []runtimeprofile.RuntimeExtensionRef{{
				ID: "missing_extension",
			}},
		},
	}
	layout, err := runner.PrepareTaskLayout(root, "task-1", profile.Provider)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	registry, err := runtimeextension.NewRegistry(nil)
	if err != nil {
		t.Fatalf("extension registry: %v", err)
	}

	if _, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{
		RuntimeExtensions: registry,
	}); err == nil {
		t.Fatal("expected unknown local runtime extension to fail")
	}
}
