package runner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"pentest/internal/runtimeprofile"
)

// OverlayFormat reports the provider-native format of the projected main
// config file for a provider, used to parse the Custom Config File.
func OverlayFormat(provider runtimeprofile.Provider) string {
	return overlayFormat(provider)
}

// overlayFormat is the internal table behind OverlayFormat.
func overlayFormat(provider runtimeprofile.Provider) string {
	switch provider {
	case runtimeprofile.ProviderCodex:
		return "toml"
	case runtimeprofile.ProviderHermes:
		return "yaml"
	case runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi:
		return "json"
	default:
		return ""
	}
}

// parseOverlayDocument parses raw provider-native config text into a generic
// document. Numbers, booleans, arrays, and nested maps round-trip through the
// provider encoder afterwards.
func parseOverlayDocument(provider runtimeprofile.Provider, raw string) (map[string]any, error) {
	format := overlayFormat(provider)
	if format == "" {
		return nil, fmt.Errorf("provider %s has no config projection to overlay", provider)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var doc map[string]any
	var err error
	switch format {
	case "json":
		err = json.Unmarshal([]byte(trimmed), &doc)
	case "toml":
		doc = map[string]any{}
		err = toml.Unmarshal([]byte(trimmed), &doc)
	case "yaml":
		doc = map[string]any{}
		err = yaml.Unmarshal([]byte(trimmed), &doc)
	}
	if err != nil {
		return nil, fmt.Errorf("parse custom config file: %w", err)
	}
	return doc, nil
}

// validateOverlayDocument refuses overlays that contain secret-shaped values.
// Managed-key drift is not refused here: structured fields win conflicts in
// deepMergeConfig, which is the authoritative structured-wins guarantee.
func validateOverlayDocument(provider runtimeprofile.Provider, doc map[string]any) error {
	return scanOverlaySecrets("", doc)
}

func scanOverlaySecrets(prefix string, doc map[string]any) error {
	for key, value := range doc {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if secretEnvKeyPattern.MatchString(key) {
			return fmt.Errorf("custom config file key %q looks like a secret; use the API keys structured field instead", path)
		}
		switch typed := value.(type) {
		case map[string]any:
			if err := scanOverlaySecrets(path, typed); err != nil {
				return err
			}
		case []any:
			for _, item := range typed {
				if child, ok := item.(map[string]any); ok {
					if err := scanOverlaySecrets(path, child); err != nil {
						return err
					}
				}
			}
		case map[string]bool:
			// BurntSushi/toml renders inline bool maps; keys are plugin ids.
			for subKey := range typed {
				if secretEnvKeyPattern.MatchString(subKey) {
					return fmt.Errorf("custom config file key %q looks like a secret; use the API keys structured field instead", path+"."+subKey)
				}
			}
		}
	}
	return nil
}

// applyConfigOverlay deep-merges the parsed Custom Config File over the
// structured generated document. Object keys merge recursively; on any leaf
// conflict the structured (generated) value wins, so drifted overlays can add
// keys the structured fields do not express but never override derived ones.
// Scalars and arrays land whole — arrays are never element-merged.
func applyConfigOverlay(provider runtimeprofile.Provider, generated map[string]any, overlayRaw string) (map[string]any, error) {
	overlay, err := parseOverlayDocument(provider, overlayRaw)
	if err != nil {
		return nil, err
	}
	if err := validateOverlayDocument(provider, overlay); err != nil {
		return nil, err
	}
	if len(overlay) == 0 {
		return generated, nil
	}
	return deepMergeConfig(generated, overlay), nil
}

// ProjectedConfigText renders the provider-native seed the Profile Config
// editor opens on: a complete, realistic file for the provider, derived from
// the profile's structured fields only (no credential resolution, no file
// writes) and redacted so secret values never enter editor text.
func ProjectedConfigText(provider runtimeprofile.Provider, profile runtimeprofile.Profile) (string, error) {
	seed, err := StructuredProjectedConfigText(provider, profile)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(profile.Fields.CustomConfigFile) == "" {
		return seed, nil
	}
	merged, err := MergedProjectedConfig(provider, profile)
	if err != nil {
		return "", err
	}
	return encodeProjectedDocument(provider, merged)
}

func StructuredProjectedConfigText(provider runtimeprofile.Provider, profile runtimeprofile.Profile) (string, error) {
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		settings := map[string]any{"env": redactEnvMap(claudeStructuredEnv(profile))}
		if allowed := claudeTrustedMCPAllowedTools(profile.Fields.MCPServers); len(allowed) > 0 {
			settings["permissions"] = map[string]any{"allow": allowed}
		}
		if refs := enabledExtensionInstallRefs(profile); len(refs) > 0 {
			enabled := make(map[string]bool, len(refs))
			for _, ref := range refs {
				enabled[ref] = true
			}
			settings["enabledPlugins"] = enabled
		}
		raw, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return "", err
		}
		return string(raw), nil
	case runtimeprofile.ProviderCodex:
		return buildCodexConfigTOML(profile, profile.Fields.MCPServers), nil
	case runtimeprofile.ProviderHermes:
		effort, err := runtimeprofile.NormalizeReasoningEffort(profile.Fields.ReasoningEffort)
		if err != nil {
			return "", err
		}
		return buildHermesConfigYAML(profile, nil, profile.Fields.MCPServers, string(effort)), nil
	case runtimeprofile.ProviderPi:
		models := buildPiModels(profile, nil)
		raw, err := json.MarshalIndent(models, "", "  ")
		if err != nil {
			return "", err
		}
		return string(raw), nil
	default:
		return "", fmt.Errorf("provider %s has no config projection", provider)
	}
}

