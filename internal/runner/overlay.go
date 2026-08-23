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
		// Remainder keys absent from the seed keep their verbatim block in
		// the operator's original document order (Story 8 verbatim).
		tail := ""
		for _, key := range blocks.keysInOrder() {
			if key == "" || consumed[key] {
				continue
			}
			if raw, ok := blocks.get(key); ok {
				tail += "\n" + raw
			}
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
			if raw, ok := blocks.get(currentKey); ok && remainderKeys[currentKey] {
				mergedParts = append(mergedParts, mergeCollidingBlock(provider, currentKey, currentSpan, raw))
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
		// A preamble comment block belongs at the very top of the document.
		if preambleBlock, ok := blocks.get(""); ok {
			result = preambleBlock + "\n" + result
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
			if raw, ok := blocks.get(collide); ok {
				out = append(out, mergeCollidingBlock(provider, collide, []string{line}, raw))
				blocks.delete(collide)
			} else {
				out = append(out, line)
			}
			continue
		}
		out = append(out, line)
	}
	// Remainder root keys must land BEFORE any generated table: TOML bare
	// keys after a [table] header belong to that table.
	for _, key := range blocks.keysInOrder() {
		if key == "" {
			continue
		}
		if raw, ok := blocks.get(key); ok && !strings.Contains(raw, "[") && remainderKeys[key] && !seedHasTopLevelKey(provider, seed, key) && isRootKeyValue(raw) {
			out = append(out, raw)
			blocks.delete(key)
		}
	}
	seenTables := map[string]bool{}
	for _, table := range tableOrder {
		if seenTables[table] {
			continue
		}
		seenTables[table] = true
		if remainderKeys[table] {
			if raw, ok := blocks.get(table); ok {
				out = append(out, mergeCollidingBlock(provider, table, tables[table], raw))
				blocks.delete(table)
				continue
			}
		}
		out = append(out, tables[table]...)
	}
	// Remaining blocks stay verbatim (comments and formatting included) in
	// document order; a comment block goes first.
	if commentBlock, ok := blocks.get(""); ok {
		out = append(out, commentBlock)
		blocks.delete("")
	}
	for _, key := range blocks.keysInOrder() {
		if raw, ok := blocks.get(key); ok {
			out = append(out, raw)
		}
	}
	return strings.Join(out, "\n")
}

// docBlocks is an ordered collection of document blocks keyed by their
// root-level key. Order matches the source document so reopen preserves
// the operator's raw root-key sequence (Story 8 verbatim).
type docBlocks struct {
	order []string
	byKey map[string]string
}

func newDocBlocks() *docBlocks {
	return &docBlocks{byKey: map[string]string{}}
}

func (d *docBlocks) set(key, raw string) {
	if _, exists := d.byKey[key]; !exists {
		d.order = append(d.order, key)
	}
	d.byKey[key] = raw
}

func (d *docBlocks) get(key string) (string, bool) {
	raw, ok := d.byKey[key]
	return raw, ok
}

