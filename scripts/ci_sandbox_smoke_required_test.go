package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCISandboxSmokeRequired(t *testing.T) {
	repoRoot := repoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "ci-sandbox-smoke-required.sh")

	tests := []struct {
		name    string
		files   string
		wantOut string
	}{
		{
			name:    "skips ordinary backend changes",
			files:   "internal/daemon/server.go\ninternal/daemon/server_test.go\n",
			wantOut: "required=false",
		},
		{
			name:    "requires smoke for sandbox Dockerfile changes",
			files:   "docker/pentest-sandbox/Dockerfile\n",
			wantOut: "required=true",
		},
		{
			name:    "requires smoke for sandbox harness changes",
			files:   "scripts/smoke-sandbox-mcp-live.sh\n",
			wantOut: "required=true",
		},
		{
			name:    "requires smoke for runner sandbox command changes",
			files:   "internal/runner/runner.go\n",
			wantOut: "required=true",
		},
		{
			name:    "requires smoke for daemon sandbox launch assembly changes",
			files:   "internal/daemon/task_handlers.go\n",
			wantOut: "required=true",
		},
		{
			name:    "requires smoke for CI workflow changes",
			files:   ".github/workflows/ci.yml\n",
			wantOut: "required=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", script, "--stdin")
			cmd.Stdin = strings.NewReader(tt.files)
			cmd.Dir = repoRoot
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tt.wantOut {
				t.Fatalf("got %q, want %q", got, tt.wantOut)
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("repo root not found")
		}
		wd = parent
	}
}
