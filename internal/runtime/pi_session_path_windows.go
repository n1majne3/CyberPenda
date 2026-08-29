//go:build windows

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// piSessionFileIdentity identifies an existing session file by its Windows
// volume and file index. These values are stable for every spelling of the
// same file, including long and 8.3 names, case differences, symlinks, and
// hard links. A missing file falls back to a normalized case-insensitive path;
// callers do not cache its header classification and retry after it appears.
func piSessionFileIdentity(path string) string {
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err == nil {
			return fmt.Sprintf("windows:%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
		}
	}
	return "path:" + strings.ToLower(canonicalPathForCompare(path))
}

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
