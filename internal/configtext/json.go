// Package configtext performs source-preserving edits on provider-native
// configuration documents. It keeps operator-owned bytes while changing the
// smallest semantic span required by structured Runtime Profile fields.
package configtext

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// MergeJSONObjects merges two JSON objects into one valid document. The
// operator document is the source-text base: its member order, whitespace,
// key/colon formatting, and remainder-only values survive. Structured values
// win collisions; object/object collisions recurse. Structured-only members
// append in structured source order.
func MergeJSONObjects(structuredRaw, operatorRaw string) (string, error) {
	structured, err := parseJSONDocument(structuredRaw)
	if err != nil {
		return "", fmt.Errorf("parse structured JSON: %w", err)
	}
	operator, err := parseJSONDocument(operatorRaw)
	if err != nil {
		return "", fmt.Errorf("parse operator JSON: %w", err)
	}
	if structured.root.kind != jsonObject || operator.root.kind != jsonObject {
		return "", fmt.Errorf("JSON config root must be an object")
	}
	body, err := mergeJSONObject(structured, structured.root, operator, operator.root)
	if err != nil {
		return "", err
	}
	return operator.prefix() + body + operator.suffix(), nil
}

// RenderJSONTarget renders target through the source document. Members absent
// from target are removed with their commas; surviving members keep their
// original bytes and order. Nested objects recurse, so a mapped path such as
// enabledPlugins.known can be removed without canonicalizing its siblings.
func RenderJSONTarget(sourceRaw string, target map[string]any) (string, error) {
	source, err := parseJSONDocument(sourceRaw)
	if err != nil {
		return "", err
	}
	if source.root.kind != jsonObject {
		return "", fmt.Errorf("JSON config root must be an object")
	}
	body, err := renderJSONObjectTarget(source, source.root, target)
	if err != nil {
		return "", err
	}
	return source.prefix() + body + source.suffix(), nil
}

type jsonKind uint8

const (
	jsonScalar jsonKind = iota
	jsonObject
	jsonArray
)

type jsonDocument struct {
	raw  string
	root jsonNode
}

func (d jsonDocument) prefix() string { return d.raw[:d.root.start] }
func (d jsonDocument) suffix() string { return d.raw[d.root.end:] }

type jsonNode struct {
	kind       jsonKind
	start      int
	end        int
	members    []jsonMember
	closeStart int
}

type jsonMember struct {
	key          string
	leadingStart int
	keyStart     int
	value        jsonNode
}

func parseJSONDocument(raw string) (jsonDocument, error) {
	p := jsonSourceParser{raw: raw}
	p.skipWhitespace()
	root, err := p.parseValue()
	if err != nil {
		return jsonDocument{}, err
	}
	p.skipWhitespace()
	if p.pos != len(raw) {
		return jsonDocument{}, fmt.Errorf("unexpected JSON content at byte %d", p.pos)
	}
	return jsonDocument{raw: raw, root: root}, nil
}

type jsonSourceParser struct {
	raw string
	pos int
}

func (p *jsonSourceParser) skipWhitespace() {
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonSourceParser) parseValue() (jsonNode, error) {
	p.skipWhitespace()
	if p.pos >= len(p.raw) {
		return jsonNode{}, fmt.Errorf("unexpected end of JSON")
	}
	switch p.raw[p.pos] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		start := p.pos
		if _, err := p.parseString(); err != nil {
			return jsonNode{}, err
		}
		return jsonNode{kind: jsonScalar, start: start, end: p.pos}, nil
	default:
		start := p.pos
		for p.pos < len(p.raw) && !strings.ContainsRune(" \t\r\n,]}", rune(p.raw[p.pos])) {
			p.pos++
		}
		if start == p.pos || !json.Valid([]byte(p.raw[start:p.pos])) {
			return jsonNode{}, fmt.Errorf("invalid JSON value at byte %d", start)
		}
		return jsonNode{kind: jsonScalar, start: start, end: p.pos}, nil
	}
}

