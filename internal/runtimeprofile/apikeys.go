package runtimeprofile

import "strings"

// ConfiguredAPIKeySentinel is returned by the API instead of secret values.
const ConfiguredAPIKeySentinel = "[configured]"

// DefaultAPIKeyEnv returns the primary env var name for inline API keys per provider.
func DefaultAPIKeyEnv(provider Provider) string {
	switch provider {
	case ProviderClaudeCode:
		return "ANTHROPIC_AUTH_TOKEN"
	case ProviderCodex:
		return "OPENAI_API_KEY"
	case ProviderPi:
		return "ANTHROPIC_API_KEY"
	default:
		return "API_KEY"
	}
}

// SanitizeProfile returns a copy safe for API responses. Inline API key values are
// replaced with ConfiguredAPIKeySentinel.
func SanitizeProfile(profile Profile) Profile {
	if len(profile.Fields.APIKeys) == 0 {
		return profile
	}
	sanitized := profile
	sanitized.Fields.APIKeys = SanitizeAPIKeys(profile.Fields.APIKeys)
	return sanitized
}

// SanitizeAPIKeys masks secret values for API responses.
func SanitizeAPIKeys(keys map[string]string) map[string]string {
	if len(keys) == 0 {
		return nil
	}
	out := make(map[string]string, len(keys))
	for key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = ConfiguredAPIKeySentinel
	}
	return out
}

// MergeAPIKeys applies an update while preserving existing secrets when the client
// sends empty values or the configured sentinel (unchanged placeholder).
func MergeAPIKeys(existing, incoming map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range existing {
		key = strings.TrimSpace(key)
		if key == "" || strings.TrimSpace(value) == "" {
			continue
		}
		out[key] = value
	}
	for key, value := range incoming {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || value == ConfiguredAPIKeySentinel {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MaterializedAPIKeys returns env var name -> secret value from inline profile keys.
func MaterializedAPIKeys(profile Profile) map[string]string {
	if len(profile.Fields.APIKeys) == 0 {
		return nil
	}
	out := make(map[string]string, len(profile.Fields.APIKeys))
	for key, value := range profile.Fields.APIKeys {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" || value == ConfiguredAPIKeySentinel {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HasInlineAPIKeys reports whether the profile stores at least one inline key.
func HasInlineAPIKeys(profile Profile) bool {
	return len(MaterializedAPIKeys(profile)) > 0
}