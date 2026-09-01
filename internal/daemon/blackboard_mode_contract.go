package daemon

import (
	"encoding/json"
	"errors"
)

var errLegacyBlackboardModeField = errors.New("blackboard_conclusion_mode is not supported; use blackboard_mode")

func rejectLegacyBlackboardModeField(raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if containsLegacyBlackboardModeField(value) {
		return errLegacyBlackboardModeField
	}
	return nil
}

func containsLegacyBlackboardModeField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "blackboard_conclusion_mode" || containsLegacyBlackboardModeField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsLegacyBlackboardModeField(child) {
				return true
			}
		}
	}
	return false
}
