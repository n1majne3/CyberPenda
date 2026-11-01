package scripts_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCICodeChangesRequired(t *testing.T) {
	repoRoot := repoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "ci-code-changes-required.sh")

	tests := []struct {
		name    string
		files   string
		wantOut string
	}{
		{
			name:    "docs only markdown",
			files:   "README.md\ndocs/guide.md\n",
			wantOut: "code=false",
		},
		{
			name:    "readme alone",
			files:   "README.md\n",
			wantOut: "code=false",
		},
		{
			name:    "workflow change is code",
			files:   ".github/workflows/ci.yml\n",
			wantOut: "code=true",
		},
		{
			name:    "mixed docs and code",
			files:   "README.md\ninternal/daemon/server.go\n",
			wantOut: "code=true",
		},
		{
			name:    "license alone",
			files:   "LICENSE\n",
			wantOut: "code=false",
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
