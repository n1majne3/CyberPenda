//go:build windows

package runtime

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// canonicalPathForCompare resolves a session-file path to a single canonical
// key. Windows may hand the daemon an 8.3 short path (C:\Users\RUNNER~1\...)
// in a session header while the directory walker yields the long form, so the
// short name must be expanded before comparison. GetLongPathName expands 8.3
// short names; EvalSymlinks then resolves any symlink aliases. When the file
// does not exist yet both calls fail, and the cleaned input is used.
func canonicalPathForCompare(path string) string {
	long := path
	if expanded, err := longPathName(path); err == nil && expanded != "" {
		long = expanded
	}
	if resolved, err := filepath.EvalSymlinks(long); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(long)
}

// longPathName returns the Windows long (non-8.3) form of path.
func longPathName(path string) (string, error) {
	utf16, err := windows.UTF16FromString(path)
	if err != nil {
		return "", err
	}
	// GetLongPathNameW returns the required buffer length when the buffer is
	// too small; grow until it fits.
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetLongPathName(&utf16[0], &buf[0], uint32(len(buf)))
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		buf = make([]uint16, n+1)
	}
}
