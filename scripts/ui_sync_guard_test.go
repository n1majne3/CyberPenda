package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedUIBuildUsesFreshAssets(t *testing.T) {
	repoRoot := repoRoot(t)

	gitignoreBytes, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	gitignore := string(gitignoreBytes)
	assertContains(t, gitignore, "internal/daemon/webfs/dist/**")
	assertContains(t, gitignore, "!internal/daemon/webfs/dist/.gitkeep")

	// Dockerfile always injects a fresh web build; it must not rely on git dist.
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot, "docker", "pentestd", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	assertContains(t, string(dockerfile), "COPY --from=web-build /src/web/dist internal/daemon/webfs/dist")
}

func TestBuildUIUsesPortableEmbedSynchronizationWithoutCommitGate(t *testing.T) {
	repoRoot := repoRoot(t)
	makefileBytes, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)

	assertContains(t, makefile, "build: build-ui")
	assertContains(t, makefile, "@node scripts/web-build-cli.mjs build-ui")
	assertContains(t, makefile, "@node scripts/web-build-cli.mjs ensure-embed-stub")
	assertContains(t, makefile, "\tgo build ./cmd/pentestd")
	if strings.Contains(makefile, "rsync") || strings.Contains(makefile, "mkdir -p internal/daemon/webfs/dist") {
		t.Fatal("native build path must not require POSIX file utilities")
	}
	if strings.Contains(makefile, "go build -o pentestd") {
		t.Fatal("Go must select the native executable suffix")
	}
	if strings.Contains(makefile, "check-ui-sync") {
		t.Fatal("check-ui-sync must not remain; embedded UI is no longer committed")
	}

	workflowBytes, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflow := string(workflowBytes)
	if strings.Contains(workflow, "check-ui-sync") {
		t.Fatal("CI must not require committed UI sync")
	}
	assertContains(t, workflow, "make build-ui")
	windowsJob := workflowJobText(t, workflow, "windows-build")
	assertContains(t, windowsJob, "runs-on: windows-latest")
	assertContains(t, windowsJob, "shell: cmd")
	assertContains(t, windowsJob, "node --test scripts/web-build.test.mjs")
	assertContains(t, windowsJob, "make build")
	assertContains(t, windowsJob, "pentestd.exe")

	releaseBytes, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	release := string(releaseBytes)
	assertContains(t, release, "make build-ui")
	if strings.Contains(release, "rm -rf internal/daemon/webfs/dist") {
		t.Fatal("release UI synchronization must preserve the embed directory")
	}

	hookPath := filepath.Join(repoRoot, ".githooks", "pre-push")
	hookBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read pre-push hook: %v", err)
	}
	if strings.Contains(string(hookBytes), "check-ui-sync") {
		t.Fatal("pre-push must not run check-ui-sync")
	}
}

func TestDevRepairsMissingOrStaleWebDependenciesWithoutBash(t *testing.T) {
	repoRoot := repoRoot(t)
	makefileBytes, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)

	assertContains(t, makefile, "dev: build-ui")
	assertContains(t, makefile, "@node scripts/web-build-cli.mjs ensure-deps")
	if strings.Contains(makefile, "ensure-web-deps.sh") {
		t.Fatal("web dependency repair must not require Bash")
	}
}
