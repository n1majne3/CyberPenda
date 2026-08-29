//go:build windows

package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPiSessionFileIdentityCollapsesWindowsAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-file-with-long-name.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	long, err := longPathName(path)
	if err != nil {
		t.Fatalf("GetLongPathNameW(%q): %v", path, err)
	}
	short, err := shortPathName(long)
	if err != nil {
		t.Fatalf("GetShortPathNameW(%q): %v", long, err)
	}

	want := piSessionFileIdentity(path)
	for name, alias := range map[string]string{
		"long":        long,
		"short":       short,
		"case-change": strings.ToUpper(long),
	} {
		if _, err := os.Stat(alias); err != nil {
			t.Fatalf("%s alias %q is not the same existing file: %v", name, alias, err)
		}
		if got := piSessionFileIdentity(alias); got != want {
			t.Fatalf("%s alias identity = %q, want %q", name, got, want)
		}
	}

	// A hard link gives the same physical file a second directory entry. The
	// tail state must still identify it once.
	hardLink := filepath.Join(filepath.Dir(path), "session-hard-link.jsonl")
	if err := os.Link(path, hardLink); err != nil {
		t.Fatalf("create hard-link alias: %v", err)
	}
	if got := piSessionFileIdentity(hardLink); got != want {
		t.Fatalf("hard-link alias identity = %q, want %q", got, want)
	}
}

func shortPathName(path string) (string, error) {
	utf16, err := windows.UTF16FromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetShortPathName(&utf16[0], &buf[0], uint32(len(buf)))
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		buf = make([]uint16, n+1)
	}
}
