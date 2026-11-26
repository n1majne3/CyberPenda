package runtime_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/runtime"
)

func TestDockerCleanupRetainsEngineFailureAndExitStatus(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	cli := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho 'engine cleanup denied' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := runtime.RemoveDockerContainer(cli, "container-owned")
	if err == nil || !strings.Contains(err.Error(), "engine cleanup denied") {
		t.Fatalf("cleanup lost the engine failure: %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("cleanup lost the process exit status: %v", err)
	}
}
