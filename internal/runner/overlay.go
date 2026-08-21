package runner

import (
	"encoding/json"
	"fmt"
	"sort"
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
		return spliceProjectedRemainder(provider, seed, remainder), nil
	}
	merged, err := MergedProjectedConfigWith(provider, profile, req)
	if err != nil {
		return "", err
	}
	return encodeProjectedDocument(provider, merged)
}

// spliceProjectedRemainder merges the operator's remainder into the generated
// seed without duplicating top-level keys. Keys only in the remainder keep
// their verbatim text (comments included); a key present on both sides is
// merged structurally — scalars/maps keep the structured value, arrays union —
// so the editor shows one coherent document that still parses.
func spliceProjectedRemainder(provider runtimeprofile.Provider, seed, remainder string) string {
	seed = strings.TrimRight(seed, "\n")
	if remainder == "" {
		return seed + "\n"
	}
	if !strings.HasPrefix(remainder, "\n") {
		remainder = "\n" + remainder
	}
	remainderKeys := topLevelKeys(provider, remainder)
	if len(remainderKeys) == 0 {
		return seed + remainder
	}

	blocks := topLevelBlocks(provider, remainder)
	var mergedParts []string
	lines := strings.Split(seed, "\n")
	format := overlayFormat(provider)

	consumed := map[string]bool{}
	appendRemainderOnly := func() string {
		// Remainder keys absent from the seed keep their verbatim block.
		tail := ""
		for _, key := range sortedRemainderKeysNonConsumed(blocks, consumed) {
			if key == "" {
				continue
			}
			tail += "\n" + blocks[key]
		}
		return tail
	}
	if format == "yaml" {
		currentKey := ""
		var currentSpan []string
		flush := func() {
			if currentKey == "" {
				return
			}
			if remainderKeys[currentKey] {
				mergedParts = append(mergedParts, mergeCollidingBlock(provider, currentKey, currentSpan, blocks[currentKey]))
				consumed[currentKey] = true
			} else {
				mergedParts = append(mergedParts, strings.Join(currentSpan, "\n"))
			}
			currentSpan = nil
		}
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			atRoot := trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "-")
			if atRoot && strings.HasSuffix(trimmed, ":") {
				flush()
				currentKey = strings.TrimSuffix(trimmed, ":")
				currentSpan = []string{line}
				continue
			}
			if currentKey != "" {
				currentSpan = append(currentSpan, line)
			} else if trimmed != "" || line == "" {
				mergedParts = append(mergedParts, line)
			}
		}
		flush()
		result := strings.Join(mergedParts, "\n")
		// Root-level scalar lines from the remainder (no trailing colon)
		// and unconsumed blocks append at the document root.
		if commentBlock, ok := blocks[""]; ok {
			result += "\n" + commentBlock
			consumed[""] = true
		}
		result += appendRemainderOnly()
		return result
	}

	// TOML: root key/values and [tables].
	currentTable := ""
	var currentTableSpan []string
	rootLines := make([]string, 0)
	tables := map[string][]string{}
	tableOrder := make([]string, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if currentTable != "" {
				tables[currentTable] = append(tables[currentTable], currentTableSpan...)
			}
			currentTable = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			currentTableSpan = []string{line}
			tableOrder = append(tableOrder, currentTable)
			continue
		}
		if currentTable != "" {
			currentTableSpan = append(currentTableSpan, line)
			continue
		}
		rootLines = append(rootLines, line)
	}
	if currentTable != "" {
		tables[currentTable] = append(tables[currentTable], currentTableSpan...)
	}

	out := make([]string, 0, len(rootLines))
	for _, line := range rootLines {
		trimmed := strings.TrimSpace(line)
		if key, _, found := strings.Cut(trimmed, "="); found && remainderKeys[strings.TrimSpace(key)] {
			collide := strings.TrimSpace(key)
			out = append(out, mergeCollidingBlock(provider, collide, []string{line}, blocks[collide]))
			delete(blocks, collide)
			continue
		}
		out = append(out, line)
	}
	// Remainder root keys must land BEFORE any generated table: TOML bare
	// keys after a [table] header belong to that table.
	for _, key := range sortedRemainderKeys(blocks) {
		if key == "" {
			continue
		}
		if !strings.Contains(blocks[key], "[") && remainderKeys[key] && !seedHasTopLevelKey(provider, seed, key) && isRootKeyValue(blocks[key]) {
			out = append(out, blocks[key])
			delete(blocks, key)
		}
	}
	seenTables := map[string]bool{}
	for _, table := range tableOrder {
		if seenTables[table] {
			continue
		}
		seenTables[table] = true
		if remainderKeys[table] {
			out = append(out, mergeCollidingBlock(provider, table, tables[table], blocks[table]))
			delete(blocks, table)
			continue
		}
		out = append(out, tables[table]...)
	}
	// Remainder keys that do not exist in the seed stay verbatim (comments
	// and formatting included). A leading comment block goes first.
	if commentBlock, ok := blocks[""]; ok {
		out = append(out, commentBlock)
		delete(blocks, "")
	}
	for _, key := range sortedRemainderKeys(blocks) {
		out = append(out, blocks[key])
	}
	return strings.Join(out, "\n")
}

