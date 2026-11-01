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
			name:    "translated readme alone",
			files:   "README.zh-CN.md\nGLOSSARY.md\n",
			wantOut: "code=false",
		},
		{
			name:    "embedded builtin skill markdown is code",
			files:   "internal/skill/builtins/assets/ctf-orchestrator/SKILL.md\n",
			wantOut: "code=true",
		},
		{
			name:    "embedded mode skill bundle is code",
			files:   "internal/modeskill/bundles/cyberpenda-blackboard-interactive/SKILL.md\n",
			wantOut: "code=true",
		},
		{
			name:    "contract-tested spec is code",
			files:   "docs/specs/blackboard-v2-spec.md\n",
			wantOut: "code=true",
		},
		{
			name:    "contract-tested adr is code",
			files:   "docs/adr/0014-use-one-versioned-blackboard-v2-project-interface.md\n",
			wantOut: "code=true",
		},
		{
			name:    "tsecbench bundle doc is code",
			files:   "docs/tsecbench/README.md\n",
			wantOut: "code=true",
		},
		{
			name:    "markdown outside the allowlist is code",
			files:   ".claude/skills/verify-cyberpenda/SKILL.md\n",
			wantOut: "code=true",
		},
		{
			name:    "unread docs tree is docs",
			files:   "docs/research/notes.md\ndocs/adr/0001-global-default-on-skills.md\n",
			wantOut: "code=false",
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

func TestCICodeChangesRequiredFailsSafeOnUnknownBase(t *testing.T) {
	repoRoot := repoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "ci-code-changes-required.sh")
	for _, base := range []string{"", "0000000000000000000000000000000000000000", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"} {
		cmd := exec.Command("bash", script, base, "HEAD")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("base %q: script failed: %v\n%s", base, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != "code=true" {
			t.Fatalf("base %q: got %q, want code=true (unknown base must run full CI)", base, got)
		}
	}
}
