package runtime

import (
	"fmt"
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

var dockerClientEnvKeys = map[string]struct{}{
	"DOCKER_HOST":           {},
	"DOCKER_CONTEXT":        {},
	"DOCKER_CONFIG":         {},
	"DOCKER_CERT_PATH":      {},
	"DOCKER_TLS_VERIFY":     {},
	"DOCKER_API_VERSION":    {},
	"PATH":                  {},
	"HOME":                  {},
	"LD_PRELOAD":            {},
	"LD_LIBRARY_PATH":       {},
	"DYLD_INSERT_LIBRARIES": {},
	"DYLD_LIBRARY_PATH":     {},
}

func prepareDockerCreateCommand(baseEnv []string, createArgs []string, createEnv map[string]string) ([]string, []string, func(), error) {
	noop := func() {}
	if len(createEnv) == 0 {
		return createArgs, nil, noop, nil
	}
	safeEnv := make(map[string]string, len(createEnv))
	fileEnv := make(map[string]string)
	for key, value := range createEnv {
		if _, reserved := dockerClientEnvKeys[strings.ToUpper(key)]; reserved {
			fileEnv[key] = value
			continue
		}
		safeEnv[key] = value
	}
	env := mergeProcessEnv(baseEnv, safeEnv)
	if len(fileEnv) == 0 {
		return createArgs, env, noop, nil
	}
	tmp, err := os.CreateTemp("", "pentest-docker-env-*")
	if err != nil {
		return nil, nil, noop, fmt.Errorf("create docker env file: %w", err)
	}
	path := tmp.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return nil, nil, noop, fmt.Errorf("chmod docker env file: %w", err)
	}
	keys := make([]string, 0, len(fileEnv))
	for key := range fileEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(fileEnv[key])
		b.WriteByte('\n')
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		cleanup()
		return nil, nil, noop, fmt.Errorf("write docker env file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, nil, noop, fmt.Errorf("close docker env file: %w", err)
	}
	filtered := make([]string, 0, len(createArgs)+2)
	for i := 0; i < len(createArgs); i++ {
		if i == 0 && createArgs[i] == "create" {
			filtered = append(filtered, "create", "--env-file", path)
			continue
		}
		if createArgs[i] == "-e" && i+1 < len(createArgs) {
			if _, moved := fileEnv[createArgs[i+1]]; moved {
				i++
				continue
			}
		}
		filtered = append(filtered, createArgs[i])
	}
	return filtered, env, cleanup, nil
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
