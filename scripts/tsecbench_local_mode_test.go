package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const localModeImage = "cyberpenda-tsecbench-hosted:local"

func TestTSecBenchLocalModeRunnerUsesTheHostedImageAndHostNetwork(t *testing.T) {
	root := repoRoot(t)
	temp := t.TempDir()
	envFile := filepath.Join(temp, "tsecbench.env")
	secret := "one-use-secret-value"
	writeFileMode(t, envFile, "BENCHMARK_BASE_URL=https://example.invalid\nBENCHMARK_TOKEN="+secret+"\n", 0o600)

	arguments := filepath.Join(temp, "container-arguments")
	fakeCLI := filepath.Join(temp, "fake-container-cli")
	writeFileMode(t, fakeCLI, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FAKE_CONTAINER_ARGUMENTS\"\n", 0o755)

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "validate-tsecbench-local-mode.sh"),
		"--env-file", envFile,
		"--container-cli", fakeCLI,
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "FAKE_CONTAINER_ARGUMENTS="+arguments)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run local-mode validation: %v\n%s", err, output)
	}
	got, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatalf("read fake container arguments: %v", err)
	}
	want := strings.Join([]string{
		"run", "--rm", "--network", "host", "--env-file", envFile,
		"--entrypoint", "/usr/local/bin/tsecbench-local-validate", localModeImage, "",
	}, "\n")
	if string(got) != want {
		t.Fatalf("container arguments:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(string(output), secret) || strings.Contains(string(got), secret) {
		t.Fatal("runner disclosed BENCHMARK_TOKEN")
	}
	for _, forbidden := range []string{"--privileged", "--cap-add", "NET_ADMIN", "/dev/net/tun", "/var/run/docker.sock"} {
		if strings.Contains(string(got), forbidden) {
			t.Fatalf("container arguments request forbidden access %q", forbidden)
		}
	}
}

func TestTSecBenchLocalModeRunnerRejectsUnsafeSecretFilesBeforeContainerStart(t *testing.T) {
	root := repoRoot(t)
	for _, test := range []struct {
		name     string
		contents string
		mode     os.FileMode
		link     bool
	}{
		{name: "group readable", contents: "BENCHMARK_BASE_URL=https://example.invalid\nBENCHMARK_TOKEN=secret-group\n", mode: 0o640},
		{name: "missing token", contents: "BENCHMARK_BASE_URL=https://example.invalid\n", mode: 0o600},
		{name: "duplicate token", contents: "BENCHMARK_BASE_URL=https://example.invalid\nBENCHMARK_TOKEN=first\nBENCHMARK_TOKEN=second\n", mode: 0o600},
		{name: "symbolic link", contents: "BENCHMARK_BASE_URL=https://example.invalid\nBENCHMARK_TOKEN=secret-link\n", mode: 0o600, link: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			temp := t.TempDir()
			realEnv := filepath.Join(temp, "real.env")
			writeFileMode(t, realEnv, test.contents, test.mode)
			envFile := realEnv
			if test.link {
				envFile = filepath.Join(temp, "linked.env")
				if err := os.Symlink(realEnv, envFile); err != nil {
					t.Fatalf("create env-file symlink: %v", err)
				}
			}
			marker := filepath.Join(temp, "container-started")
			fakeCLI := filepath.Join(temp, "fake-container-cli")
			writeFileMode(t, fakeCLI, "#!/bin/sh\ntouch \"$FAKE_CONTAINER_MARKER\"\n", 0o755)
			cmd := exec.Command("bash", filepath.Join(root, "scripts", "validate-tsecbench-local-mode.sh"),
				"--env-file", envFile, "--container-cli", fakeCLI)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "FAKE_CONTAINER_MARKER="+marker)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("unsafe env file succeeded:\n%s", output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("container started before env-file rejection: %v", err)
			}
			for _, secret := range []string{"secret-group", "secret-link", "first", "second"} {
				if strings.Contains(string(output), secret) {
					t.Fatalf("error disclosed Secret %q: %s", secret, output)
				}
			}
		})
	}
}

func TestTSecBenchLocalModeRunnerRejectsSecretCommandLineOptions(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "validate-tsecbench-local-mode.sh"),
		"--token", "must-not-appear")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("Secret command-line option succeeded:\n%s", output)
	}
	if strings.Contains(string(output), "must-not-appear") {
		t.Fatalf("error disclosed command-line Secret: %s", output)
	}
}

func TestTSecBenchLocalModeValidationDoesNotConfigureVPNOrPrintSecrets(t *testing.T) {
	root := repoRoot(t)
	for _, relative := range []string{
		"scripts/validate-tsecbench-local-mode.sh",
		"docker/tsecbench-hosted/tsecbench-local-validate.py",
	} {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		text := strings.ToLower(string(contents))
		for _, forbidden := range []string{
			"set -x", "printenv", "openvpn", "wireguard", "/dev/net/tun",
			"net_admin", "--privileged", "/var/run/docker.sock",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s includes forbidden local-mode behavior %q", relative, forbidden)
			}
		}
	}
}

func TestTSecBenchLocalModeEnvironmentTemplateIsSecretFree(t *testing.T) {
	root := repoRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "docs", "tsecbench", "tsecbench-local.env.example"))
	if err != nil {
		t.Fatalf("read local-mode environment template: %v", err)
	}
	if string(contents) != "BENCHMARK_BASE_URL=\nBENCHMARK_TOKEN=\n" {
		t.Fatalf("local-mode environment template = %q", contents)
	}
}

func TestTSecBenchHostedImageInstallsTheLocalModeValidator(t *testing.T) {
	root := repoRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "docker", "tsecbench-hosted", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Hosted Image Dockerfile: %v", err)
	}
	for _, required := range []string{
		"COPY docker/tsecbench-hosted/tsecbench-local-validate.py /usr/local/bin/tsecbench-local-validate",
		"chmod 0755 /usr/local/bin/tsecbench-local-validate",
		"command -v tsecbench-local-validate",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("Hosted Image Dockerfile does not contain %q", required)
		}
	}
}

func writeFileMode(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
