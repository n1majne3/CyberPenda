package runner_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

// Story 2/11/16: a JSON editor reopen is one complete valid provider-native
// document containing both generated and Custom Config File members.
func TestProjectedConfigJSONReopenIsOneValidDocument(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{
		runtimeprofile.ProviderClaudeCode,
		runtimeprofile.ProviderPi,
	} {
		t.Run(string(provider), func(t *testing.T) {
			profile := runtimeprofile.Profile{Provider: provider}
			profile.Fields.CustomConfigFile = "{\n  \"customFlag\" : true\n}"
			text, err := runner.ProjectedConfigText(provider, profile)
			if err != nil {
				t.Fatalf("projected text: %v", err)
			}
			if !json.Valid([]byte(text)) {
				t.Fatalf("reopen must be one valid JSON document:\n%s", text)
			}
			if !strings.Contains(text, "\"customFlag\" : true") {
				t.Fatalf("remainder member formatting must survive:\n%s", text)
			}
		})
	}
}

// Story 15/16: a conflict that reaches projection still displays the
// structured value in the editor preview, matching the runtime projection.
func TestProjectedConfigYAMLConflictShowsStructuredValue(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "terminal:\n  # operator comment\n  backend: docker\n  shell: /bin/zsh\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "# operator comment") || !strings.Contains(text, "shell: /bin/zsh") {
		t.Fatalf("operator formatting/content must survive:\n%s", text)
	}
	if !strings.Contains(text, "backend: local") || strings.Contains(text, "backend: docker") {
		t.Fatalf("preview must show structured-wins backend:\n%s", text)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("preview must parse as YAML: %v\n%s", err, text)
	}
}

// Story 8/15/16: structured replacement preserves the operator's spacing and
// real comment; a # inside a quoted scalar is value data.
func TestProjectedConfigYAMLConflictQuotedHashKeepsRealComment(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.CustomConfigFile = "terminal:\n  backend: 'docker#tag'    # keep spacing\n  shell: /bin/zsh\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "backend: local    # keep spacing") || strings.Contains(text, "#tag") {
		t.Fatalf("structured replacement must preserve operator spacing and comment:\n%s", text)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("preview must parse as YAML: %v\n%s", err, text)
	}
}

// Story 15/16: numeric structured leaves win conflicts too, and a structured
// string containing # stays quoted so the preview parses to the exact value
// the runtime receives.
func TestProjectedConfigYAMLNumericAndQuotedStructuredLeavesWin(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
	profile.Fields.Model = "structured # model"
	profile.Fields.CustomConfigFile = "agent:\n  max_turns: 1 # keep numeric comment\nmodel:\n  provider: custom # keep provider comment\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("preview must parse as YAML: %v\n%s", err, text)
	}
	agent, _ := doc["agent"].(map[string]any)
	if agent["max_turns"] != 100000 {
		t.Fatalf("numeric structured leaf must win, got %#v\n%s", agent["max_turns"], text)
	}
	if !strings.Contains(text, "max_turns: 100000 # keep numeric comment") {
		t.Fatalf("operator comment must survive the numeric replacement:\n%s", text)
	}
	model, _ := doc["model"].(map[string]any)
	if model["provider"] != "custom" {
		t.Fatalf("model.provider must stay custom, got %#v\n%s", model["provider"], text)
	}
}

// Story 8/15/16: block-scalar continuation lines stay attached to their
// operator-owned child, and a conflicting structured child replaces the whole
// scalar span so preview semantics match the written runtime config.
func TestProjectedConfigYAMLBlockScalarChildrenRemainWellFormed(t *testing.T) {
	tests := []struct {
		name            string
		overlay         string
		shell           string
		preservedSource string
	}{
		{
			name:    "operator-only shell before injected backend",
			overlay: "terminal:\n  shell: |\n    /bin/zsh\n    -l\n",
			shell:   "/bin/zsh\n-l\n",
		},
		{
			name:    "conflicting backend block scalar",
			overlay: "terminal:\n  backend: |\n    docker\n  shell: /bin/zsh\n",
			shell:   "/bin/zsh",
		},
		{
			name:            "sibling comment after conflicting block scalar",
			overlay:         "terminal:\n  backend: |\n    docker\n  # operator comment for shell\n  shell: /bin/zsh\n",
			shell:           "/bin/zsh",
			preservedSource: "# operator comment for shell",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderHermes}
			profile.Fields.CustomConfigFile = test.overlay
			text, err := runner.ProjectedConfigText(runtimeprofile.ProviderHermes, profile)
			if err != nil {
				t.Fatalf("projected text: %v", err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
				t.Fatalf("preview must parse as YAML: %v\n%s", err, text)
			}
			terminal, _ := doc["terminal"].(map[string]any)
			if terminal["backend"] != "local" || terminal["shell"] != test.shell {
				t.Fatalf("preview must match structured-wins runtime semantics: %#v\n%s", terminal, text)
			}
			if test.preservedSource != "" && !strings.Contains(text, test.preservedSource) {
				t.Fatalf("operator-owned sibling comment must survive block-scalar replacement:\n%s", text)
			}

			layout, err := runner.PrepareTaskLayout(t.TempDir(), "yaml-block-scalar", runtimeprofile.ProviderHermes)
			if err != nil {
				t.Fatalf("prepare layout: %v", err)
			}
			projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
			if err != nil {
				t.Fatalf("write runtime projection: %v", err)
			}
			runtimeRaw, err := os.ReadFile(projection.ConfigPath)
			if err != nil {
				t.Fatalf("read runtime projection: %v", err)
			}
			var runtimeDoc map[string]any
			if err := yaml.Unmarshal(runtimeRaw, &runtimeDoc); err != nil {
				t.Fatalf("runtime projection must parse as YAML: %v\n%s", err, runtimeRaw)
			}
			if !reflect.DeepEqual(doc["terminal"], runtimeDoc["terminal"]) {
				t.Fatalf("preview terminal must equal written runtime terminal:\npreview=%#v\nruntime=%#v\n%s", doc["terminal"], runtimeDoc["terminal"], text)
			}
		})
	}
}

