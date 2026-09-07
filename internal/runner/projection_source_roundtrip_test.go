package runner_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

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