// sortedRemainderKeys lists remainder-only keys in stable order.
func sortedRemainderKeys(blocks map[string]string) []string {
	keys := make([]string, 0, len(blocks))
	for key := range blocks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// isRootKeyValue reports whether a TOML block is a single "key = value" line
// rather than a [table].
func isRootKeyValue(block string) bool {
	trimmed := strings.TrimSpace(block)
	return trimmed != "" && !strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "=")
}

// sortedRemainderKeysNonConsumed lists remainder keys that the seed merge did
// not consume, so they append at the document root.
func sortedRemainderKeysNonConsumed(blocks map[string]string, consumed map[string]bool) []string {
	keys := make([]string, 0, len(blocks))
	for key := range blocks {
		if !consumed[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// topLevelBlocks splits a document into verbatim text blocks keyed by their
// root-level key. YAML blocks span the header plus its indented subtree; TOML
// blocks are either a "key = ..." line or a full [table].
func topLevelBlocks(provider runtimeprofile.Provider, raw string) map[string]string {
	blocks := map[string]string{}
	format := overlayFormat(provider)
	lines := strings.Split(raw, "\n")
	if format == "yaml" {
		currentKey := ""
		var current []string
		flush := func() {
			if currentKey != "" {
				blocks[currentKey] = strings.Join(current, "\n")
			}
			current = nil
		}
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			atRoot := trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "-")
			if atRoot && strings.HasSuffix(trimmed, ":") {
				flush()
				currentKey = strings.TrimSuffix(trimmed, ":")
				current = []string{line}
				continue
			}
			if atRoot {
				// A root-level "scalar_key: value" line is its own block.
				flush()
				if key, _, found := strings.Cut(trimmed, ":"); found {
					currentKey = strings.TrimSpace(key)
					blocks[currentKey] = line
					currentKey = ""
				}
				continue
			}
			if currentKey != "" {
				current = append(current, line)
			}
		}
		flush()
		return blocks
	}
	currentTable := ""
	var current []string
	flush := func() {
		if currentTable != "" {
			blocks[currentTable] = strings.Join(current, "\n")
		}
		current = nil
	}
	var preamble []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			flush()
			currentTable = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			current = []string{line}
			continue
		}
		if currentTable != "" {
			current = append(current, line)
			continue
		}
		if key, _, found := strings.Cut(trimmed, "="); found && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			blocks[strings.TrimSpace(key)] = line
			continue
		}
		if trimmed != "" || line == "" {
			preamble = append(preamble, line)
		}
	}
	flush()
	if len(preamble) > 0 {
		blocks[""] = strings.Join(preamble, "\n")
	}
	return blocks
}