// claudeStructuredEnv renders the structured env view of a Claude Code
// profile without resolving credentials: env vars plus endpoint/model
// fallbacks. Secret-shaped keys are redacted by the caller.
func claudeStructuredEnv(profile runtimeprofile.Profile) map[string]string {
	env := map[string]string{}
	for key, value := range profile.Fields.Env {
		env[key] = value
	}
	if profile.Fields.Endpoint != "" && env["ANTHROPIC_BASE_URL"] == "" {
		env["ANTHROPIC_BASE_URL"] = profile.Fields.Endpoint
	}
	if profile.Fields.Model != "" && env["ANTHROPIC_MODEL"] == "" {
		env["ANTHROPIC_MODEL"] = profile.Fields.Model
	}
	return env
}

// MergedProjectedConfig renders the final merged result the runtime
// receives: the provider-native projected config parsed and deep-merged with
// the profile's Custom Config File overlay (structured fields win). It backs
// the merged config preview so operators see exactly the file shape that
// will run.
func MergedProjectedConfig(provider runtimeprofile.Provider, profile runtimeprofile.Profile) (map[string]any, error) {
	seed, err := StructuredProjectedConfigText(provider, profile)
	if err != nil {
		return nil, err
	}
	generated, err := parseOverlayDocument(provider, seed)
	if err != nil {
		return nil, fmt.Errorf("parse projected config: %w", err)
	}
	return applyConfigOverlay(provider, generated, profile.Fields.CustomConfigFile)
}

func encodeProjectedDocument(provider runtimeprofile.Provider, doc map[string]any) (string, error) {
	if doc == nil {
		doc = map[string]any{}
	}
	switch overlayFormat(provider) {
	case "json":
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return "", err
		}
		return string(raw), nil
	case "toml":
		var b strings.Builder
		if err := toml.NewEncoder(&b).Encode(doc); err != nil {
			return "", err
		}
		return b.String(), nil
	case "yaml":
		var b strings.Builder
		encoder := yaml.NewEncoder(&b)
		encoder.SetIndent(2)
		if err := encoder.Encode(doc); err != nil {
			return "", err
		}
		_ = encoder.Close()
		return b.String(), nil
	default:
		return "", fmt.Errorf("provider %s has no config projection", provider)
	}
}

// deepMergeConfig merges overlay into base. Recursion happens only when both
// sides hold maps; every other existing base value (scalar, array, type
// mismatch) is kept — structured fields win conflicts, overlays add the rest.
func deepMergeConfig(base, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, overlayValue := range overlay {
		baseValue, exists := merged[key]
		if exists {
			baseMap, baseOK := normalizeConfigMap(baseValue)
			overlayMap, overlayOK := normalizeConfigMap(overlayValue)
			if baseOK && overlayOK {
				merged[key] = deepMergeConfig(baseMap, overlayMap)
				continue
			}
			// Existing structured leaf: it wins.
			continue
		}
		merged[key] = overlayValue
	}
	return merged
}

// normalizeConfigMap widens provider-native map shapes (map[string]string,
// map[string]bool, map[any]any from YAML, map[interface{}]interface{}) into
// the generic map[string]any merge shape.
func normalizeConfigMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	case map[string]bool:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			text, ok := key.(string)
			if !ok {
				return nil, false
			}
			out[text] = item
		}
		return out, true
	default:
		return nil, false
	}
}
