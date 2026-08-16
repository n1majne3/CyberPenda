package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const hostedImageEnvironment = "CYBERPENDA_TSECBENCH_HOSTED_IMAGE"

func TestTSecBenchHostedDockerfileDefinesTheIsolatedAMD64Image(t *testing.T) {
	root := repoRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "docker", "tsecbench-hosted", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Hosted Image Dockerfile: %v", err)
	}
	dockerfile := string(contents)

	for _, required := range []string{
		"FROM --platform=$BUILDPLATFORM node:",
		"COPY web/package.json web/package-lock.json ./",
		"RUN --mount=type=cache,target=/root/.npm npm ci",
		"RUN npm run build",
		"FROM --platform=$BUILDPLATFORM golang:",
		"ARG TARGETOS",
		"ARG TARGETARCH",
		`test "${TARGETOS}/${TARGETARCH}" = "linux/amd64"`,
		"COPY --from=web-build /src/web/dist internal/daemon/webfs/dist",
		"test -s internal/daemon/webfs/dist/index.html",
		"FROM --platform=$TARGETPLATFORM kalilinux/kali-rolling:latest AS runtime",
		"USER root",
		`ENTRYPOINT ["/usr/local/bin/pentest-tsecbench-hosted"]`,
		"CYBERPENDA_PROVIDER_BRIDGE=/usr/local/bin/pentest-provider-bridge",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"IS_SANDBOX=1",
		"PI_OFFLINE=1",
		"/opt/cyberpenda/runtime-versions.json",
		"cyberpenda-hosted-runtime-versions/v1",
		`require('/opt/pentest/claude-sdk-bridge/node_modules/@anthropic-ai/claude-agent-sdk/package.json').version`,
		`claude_agent_sdk`,
	} {
		assertContains(t, dockerfile, required)
	}
	if strings.Contains(strings.ToUpper(dockerfile), "EXPOSE ") {
		t.Fatal("Hosted Image must not expose the loopback daemon")
	}
	if strings.Contains(dockerfile, "Hosted Image has no exposed Web UI") {
		t.Fatal("Hosted Image must embed the normal Web UI, not a placeholder")
	}
}

func TestTSecBenchHostedImageHasSeparateBuildAndInventoryCommands(t *testing.T) {
	root := repoRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(contents)
	for _, required := range []string{
		"TSECBENCH_HOSTED_IMAGE ?= cyberpenda-tsecbench-hosted:local",
		"build-tsecbench-hosted-image:",
		"docker buildx build --platform linux/amd64 --load",
		"smoke-tsecbench-hosted-image:",
		"CYBERPENDA_TSECBENCH_HOSTED_IMAGE=$(TSECBENCH_HOSTED_IMAGE)",
		"tsecbench-hosted-runtime-inventory:",
		"/opt/cyberpenda/runtime-versions.json",
	} {
		assertContains(t, makefile, required)
	}
}

func TestTSecBenchHostedDockerfileInstallsAndChecksTheBoundedToolBaseline(t *testing.T) {
	root := repoRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "docker", "tsecbench-hosted", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Hosted Image Dockerfile: %v", err)
	}
	dockerfile := string(contents)

	for _, required := range []string{
		"@earendil-works/pi-coding-agent@latest",
		"@tintinweb/pi-subagents@latest",
		"pi-subagents@latest",
		"@openai/codex@latest",
		"@anthropic-ai/claude-code@latest",
		"agent-browser@latest",
		"python3-pwntools",
		"python3-pil",
		"tesseract-ocr",
		"SYSTEMD_OFFLINE=1",
		"default-jre-headless",
		"jadx",
		"apktool",
		"bsdextrautils",
		"chromium",
		"golang-go",
		"build-essential",
		"gdb",
		"radare2",
		"strace",
		"ltrace",
		"patchelf",
		"checksec",
		"nmap",
		"netcat-openbsd",
		"socat",
		"dnsutils",
		"iproute2",
		"iputils-ping",
		"openssh-client",
		"openssl",
		"ARG RUNTIME_RELEASE_CACHE_BUST",
		`test -n "${RUNTIME_RELEASE_CACHE_BUST}"`,
	} {
		assertContains(t, dockerfile, required)
	}

	for _, executable := range []string{
		"pi", "codex", "claude", "bash", "git", "curl", "jq", "rg",
		"python3", "go", "gcc", "g++", "make", "gdb", "radare2", "strace",
		"ltrace", "patchelf", "checksec", "nmap", "nc", "socat", "dig", "ip",
		"ss", "ping", "ssh", "openssl", "chromium", "agent-browser", "tesseract", "java", "jadx", "apktool", "column",
		"pentest-provider-bridge", "pentest-claude-sdk-bridge", "pentest-tsecbench-hosted",
		"pentest-tsecbench-client",
	} {
		assertContains(t, dockerfile, `command -v `+executable)
	}
	assertContains(t, dockerfile, `python3 -c 'import pwn; from PIL import Image'`)
	if strings.Contains(dockerfile, "pip3 install --no-cache-dir --break-system-packages pwntools") {
		t.Fatal("Hosted Image must use Kali python3-pwntools instead of building unicorn from source on Python 3.14")
	}

	for _, excluded := range []string{
		"kali-linux-headless", "ghidra", "android-sdk", "seclists",
		"docker.io", "docker-ce", "docker-cli", "podman", "openvpn", "wireguard", "tunneling",
		"/var/run/docker.sock", "--privileged", "--cap-add", "NET_ADMIN", "/dev/net/tun",
	} {
		if strings.Contains(strings.ToLower(dockerfile), strings.ToLower(excluded)) {
			t.Fatalf("Hosted Image Dockerfile includes excluded content %q", excluded)
		}
	}
}

