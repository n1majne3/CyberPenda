package blackboardv2

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"pentest/internal/jsonnumber"
)

const maxExactJSONInteger = 9007199254740991

// decodeJSON rejects invalid UTF-8 before encoding/json can replace malformed
// bytes with U+FFFD and collapse distinct wire inputs into one semantic value.
func decodeJSON(raw []byte, target any) error {
	if err := requireValidJSONUTF8(raw); err != nil {
		return err
	}
	if integer, ok := target.(*int); ok {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var number json.Number
		if err := decoder.Decode(&number); err != nil {
			return err
		}
		value, err := jsonnumber.ExactInteger(number, maxExactJSONInteger)
		if err != nil {
			return err
		}
		*integer = int(value)
		return nil
	}
	return json.Unmarshal(raw, target)
}

func requireValidJSONUTF8(raw []byte) error {
	if utf8.Valid(raw) {
		return nil
	}
	return semanticError("semantic_validation", "JSON input must be valid UTF-8", "", map[string]any{"reason": "invalid_utf8"})
}
