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

func TestBuiltinPluginsDeclareModelProviderProtocols(t *testing.T) {
	registry, err := runtimeplugin.BuiltinRegistry()
	if err != nil {
		t.Fatalf("builtin registry: %v", err)
	}
	cases := map[string]struct {
		required  string
		supported []string
		preferred []string
	}{
		"fake":        {required: "none"},
		"codex":       {required: "required", supported: []string{"openai_responses"}, preferred: []string{"openai_responses"}},
		"claude_code": {required: "required", supported: []string{"anthropic_messages"}, preferred: []string{"anthropic_messages"}},
		"pi": {
			required:  "required",
			supported: []string{"openai_chat_completions", "openai_responses", "anthropic_messages"},
			preferred: []string{"openai_chat_completions", "openai_responses", "anthropic_messages"},
		},
	}
	for id, want := range cases {
		t.Run(id, func(t *testing.T) {
			plugin, ok := registry.Get(id)
			if !ok {
				t.Fatalf("missing plugin %q", id)
			}
			if plugin.ModelProvider.Requirement != want.required {
				t.Fatalf("requirement = %q, want %q", plugin.ModelProvider.Requirement, want.required)
			}
			if !reflect.DeepEqual(plugin.ModelProvider.SupportedProtocols, want.supported) {
				t.Fatalf("supported = %#v, want %#v", plugin.ModelProvider.SupportedProtocols, want.supported)
			}
			if !reflect.DeepEqual(plugin.ModelProvider.ProtocolPreference, want.preferred) {
				t.Fatalf("preference = %#v, want %#v", plugin.ModelProvider.ProtocolPreference, want.preferred)
			}
		})
	}
}

func TestClaudeCodeBuiltinDeclaresNativeResume(t *testing.T) {
	registry, err := runtimeplugin.BuiltinRegistry()
	if err != nil {
		t.Fatalf("builtin registry: %v", err)
	}
	plugin, ok := registry.Get("claude_code")
	if !ok {
		t.Fatal("missing claude_code plugin")
	}
	if !plugin.Capabilities.Resume {
		t.Fatal("expected claude_code resume capability")
	}
	if !plugin.NativeResume.Supported {
		t.Fatal("expected claude_code native resume support")
	}
	if plugin.NativeResume.SessionSource != "claude_stream_json" {
		t.Fatalf("session source = %q, want claude_stream_json", plugin.NativeResume.SessionSource)
	}
	want := []string{
		"{{binary}}",
		"--resume", "{{native_session}}",
		"--model", "{{model}}",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"{{custom_args}}",
		"{{claude_goal_prefix}}",
		"{{resumed_message}}",
	}
	if !reflect.DeepEqual(plugin.NativeResume.Args, want) {
		t.Fatalf("native resume args = %#v, want %#v", plugin.NativeResume.Args, want)
	}
}

func TestPiBuiltinDeclaresNativeResume(t *testing.T) {
	registry, err := runtimeplugin.BuiltinRegistry()
	if err != nil {
		t.Fatalf("builtin registry: %v", err)
	}
	plugin, ok := registry.Get("pi")
	if !ok {
		t.Fatal("missing pi plugin")
	}
	if !plugin.Capabilities.Resume {
		t.Fatal("expected pi resume capability")
	}
	if !plugin.NativeResume.Supported {
		t.Fatal("expected pi native resume support")
	}
	if plugin.NativeResume.SessionSource != "pi_json_session" {
		t.Fatalf("session source = %q, want pi_json_session", plugin.NativeResume.SessionSource)
	}
	want := []string{
		"{{binary}}",
		"{{pi_provider_args}}",
		"--model", "{{model}}",
		"--mode", "json",
		"--session", "{{native_session}}",
		"{{custom_args}}",
		"{{resumed_message}}",
	}
	if !reflect.DeepEqual(plugin.NativeResume.Args, want) {
		t.Fatalf("native resume args = %#v, want %#v", plugin.NativeResume.Args, want)
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
