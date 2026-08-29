//go:build !windows

package runtime

import "path/filepath"

// canonicalPathForCompare resolves a session-file path to a single canonical
// key. Non-Windows platforms have no 8.3 short-name aliasing, so resolving
// symlinks and cleaning the result is sufficient. When the file does not exist
// yet EvalSymlinks fails and the cleaned input is used.
func canonicalPathForCompare(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// piSessionFileIdentity returns the stable key used by the parent graph and
// per-file tail state. On non-Windows systems the existing canonical path is
// the deterministic file identity.
func piSessionFileIdentity(path string) string {
	return canonicalPathForCompare(path)
}
