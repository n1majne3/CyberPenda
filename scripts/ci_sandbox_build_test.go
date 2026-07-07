package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxShellScriptsAreExecutable(t *testing.T) {
	repoRoot := repoRoot(t)
	scripts := []string{
		"scripts/build-release-binaries.sh",
		"scripts/ci-sandbox-smoke-required.sh",
		"scripts/smoke-sandbox-mcp-live.sh",
		"scripts/with-pentestd-live.sh",
	}

	for _, script := range scripts {
		t.Run(script, func(t *testing.T) {
			info, err := os.Stat(filepath.Join(repoRoot, script))
			if err != nil {
				t.Fatalf("stat script: %v", err)
			}
			if info.Mode().Perm()&0111 == 0 {
				t.Fatalf("%s is not executable; CI invokes shell scripts directly", script)
			}
		})
	}
}

func TestSandboxDockerfileKeepsKaliLinuxHeadlessMetaPackage(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerfilePath := filepath.Join(repoRoot, "docker", "pentest-sandbox", "Dockerfile")
	dockerfileBytes, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	if !strings.Contains(dockerfile, "kali-linux-headless") {
		t.Fatal("sandbox Dockerfile must keep kali-linux-headless for the full Kali baseline")
	}

	for _, tool := range []string{"nmap", "sqlmap", "nuclei", "subfinder", "naabu", "ffuf", "dirsearch", "gitleaks", "nikto", "netexec"} {
		if !strings.Contains(dockerfile, tool) {
			t.Fatalf("sandbox Dockerfile should keep explicit tool %q installed", tool)
		}
	}
}

func TestReleaseWorkflowBuildsSandboxImagePerPlatform(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "release.yml")
	workflowBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(workflowBytes)

	assertContains(t, workflow, "Free disk space for sandbox image")
	assertContains(t, workflow, "/usr/share/dotnet")
	assertContains(t, workflow, "/usr/local/lib/android")
	assertContains(t, workflow, "${AGENT_TOOLSDIRECTORY:-}")
	assertContains(t, workflow, "docker system prune -af")
	assertContains(t, workflow, "matrix:")
	assertContains(t, workflow, "platform:")
	assertContains(t, workflow, "linux/amd64")
	assertContains(t, workflow, "linux/arm64")
	assertContains(t, workflow, "platforms: ${{ matrix.platform }}")
	assertContains(t, workflow, "push-by-digest=true")
	assertContains(t, workflow, "steps.build.outputs.digest")
	assertContains(t, workflow, "actions/upload-artifact@v7")
	assertContains(t, workflow, "pattern: sandbox-image-digest-*")
	assertContains(t, workflow, "merge-multiple: true")
	assertContains(t, workflow, "docker buildx imagetools create")

	if strings.Contains(workflow, "file: docker/pentest-sandbox/Dockerfile\n          platforms: linux/amd64,linux/arm64") {
		t.Fatal("release workflow must not build both sandbox platforms in one Buildx invocation")
	}

	sandboxStart := strings.Index(workflow, "publish-sandbox-image:")
	manifestStart := strings.Index(workflow, "publish-sandbox-manifest:")
	if sandboxStart == -1 || manifestStart == -1 || manifestStart <= sandboxStart {
		t.Fatal("release workflow must include sandbox image and manifest jobs")
	}
	sandboxJob := workflow[sandboxStart:manifestStart]
	cleanupIndex := strings.Index(sandboxJob, "Free disk space for sandbox image")
	buildxIndex := strings.Index(sandboxJob, "docker/setup-buildx-action@v4")
	if cleanupIndex == -1 || buildxIndex == -1 {
		t.Fatal("release workflow must include disk cleanup and Buildx setup")
	}
	if cleanupIndex > buildxIndex {
		t.Fatal("release workflow must free disk before setting up Buildx")
	}
}
