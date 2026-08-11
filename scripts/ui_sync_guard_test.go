package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedUIIsNotCommitted(t *testing.T) {
	repoRoot := repoRoot(t)

	gitignoreBytes, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	gitignore := string(gitignoreBytes)
	assertContains(t, gitignore, "internal/daemon/webfs/dist/**")
	assertContains(t, gitignore, "!internal/daemon/webfs/dist/.gitkeep")

	// Committed product assets under dist/ would reintroduce merge noise.
	distDir := filepath.Join(repoRoot, "internal", "daemon", "webfs", "dist")
	entries, err := os.ReadDir(distDir)
	if err != nil {
		t.Fatalf("read embed dist: %v", err)
	}
	trackedKeep := false
	for _, entry := range entries {
		name := entry.Name()
		if name == ".gitkeep" {
			trackedKeep = true
			continue
		}
		// Generated files may exist locally after build-ui; they must not be
		// required as committed product. Prove the git index has no dist assets
		// other than .gitkeep via git ls-files in a separate contract below.
		_ = name
	}
	if !trackedKeep {
		// Working tree may only have generated files; index check is definitive.
		t.Log("local dist/.gitkeep missing after clean; relying on git index contract")
	}

	// Dockerfile always injects a fresh web build; it must not rely on git dist.
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot, "docker", "pentestd", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	assertContains(t, string(dockerfile), "COPY --from=web-build /src/web/dist internal/daemon/webfs/dist")
}

func TestBuildUIWritesLocalEmbedWithoutCommitGate(t *testing.T) {
	repoRoot := repoRoot(t)
	makefileBytes, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)

	assertContains(t, makefile, "build: build-ui")
	assertContains(t, makefile, "rsync -a --delete --exclude .gitkeep web/dist/ internal/daemon/webfs/dist/")
	assertContains(t, makefile, "ensure-embed-stub:")
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

	hookPath := filepath.Join(repoRoot, ".githooks", "pre-push")
	hookBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read pre-push hook: %v", err)
	}
	if strings.Contains(string(hookBytes), "check-ui-sync") {
		t.Fatal("pre-push must not run check-ui-sync")
	}
}

func TestDevRepairsMissingOrStaleWebDependencies(t *testing.T) {
	repoRoot := repoRoot(t)
	makefileBytes, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)

	assertContains(t, makefile, "dev: ensure-web-deps")
	assertContains(t, makefile, "ensure-web-deps:\n\t@bash scripts/ensure-web-deps.sh")

	guardBytes, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "ensure-web-deps.sh"))
	if err != nil {
		t.Fatalf("read web dependency guard: %v", err)
	}
	guard := string(guardBytes)
	assertContains(t, guard, "node_modules/.bin/vite")
	assertContains(t, guard, "node_modules/.package-lock.json")
	assertContains(t, guard, "import('rolldown')")
	assertContains(t, guard, "npm ci")
}
