package scripts_test

import (
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTSecBenchHostedBundleExportsAndVerifiesUploadArchive(t *testing.T) {
	fixture := newBundleFixture(t)
	output, err := fixture.run("v1.2.3", "cyberpenda-tsecbench-hosted:local")
	if err != nil {
		t.Fatalf("bundle failed: %v\n%s", err, output)
	}
	bundle := filepath.Join(fixture.dist, "cyberpenda-tsecbench-hosted_v1.2.3_linux_amd64")
	archive := filepath.Join(bundle, "cyberpenda-tsecbench-hosted_v1.2.3_linux_amd64.tar.gz")
	for _, name := range []string{
		filepath.Base(archive), "SHA256SUMS", "COMPONENTS.txt", "tsecbench.env.example",
		"tsecbench-local.env.example", "run-tsecbench-local-mode.sh", "README.md", "TROUBLESHOOTING.md",
	} {
		if _, err := os.Stat(filepath.Join(bundle, name)); err != nil {
			t.Fatalf("bundle missing %s: %v", name, err)
		}
	}
	compressed, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = compressed.Close()
	if string(payload) != "fake docker archive\n" {
		t.Fatalf("archive payload = %q", payload)
	}
	logBytes, _ := os.ReadFile(fixture.log)
	log := string(logBytes)
	if strings.Index(log, "make test-ci") < 0 || strings.Index(log, "docker save") < strings.Index(log, "make test-ci") {
		t.Fatalf("acceptance did not run before docker save:\n%s", log)
	}
	for _, want := range []string{"make smoke-tsecbench-hosted-image", "docker image inspect", "docker run", "docker save"} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q:\n%s", want, log)
		}
	}
	checksum := exec.Command("shasum", "-a", "256", "-c", "SHA256SUMS")
	checksum.Dir = bundle
	if result, err := checksum.CombinedOutput(); err != nil {
		t.Fatalf("checksum cannot be verified: %v: %s", err, result)
	}
	components, _ := os.ReadFile(filepath.Join(bundle, "COMPONENTS.txt"))
	for _, want := range []string{"cyberpenda-hosted-runtime-versions/v1", `"pi"`, `"codex"`, `"claude_code"`, `"claude_agent_sdk"`, `"@anthropic-ai/claude-agent-sdk"`, "python3=", "openssl=", "chromium="} {
		if !strings.Contains(string(components), want) {
			t.Fatalf("component inventory missing %q:\n%s", want, components)
		}
	}
	wantRunner, _ := os.ReadFile(fixture.runner)
	gotRunner, _ := os.ReadFile(filepath.Join(bundle, "run-tsecbench-local-mode.sh"))
	if string(gotRunner) != string(wantRunner) {
		t.Fatal("bundle changed the #216 local-mode runner")
	}
	if info, _ := os.Stat(filepath.Join(bundle, "run-tsecbench-local-mode.sh")); info.Mode().Perm()&0o111 == 0 {
		t.Fatal("bundled local-mode runner is not executable")
	}
	assertSecretFreeTemplate(t, filepath.Join(bundle, "tsecbench.env.example"))
	assertSecretFreeTemplate(t, filepath.Join(bundle, "tsecbench-local.env.example"))
	for _, document := range []string{"README.md", "TROUBLESHOOTING.md"} {
		contents, _ := os.ReadFile(filepath.Join(bundle, document))
		for _, want := range map[string][]string{
			"README.md": {
				".tsecbench.gw", "openai_chat_completions", "openai_responses", "anthropic_messages",
				"does not start a VPN", "no public Internet", "formal score", "--env-file",
				"Before you upload", "successful Local Mode validation",
			},
			"TROUBLESHOOTING.md": {
				"Bootstrap validation", "Runtime and protocol mismatch", "Runtime fails", "Challenge Platform API",
				"JSONL Hosted Transcript Stream", "VPN", "3 GB",
			},
		}[document] {
			if !strings.Contains(string(contents), want) {
				t.Fatalf("%s missing %q", document, want)
			}
		}
	}
}

func TestTSecBenchHostedBundleRejectsTheExactSizeBoundaryWithoutPublishing(t *testing.T) {
	fixture := newBundleFixture(t)
	fixture.extraEnv = append(fixture.extraEnv, "TSECBENCH_MAX_ARCHIVE_BYTES=40")
	output, err := fixture.run("v1", "image:test")
	if err == nil || !strings.Contains(string(output), "must be smaller than") {
		t.Fatalf("size gate error=%v output=%s", err, output)
	}
	entries, _ := os.ReadDir(fixture.dist)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "cyberpenda-tsecbench-hosted_") {
			t.Fatalf("failed bundle published %s", entry.Name())
		}
	}
}

func TestTSecBenchHostedBundleFailsClosedOnImageAndInventoryErrors(t *testing.T) {
	tests := []struct {
		name, env, want string
	}{
		{"wrong architecture", "FAKE_DOCKER_PLATFORM=linux/arm64", "linux/amd64"},
		{"missing Runtime", "FAKE_DOCKER_INVENTORY=missing", "Runtime inventory is missing"},
		{"missing Claude Agent SDK", "FAKE_DOCKER_INVENTORY=missing_sdk", "Runtime inventory is missing claude_agent_sdk"},
		{"save failure", "FAKE_DOCKER_SAVE_FAIL=1", "export Hosted Image"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBundleFixture(t)
			fixture.extraEnv = append(fixture.extraEnv, test.env)
			output, err := fixture.run("v1", "image:test")
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("error=%v output=%s want=%q", err, output, test.want)
			}
		})
	}
}

