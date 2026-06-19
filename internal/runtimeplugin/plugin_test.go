package runtimeplugin_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"pentest/internal/runtimeplugin"
)

func TestValidateManifestRejectsInvalidInput(t *testing.T) {
	valid := testPlugin()

	cases := []struct {
		name   string
		mutate func(*runtimeplugin.Plugin)
	}{
		{name: "missing id", mutate: func(plugin *runtimeplugin.Plugin) { plugin.ID = "" }},
		{name: "unknown projection", mutate: func(plugin *runtimeplugin.Plugin) { plugin.ConfigProjection.Primitive = "mystery" }},
		{name: "unknown parser", mutate: func(plugin *runtimeplugin.Plugin) { plugin.Transcript.Parser = "mystery" }},
		{name: "duplicate fields", mutate: func(plugin *runtimeplugin.Plugin) {
			plugin.ProfileSchema.Fields = append(plugin.ProfileSchema.Fields, runtimeplugin.ProfileField{Name: "model", Type: "string", Label: "Again"})
		}},
		{name: "credential value", mutate: func(plugin *runtimeplugin.Plugin) { plugin.CredentialEnv = []string{"ANTHROPIC_API_KEY=secret"} }},
	}

	if err := runtimeplugin.Validate(valid); err != nil {
		t.Fatalf("expected valid plugin: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plugin := valid
			tc.mutate(&plugin)
			if err := runtimeplugin.Validate(plugin); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestBuiltinRegistryContainsStableBuiltIns(t *testing.T) {
	registry, err := runtimeplugin.BuiltinRegistry()
	if err != nil {
		t.Fatalf("builtin registry: %v", err)
	}
	got := registry.IDs()
	want := []string{"claude_code", "codex", "fake", "pi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %#v, want %#v", got, want)
	}
	for _, id := range want {
		if !registry.Has(id) {
			t.Fatalf("expected registry to contain %q", id)
		}
	}
}

func TestBuiltinPluginsExposeRuntimeExtensionProfileField(t *testing.T) {
	registry, err := runtimeplugin.BuiltinRegistry()
	if err != nil {
		t.Fatalf("builtin registry: %v", err)
	}
	for _, id := range []string{"codex", "claude_code", "pi"} {
		t.Run(id, func(t *testing.T) {
			plugin, ok := registry.Get(id)
			if !ok {
				t.Fatalf("missing plugin %q", id)
			}
			found := false
			for _, field := range plugin.ProfileSchema.Fields {
				if field.Name == "runtime_extensions" {
					found = true
					if field.Type != "runtime_extensions" {
						t.Fatalf("runtime_extensions field type = %q", field.Type)
					}
				}
			}
			if !found {
				t.Fatalf("expected runtime_extensions field in %#v", plugin.ProfileSchema.Fields)
			}
		})
	}
}

func TestRegistryReturnsCopies(t *testing.T) {
	registry, err := runtimeplugin.BuiltinRegistry()
	if err != nil {
		t.Fatalf("builtin registry: %v", err)
	}
	plugin, ok := registry.Get("claude_code")
	if !ok {
		t.Fatal("missing claude_code")
	}
	plugin.Name = "mutated"
	again, _ := registry.Get("claude_code")
	if again.Name == "mutated" {
		t.Fatal("registry returned mutable shared plugin")
	}
}

func TestNewRegistryRejectsDuplicateIDs(t *testing.T) {
	plugin := testPlugin()
	_, err := runtimeplugin.NewRegistry([]runtimeplugin.Plugin{plugin, plugin})
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestLoadDirectoryReadsTrustedJSONManifests(t *testing.T) {
	dir := t.TempDir()
	plugin := testPlugin()
	plugin.ID = "external_runtime"
	raw, err := json.Marshal(plugin)
	if err != nil {
		t.Fatalf("marshal plugin: %v", err)
	}
	if err := os.WriteFile(dir+"/external.json", raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(dir+"/notes.txt", []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	plugins, errs := runtimeplugin.LoadDirectory(dir)
	if len(errs) > 0 {
		t.Fatalf("load errors: %v", errs)
	}
	if len(plugins) != 1 || plugins[0].ID != "external_runtime" {
		t.Fatalf("unexpected plugins: %#v", plugins)
	}
}

func TestLoadDirectoryReportsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/broken.json", []byte("{"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	plugins, errs := runtimeplugin.LoadDirectory(dir)
	if len(plugins) != 0 {
		t.Fatalf("expected no plugins, got %#v", plugins)
	}
	if len(errs) != 1 {
		t.Fatalf("expected one load error, got %#v", errs)
	}
}

func TestExternalManifestDuplicateBuiltinFailsRegistryConstruction(t *testing.T) {
	plugin := testPlugin()
	plugin.ID = "codex"
	plugins := append(runtimeplugin.BuiltinPlugins(), plugin)
	if _, err := runtimeplugin.NewRegistry(plugins); err == nil {
		t.Fatal("expected duplicate builtin id error")
	}
}

func TestLoadDirectoryDisabledWhenPathBlank(t *testing.T) {
	plugins, errs := runtimeplugin.LoadDirectory("  ")
	if len(plugins) != 0 || len(errs) != 0 {
		t.Fatalf("expected blank directory to do nothing, got plugins=%#v errs=%#v", plugins, errs)
	}
}

func testPlugin() runtimeplugin.Plugin {
	return runtimeplugin.Plugin{
		SchemaVersion: runtimeplugin.SchemaVersion,
		ID:            "test_plugin",
		Name:          "Test Plugin",
		Binary:        runtimeplugin.Binary{Default: "test"},
		ProfileSchema: runtimeplugin.ProfileSchema{Fields: []runtimeplugin.ProfileField{
			{Name: "model", Type: "string", Label: "Model"},
		}},
		ConfigProjection: runtimeplugin.ConfigProjection{Primitive: "none"},
		Launch:           runtimeplugin.LaunchTemplate{Args: []string{"{{binary}}", "{{goal}}"}},
		CredentialEnv:    []string{"TEST_API_KEY"},
		Transcript:       runtimeplugin.Transcript{Parser: "plain_runtime_output"},
	}
}