func (d *docBlocks) delete(key string) {
	delete(d.byKey, key)
	for i, existing := range d.order {
		if existing == key {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
}

func (d *docBlocks) keysInOrder() []string {
	out := make([]string, 0, len(d.order))
	out = append(out, d.order...)
	return out
}

// topLevelBlocks splits a document into verbatim text blocks keyed by their
// root-level key. YAML blocks span the header plus its indented subtree; TOML
// blocks are either a "key = ..." line or a full [table].
func topLevelBlocks(provider runtimeprofile.Provider, raw string) *docBlocks {
	blocks := newDocBlocks()
	format := overlayFormat(provider)
	lines := strings.Split(raw, "\n")
	if format == "yaml" {
		currentKey := ""
		var current []string
		var preamble []string
		flush := func() {
			if currentKey != "" {
				blocks.set(currentKey, strings.Join(current, "\n"))
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
				// flush() keeps currentKey for span resumption, but a root
				// scalar terminates the previous mapping block: clear the
				// key so the next root mapping does not re-flush and
				// overwrite the saved block with an empty span.
				currentKey = ""
				if key, _, found := strings.Cut(trimmed, ":"); found {
					blocks.set(strings.TrimSpace(key), line)
				}
				continue
			}
			if currentKey != "" {
				current = append(current, line)
				continue
			}
			// Lines before any root key (preamble comments, blank lines)
			// belong to the document itself, not to a key block.
			if trimmed != "" || line == "" {
				preamble = append(preamble, line)
			}
		}
		flush()
		if len(preamble) > 0 {
			blocks.set("", strings.Join(preamble, "\n"))
		}
		return blocks
	}
	currentTable := ""
	var current []string
	flush := func() {
		if currentTable != "" {
			blocks.set(currentTable, strings.Join(current, "\n"))
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
			blocks.set(strings.TrimSpace(key), line)
			continue
		}
		if trimmed != "" || line == "" {
			preamble = append(preamble, line)
		}
	}
	flush()
	if len(preamble) > 0 {
		blocks.set("", strings.Join(preamble, "\n"))
	}
	return blocks
}

// mergeCollidingBlock merges one colliding top-level key. The operator's raw
// block is the display base (Story 8 verbatim): harness-only list entries
// inject into it textually at the matching indentation, so operator comments
// and formatting survive. When a textual injection cannot express the merge,
// fall back to parse → deep-merge → re-encode.
func mergeCollidingBlock(provider runtimeprofile.Provider, key string, seedSpan []string, operatorBlock string) string {
	format := overlayFormat(provider)
	if format == "yaml" {
		if merged, ok := mergeYAMLCollidingBlockVerbatim(key, seedSpan, operatorBlock); ok {
			return merged
		}
	}
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

// mergeYAMLCollidingBlockVerbatim merges a colliding YAML block keeping the
// operator's raw text: every list entry the seed carries but the operator
// block lacks is injected after the last existing "- entry" line at that
// list's indentation. It reports false when the shape defeats textual
// injection (no shared list, or non-list leaf conflicts).
func mergeYAMLCollidingBlockVerbatim(key string, seedSpan []string, operatorBlock string) (string, bool) {
	var seedDoc, opDoc map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(seedSpan, "\n")), &seedDoc); err != nil {
		return "", false
	}
	if err := yaml.Unmarshal([]byte(operatorBlock), &opDoc); err != nil {
		return "", false
	}
	seedValue, ok := seedDoc[key].(map[string]any)
	if !ok {
		return "", false
	}
	opValue, ok := opDoc[key].(map[string]any)
	if !ok {
		return "", false
	}
	// Find shared list sub-keys (e.g. enabled:) needing entry injection.
	listKey := ""
	var seedList []any
	for subKey, sub := range seedValue {
		list, isList := sub.([]any)
		if !isList {
			continue
		}
		opList, opIsList := opValue[subKey].([]any)
		if !opIsList {
			continue
		}
		// Only handle the single-list case textually.
		if listKey != "" {
			return "", false
		}
		listKey = subKey
		seedList = list
		_ = opList
	}
	if listKey == "" {
		// No shared list: nothing to union textually. Scalars/maps in the
		// seed win per structured-wins, so fall through to re-encode.
		return "", false
	}
	opLines := strings.Split(strings.TrimRight(operatorBlock, "\n"), "\n")
	// Locate the target list at the root block's DIRECT child indentation:
	// the operator block starts with "key:" at some indent, and the list
	// key must sit exactly one level deeper. A nested same-name key
	// (plugins.metadata.enabled) sits deeper and must not match.
	rootLineIdx := -1
	for i, line := range opLines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+":") {
			rootLineIdx = i
			break
		}
	}
	if rootLineIdx == -1 {
		return "", false
	}
	rootIndentLen := len(opLines[rootLineIdx]) - len(strings.TrimLeft(opLines[rootLineIdx], " 	"))
	// Derive the actual direct-child indentation from the block's first
	// content line: YAML allows 2/4/any-space indents, so a hardcoded +2
	// would miss deeper layouts and fall back to re-encoding (losing the
	// operator's comments). Skip comment-only lines so an indented "# ..."
	// does not define the child level.
	childIndentLen := -1
	for i := rootLineIdx + 1; i < len(opLines); i++ {
		line := opLines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rawIndentLen := len(line) - len(strings.TrimLeft(line, " 	"))
		if rawIndentLen <= rootIndentLen {
			break
		}
		childIndentLen = rawIndentLen
		break
	}
	if childIndentLen == -1 {
		return "", false
	}
	listLineIdx := -1
	for i := rootLineIdx + 1; i < len(opLines); i++ {
		line := opLines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rawIndentLen := len(line) - len(strings.TrimLeft(line, " 	"))
		// Left the root block entirely.
		if rawIndentLen <= rootIndentLen {
			break
		}
		// The direct child list key sits exactly at the derived child
		// indentation; deeper same-name keys (metadata.enabled) do not match.
		if rawIndentLen == childIndentLen && strings.HasPrefix(trimmed, listKey+":") {
			listLineIdx = i
			break
		}
	}
	if listLineIdx == -1 {
		return "", false
	}
	listLine := opLines[listLineIdx]
	listIndentLen := len(listLine) - len(strings.TrimLeft(listLine, " 	"))
	entryIndent := ""
	lastEntryIdx := -1
	opEntries := map[string]bool{}
	for i := listLineIdx + 1; i < len(opLines); i++ {
		line := opLines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rawIndentLen := len(line) - len(strings.TrimLeft(line, " 	"))
		// The list ends at the first non-entry line at or above the list
		// key's indentation (a sibling key like "disabled:").
		if !strings.HasPrefix(trimmed, "- ") {
			if rawIndentLen <= listIndentLen {
				break
			}
			// A deeper non-entry line is nested content, not a sibling.
			continue
		}
		if rawIndentLen <= listIndentLen {
			break
		}
		entryIndent = line[:len(line)-len(strings.TrimLeft(line, " 	"))]
		lastEntryIdx = i
		opEntries[strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))] = true
	}
	// Seed-only entries inject after the last operator entry.
	var inject []string
	for _, item := range seedList {
		text, ok := item.(string)
		if !ok || opEntries[text] {
			continue
		}
		inject = append(inject, entryIndent+"- "+text)
	}
	if len(inject) == 0 {
		// Operator already carries every seed entry: the raw block stands.
		return strings.Join(opLines, "\n") + "\n", true
	}
	if lastEntryIdx == -1 || entryIndent == "" {
		return "", false
	}
	out := make([]string, 0, len(opLines)+len(inject))
	out = append(out, opLines[:lastEntryIdx+1]...)
	out = append(out, inject...)
	out = append(out, opLines[lastEntryIdx+1:]...)
	return strings.Join(out, "\n") + "\n", true
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

// isRootKeyValue reports whether a TOML block is a single "key = value" line
// rather than a [table].
func isRootKeyValue(block string) bool {
	trimmed := strings.TrimSpace(block)
	return trimmed != "" && !strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "=")
}

// topLevelKeys lists the root-level keys of a provider-native document.
func topLevelKeys(provider runtimeprofile.Provider, raw string) map[string]bool {
	keys := map[string]bool{}
	for _, key := range topLevelBlocks(provider, raw).keysInOrder() {
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
