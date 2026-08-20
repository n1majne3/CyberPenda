package runtime

import (
	"path/filepath"
	"strings"
)

// ProjectedHermesHome returns the host path of the projected HERMES_HOME.
// Host Command adapters expose HERMES_HOME in process env. Sandbox Docker
// adapters set HERMES_HOME to /task/runtime-home/hermes; this maps that
// container path through the /task bind so the daemon can write per-turn
// Requested Reasoning Effort where the sandbox Hermes process can read it.
func ProjectedHermesHome(adapter Adapter) string {
	if launch, ok := CommandAdapterLaunch(adapter); ok {
		if home := strings.TrimSpace(launch.Env["HERMES_HOME"]); home != "" {
			return home
		}
	}
	args, ok := DockerSandboxCreateArgs(adapter)
	if !ok {
		return ""
	}
	return projectedHermesHomeFromCreateArgs(args)
}

func projectedHermesHomeFromCreateArgs(args []string) string {
	containerHome := dockerCreateEnv(args, "HERMES_HOME")
	if containerHome == "" {
		containerHome = "/task/runtime-home/hermes"
	}
	hostRoot, dest, ok := dockerBindForContainerPath(args, containerHome)
	if !ok {
		return ""
	}
	if dest == containerHome {
		return hostRoot
	}
	rel := strings.TrimPrefix(containerHome, dest+"/")
	if rel == containerHome {
		return ""
	}
	return filepath.Join(hostRoot, filepath.FromSlash(rel))
}

func dockerCreateEnv(args []string, key string) string {
	prefix := key + "="
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-e" || arg == "--env":
			if i+1 >= len(args) {
				continue
			}
			i++
			if strings.HasPrefix(args[i], prefix) {
				return args[i][len(prefix):]
			}
		case strings.HasPrefix(arg, "-e=") || strings.HasPrefix(arg, "--env="):
			value := arg[strings.Index(arg, "=")+1:]
			if strings.HasPrefix(value, prefix) {
				return value[len(prefix):]
			}
		}
	}
	return ""
}

func dockerBindForContainerPath(args []string, containerPath string) (host, dest string, ok bool) {
	bestLen := -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		spec := ""
		switch {
		case arg == "--mount" && i+1 < len(args):
			i++
			spec = args[i]
		case strings.HasPrefix(arg, "--mount="):
			spec = strings.TrimPrefix(arg, "--mount=")
		default:
			continue
		}
		src, dst, isBind := parseDockerBindMount(spec)
		if !isBind || src == "" || dst == "" {
			continue
		}
		if containerPath != dst && !strings.HasPrefix(containerPath, dst+"/") {
			continue
		}
		if len(dst) > bestLen {
			host, dest, ok, bestLen = src, dst, true, len(dst)
		}
	}
	return host, dest, ok
}

func parseDockerBindMount(spec string) (src, dst string, ok bool) {
	fields := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		fields[key] = value
	}
	if fields["type"] != "bind" {
		return "", "", false
	}
	src = firstNonEmpty(fields["src"], fields["source"])
	dst = firstNonEmpty(fields["dst"], fields["destination"], fields["target"])
	if src == "" || dst == "" {
		return "", "", false
	}
	return src, dst, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
