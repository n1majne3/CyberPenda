package credential

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Materialize reads the secret value described by a binding source.
func Materialize(source Source) (string, error) {
	switch source.Kind {
	case SourceEnv:
		name := strings.TrimSpace(source.Value)
		if name == "" {
			return "", fmt.Errorf("env source name is required")
		}
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("environment variable %q is not set", name)
		}
		return value, nil
	case SourceFile:
		path := strings.TrimSpace(source.Value)
		if path == "" {
			return "", fmt.Errorf("file source path is required")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read credential file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	case SourceCommand:
		command := strings.TrimSpace(source.Value)
		if command == "" {
			return "", fmt.Errorf("command source is required")
		}
		out, err := exec.Command("sh", "-c", command).Output()
		if err != nil {
			return "", fmt.Errorf("run credential command: %w", err)
		}
		value := strings.TrimSpace(string(out))
		if value == "" {
			return "", fmt.Errorf("credential command returned empty output")
		}
		return value, nil
	case SourceLiteral:
		value := strings.TrimSpace(source.Value)
		if value == "" {
			return "", fmt.Errorf("literal source value is required")
		}
		if value == ConfiguredSourceSentinel {
			return "", fmt.Errorf("literal source value is not materialized")
		}
		return value, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidSourceKind, source.Kind)
	}
}

// ResolveMaterializedEnv resolves credential references to env var name -> value
// pairs using each binding's source.Value as the runtime env key.
func (s *Service) ResolveMaterializedEnv(projectID string, refs []string) (map[string]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		resolution, err := s.Resolve(ref, projectID)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %w", ref, err)
		}
		if !resolution.Found || resolution.Disabled || resolution.Source == nil {
			return nil, fmt.Errorf("credential %q is not available", ref)
		}
		value, err := Materialize(*resolution.Source)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %w", ref, err)
		}
		key := strings.TrimSpace(resolution.Source.Value)
		if key == "" {
			return nil, fmt.Errorf("credential %q has empty source value", ref)
		}
		out[key] = value
	}
	return out, nil
}