func (p *jsonSourceParser) parseObject() (jsonNode, error) {
	start := p.pos
	p.pos++
	var members []jsonMember
	leadingStart := p.pos
	p.skipWhitespace()
	if p.pos < len(p.raw) && p.raw[p.pos] == '}' {
		closeStart := p.pos
		p.pos++
		return jsonNode{kind: jsonObject, start: start, end: p.pos, closeStart: closeStart}, nil
	}
	for {
		p.skipWhitespace()
		keyStart := p.pos
		key, err := p.parseString()
		if err != nil {
			return jsonNode{}, err
		}
		p.skipWhitespace()
		if p.pos >= len(p.raw) || p.raw[p.pos] != ':' {
			return jsonNode{}, fmt.Errorf("expected ':' after JSON key %q", key)
		}
		p.pos++
		value, err := p.parseValue()
		if err != nil {
			return jsonNode{}, err
		}
		members = append(members, jsonMember{key: key, leadingStart: leadingStart, keyStart: keyStart, value: value})
		p.skipWhitespace()
		if p.pos >= len(p.raw) {
			return jsonNode{}, fmt.Errorf("unterminated JSON object")
		}
		switch p.raw[p.pos] {
		case ',':
			p.pos++
			leadingStart = p.pos
			continue
		case '}':
			closeStart := p.pos
			p.pos++
			return jsonNode{kind: jsonObject, start: start, end: p.pos, members: members, closeStart: closeStart}, nil
		default:
			return jsonNode{}, fmt.Errorf("expected ',' or '}' at byte %d", p.pos)
		}
	}
}

func (p *jsonSourceParser) parseArray() (jsonNode, error) {
	start := p.pos
	p.pos++
	p.skipWhitespace()
	if p.pos < len(p.raw) && p.raw[p.pos] == ']' {
		p.pos++
		return jsonNode{kind: jsonArray, start: start, end: p.pos}, nil
	}
	for {
		if _, err := p.parseValue(); err != nil {
			return jsonNode{}, err
		}
		p.skipWhitespace()
		if p.pos >= len(p.raw) {
			return jsonNode{}, fmt.Errorf("unterminated JSON array")
		}
		switch p.raw[p.pos] {
		case ',':
			p.pos++
			continue
		case ']':
			p.pos++
			return jsonNode{kind: jsonArray, start: start, end: p.pos}, nil
		default:
			return jsonNode{}, fmt.Errorf("expected ',' or ']' at byte %d", p.pos)
		}
	}
}

func (p *jsonSourceParser) parseString() (string, error) {
	if p.pos >= len(p.raw) || p.raw[p.pos] != '"' {
		return "", fmt.Errorf("expected JSON string at byte %d", p.pos)
	}
	start := p.pos
	p.pos++
	escaped := false
	for p.pos < len(p.raw) {
		ch := p.raw[p.pos]
		p.pos++
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			var decoded string
			if err := json.Unmarshal([]byte(p.raw[start:p.pos]), &decoded); err != nil {
				return "", err
			}
			return decoded, nil
		}
	}
	return "", fmt.Errorf("unterminated JSON string at byte %d", start)
}

func mergeJSONObject(structuredDoc jsonDocument, structured jsonNode, operatorDoc jsonDocument, operator jsonNode) (string, error) {
	structuredByKey := memberIndex(structured.members)
	operatorByKey := memberIndex(operator.members)
	var rendered []renderedJSONMember
	for _, opMember := range operator.members {
		structuredMember, collides := structuredByKey[opMember.key]
		if !collides {
			rendered = append(rendered, rawJSONMember(operatorDoc, opMember))
			continue
		}
		value := structuredDoc.raw[structuredMember.value.start:structuredMember.value.end]
		if structuredMember.value.kind == jsonObject && opMember.value.kind == jsonObject {
			merged, err := mergeJSONObject(structuredDoc, structuredMember.value, operatorDoc, opMember.value)
			if err != nil {
				return "", err
			}
			value = merged
		}
		rendered = append(rendered, renderedJSONMember{
			leading: operatorDoc.raw[opMember.leadingStart:opMember.keyStart],
			body:    operatorDoc.raw[opMember.keyStart:opMember.value.start] + value,
		})
	}
	for _, structuredMember := range structured.members {
		if _, exists := operatorByKey[structuredMember.key]; exists {
			continue
		}
		rendered = append(rendered, rawJSONMember(structuredDoc, structuredMember))
	}
	return renderJSONObject(operatorDoc, operator, rendered), nil
}

