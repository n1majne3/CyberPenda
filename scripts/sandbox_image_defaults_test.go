package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canonicalSandboxImage = "ghcr.io/n1majne3/cyberpenda-sandbox:latest"

func TestRuntimeSandboxImageDefaultsUsePublishedImage(t *testing.T) {
	repoRoot := repoRoot(t)

	for _, test := range []struct {
		path string
		want string
	}{
		{"cmd/pentestd/main.go", `const defaultSandboxImage = "` + canonicalSandboxImage + `"`},
		{"Makefile", "SANDBOX_IMAGE ?= " + canonicalSandboxImage},
		{"docker-compose.yaml", "PENTEST_SANDBOX_IMAGE: ${PENTEST_SANDBOX_IMAGE:-" + canonicalSandboxImage + "}"},
		{"scripts/with-pentestd-live.sh", "SANDBOX_IMAGE=\"${PENTEST_SANDBOX_IMAGE:-" + canonicalSandboxImage + "}\""},
		{"scripts/run-juice-shop-live.py", `os.environ.get("PENTEST_SANDBOX_IMAGE", "` + canonicalSandboxImage + `")`},
		{"scripts/smoke-sandbox-mcp-live.sh", "IMAGE=\"${PENTEST_SANDBOX_IMAGE:-" + canonicalSandboxImage + "}\""},
		{"README.md", "| `-sandbox-image` | `PENTEST_SANDBOX_IMAGE` | `" + canonicalSandboxImage + "` |"},
	} {
		t.Run(test.path, func(t *testing.T) {
			contents, err := os.ReadFile(filepath.Join(repoRoot, test.path))
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			assertContains(t, string(contents), test.want)
		})
	}
}

func TestSourceBuildTargetsForwardConfiguredSandboxImage(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	assertContains(t, string(makefile), "smoke-sandbox-mcp:\n\t@PENTEST_SANDBOX_IMAGE=$(SANDBOX_IMAGE) bash scripts/smoke-sandbox-mcp-live.sh")
	assertContains(t, string(makefile), "smoke-runtime-tasks:\n\t@PENTEST_SANDBOX_IMAGE=$(SANDBOX_IMAGE) python3 scripts/smoke-runtime-tasks-live.py")
	assertContains(t, string(makefile), "juice-shop-live:\n\t@PENTEST_SANDBOX_IMAGE=$(SANDBOX_IMAGE) python3 scripts/run-juice-shop-live.py")
}

func TestRetiredSandboxImageDefaultsAreAbsent(t *testing.T) {
	repoRoot := repoRoot(t)
	retired := []string{"pentest-sandbox:latest", "gemini_kali-gemini-kali:latest"}

	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel == ".git" || rel == "web/node_modules" || rel == "web/dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		switch filepath.Ext(rel) {
		case ".go", ".html", ".js", ".md", ".py", ".sh", ".ts", ".tsx", ".yaml", ".yml":
		default:
			if filepath.Base(rel) != "Makefile" {
				return nil
			}
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, image := range retired {
			if strings.Contains(string(contents), image) {
				t.Errorf("%s must not retain the retired sandbox image default %q", rel, image)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan sandbox image defaults: %v", err)
	}
}
