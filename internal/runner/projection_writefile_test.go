package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteJSONConfigFileUsesSecurePermissions pins the single audited path for
// persisting projected runtime config files. These files can carry resolved
// model credentials, so they must always be written owner-read/write only.
func TestWriteJSONConfigFileUsesSecurePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	doc := map[string]any{"model": "gpt-x", "api_key": "secret"}
	if err := writeJSONConfigFile(path, doc); err != nil {
		t.Fatalf("writeJSONConfigFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat projected config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("projected config permissions = %o, want 600", perm)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read projected config: %v", err)
	}
	if !strings.Contains(string(raw), "\n  ") {
		t.Fatalf("expected indented JSON, got %q", raw)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal projected config: %v", err)
	}
	if got["model"] != "gpt-x" || got["api_key"] != "secret" {
		t.Fatalf("round-tripped config = %#v, want model+api_key preserved", got)
	}
}

// TestWriteJSONConfigFileReportsWriteError confirms an unwritable target path
// surfaces an error instead of silently dropping the config.
func TestWriteJSONConfigFileReportsWriteError(t *testing.T) {
	dir := t.TempDir()
	// A path whose parent is a regular file cannot be created.
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	path := filepath.Join(notADir, "config.json")

	if err := writeJSONConfigFile(path, map[string]any{"a": 1}); err == nil {
		t.Fatalf("expected error writing under a non-directory parent, got nil")
	}
}