func renderJSONObjectTarget(source jsonDocument, node jsonNode, target map[string]any) (string, error) {
	seen := map[string]bool{}
	var rendered []renderedJSONMember
	for _, member := range node.members {
		targetValue, keep := target[member.key]
		if !keep {
			continue
		}
		seen[member.key] = true
		valueRaw := source.raw[member.value.start:member.value.end]
		if member.value.kind == jsonObject {
			if targetMap, ok := normalizeStringMap(targetValue); ok {
				nested, err := renderJSONObjectTarget(source, member.value, targetMap)
				if err != nil {
					return "", err
				}
				valueRaw = nested
			} else if !jsonNodeEqualTarget(source, member.value, targetValue) {
				encoded, err := json.Marshal(targetValue)
				if err != nil {
					return "", err
				}
				valueRaw = string(encoded)
			}
		} else if !jsonNodeEqualTarget(source, member.value, targetValue) {
			encoded, err := json.Marshal(targetValue)
			if err != nil {
				return "", err
			}
			valueRaw = string(encoded)
		}
		rendered = append(rendered, renderedJSONMember{
			leading: source.raw[member.leadingStart:member.keyStart],
			body:    source.raw[member.keyStart:member.value.start] + valueRaw,
		})
	}
	for key, value := range target {
		if seen[key] {
			continue
		}
		keyRaw, _ := json.Marshal(key)
		valueRaw, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		rendered = append(rendered, renderedJSONMember{leading: inferredJSONIndent(source, node), body: string(keyRaw) + ": " + string(valueRaw)})
	}
	return renderJSONObject(source, node, rendered), nil
}

type renderedJSONMember struct {
	leading string
	body    string
}

func rawJSONMember(doc jsonDocument, member jsonMember) renderedJSONMember {
	return renderedJSONMember{
		leading: doc.raw[member.leadingStart:member.keyStart],
		body:    doc.raw[member.keyStart:member.value.end],
	}
}

func renderJSONObject(doc jsonDocument, node jsonNode, members []renderedJSONMember) string {
	if len(members) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, member := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(member.leading)
		b.WriteString(member.body)
	}
	b.WriteString(doc.raw[lastJSONValueEnd(node):node.closeStart])
	b.WriteByte('}')
	return b.String()
}

func lastJSONValueEnd(node jsonNode) int {
	if len(node.members) == 0 {
		return node.start + 1
	}
	return node.members[len(node.members)-1].value.end
}

func memberIndex(members []jsonMember) map[string]jsonMember {
	out := make(map[string]jsonMember, len(members))
	for _, member := range members {
		out[member.key] = member
	}
	return out
}

func normalizeStringMap(value any) (map[string]any, bool) {
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
	default:
		return nil, false
	}
}

func jsonNodeEqualTarget(doc jsonDocument, node jsonNode, target any) bool {
	var decoded any
	if err := json.Unmarshal([]byte(doc.raw[node.start:node.end]), &decoded); err != nil {
		return false
	}
	return reflect.DeepEqual(decoded, target)
}

func inferredJSONIndent(doc jsonDocument, node jsonNode) string {
	if len(node.members) > 0 {
		return doc.raw[node.members[0].leadingStart:node.members[0].keyStart]
	}
	return "\n  "
}
