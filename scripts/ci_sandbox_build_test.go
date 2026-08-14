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
		"blackboard_record_attempt_result",
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
		`"kind":"pentest"`,
		"curl -sf -X POST \"${mcp_url}\" \\\n    \"${auth_args[@]+\"${auth_args[@]}\"}\"",
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

	for _, tool := range []string{
		"nmap", "sqlmap", "nuclei", "subfinder", "naabu", "ffuf", "dirsearch", "gitleaks", "nikto", "netexec",
		// Reverse-engineering toolchain for vendored reverse-skill builtins.
		"default-jdk", "jadx", "apktool", "ghidra", "yara",
		"android-sdk-platform-tools", "seclists", "graphviz", "plantuml",
	} {
		if !strings.Contains(dockerfile, tool) {
			t.Fatalf("sandbox Dockerfile should keep explicit tool %q installed", tool)
		}
	}
}

func TestSandboxDockerfileInstallsHermesWithXZForOfficialInstaller(t *testing.T) {
	dockerfileBytes, err := os.ReadFile(filepath.Join(repoRoot(t), "docker", "pentest-sandbox", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	aptStart := strings.Index(dockerfile, "apt-get install")
	hermesInstall := strings.Index(dockerfile, "hermes-agent.nousresearch.com/install.sh")
	if aptStart == -1 || hermesInstall == -1 {
		t.Fatal("sandbox Dockerfile must apt-get install packages and run the Hermes installer")
	}
	if hermesInstall < aptStart {
		t.Fatal("Hermes installer must run after apt-get so xz-utils is present")
	}
	aptEnd := strings.Index(dockerfile[aptStart:], "\nRUN")
	if aptEnd == -1 {
		aptEnd = hermesInstall - aptStart
	}
	aptBlock := dockerfile[aptStart : aptStart+aptEnd]
	if !strings.Contains(aptBlock, "xz-utils") {
		t.Fatal("sandbox Dockerfile must install xz-utils before Hermes; install.sh extracts node-*.tar.xz")
	}
	if strings.Contains(dockerfile, "/root/.local/bin/hermes") {
		t.Fatal("root Hermes install uses FHS /usr/local/bin/hermes, not ~/.local/bin")
	}
	hermesBlock := dockerfile[hermesInstall:]
	if next := strings.Index(hermesBlock[1:], "\nRUN"); next != -1 {
		hermesBlock = hermesBlock[:next+1]
	}
	for _, want := range []string{"--stage", "python-deps", "--skip-browser", "--skip-computer-use", "--non-interactive"} {
		if !strings.Contains(hermesBlock, want) {
			t.Fatalf("Hermes ACP install must use staged install.sh without node-deps; missing %q", want)
		}
	}
	stageLine := ""
	for _, line := range strings.Split(hermesBlock, "\n") {
		if strings.Contains(line, "for stage in") {
			stageLine = line
			break
		}
	}
	if stageLine == "" || !strings.Contains(stageLine, "python-deps") {
		t.Fatal("Hermes ACP install must loop official install.sh stages including python-deps")
	}
	if strings.Contains(stageLine, "node-deps") {
		t.Fatal("Hermes ACP install must not run install.sh --stage node-deps; npm install is fatal on timeout")
	}
}

func TestSandboxDockerfileInstallsPipOnlyToolsViaPip(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(repoRoot, "docker", "pentest-sandbox", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	// pwntools and frida-tools have no Kali apt package; they must be pip.
	// unicorn (a pwntools dep) builds from source when no wheel matches;
	// cmake must already be in the apt layer.
	if !strings.Contains(dockerfile, "cmake") || !strings.Contains(dockerfile, "pkg-config") {
		t.Fatal("sandbox Dockerfile must install cmake and pkg-config before pip-installing pwntools/unicorn")
	}
	pipLine := "pip3 install --no-cache-dir pwntools frida-tools --break-system-packages"
	if !strings.Contains(dockerfile, pipLine) {
		t.Fatalf("sandbox Dockerfile should install pip-only tools together: %s", pipLine)
	}
	// The apt block spans from "apt-get install" to the next RUN; pip-only
	// tools must not appear inside any apt-get package list.
	aptStart := strings.Index(dockerfile, "apt-get install")
	aptEnd := strings.Index(dockerfile[aptStart:], "\nRUN")
	if aptEnd == -1 {
		aptEnd = len(dockerfile) - aptStart
	}
	aptBlock := dockerfile[aptStart : aptStart+aptEnd]
	for _, tool := range []string{"pwntools", "frida-tools"} {
		if strings.Contains(aptBlock, " "+tool) {
			t.Fatalf("%s has no apt package; it must not be installed via apt-get", tool)
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

func TestPullRequestSandboxSmokeSkipsFullKaliImageBuild(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(repoRoot, "docker", "pentest-sandbox", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)
	assertContains(t, dockerfile, "FROM alpine:3.22 AS smoke")
	assertContains(t, dockerfile, "FROM kalilinux/kali-rolling:latest AS runtime")

	makefileBytes, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makefileBytes)
	assertContains(t, makefile, "SANDBOX_SMOKE_IMAGE ?= cyberpenda-sandbox-smoke:ci")
	assertContains(t, makefile, "docker build --target smoke -t $(SANDBOX_SMOKE_IMAGE) -f docker/pentest-sandbox/Dockerfile .")
	assertContains(t, makefile, "go test ./cmd/... ./internal/... ./scripts")

	workflowBytes, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflow := string(workflowBytes)
	smokeJobStart := strings.Index(workflow, "  smoke-sandbox-mcp:")
	if smokeJobStart == -1 {
		t.Fatal("CI workflow must include the Sandbox MCP smoke job")
	}
	smokeJob := workflow[smokeJobStart:]
	for _, forbidden := range []string{
		"Validate full sandbox image build",
		"docker/setup-buildx-action",
		"docker/build-push-action",
		"target: runtime",
	} {
		if strings.Contains(smokeJob, forbidden) {
			t.Fatalf("Sandbox MCP smoke job must not build the full Kali image: found %q", forbidden)
		}
	}
	assertContains(t, smokeJob, "make build-sandbox-smoke-image")
	assertContains(t, smokeJob, "PENTEST_SANDBOX_IMAGE: cyberpenda-sandbox-smoke:ci")
	assertContains(t, smokeJob, "\n          SANDBOX_IMAGE: cyberpenda-sandbox-smoke:ci")
	assertContains(t, smokeJob, "\n          PENTEST_DAEMON_WAIT_SECONDS: \"120\"")
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
