package credential

import (
	"fmt"
	"os"
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

// ResolveGlobalEnv materializes every active global Credential Binding into a
// runtime env var name -> value map. Each binding projects under its
// DestinationEnv (or, for env sources, the variable name in Value). Disabled
// bindings are skipped. A binding that cannot be materialized or that lacks a
// projectable env var name returns an error naming the credential reference so
// preflight can block launch.
//
// This is the mechanism behind the Global Environment Variable concept: one
// global binding injects into every Runtime without a per-profile
// credential_ref.
func (s *Service) ResolveGlobalEnv() (map[string]string, error) {
	bindings, err := s.ListGlobal()
	if err != nil {
		return nil, fmt.Errorf("list global bindings: %w", err)
	}
	out := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Disabled {
			continue
		}
		envName, value, err := ResolveSourceEnv(binding.Source)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %w", binding.CredentialRef, err)
		}
		out[envName] = value
	}
	return out, nil
}

// ResolveMaterializedEnv resolves credential references to env var name -> value
// pairs. The runtime env key is the binding's DestinationEnv when set; for env
// sources it falls back to Value (so existing bindings behave unchanged).
// Literal sources must declare DestinationEnv, otherwise they would project
// under a secret-shaped key instead of a real env var.
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
		envName, value, err := ResolveSourceEnv(*resolution.Source)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %w", ref, err)
		}
		out[envName] = value
	}
	return out, nil
}

// ResolveSourceEnv materializes a credential source and returns the runtime
// env var name it projects under together with its value. It is the single
// check that projection performs per credential, so preflight can call it to
// validate that a source is launch-ready (materializable AND projectable under
// a real env var name) without duplicating the destination-env logic.
func ResolveSourceEnv(source Source) (envName, value string, err error) {
	value, err = Materialize(source)
	if err != nil {
		return "", "", err
	}
	envName, err = destinationEnv(source)
	if err != nil {
		return "", "", err
	}
	return envName, value, nil
}

// destinationEnv returns the runtime env var name a materialized secret projects
// under. DestinationEnv wins; otherwise env sources fall back to their Value
// (the variable name). Literal sources must declare DestinationEnv because
// their Value is a secret, not an env var name.
// DestinationEnv returns the runtime env var name a credential source
// projects under, without materializing any secret value. DestinationEnv
// wins; env sources fall back to their Value (the variable name).
func DestinationEnv(source Source) (string, error) {
	return destinationEnv(source)
}

func destinationEnv(source Source) (string, error) {
	if dest := strings.TrimSpace(source.DestinationEnv); dest != "" {
		return dest, nil
	}
	if source.Kind == SourceEnv {
		return strings.TrimSpace(source.Value), nil
	}
	return "", fmt.Errorf("%s source must declare destination_env to project as a runtime env var", source.Kind)
}
