package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRuntimeStorageNormalizesExplicitTaskVolumeRoot(t *testing.T) {
	volumeRoot := filepath.Join(t.TempDir(), "data")
	runtimeRoot := filepath.Join(volumeRoot, "nested", "runs") + string(filepath.Separator)

	gotRuntimeRoot, gotVolumeRoot, err := resolveRuntimeStorage(Config{
		DBPath:         filepath.Join(volumeRoot, "pentest.db"),
		RuntimeRoot:    runtimeRoot,
		TaskVolume:     "cyberpenda-data",
		TaskVolumeRoot: volumeRoot + string(filepath.Separator),
	})
	if err != nil {
		t.Fatalf("resolve runtime storage: %v", err)
	}
	if gotRuntimeRoot != filepath.Clean(runtimeRoot) {
		t.Fatalf("runtime root = %q, want %q", gotRuntimeRoot, filepath.Clean(runtimeRoot))
	}
	if gotVolumeRoot != filepath.Clean(volumeRoot) {
		t.Fatalf("task volume root = %q, want %q", gotVolumeRoot, filepath.Clean(volumeRoot))
	}
}

func TestResolveRuntimeStorageRejectsRuntimeRootOutsideTaskVolume(t *testing.T) {
	root := t.TempDir()
	_, _, err := resolveRuntimeStorage(Config{
		RuntimeRoot:    filepath.Join(root, "runs"),
		TaskVolume:     "cyberpenda-data",
		TaskVolumeRoot: filepath.Join(root, "other"),
	})
	if err == nil || !strings.Contains(err.Error(), "outside task volume root") {
		t.Fatalf("expected outside-volume error, got %v", err)
	}
}
