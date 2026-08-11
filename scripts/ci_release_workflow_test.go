package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReleaseBinaryTargetsCoverSupportedPlatforms(t *testing.T) {
	repoRoot := repoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "build-release-binaries.sh")

	cmd := exec.Command("bash", script, "--list-targets")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list release targets failed: %v\n%s", err, out)
	}

	got := strings.Fields(string(out))
	want := []string{
		"linux/amd64",
		"linux/arm64",
		"darwin/amd64",
		"darwin/arm64",
		"windows/amd64",
		"windows/arm64",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("release targets = %#v, want %#v", got, want)
	}
}

func TestReleaseBinaryBuildRejectsOutputOutsideRepositoryWithoutDeletingIt(t *testing.T) {
	repoRoot := repoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "build-release-binaries.sh")
	distDir := filepath.Join(t.TempDir(), "shared-output")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatalf("create outside output: %v", err)
	}
	marker := filepath.Join(distDir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write outside output marker: %v", err)
	}

	cmd := exec.Command("bash", script, "v1.2.3", distDir)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "PENTEST_RELEASE_TARGETS=linux/amd64")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("build with outside output unexpectedly succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "dist-dir must stay inside the repository") {
		t.Fatalf("build error = %q, want safe output-path rejection", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("outside output marker was changed: %v", err)
	}
}

func TestReleaseBinaryBuildRejectsRepositoryRoot(t *testing.T) {
	repoRoot := repoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "build-release-binaries.sh")

	cmd := exec.Command("bash", script, "v1.2.3", repoRoot)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "PENTEST_RELEASE_TARGETS=linux/amd64")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("build at repository root unexpectedly succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "dist-dir must not be the repository root") {
		t.Fatalf("build error = %q, want repository-root rejection", out)
	}
}

func TestReleaseBinaryBuildRejectsUnsafePathComponentsBeforeChangingOutput(t *testing.T) {
	repoRoot := repoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "build-release-binaries.sh")
	testRoot, err := os.MkdirTemp(repoRoot, ".release-script-test-")
	if err != nil {
		t.Fatalf("create repository test directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	distDir := filepath.Join(testRoot, "output")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatalf("create output: %v", err)
	}
	marker := filepath.Join(distDir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write output marker: %v", err)
	}

	tests := []struct {
		name    string
		version string
		target  string
		want    string
	}{
		{name: "version traversal", version: "v1/../../escape", target: "linux/amd64", want: "invalid version"},
		{name: "target traversal", version: "v1.2.3", target: "linux/amd64/../../escape", want: "invalid target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", script, tt.version, distDir)
			cmd.Dir = repoRoot
			cmd.Env = append(os.Environ(), "PENTEST_RELEASE_TARGETS="+tt.target)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("unsafe build unexpectedly succeeded:\n%s", out)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Fatalf("build error = %q, want %q", out, tt.want)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("output marker was changed: %v", err)
			}
		})
	}
}

func TestReleaseWorkflowPublishesBinariesAndAppImageWithoutSandboxBuild(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "release.yml")
	workflowBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(workflowBytes)

	assertContains(t, workflow, `tags: ["v*"]`)
	assertContains(t, workflow, "contents: write")
	assertContains(t, workflow, "packages: write")
	assertContains(t, workflow, `actions/checkout@v7`)
	assertContains(t, workflow, `actions/setup-go@v6`)
	assertContains(t, workflow, `actions/setup-node@v6`)
	assertContains(t, workflow, `actions/upload-artifact@v7`)
	assertContains(t, workflow, `actions/download-artifact@v8`)
	assertContains(t, workflow, `scripts/build-release-binaries.sh "${GITHUB_REF_NAME}" dist/release`)
	assertContains(t, workflow, `gh release create "${GITHUB_REF_NAME}" dist/release/* --verify-tag --generate-notes`)
	assertContains(t, workflow, `gh release upload "${GITHUB_REF_NAME}" dist/release/* --clobber`)
	assertContains(t, workflow, `docker/login-action@v4`)
	assertContains(t, workflow, `registry: ghcr.io`)
	assertContains(t, workflow, `docker/setup-qemu-action@v4`)
	assertContains(t, workflow, `docker/setup-buildx-action@v4`)
	assertContains(t, workflow, `docker/metadata-action@v6`)
	assertContains(t, workflow, `docker/build-push-action@v7`)
	assertContains(t, workflow, `ghcr.io/${image_name}`)

	for _, forbidden := range []string{
		"publish-sandbox-image:",
		"publish-sandbox-manifest:",
		"docker/pentest-sandbox/Dockerfile",
		"sandbox-image-digest-",
		"docker buildx imagetools create",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("ordinary release workflow must not contain sandbox publication detail %q", forbidden)
		}
	}
}

func assertContains(t *testing.T, value string, want string) {
	t.Helper()
	if !strings.Contains(value, want) {
		t.Fatalf("expected value to contain %q", want)
	}
}
