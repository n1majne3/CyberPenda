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

func TestReleaseWorkflowPublishesBinariesAndSandboxImage(t *testing.T) {
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
	assertContains(t, workflow, `file: docker/pentest-sandbox/Dockerfile`)
	assertContains(t, workflow, `platforms: ${{ matrix.platform }}`)
	assertContains(t, workflow, `outputs: type=image,push-by-digest=true,name-canonical=true,push=true`)
	assertContains(t, workflow, `steps.build.outputs.digest`)
	assertContains(t, workflow, `docker buildx imagetools create`)
	assertContains(t, workflow, `ghcr.io/${image_name}`)
}

func assertContains(t *testing.T, value string, want string) {
	t.Helper()
	if !strings.Contains(value, want) {
		t.Fatalf("expected value to contain %q", want)
	}
}
