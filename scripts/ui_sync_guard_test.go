package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedUISyncGuardRunsLocallyAndInCI(t *testing.T) {
	repoRoot := repoRoot(t)

	makefileBytes, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)
	assertContains(t, makefile, "check-ui-sync:\n\t@bash scripts/check-embedded-ui-sync.sh")
	assertContains(t, makefile, "install-git-hooks:\n\tgit config core.hooksPath .githooks")

	workflowBytes, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	assertContains(t, string(workflowBytes), "run: make check-ui-sync")

	hookPath := filepath.Join(repoRoot, ".githooks", "pre-push")
	hookInfo, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("stat pre-push hook: %v", err)
	}
	if hookInfo.Mode().Perm()&0111 == 0 {
		t.Fatal("pre-push hook must be executable")
	}
	hookBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read pre-push hook: %v", err)
	}
	assertContains(t, string(hookBytes), "make check-ui-sync")

	guardBytes, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "check-embedded-ui-sync.sh"))
	if err != nil {
		t.Fatalf("read embedded UI sync guard: %v", err)
	}
	guard := string(guardBytes)
	assertContains(t, guard, "make build-ui")
	assertContains(t, guard, "git diff --exit-code HEAD -- internal/daemon/webfs/dist")
}

func TestBuildUIUpdatesEmbedWithoutReplacingSyncedDirectory(t *testing.T) {
	repoRoot := repoRoot(t)
	makefileBytes, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)

	assertContains(t, makefile, "rsync -a --delete web/dist/ internal/daemon/webfs/dist/")
	for _, destructiveCopy := range []string{
		"rm -rf internal/daemon/webfs/dist",
		"cp -r web/dist internal/daemon/webfs/dist",
	} {
		if strings.Contains(makefile, destructiveCopy) {
			t.Fatalf("build-ui must not replace the iCloud-synced embed directory with %q", destructiveCopy)
		}
	}
}
