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
		"scripts/smoke-sandbox-fgs-live.sh",
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

func TestSandboxLiveSmokeRunsFGSContainerAcceptance(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "smoke-sandbox-fgs-live.sh"))
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, string(script), "PENTEST_SANDBOX_FGS_SMOKE=1")
	assertContains(t, string(script), "TestSandboxFGSOutboxLive")
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot(t), "docker", "pentest-sandbox", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	smoke := strings.Split(strings.Split(string(dockerfile), "FROM alpine:3.22 AS smoke")[1], "FROM kalilinux/")[0]
	assertContains(t, smoke, "COPY --from=pentestctl-build /out/pentestctl /usr/local/bin/pentestctl")
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

func TestSandboxDockerfileProvidesQemuUserEmulation(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(repoRoot, "docker", "pentest-sandbox", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	for _, required := range []string{
		// User-mode emulators for foreign-architecture binaries.
		"qemu-user-static",
		// Kali qemu-user 11 ships qemu-<arch>, not qemu-<arch>-static.
		// The image must provide both name forms.
		`"/usr/bin/${base}-static"`,
		// The build must fail when a pinned emulator is missing. Keep the
		// >/dev/null suffix so this cannot match qemu-x86_64-static.
		"command -v qemu-x86_64 >/dev/null",
		"command -v qemu-x86_64-static",
		"command -v qemu-aarch64-static",
		"command -v qemu-arm-static",
		"command -v qemu-i386-static",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("sandbox Dockerfile must keep qemu user-mode emulation; missing %q", required)
		}
	}
}

func TestSandboxRuntimeImageProvidesPentestctlOnPath(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(repoRoot, "docker", "pentest-sandbox", "Dockerfile"))
	if err != nil {
		t.Fatalf("read sandbox Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	for _, required := range []string{
		"AS pentestctl-build",
		"go build -trimpath -o /out/pentestctl ./cmd/pentestctl",
		"COPY --from=pentestctl-build /out/pentestctl /usr/local/bin/pentestctl",
		"pentestctl blackboard --help",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("sandbox Runtime image must provide a verified pentestctl CLI; missing %q", required)
		}
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
	// Crypto/RE python libs (pycryptodome, z3-solver) ride pip for portability.
	cryptoPipLine := "pip3 install --no-cache-dir pycryptodome z3-solver --break-system-packages"
	if !strings.Contains(dockerfile, cryptoPipLine) {
		t.Fatalf("sandbox Dockerfile should install crypto/RE python libs via pip: %s", cryptoPipLine)
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
	assertContains(t, makefile, "go test -timeout 20m ./cmd/... ./internal/... ./scripts")

	workflowBytes, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflow := string(workflowBytes)
	smokeJobStart := strings.Index(workflow, "  smoke-sandbox-fgs:")
	if smokeJobStart == -1 {
		t.Fatal("CI workflow must include the Sandbox FGS smoke job")
	}
	smokeJob := workflow[smokeJobStart:]
	for _, forbidden := range []string{
		"Validate full sandbox image build",
		"target: runtime",
	} {
		if strings.Contains(smokeJob, forbidden) {
			t.Fatalf("Sandbox FGS smoke job must not build the full Kali image: found %q", forbidden)
		}
	}
	assertContains(t, smokeJob, "make build-sandbox-smoke-image")
	assertContains(t, smokeJob, "PENTEST_SANDBOX_IMAGE: cyberpenda-sandbox-smoke:ci")
	assertContains(t, smokeJob, "\n          SANDBOX_IMAGE: cyberpenda-sandbox-smoke:ci")
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
	assertContains(t, workflow, "default: latest")
	assertContains(t, workflow, "ghcr.io/${image_name}")

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

	assertContains(t, workflow, "pattern: sandbox-image-digest-*")
	assertContains(t, workflow, "merge-multiple: true")
	assertContains(t, workflow, "docker buildx imagetools create")

	if strings.Contains(workflow, "file: docker/pentest-sandbox/Dockerfile\n          platforms: linux/amd64,linux/arm64") {
		t.Fatal("manual sandbox workflow must not build both sandbox platforms in one Buildx invocation")
	}
	if strings.Contains(workflow, "docker/setup-qemu-action") {
		t.Fatal("manual sandbox workflow must use native per-platform runners instead of QEMU")
	}

}
