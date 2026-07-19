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

func TestSandboxMCPLiveSmokeUsesBlackboardV2Boundaries(t *testing.T) {
	repoRoot := repoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "smoke-sandbox-mcp-live.sh")
	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read sandbox MCP smoke script: %v", err)
	}
	script := string(scriptBytes)

	for _, tool := range []string{
		"blackboard_change",
		"blackboard_read",
		"blackboard_history",
		"blackboard_retain_evidence",
		"blackboard_checkpoint_attempt",
		"blackboard_finish",
	} {
		assertContains(t, script, tool)
	}
	for _, required := range []string{
		`"method":"tools/list"`,
		"curl -sf -X POST \"${mcp_url}\" \\\n    \"${auth_args[@]}\"",
		"/api/v2/projects/",
		"/blackboard/changes",
		"/blackboard/records/",
		"Authorization: Bearer",
		"Idempotency-Key",
		"semantic-change-batch/v2",
	} {
		assertContains(t, script, required)
	}

	for _, retired := range []string{
		"upsert_project_fact",
		`"method":"tools/call"`,
		"/api/projects/",
		"/facts/",
	} {
		if strings.Contains(script, retired) {
			t.Fatalf("sandbox MCP smoke script still contains retired boundary %q", retired)
		}
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

func TestSandboxDockerfileKeepsProviderBridgeSourceInLateCacheLayer(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(repoRoot, "docker", "pentest-sandbox", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	sdkInstall := strings.Index(dockerfile, "npm install --prefix /opt/pentest/claude-sdk-bridge")
	hostEntrypoint := strings.Index(dockerfile, "COPY docker/pentest-sandbox/host-proxy-only-entrypoint.sh")
	bridgeSource := strings.Index(dockerfile, "COPY cmd/pentest-claude-sdk-bridge/bridge.mjs")
	if sdkInstall == -1 || hostEntrypoint == -1 || bridgeSource == -1 {
		t.Fatalf("sandbox Dockerfile is missing Claude bridge build steps")
	}
	if sdkInstall > hostEntrypoint {
		t.Fatal("Claude Agent SDK dependency layer should remain before heavyweight sandbox tools")
	}
	if bridgeSource < hostEntrypoint {
		t.Fatal("Claude bridge source should be copied after heavyweight sandbox layers for cache reuse")
	}
}

func TestManualSandboxWorkflowBuildsAndPublishesImagePerPlatform(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "publish-sandbox.yml")
	workflowBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read sandbox publication workflow: %v", err)
	}
	workflow := string(workflowBytes)

	assertContains(t, workflow, "workflow_dispatch:")
	assertContains(t, workflow, "image_tag:")
	assertContains(t, workflow, "Sandbox image tag to publish")
	assertContains(t, workflow, "default: latest")
	assertContains(t, workflow, "ghcr.io/${image_name}")
	assertContains(t, workflow, "docker/metadata-action@v6")
	assertContains(t, workflow, "type=raw,value=${{ inputs.image_tag }}")
	assertContains(t, workflow, "publish-sandbox-image:")
	assertContains(t, workflow, "publish-sandbox-manifest:")
	assertContains(t, workflow, "file: docker/pentest-sandbox/Dockerfile")
	assertContains(t, workflow, "Free disk space for sandbox image")
	assertContains(t, workflow, "/usr/share/dotnet")
	assertContains(t, workflow, "/usr/local/lib/android")
	assertContains(t, workflow, "${AGENT_TOOLSDIRECTORY:-}")
	assertContains(t, workflow, "docker system prune -af")
	assertContains(t, workflow, "matrix:")
	assertContains(t, workflow, "include:")
	assertContains(t, workflow, "linux/amd64")
	assertContains(t, workflow, "linux/arm64")
	assertContains(t, workflow, "runner: ubuntu-latest")
	assertContains(t, workflow, "runner: ubuntu-24.04-arm")
	assertContains(t, workflow, "runs-on: ${{ matrix.runner }}")
	assertContains(t, workflow, "platforms: ${{ matrix.platform }}")
	assertContains(t, workflow, "push-by-digest=true")
	assertContains(t, workflow, "steps.build.outputs.digest")
	assertContains(t, workflow, "actions/upload-artifact@v7")
	assertContains(t, workflow, "pattern: sandbox-image-digest-*")
	assertContains(t, workflow, "merge-multiple: true")
	assertContains(t, workflow, "docker buildx imagetools create")

	if strings.Contains(workflow, "file: docker/pentest-sandbox/Dockerfile\n          platforms: linux/amd64,linux/arm64") {
		t.Fatal("manual sandbox workflow must not build both sandbox platforms in one Buildx invocation")
	}
	if strings.Contains(workflow, "docker/setup-qemu-action") {
		t.Fatal("manual sandbox workflow must use native per-platform runners instead of QEMU")
	}

	sandboxStart := strings.Index(workflow, "publish-sandbox-image:")
	manifestStart := strings.Index(workflow, "publish-sandbox-manifest:")
	if sandboxStart == -1 || manifestStart == -1 || manifestStart <= sandboxStart {
		t.Fatal("manual sandbox workflow must include sandbox image and manifest jobs")
	}
	sandboxJob := workflow[sandboxStart:manifestStart]
	cleanupIndex := strings.Index(sandboxJob, "Free disk space for sandbox image")
	buildxIndex := strings.Index(sandboxJob, "docker/setup-buildx-action@v4")
	if cleanupIndex == -1 || buildxIndex == -1 {
		t.Fatal("manual sandbox workflow must include disk cleanup and Buildx setup")
	}
	if cleanupIndex > buildxIndex {
		t.Fatal("manual sandbox workflow must free disk before setting up Buildx")
	}
}
