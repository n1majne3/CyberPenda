package runner

import (
	"path/filepath"
	"strings"
)

// FormatContainerHostPath converts a host filesystem path into the form that
// Docker Desktop / Podman Desktop accept as a bind-mount source when the
// daemon runs natively on Windows and Linux containers run in the Desktop
// WSL/Linux VM.
//
// Windows drive paths become forward-slash form (C:/Users/...) so engines do
// not mis-parse the drive colon when combined with -v host:container. Non-
// Windows paths are returned unchanged.
func FormatContainerHostPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if looksLikeWindowsAbsPath(path) {
		return windowsContainerHostPath(path)
	}
	// Native Windows builds also report a volume name via filepath.
	if vol := filepath.VolumeName(path); vol != "" {
		return windowsContainerHostPath(path)
	}
	return path
}

// bindMountArgs returns docker/podman create flags for a host bind. Prefer
// --mount over -v so Windows drive letters never collide with the host:container
// separator.
func bindMountArgs(hostPath, containerPath string, readOnly bool) []string {
	spec := "type=bind,src=" + FormatContainerHostPath(hostPath) + ",dst=" + containerPath
	if readOnly {
		spec += ",readonly"
	}
	return []string{"--mount", spec}
}

func looksLikeWindowsAbsPath(path string) bool {
	if len(path) >= 3 && path[1] == ':' {
		drive := path[0]
		if (drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z') {
			sep := path[2]
			return sep == '\\' || sep == '/'
		}
	}
	// UNC paths shared into Desktop (\\server\share or //server/share).
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	return false
}

func windowsContainerHostPath(path string) string {
	// Keep drive letter / UNC prefix; normalize separators to '/'.
	return strings.ReplaceAll(path, `\`, `/`)
}
