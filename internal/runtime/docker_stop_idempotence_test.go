package runtime_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/runtime"
)

func TestDockerStopConfirmsAlreadyStoppedContainer(t *testing.T) {
	skipUnlessPOSIXProcessDoubles(t)
	for _, test := range []struct {
		name      string
		stopError string
		killError string
		wantCalls string
		wantError string
	}{
		{
			name:      "graceful stop observes an exited container",
			stopError: "Error response from daemon: container ctr-stopped is not running",
			killError: "kill must not be called",
			wantCalls: "stop\n",
		},
		{
			name:      "container exits before fallback kill",
			stopError: "Error response from daemon: stop request timed out",
			killError: "Error response from daemon: cannot kill container ctr-stopped: container ctr-stopped is not running",
			wantCalls: "stop\nkill\n",
		},
		{
			name:      "unconfirmed stop retains the error",
			stopError: "Error response from daemon: permission denied",
			killError: "Error response from daemon: permission denied",
			wantCalls: "stop\nkill\n",
			wantError: "permission denied",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "calls")
			docker := filepath.Join(dir, "docker")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$1\" >> %q\ncase \"$1\" in\nstop) printf '%%s\\n' %q >&2;;\nkill) printf '%%s\\n' %q >&2;;\nesac\nexit 1\n", logPath, test.stopError, test.killError)
			if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			err := runtime.StopDockerContainer(docker, "ctr-stopped", time.Second)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("confirmed stopped container returned an error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("stop error = %v, want %q", err, test.wantError)
			}
			calls, readErr := os.ReadFile(logPath)
			if readErr != nil || string(calls) != test.wantCalls {
				t.Fatalf("commands = %q, want %q; read error = %v", calls, test.wantCalls, readErr)
			}
		})
	}
}
