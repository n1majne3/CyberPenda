package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// The tailer must map every spelling of one session file to a single canonical
// path. A header may carry a symlinked or separator-mangled spelling of the
// walker's path; both must produce the same canonical form. The tailer uses
// piSessionFileIdentity for graph and dedup keys; native Windows tests cover
// its long-name, 8.3, case, and hard-link aliases separately.
func TestNormalizePiSessionPathCollapsesEquivalentSpellings(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "sessions", "child.jsonl")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	long := normalizePiSessionPath(real)
	if long == "" {
		t.Fatal("normalizePiSessionPath returned empty for an existing file")
	}

	// The canonical key must remain an openable path to the same file.
	if _, err := os.Stat(long); err != nil {
		t.Fatalf("canonical key %q is not statable: %v", long, err)
	}

	// A symlinked directory alias must collapse to the same key on every OS.
	aliasRoot := filepath.Join(root, "alias")
	if err := os.Symlink(root, aliasRoot); err == nil {
		aliased := normalizePiSessionPath(filepath.Join(aliasRoot, "sessions", "child.jsonl"))
		if aliased != long {
			t.Fatalf("symlink alias key = %q, want %q", aliased, long)
		}
	} else {
		t.Logf("symlinks unavailable: %v", err)
	}

	// Redundant separators / . / .. segments must collapse lexically.
	messy := normalizePiSessionPath(filepath.Join(root, "sessions", ".", "..", "sessions", "child.jsonl"))
	if messy != long {
		t.Fatalf("messy path key = %q, want %q", messy, long)
	}

	// Normalization must be deterministic across calls.
	if normalizePiSessionPath(real) != long {
		t.Fatal("normalizePiSessionPath is not deterministic")
	}
}

// A path that does not exist yet must still normalize to a stable cleaned form
// (EvalSymlinks/GetLongPathName fail on missing files) without panicking.
func TestNormalizePiSessionPathMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist.jsonl")
	got := normalizePiSessionPath(missing)
	if got == "" {
		t.Fatal("normalizePiSessionPath returned empty for a missing file")
	}
	if got != filepath.Clean(missing) {
		t.Fatalf("missing file key = %q, want cleaned %q", got, filepath.Clean(missing))
	}
}