// Story 8/16: an operator-only multiline TOML leaf under a generated table
// merges as one contiguous span with its comment; the merged file stays valid.
func TestProjectedConfigTOMLOperatorMultilineLeafInGeneratedTable(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	profile.Fields.Endpoint = "https://proxy.example.test/v1"
	profile.Fields.CustomConfigFile = "[model_providers.custom]\n# why this list exists\nextra = [\n  \"a\",\n  \"b\",\n]\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatalf("merged TOML must stay valid: %v\n%s", err, text)
	}
	if !strings.Contains(text, "# why this list exists") || !strings.Contains(text, "\"a\",\n  \"b\",\n") {
		t.Fatalf("operator comment and multiline value must survive as one span:\n%s", text)
	}
}

// Story 8/13/16: a dotted operator-only leaf under a generated table remains
// verbatim and appears in the same semantic location in preview and runtime.
func TestProjectedConfigTOMLDottedLeafInGeneratedTable(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	profile.Fields.Endpoint = "https://proxy.example.test/v1"
	profile.Fields.CustomConfigFile = "[model_providers.custom]\nnested.z   =   3 # keep dotted leaf\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatalf("merged TOML must stay valid: %v\n%s", err, text)
	}
	providers, _ := doc["model_providers"].(map[string]any)
	custom, _ := providers["custom"].(map[string]any)
	nested, _ := custom["nested"].(map[string]any)
	if nested["z"] != int64(3) || !strings.Contains(text, "nested.z   =   3 # keep dotted leaf") {
		t.Fatalf("dotted operator leaf must survive verbatim at its semantic path: %#v\n%s", nested, text)
	}
}

// Story 8: trailing standalone comments in a colliding generated table remain
// part of the operator's Custom Config File text on reopen.
func TestProjectedConfigTOMLTrailingStandaloneCommentInGeneratedTable(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	profile.Fields.Endpoint = "https://proxy.example.test/v1"
	profile.Fields.CustomConfigFile = "[model_providers.custom]\nextra = [\n  \"a\",\n  \"b\",\n]\n# operator standalone comment\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, "# operator standalone comment") {
		t.Fatalf("trailing standalone comment must survive verbatim:\n%s", text)
	}
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatalf("merged TOML must stay valid: %v\n%s", err, text)
	}
}

// Story 8: repeated TOML array-of-table blocks survive in source order.
func TestProjectedConfigTOMLArrayOfTablesPreserved(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	profile.Fields.CustomConfigFile = "[[custom.backends]]\nname = \"a\"\n\n[[custom.backends]]\nname = \"b\"\n"
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if strings.Count(text, "[[custom.backends]]") != 2 || !strings.Contains(text, "name = \"a\"") || !strings.Contains(text, "name = \"b\"") {
		t.Fatalf("both array-of-table blocks must survive verbatim:\n%s", text)
	}
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatalf("preview must parse as TOML: %v\n%s", err, text)
	}
}

// Story 8: a multiline root TOML value remains one contiguous root block,
// before generated tables, with its original formatting intact.
func TestProjectedConfigTOMLMultilineRootValuePreserved(t *testing.T) {
	profile := runtimeprofile.Profile{Provider: runtimeprofile.ProviderCodex}
	remainder := "my_list = [\n  \"a\",\n  \"b\",\n]\n"
	profile.Fields.CustomConfigFile = remainder
	text, err := runner.ProjectedConfigText(runtimeprofile.ProviderCodex, profile)
	if err != nil {
		t.Fatalf("projected text: %v", err)
	}
	if !strings.Contains(text, remainder) {
		t.Fatalf("multiline root value must remain contiguous/verbatim:\n%s", text)
	}
	if strings.Index(text, "my_list = [") == -1 {
		t.Fatalf("root value is missing:\n%s", text)
	}
	var doc map[string]any
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatalf("preview must parse as TOML: %v\n%s", err, text)
	}
}
