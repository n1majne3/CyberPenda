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
// Scalars and arrays land whole. The Hermes plugins.enabled list is the
// single exception: harness-derived entries coexist with operator-added ones.
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
	return deepMergeConfig("", generated, overlay), nil
}

// ProjectedConfigText renders the provider-native seed the Profile Config
// editor opens on: a complete, realistic file for the provider, derived from
// the same projection builders launch uses (no file writes) and redacted so
// secret values never enter editor text.
func ProjectedConfigText(provider runtimeprofile.Provider, profile runtimeprofile.Profile) (string, error) {
	return ProjectedConfigTextWith(provider, profile, ProjectionRequest{})
}

// ProjectedConfigTextWith is ProjectedConfigText with launch-equivalent
// Model Provider resolution and Pi global projection.
func ProjectedConfigTextWith(provider runtimeprofile.Provider, profile runtimeprofile.Profile, req ProjectionRequest) (string, error) {
	seed, err := StructuredProjectedConfigTextWith(provider, profile, req)
	if err != nil {
		return "", err
	}
	remainder := profile.Fields.CustomConfigFile
	if strings.TrimSpace(remainder) == "" {
		return seed, nil
	}
	// Story 8: TOML/YAML reopen keeps the remainder byte-for-byte, including
	// comments. JSON has no comments, so it still merge-encodes.
	switch overlayFormat(provider) {
	case "toml", "yaml":
		return spliceProjectedRemainder(seed, remainder), nil
	}
	merged, err := MergedProjectedConfigWith(provider, profile, req)
	if err != nil {
		return "", err
	}
	return encodeProjectedDocument(provider, merged)
}

func spliceProjectedRemainder(seed, remainder string) string {
	seed = strings.TrimRight(seed, "\n")
	if remainder == "" {
		return seed + "\n"
	}
	if strings.HasPrefix(remainder, "\n") {
		return seed + remainder
	}
	return seed + "\n" + remainder
}

// redactMCPServerURLs strips token query parameters from trusted MCP URLs so
// the editor preview never carries the daemon operator credential.
func redactMCPServerURLs(servers []runtimeprofile.MCPServer) []runtimeprofile.MCPServer {
	out := make([]runtimeprofile.MCPServer, 0, len(servers))
	for _, server := range servers {
		if url := strings.TrimSpace(server.URL); url != "" {
			if cut, _, found := strings.Cut(url, "?token="); found {
				server.URL = cut
			}
		}
		out = append(out, server)
	}
	return out
}

func StructuredProjectedConfigText(provider runtimeprofile.Provider, profile runtimeprofile.Profile) (string, error) {
	return StructuredProjectedConfigTextWith(provider, profile, ProjectionRequest{})
}

// StructuredProjectedConfigTextWith renders the structured projection using
// the same builders as launch. When the request carries CredentialEnvNames
// (the editor preview path), credential-derived env keys render as redacted
// placeholders from metadata only and the trusted MCP URL carries no token.
func StructuredProjectedConfigTextWith(provider runtimeprofile.Provider, profile runtimeprofile.Profile, req ProjectionRequest) (string, error) {
	profile = resolvePreviewProfile(profile, req)
	projected, err := listPiLaunchReadyProviders(profile, req)
	if err != nil {
		return "", err
	}
	servers, err := collectMCPServers(profile, req)
	if err != nil {
		return "", err
	}
	preview := len(req.CredentialEnvNames) > 0 || (req.DaemonAddr != "" && req.AuthToken == "")
	if preview {
		req.AuthToken = ""
		servers = redactMCPServerURLs(servers)
	}
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		var env map[string]string
		if preview {
			env = claudeStructuredEnv(profile)
			for _, name := range req.CredentialEnvNames {
				env[name] = "[REDACTED]"
			}
		} else {
			env, err = buildClaudeEnv(profile, req)
			if err != nil {
				return "", err
			}
		}
		settings := map[string]any{"env": redactEnvMap(env)}
		if allowed := claudeTrustedMCPAllowedTools(servers); len(allowed) > 0 {
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
		return buildCodexConfigTOML(profile, servers), nil
	case runtimeprofile.ProviderHermes:
		effort, err := runtimeprofile.NormalizeReasoningEffort(profile.Fields.ReasoningEffort)
		if err != nil {
			return "", err
		}
		return buildHermesConfigYAML(profile, projected, servers, string(effort)), nil
	case runtimeprofile.ProviderPi:
		models := buildPiModels(profile, nil)
		if len(projected) > 0 {
			models = buildPiModelsFromProjected(projected)
		}
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
	return MergedProjectedConfigWith(provider, profile, ProjectionRequest{})
}

func MergedProjectedConfigWith(provider runtimeprofile.Provider, profile runtimeprofile.Profile, req ProjectionRequest) (map[string]any, error) {
	seed, err := StructuredProjectedConfigTextWith(provider, profile, req)
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

// deepMergeConfig merges overlay into base. Recursion happens when both
// sides hold maps; arrays are whole leaves (structured wins) except for
// plugins.enabled, which unions so Hermes operator plugins coexist.
func deepMergeConfig(path string, base, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, overlayValue := range overlay {
		childPath := key
		if path != "" {
			childPath = path + "." + key
		}
		baseValue, exists := merged[key]
		if exists {
			baseMap, baseOK := normalizeConfigMap(baseValue)
			overlayMap, overlayOK := normalizeConfigMap(overlayValue)
			if baseOK && overlayOK {
				merged[key] = deepMergeConfig(childPath, baseMap, overlayMap)
				continue
			}
			baseArr, baseArrOK := normalizeConfigArray(baseValue)
			overlayArr, overlayArrOK := normalizeConfigArray(overlayValue)
			if baseArrOK && overlayArrOK && childPath == "plugins.enabled" {
				merged[key] = unionConfigArrays(baseArr, overlayArr)
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

// normalizeConfigArray widens YAML/TOML array shapes into []any.
func normalizeConfigArray(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

// unionConfigArrays keeps base order and appends overlay entries that are
// not already present. Structured (harness-derived) entries stay first;
// operator-added entries coexist.
func unionConfigArrays(base, overlay []any) []any {
	out := make([]any, 0, len(base)+len(overlay))
	seen := map[string]bool{}
	add := func(item any) {
		key := fmt.Sprintf("%#v", item)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, item)
	}
	for _, item := range base {
		add(item)
	}
	for _, item := range overlay {
		add(item)
	}
	return out
}