func TestTSecBenchHostedImageSmokeWhenAnImageIsConfigured(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(hostedImageEnvironment))
	if image == "" {
		t.Skip(hostedImageEnvironment + " is not set; static Hosted Image tests still run")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is unavailable")
	}
	if err := exec.Command(docker, "info").Run(); err != nil {
		t.Skip("Docker Engine is unavailable")
	}

	inspect := func(format string) string {
		t.Helper()
		output, err := exec.Command(docker, "image", "inspect", "--format", format, image).CombinedOutput()
		if err != nil {
			t.Fatalf("inspect Hosted Image: %v: %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if got := inspect("{{.Architecture}}"); got != "amd64" {
		t.Fatalf("Hosted Image architecture = %q, want amd64", got)
	}
	if got := inspect("{{json .Config.ExposedPorts}}"); got != "null" {
		t.Fatalf("Hosted Image exposed ports = %s, want null", got)
	}
	if got := inspect("{{.Config.User}}"); got != "root" {
		t.Fatalf("Hosted Image user = %q, want root", got)
	}

	smoke := `set -eu
test "$(id -u)" = 0
	for command in pi codex claude bash git curl jq rg python3 go gcc g++ make gdb radare2 strace ltrace patchelf checksec nmap nc socat dig ip ss ping ssh openssl chromium agent-browser tesseract java jadx apktool column pentest-provider-bridge pentest-claude-sdk-bridge pentest-tsecbench-hosted pentest-tsecbench-client; do
  command -v "$command" >/dev/null
done
python3 -c 'import pwn; from PIL import Image'
test -s /opt/cyberpenda/runtime-versions.json
`
	output, err := exec.Command(docker, "run", "--rm", "--network", "none", "--entrypoint", "sh", image, "-c", smoke).CombinedOutput()
	if err != nil {
		t.Fatalf("smoke Hosted Image without added capabilities: %v: %s", err, output)
	}

	output, err = exec.Command(docker, "run", "--rm", "--network", "none", "--entrypoint", "cat", image, "/opt/cyberpenda/runtime-versions.json").CombinedOutput()
	if err != nil {
		t.Fatalf("read Runtime inventory: %v: %s", err, output)
	}
	var inventory struct {
		Schema   string `json:"schema"`
		Runtimes map[string]struct {
			Package string `json:"package"`
			Version string `json:"version"`
			Binary  string `json:"binary"`
		} `json:"runtimes"`
		Components map[string]struct {
			Package string `json:"package"`
			Version string `json:"version"`
		} `json:"components"`
	}
	if err := json.Unmarshal(output, &inventory); err != nil {
		t.Fatalf("decode Runtime inventory: %v: %s", err, output)
	}
	if inventory.Schema != "cyberpenda-hosted-runtime-versions/v1" {
		t.Fatalf("Runtime inventory schema = %q", inventory.Schema)
	}
	for _, runtimeName := range []string{"pi", "codex", "claude_code"} {
		entry, ok := inventory.Runtimes[runtimeName]
		if !ok || entry.Package == "" || entry.Version == "" || entry.Binary == "" {
			t.Fatalf("Runtime inventory %s = %#v", runtimeName, entry)
		}
	}
	sdk, ok := inventory.Components["claude_agent_sdk"]
	if !ok || sdk.Package != "@anthropic-ai/claude-agent-sdk" || sdk.Version == "" {
		t.Fatalf("Runtime inventory claude_agent_sdk = %#v", sdk)
	}
}
