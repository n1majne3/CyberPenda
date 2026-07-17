package blackboardv2

import (
	"encoding/json"
	"unicode/utf8"
)

// decodeJSON rejects invalid UTF-8 before encoding/json can replace malformed
// bytes with U+FFFD and collapse distinct wire inputs into one semantic value.
func decodeJSON(raw []byte, target any) error {
	if err := requireValidJSONUTF8(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func requireValidJSONUTF8(raw []byte) error {
	if utf8.Valid(raw) {
		return nil
	}
	return semanticError("semantic_validation", "JSON input must be valid UTF-8", "", map[string]any{"reason": "invalid_utf8"})
}