func TestTSecBenchHostedBundleRejectsUnsafeOutputAndExistingBundle(t *testing.T) {
	fixture := newBundleFixture(t)
	outside := t.TempDir()
	fixture.dist = outside
	output, err := fixture.run("v1", "image:test")
	if err == nil || !strings.Contains(string(output), "must stay inside the repository") {
		t.Fatalf("outside output error=%v output=%s", err, output)
	}

	fixture = newBundleFixture(t)
	final := filepath.Join(fixture.dist, "cyberpenda-tsecbench-hosted_v1_linux_amd64")
	if err := os.MkdirAll(final, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(final, "keep")
	_ = os.WriteFile(marker, []byte("keep"), 0o600)
	output, err = fixture.run("v1", "image:test")
	if err == nil || !strings.Contains(string(output), "already exists") {
		t.Fatalf("existing output error=%v output=%s", err, output)
	}
	if content, _ := os.ReadFile(marker); string(content) != "keep" {
		t.Fatal("existing bundle was modified")
	}
}

type bundleFixture struct {
	repoRoot, dist, bin, log, runner, localEnv string
	extraEnv                                   []string
}

func newBundleFixture(t *testing.T) *bundleFixture {
	t.Helper()
	repo := repoRoot(t)
	testRoot, err := os.MkdirTemp(repo, ".tsecbench-bundle-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	fixture := &bundleFixture{
		repoRoot: repo, dist: filepath.Join(testRoot, "dist"), bin: filepath.Join(testRoot, "bin"),
		log: filepath.Join(testRoot, "commands.log"), runner: filepath.Join(testRoot, "validate-local.sh"),
		localEnv: filepath.Join(testRoot, "tsecbench-local.env.example"),
	}
	_ = os.MkdirAll(fixture.dist, 0o755)
	_ = os.MkdirAll(fixture.bin, 0o755)
	_ = os.WriteFile(fixture.runner, []byte("#!/usr/bin/env bash\necho local validation\n"), 0o755)
	_ = os.WriteFile(fixture.localEnv, []byte("BENCHMARK_BASE_URL=\nBENCHMARK_TOKEN=\nTSECBENCH_CHALLENGE_UNIQUE_CODE=\nTSECBENCH_TEST_FLAG=\n"), 0o644)
	writeExecutable(t, filepath.Join(fixture.bin, "make"), `#!/usr/bin/env bash
printf 'make %s\n' "$*" >> "$FAKE_COMMAND_LOG"
`)
	writeExecutable(t, filepath.Join(fixture.bin, "docker"), `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$FAKE_COMMAND_LOG"
case "${1:-} ${2:-}" in
  "image inspect")
    if [[ "$*" == *"{{.Os}}/{{.Architecture}}"* ]]; then printf '%s\n' "${FAKE_DOCKER_PLATFORM:-linux/amd64}"; else printf 'sha256:fake-image-id\n'; fi
    ;;
  "run --rm")
    if [[ "$*" == *"--entrypoint cat"* ]]; then
      if [[ "${FAKE_DOCKER_INVENTORY:-}" = missing ]]; then
        printf '%s\n' '{"schema":"cyberpenda-hosted-runtime-versions/v1","runtimes":{"pi":{"version":"1"},"codex":{"version":"2"}}}'
	  elif [[ "${FAKE_DOCKER_INVENTORY:-}" = missing_sdk ]]; then
		printf '%s\n' '{"schema":"cyberpenda-hosted-runtime-versions/v1","runtimes":{"pi":{"package":"pi","version":"1","binary":"pi"},"codex":{"package":"codex","version":"2","binary":"codex"},"claude_code":{"package":"claude","version":"3","binary":"claude"}}}'
      else
		printf '%s\n' '{"schema":"cyberpenda-hosted-runtime-versions/v1","runtimes":{"pi":{"package":"pi","version":"1","binary":"pi"},"codex":{"package":"codex","version":"2","binary":"codex"},"claude_code":{"package":"claude","version":"3","binary":"claude"}},"components":{"claude_agent_sdk":{"package":"@anthropic-ai/claude-agent-sdk","version":"0.3.220"}}}'
      fi
    else
      printf '%s\n' 'kali=rolling' 'python3=3.13' 'go=1.25' 'gcc=14' 'gdb=16' 'binutils=2.45' 'nmap=7.95' 'openssl=3.5' 'chromium=140' 'agent_browser=0.1' 'pwntools=4.14'
    fi
    ;;
  save\ *)
    if [[ "${FAKE_DOCKER_SAVE_FAIL:-}" = 1 ]]; then exit 19; fi
    printf 'fake docker archive\n'
    ;;
  *) printf 'unexpected fake docker command: %s\n' "$*" >&2; exit 23 ;;
esac
`)
	return fixture
}

func (fixture *bundleFixture) run(version, image string) ([]byte, error) {
	script := filepath.Join(fixture.repoRoot, "scripts", "build-tsecbench-hosted-bundle.sh")
	command := exec.Command("bash", script, version, image, fixture.dist)
	command.Dir = fixture.repoRoot
	command.Env = append(os.Environ(),
		"PATH="+fixture.bin+":"+os.Getenv("PATH"), "FAKE_COMMAND_LOG="+fixture.log,
		"TSECBENCH_LOCAL_RUNNER_SOURCE="+fixture.runner,
		"TSECBENCH_LOCAL_ENV_TEMPLATE_SOURCE="+fixture.localEnv,
	)
	command.Env = append(command.Env, fixture.extraEnv...)
	return command.CombinedOutput()
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertSecretFreeTemplate(t *testing.T, path string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.Contains(key, "KEY") || strings.Contains(key, "TOKEN") {
			if strings.TrimSpace(value) != "" {
				t.Fatalf("template %s contains a Secret value for %s", path, key)
			}
		}
	}
}
