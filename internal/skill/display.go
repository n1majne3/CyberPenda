package skill

import "strings"

// DisplayID returns the source-free identifier for user-facing and task-local
// surfaces while preserving canonical storage IDs elsewhere.
func DisplayID(id string, source SourceProvenance) string {
	id = strings.TrimSpace(id)
	if source.Kind != "builtin" {
		return id
	}
	return stripBuiltinSourcePrefix(id)
}

// DisplayName returns the source-free name for user-facing and task-local
// surfaces while preserving canonical storage metadata elsewhere.
func DisplayName(name, id string, source SourceProvenance) string {
	name = strings.TrimSpace(name)
	if source.Kind != "builtin" {
		return name
	}
	display := stripBuiltinSourcePrefix(name)
	if strings.TrimSpace(display) == "" {
		return DisplayID(id, source)
	}
	return display
}

func stripBuiltinSourcePrefix(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"cyberstrikeai-", "strix-"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			return strings.TrimSpace(value[len(prefix):])
		}
	}
	return value
}
