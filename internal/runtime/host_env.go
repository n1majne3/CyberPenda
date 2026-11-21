package runtime

import (
	"os"
	"sort"
	"strings"
)

var daemonOnlyEnvKeys = map[string]struct{}{
	"PENTEST_AUTH_TOKEN":              {},
	"PENTEST_OPERATOR_TOKEN":          {},
	"PENTEST_DB":                      {},
	"PENTEST_LISTEN_ADDR":             {},
	"PENTEST_RUNTIME_ROOT":            {},
	"PENTEST_ARTIFACT_ROOT":           {},
	"PENTEST_EVIDENCE_SOURCE_ROOTS":   {},
	"PENTEST_SANDBOX_IMAGE":           {},
	"PENTEST_CONTAINER_CLI":           {},
	"PENTEST_TASK_VOLUME":             {},
	"PENTEST_TASK_VOLUME_ROOT":        {},
	"PENTEST_RUNTIME_PLUGIN_DIRS":     {},
	"PENTEST_RUNTIME_EXTENSION_DIRS":  {},
	"PENTEST_HOST_BROWSER_MCP_BINARY": {},
	"PENTEST_HOST_BROWSER_BROWSER":    {},
	"PENTEST_HOST_BROWSER_EXECUTABLE": {},
}

func buildHostProcessEnv(extra map[string]string) []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, blocked := daemonOnlyEnvKeys[strings.ToUpper(key)]; blocked {
			continue
		}
		filtered = append(filtered, entry)
	}
	return mergeProcessEnv(filtered, extra)
}

func mergeProcessEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		out := make([]string, len(base))
		copy(out, base)
		return out
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, overridden := extra[key]; overridden {
			continue
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+extra[key])
	}
	return out
}
