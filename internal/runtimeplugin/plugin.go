// Package runtimeplugin owns declarative runtime provider manifests.
package runtimeplugin

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const SchemaVersion = 1

var (
	ErrInvalidPlugin = errors.New("invalid runtime plugin")
	idPattern        = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
	valueLikePattern = regexp.MustCompile(`(?i:[=/]|sk-|bearer\s+|api[_-]?key=|token=|secret=)`)
)

type Plugin struct {
	SchemaVersion    int               `json:"schema_version"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	Binary           Binary            `json:"binary"`
	Capabilities     Capabilities      `json:"capabilities"`
	ProfileSchema    ProfileSchema     `json:"profile_schema"`
	ConfigProjection ConfigProjection  `json:"config_projection"`
	Launch           LaunchTemplate    `json:"launch"`
	ProcessEnv       map[string]string `json:"process_env,omitempty"`
	CredentialEnv    []string          `json:"credential_env,omitempty"`
	Transcript       Transcript        `json:"transcript"`
}

type Binary struct {
	Default      string `json:"default"`
	ProfileField string `json:"profile_field,omitempty"`
}

type Capabilities struct {
	Sandbox             bool `json:"sandbox"`
	Host                bool `json:"host"`
	MCPConfig           bool `json:"mcp_config"`
	StreamingTranscript bool `json:"streaming_transcript"`
	Resume              bool `json:"resume"`
}

type ProfileSchema struct {
	Fields []ProfileField `json:"fields"`
}

type ProfileField struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

type ConfigProjection struct {
	Primitive     string `json:"primitive"`
	ConfigPath    string `json:"config_path,omitempty"`
	MCPConfigPath string `json:"mcp_config_path,omitempty"`
}

type LaunchTemplate struct {
	Args             []string          `json:"args"`
	SingletonOptions []SingletonOption `json:"singleton_options,omitempty"`
}

type SingletonOption struct {
	Options []string `json:"options"`
	Arity   int      `json:"arity"`
}

type Transcript struct {
	Parser string `json:"parser"`
}

var profileFieldTypes = map[string]bool{
	"string":             true,
	"url":                true,
	"string_list":        true,
	"env_map":            true,
	"secret_env_map":     true,
	"mcp_servers":        true,
	"runtime_extensions": true,
	"runner":             true,
}

var projectionPrimitives = map[string]bool{
	"none":            true,
	"generic_config":  true,
	"codex_home":      true,
	"claude_settings": true,
	"pi_agent":        true,
}

var transcriptParsers = map[string]bool{
	"plain_runtime_output": true,
	"codex_json":           true,
	"claude_stream_json":   true,
	"pi_json_session":      true,
}

func Validate(plugin Plugin) error {
	if plugin.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema_version must be %d", ErrInvalidPlugin, SchemaVersion)
	}
	if !idPattern.MatchString(strings.TrimSpace(plugin.ID)) {
		return fmt.Errorf("%w: invalid id %q", ErrInvalidPlugin, plugin.ID)
	}
	if strings.TrimSpace(plugin.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidPlugin)
	}
	if strings.TrimSpace(plugin.Binary.Default) == "" {
		return fmt.Errorf("%w: binary.default is required", ErrInvalidPlugin)
	}
	if !projectionPrimitives[plugin.ConfigProjection.Primitive] {
		return fmt.Errorf("%w: unknown config projection primitive %q", ErrInvalidPlugin, plugin.ConfigProjection.Primitive)
	}
	if !transcriptParsers[plugin.Transcript.Parser] {
		return fmt.Errorf("%w: unknown transcript parser %q", ErrInvalidPlugin, plugin.Transcript.Parser)
	}
	if len(plugin.Launch.Args) == 0 {
		return fmt.Errorf("%w: launch args are required", ErrInvalidPlugin)
	}
	seen := map[string]bool{}
	for _, field := range plugin.ProfileSchema.Fields {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			return fmt.Errorf("%w: profile field name is required", ErrInvalidPlugin)
		}
		if seen[name] {
			return fmt.Errorf("%w: duplicate profile field %q", ErrInvalidPlugin, name)
		}
		seen[name] = true
		if !profileFieldTypes[field.Type] {
			return fmt.Errorf("%w: unknown profile field type %q", ErrInvalidPlugin, field.Type)
		}
	}
	for _, env := range plugin.CredentialEnv {
		if strings.TrimSpace(env) == "" {
			return fmt.Errorf("%w: credential env name is required", ErrInvalidPlugin)
		}
		if valueLikePattern.MatchString(env) {
			return fmt.Errorf("%w: credential env must be a variable name, got %q", ErrInvalidPlugin, env)
		}
	}
	for _, singleton := range plugin.Launch.SingletonOptions {
		if singleton.Arity < 0 {
			return fmt.Errorf("%w: singleton arity must be non-negative", ErrInvalidPlugin)
		}
		if len(singleton.Options) == 0 {
			return fmt.Errorf("%w: singleton option group is empty", ErrInvalidPlugin)
		}
	}
	return nil
}

func SupportedProjectionPrimitive(name string) bool {
	return projectionPrimitives[name]
}

func SupportedTranscriptParser(name string) bool {
	return transcriptParsers[name]
}