// mergeCollidingBlock merges one colliding top-level key: the structured seed
// span and the operator block are parsed and deep-merged (structured wins,
// plugins.enabled unions), then re-encoded in the provider format.
func mergeCollidingBlock(provider runtimeprofile.Provider, key string, seedSpan []string, operatorBlock string) string {
	format := overlayFormat(provider)
	parse := func(text string) map[string]any {
		var doc map[string]any
		if format == "yaml" {
			body := strings.TrimSpace(text)
			if strings.HasPrefix(body, key+":") {
				// The span is already a full "key: ..." document.
				_ = yaml.Unmarshal([]byte(body), &doc)
				if inner, ok := doc[key]; ok {
					return map[string]any{key: inner}
				}
				return doc
			}
			wrapped := key + ":\n" + indentLines(body, "  ")
			_ = yaml.Unmarshal([]byte(wrapped), &doc)
			if inner, ok := doc[key].(map[string]any); ok {
				doc = inner
			}
		} else {
			wrapped := "[parent]\n" + text
			_ = toml.Unmarshal([]byte(wrapped), &doc)
			if inner, ok := doc["parent"].(map[string]any); ok {
				doc = inner
			}
		}
		if doc == nil {
			doc = map[string]any{}
		}
		return doc
	}
	seedDoc := parse(strings.Join(seedSpan, "\n"))
	operatorDoc := parse(operatorBlock)
	merged := deepMergeConfig("", seedDoc, operatorDoc)
	value := merged[key]
	if value == nil {
		value = merged
	}
	var b strings.Builder
	if format == "yaml" {
		encoder := yaml.NewEncoder(&b)
		encoder.SetIndent(2)
		if err := encoder.Encode(map[string]any{key: value}); err == nil {
			text := b.String()
			return strings.TrimRight(text, "\n") + "\n"
		}
	} else if format == "toml" {
		if err := toml.NewEncoder(&b).Encode(map[string]any{key: value}); err == nil {
			return b.String()
		}
	}
	return strings.Join(seedSpan, "\n")
}

// indentLines prefixes every non-empty line with the given indentation.
func indentLines(text, indent string) string {
	split := strings.Split(text, "\n")
	for i, line := range split {
		if strings.TrimSpace(line) != "" {
			split[i] = indent + line
		}
	}
	return strings.Join(split, "\n")
}

// InlineAPIKeyEnvNames lists the env var names the profile's inline API keys
// project under at launch. Metadata only — never secret values.
func InlineAPIKeyEnvNames(profile runtimeprofile.Profile) []string {
	keys := runtimeprofile.MaterializedAPIKeys(profile)
	names := make([]string, 0, len(keys))
	for key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			names = append(names, key)
		}
	}
	sort.Strings(names)
	return names
}

// topLevelKeys lists the root-level keys of a provider-native document.
func topLevelKeys(provider runtimeprofile.Provider, raw string) map[string]bool {
	keys := map[string]bool{}
	for key := range topLevelBlocks(provider, raw) {
		keys[key] = true
	}
	return keys
}

// seedHasTopLevelKey reports whether the seed text declares the key at root level.
func seedHasTopLevelKey(provider runtimeprofile.Provider, seed, key string) bool {
	format := overlayFormat(provider)
	for _, line := range strings.Split(seed, "\n") {
		trimmed := strings.TrimSpace(line)
		switch format {
		case "yaml":
			if !strings.HasPrefix(line, " ") && strings.HasPrefix(trimmed, key+":") {
				return true
			}
		case "toml":
			if strings.HasPrefix(trimmed, "[") && strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]") == key {
				return true
			}
		}
	}
	return false
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
			// Every credential channel the launch materializes renders as
			// a redacted placeholder key: inline API keys, the resolved
			// Model Provider API-key env, and global bindings.
			for _, name := range InlineAPIKeyEnvNames(profile) {
				env[name] = "[REDACTED]"
			}
			if req.ModelSnapshot != nil && strings.TrimSpace(req.ModelSnapshot.APIKeyEnv) != "" {
				env[req.ModelSnapshot.APIKeyEnv] = "[REDACTED]"
				env["ANTHROPIC_API_KEY"] = "[REDACTED]"
			}
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
