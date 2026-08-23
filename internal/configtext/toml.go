package configtext

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// TOMLBlock is one source-preserved root assignment or table section.
type TOMLBlock struct {
	Key   string
	Raw   string
	Table bool
}

// SplitTOMLBlocks splits a TOML document into ordered source spans. It handles
// multiline values and repeated [[array-of-table]] blocks.
func SplitTOMLBlocks(raw string) ([]TOMLBlock, error) {
	lines := splitLinesKeepEnd(raw)
	var blocks []TOMLBlock
	var pending strings.Builder
	for i := 0; i < len(lines); {
		trimmed := strings.TrimSpace(strings.TrimSuffix(lines[i], "\n"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			pending.WriteString(lines[i])
			i++
			continue
		}
		if key, ok := tomlTableHeader(trimmed); ok {
			start := i
			i++
			for i < len(lines) {
				next := strings.TrimSpace(strings.TrimSuffix(lines[i], "\n"))
				if _, header := tomlTableHeader(next); header {
					break
				}
				i++
			}
			blocks = append(blocks, TOMLBlock{Key: key, Raw: pending.String() + strings.Join(lines[start:i], ""), Table: true})
			pending.Reset()
			continue
		}
		key, ok := tomlAssignmentKey(trimmed)
		if !ok {
			return nil, fmt.Errorf("cannot locate TOML expression at line %d", i+1)
		}
		start := i
		state := tomlLexState{}
		for ; i < len(lines); i++ {
			state.scan(lines[i])
			if state.complete() {
				i++
				break
			}
		}
		if !state.complete() {
			return nil, fmt.Errorf("unterminated TOML value for %s", key)
		}
		blocks = append(blocks, TOMLBlock{Key: key, Raw: pending.String() + strings.Join(lines[start:i], "")})
		pending.Reset()
	}
	if pending.Len() > 0 {
		blocks = append(blocks, TOMLBlock{Raw: pending.String()})
	}
	return blocks, nil
}

// MergeTOMLDocuments preserves repeated tables/multiline values. Generated
// source wins exact block collisions; Custom Config File-only blocks survive.
func MergeTOMLDocuments(seed, remainder string) (string, error) {
	var seedDoc, operatorDoc map[string]any
	if _, err := toml.Decode(seed, &seedDoc); err != nil {
		return "", err
	}
	if _, err := toml.Decode(remainder, &operatorDoc); err != nil {
		return "", err
	}
	seedBlocks, err := SplitTOMLBlocks(seed)
	if err != nil {
		return "", err
	}
	operatorBlocks, err := SplitTOMLBlocks(remainder)
	if err != nil {
		return "", err
	}
	seedKeys := map[string]bool{}
	seedTableIndex := map[string]int{}
	for index, block := range seedBlocks {
		seedKeys[block.Key] = block.Key != ""
		if block.Table {
			seedTableIndex[block.Key] = index
		}
	}
	var root, tables []TOMLBlock
	for _, block := range operatorBlocks {
		if block.Key != "" && seedKeys[block.Key] {
			if block.Table {
				seedValue, seedOK := lookupTOMLSemanticPath(seedDoc, block.Key)
				operatorValue, operatorOK := lookupTOMLSemanticPath(operatorDoc, block.Key)
				seedMap, seedMapOK := seedValue.(map[string]any)
				operatorMap, operatorMapOK := operatorValue.(map[string]any)
				if seedOK && operatorOK && seedMapOK && operatorMapOK {
					extra := operatorOnlyTOMLMap(seedMap, operatorMap)
					if len(extra) > 0 {
						// Splice the operator's own source spans for operator-only
						// children: comments, continuation lines, spacing, and
						// quoting stay verbatim. Deeper sub-tables keep their own
						// headers because their key path differs.
						leafKeys := flattenTOMLTarget(extra, "")
						if index, ok := seedTableIndex[block.Key]; ok {
							for _, span := range tomlLeafSpans(block.Raw) {
								if !leafKeys[span.key] {
									continue
								}
								seedBlocks[index].Raw += span.raw
							}
						}
					}
				}
			}
			continue
		}
		if block.Table {
			tables = append(tables, block)
		} else {
			root = append(root, block)
		}
	}
	firstTable := len(seedBlocks)
	for i, block := range seedBlocks {
		if block.Table {
			firstTable = i
			break
		}
	}
	var out strings.Builder
	for _, block := range seedBlocks[:firstTable] {
		out.WriteString(block.Raw)
	}
	for _, block := range root {
		out.WriteString(block.Raw)
	}
	for _, block := range seedBlocks[firstTable:] {
		out.WriteString(block.Raw)
	}
	for _, block := range tables {
		out.WriteString(block.Raw)
	}
	return out.String(), nil
}

func lookupTOMLSemanticPath(doc map[string]any, key string) (any, bool) {
	var current any = doc
	for _, part := range strings.Split(key, "\x00") {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// tomlLeafSpan is one assignment's full source span inside a table block:
// leading comments/blank lines, the key line, and every continuation line.
type tomlLeafSpan struct {
	key string
	raw string
}

// tomlLeafSpans re-splits one table block into assignment spans: each span
// carries its leading comments/blank lines, the key line, and every
// continuation line, so multiline values stay contiguous and a comment above
// an assignment travels with it. Keys are table-relative.
func tomlLeafSpans(blockRaw string) []tomlLeafSpan {
	lines := splitLinesKeepEnd(blockRaw)
	var spans []tomlLeafSpan
	var pending strings.Builder
	for i := 0; i < len(lines); {
		trimmed := strings.TrimSpace(strings.TrimSuffix(lines[i], "\n"))
		if strings.HasPrefix(trimmed, "[") {
			// The table header itself belongs to the caller's block, not to
			// any leaf span; skipping it avoids duplicating the header when a
			// span is spliced after the generated table.
			i++
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			pending.WriteString(lines[i])
			i++
			continue
		}
		key, ok := tomlAssignmentKey(trimmed)
		if !ok {
			pending.WriteString(lines[i])
			i++
			continue
		}
		start := i
		state := tomlLexState{}
		for ; i < len(lines); i++ {
			state.scan(lines[i])
			if state.complete() {
				i++
				break
			}
		}
		spans = append(spans, tomlLeafSpan{key: key, raw: pending.String() + strings.Join(lines[start:i], "")})
		pending.Reset()
	}
	if pending.Len() > 0 {
		if len(spans) > 0 {
			// A trailing standalone comment/blank-line span remains part of
			// the preceding operator leaf when that leaf is spliced into a
			// colliding generated table.
			spans[len(spans)-1].raw += pending.String()
		} else {
			spans = append(spans, tomlLeafSpan{raw: pending.String()})
		}
	}
	return spans
}

func operatorOnlyTOMLMap(structured, operator map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range operator {
		structuredValue, exists := structured[key]
		if !exists {
			out[key] = value
			continue
		}
		structuredMap, structuredOK := structuredValue.(map[string]any)
		operatorMap, operatorOK := value.(map[string]any)
		if structuredOK && operatorOK {
			if nested := operatorOnlyTOMLMap(structuredMap, operatorMap); len(nested) > 0 {
				out[key] = nested
			}
		}
	}
	return out
}

// RenderTOMLTarget removes source blocks absent from target; surviving source
// spans stay byte-for-byte.
func RenderTOMLTarget(source string, target map[string]any) (string, error) {
	blocks, err := SplitTOMLBlocks(source)
	if err != nil {
		return "", err
	}
	keys := flattenTOMLTarget(target, "")
	var out strings.Builder
	for _, block := range blocks {
		if block.Key == "" || tomlTargetCovers(keys, block.Key) {
			out.WriteString(block.Raw)
		}
	}
	return out.String(), nil
}

func splitLinesKeepEnd(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.SplitAfter(raw, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func tomlTableHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(stripTOMLComment(line))
	if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
		return semanticTOMLKey(strings.TrimSpace(trimmed[2 : len(trimmed)-2])), true
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return semanticTOMLKey(strings.TrimSpace(trimmed[1 : len(trimmed)-1])), true
	}
	return "", false
}

func tomlAssignmentKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	inQuote := byte(0)
	escaped := false
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote != 0 {
			if ch == '\\' && inQuote == '"' {
				escaped = true
			} else if ch == inQuote {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == '=' {
			return semanticTOMLKey(strings.TrimSpace(trimmed[:i])), true
		}
		if ch == '#' {
			break
		}
	}
	return "", false
}

func semanticTOMLKey(raw string) string {
	var doc map[string]any
	if _, err := toml.Decode(raw+" = true\n", &doc); err == nil {
		return firstTOMLLeafPath(doc, nil)
	}
	// A table header key needs a dummy leaf to parse as a semantic path.
	doc = nil
	if _, err := toml.Decode("["+raw+"]\n__value = true\n", &doc); err == nil {
		path := firstTOMLLeafPath(doc, nil)
		path = strings.TrimSuffix(path, "\x00__value")
		return path
	}
	return raw
}

func firstTOMLLeafPath(doc map[string]any, prefix []string) string {
	for key, value := range doc {
		path := append(append([]string{}, prefix...), key)
		if child, ok := value.(map[string]any); ok {
			return firstTOMLLeafPath(child, path)
		}
		return strings.Join(path, "\x00")
	}
	return strings.Join(prefix, "\x00")
}

func stripTOMLComment(line string) string {
	inQuote := byte(0)
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote != 0 {
			if ch == '\\' && inQuote == '"' {
				escaped = true
			} else if ch == inQuote {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == '#' {
			return line[:i]
		}
	}
	return line
}

type tomlLexState struct {
	square, curly            int
	basic, literal           bool
	multiBasic, multiLiteral bool
	escaped                  bool
}

func (s *tomlLexState) complete() bool {
	return s.square == 0 && s.curly == 0 && !s.basic && !s.literal && !s.multiBasic && !s.multiLiteral
}

func (s *tomlLexState) scan(line string) {
	for i := 0; i < len(line); i++ {
		if s.multiBasic {
			if strings.HasPrefix(line[i:], `"""`) {
				s.multiBasic = false
				i += 2
				for i+1 < len(line) && line[i+1] == '"' {
					i++
				}
			}
			continue
		}
		if s.multiLiteral {
			if strings.HasPrefix(line[i:], `'''`) {
				s.multiLiteral = false
				i += 2
				for i+1 < len(line) && line[i+1] == '\'' {
					i++
				}
			}
			continue
		}
		ch := line[i]
		if s.escaped {
			s.escaped = false
			continue
		}
		if s.basic {
			if ch == '\\' {
				s.escaped = true
			} else if ch == '"' {
				s.basic = false
			}
			continue
		}
		if s.literal {
			if ch == '\'' {
				s.literal = false
			}
			continue
		}
		if ch == '#' {
			break
		}
		if strings.HasPrefix(line[i:], `"""`) {
			s.multiBasic = true
			i += 2
			continue
		}
		if strings.HasPrefix(line[i:], `'''`) {
			s.multiLiteral = true
			i += 2
			continue
		}
		switch ch {
		case '"':
			s.basic = true
		case '\'':
			s.literal = true
		case '[':
			s.square++
		case ']':
			if s.square > 0 {
				s.square--
			}
		case '{':
			s.curly++
		case '}':
			if s.curly > 0 {
				s.curly--
			}
		}
	}
}

func flattenTOMLTarget(node map[string]any, prefix string) map[string]bool {
	out := map[string]bool{}
	var walk func(map[string]any, string)
	walk = func(current map[string]any, base string) {
		for key, value := range current {
			path := key
			if base != "" {
				path = base + "\x00" + key
			}
			out[path] = true
			if child, ok := value.(map[string]any); ok {
				walk(child, path)
			}
		}
	}
	walk(node, prefix)
	return out
}

func tomlTargetCovers(keys map[string]bool, blockKey string) bool {
	if keys[blockKey] {
		return true
	}
	prefix := blockKey + "\x00"
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
