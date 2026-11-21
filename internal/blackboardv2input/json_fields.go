package blackboardv2input

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return scanJSONFields(decoder)
}

func scanJSONFields(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			field, ok := token.(string)
			if !ok {
				return fmt.Errorf("JSON object field must be a string")
			}
			folded := foldJSONField(field)
			if _, duplicate := seen[folded]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", field)
			}
			seen[folded] = struct{}{}
			if err := scanJSONFields(decoder); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for decoder.More() {
			if err := scanJSONFields(decoder); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	_, err = decoder.Token()
	return err
}

func foldJSONField(field string) string {
	return strings.Map(func(value rune) rune {
		folded := value
		for next := unicode.SimpleFold(value); next != value; next = unicode.SimpleFold(next) {
			if next < folded {
				folded = next
			}
		}
		return folded
	}, field)
}
