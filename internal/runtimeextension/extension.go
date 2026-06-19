// Package runtimeextension owns runtime-native extension manifests.
package runtimeextension

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const SchemaVersion = 1

var (
	ErrInvalidExtension = errors.New("invalid runtime extension")
	idPattern           = regexp.MustCompile("^[a-z][a-z0-9_.-]*$")
	secretLikePattern   = regexp.MustCompile("(?i)(sk-|bearer\\s+|api[_-]?key=|token=|secret=|password=)")
)

type Extension struct {
	SchemaVersion            int               `json:"schema_version"`
	ID                       string            `json:"id"`
	Name                     string            `json:"name"`
	Description              string            `json:"description,omitempty"`
	CompatibleRuntimePlugins []string          `json:"compatible_runtime_plugins"`
	Source                   Source            `json:"source"`
	Projection               Projection        `json:"projection"`
	Config                   map[string]string `json:"config,omitempty"`
}

type Source struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

type Projection struct {
	Location string `json:"location"`
	Path     string `json:"path"`
}

var sourceTypes = map[string]bool{
	"local_dir":  true,
	"local_file": true,
}

var projectionLocations = map[string]bool{
	"provider_home": true,
	"runtime_home":  true,
	"workdir":       true,
}

func Validate(extension Extension) error {
	if extension.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema_version must be %d", ErrInvalidExtension, SchemaVersion)
	}
	if !idPattern.MatchString(strings.TrimSpace(extension.ID)) {
		return fmt.Errorf("%w: invalid id %q", ErrInvalidExtension, extension.ID)
	}
	if strings.TrimSpace(extension.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidExtension)
	}
	if len(extension.CompatibleRuntimePlugins) == 0 {
		return fmt.Errorf("%w: compatible_runtime_plugins is required", ErrInvalidExtension)
	}
	seen := map[string]bool{}
	for _, pluginID := range extension.CompatibleRuntimePlugins {
		pluginID = strings.TrimSpace(pluginID)
		if !idPattern.MatchString(pluginID) {
			return fmt.Errorf("%w: invalid compatible runtime plugin %q", ErrInvalidExtension, pluginID)
		}
		if seen[pluginID] {
			return fmt.Errorf("%w: duplicate compatible runtime plugin %q", ErrInvalidExtension, pluginID)
		}
		seen[pluginID] = true
	}
	if !sourceTypes[extension.Source.Type] {
		return fmt.Errorf("%w: unknown source type %q", ErrInvalidExtension, extension.Source.Type)
	}
	if strings.TrimSpace(extension.Source.Path) == "" {
		return fmt.Errorf("%w: source path is required", ErrInvalidExtension)
	}
	if secretLikePattern.MatchString(extension.Source.Path) {
		return fmt.Errorf("%w: source path looks like it contains a secret", ErrInvalidExtension)
	}
	if !projectionLocations[extension.Projection.Location] {
		return fmt.Errorf("%w: unknown projection location %q", ErrInvalidExtension, extension.Projection.Location)
	}
	if err := validateRelativePath(extension.Projection.Path); err != nil {
		return err
	}
	for key, value := range extension.Config {
		if secretLikePattern.MatchString(key + "=" + value) {
			return fmt.Errorf("%w: config value for %q looks like a secret", ErrInvalidExtension, key)
		}
	}
	return nil
}

func CompatibleWith(extension Extension, runtimePluginID string) bool {
	for _, compatible := range extension.CompatibleRuntimePlugins {
		if compatible == runtimePluginID {
			return true
		}
	}
	return false
}

func validateRelativePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("%w: projection path is required", ErrInvalidExtension)
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return fmt.Errorf("%w: projection path must be relative", ErrInvalidExtension)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: projection path must not escape target root", ErrInvalidExtension)
		}
	}
	return nil
}
