package runner_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestBuildSandboxCommandMountsPentestDirectoryReadOnlyWithoutSnapshotInodeMounts(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-v2-directory-mount", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(layout.Workdir, ".pentest"), 0o700); err != nil {
		t.Fatalf("prepare .pentest: %v", err)
	}
	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout: layout, Provider: runtimeprofile.ProviderCodex, RuntimeCommand: []string{"codex", "exec"},
		ReadOnlyTaskDirs: []string{"workdir/.pentest"},
	})
	if err != nil {
		t.Fatalf("build sandbox command: %v", err)
	}
	joined := strings.Join(command.Args, " ")
	if !strings.Contains(joined, "src="+filepath.Join(layout.Workdir, ".pentest")+",dst=/task/workdir/.pentest,readonly") {
		t.Fatalf("approved .pentest parent is not mounted read-only: %v", command.Args)
	}
	for _, forbidden := range []string{"src=" + filepath.Join(layout.Workdir, ".pentest", "blackboard.json"), "src=" + filepath.Join(layout.Workdir, ".pentest", "scope.json")} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("sandbox command bind-mounted a replaceable file inode %q: %v", forbidden, command.Args)
		}
	}
}

func TestBuildSandboxCommandRejectsEscapingOrSymlinkedReadOnlyDirectory(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-v2-confined-mount", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	for _, relative := range []string{"../outside", "/absolute"} {
		_, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
			Layout: layout, Provider: runtimeprofile.ProviderCodex, RuntimeCommand: []string{"codex"},
			ReadOnlyTaskDirs: []string{relative},
		})
		if err == nil {
			t.Fatalf("accepted escaping read-only directory %q", relative)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(layout.Workdir, ".pentest")); err != nil {
		t.Fatalf("create .pentest symlink: %v", err)
	}
	_, err = runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout: layout, Provider: runtimeprofile.ProviderCodex, RuntimeCommand: []string{"codex"},
		ReadOnlyTaskDirs: []string{"workdir/.pentest"},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic") {
		t.Fatalf("symlinked .pentest mount error = %v", err)
	}
}

func TestDockerPentestDirectoryMountSeesAtomicSnapshotReplacement(t *testing.T) {
	docker, image := localDockerShellImage(t)
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-v2-docker-reread", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	pentestDir := filepath.Join(layout.Workdir, ".pentest")
	if err := os.MkdirAll(pentestDir, 0o700); err != nil {
		t.Fatalf("prepare .pentest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pentestDir, "blackboard.json"), []byte(`{"revision":1}`), 0o600); err != nil {
		t.Fatalf("write initial Snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pentestDir, "scope.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write Scope: %v", err)
	}
	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout: layout, Provider: runtimeprofile.ProviderCodex, ContainerCLI: docker, Image: image,
		ReadOnlyTaskDirs: []string{"workdir/.pentest"},
		RuntimeCommand: []string{"sh", "-c", `set -eu
test "$(find /task/workdir/.pentest -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d ' ')" = 2
if touch /task/workdir/.pentest/must-not-write 2>/dev/null; then exit 23; fi
while ! grep -q '"revision":2' /task/workdir/.pentest/blackboard.json; do sleep 0.05; done
cat /task/workdir/.pentest/blackboard.json`},
	})
	if err != nil {
		t.Fatalf("build Docker command: %v", err)
	}
	created, err := exec.Command(command.Program, command.Args...).CombinedOutput()
	if err != nil {
		t.Fatalf("create Docker mount conformance container: %v: %s", err, created)
	}
	containerID := strings.TrimSpace(string(created))
	t.Cleanup(func() { _ = exec.Command(docker, "rm", "-f", containerID).Run() })

	var output bytes.Buffer
	start := exec.Command(docker, "start", "-a", containerID)
	start.Stdout = &output
	start.Stderr = &output
	if err := start.Start(); err != nil {
		t.Fatalf("start Docker mount conformance container: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := replaceTestFileAtomically(pentestDir, "blackboard.json", []byte(`{"revision":2}`)); err != nil {
		t.Fatalf("atomically replace host Snapshot: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- start.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Docker mount conformance failed: %v: %s", err, output.String())
		}
	case <-time.After(8 * time.Second):
		_ = exec.Command(docker, "rm", "-f", containerID).Run()
		t.Fatalf("sandbox kept a stale Snapshot inode after atomic replacement: %s", output.String())
	}
	if !strings.Contains(output.String(), `{"revision":2}`) {
		t.Fatalf("sandbox did not reread replacement bytes: %s", output.String())
	}
}

func localDockerShellImage(t *testing.T) (string, string) {
	t.Helper()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker namespace conformance unavailable: docker CLI is not installed")
	}
	if output, err := exec.Command(docker, "info").CombinedOutput(); err != nil {
		t.Skipf("Docker namespace conformance unavailable: daemon is not reachable: %v: %s", err, output)
	}
	for _, image := range []string{"alpine:3.20", "alpine:latest", "busybox:latest", "kalilinux/kali-rolling"} {
		if exec.Command(docker, "image", "inspect", image).Run() == nil {
			return docker, image
		}
	}
	t.Skip("Docker namespace conformance unavailable: no local shell-capable test image")
	return "", ""
}

func replaceTestFileAtomically(dir, name string, data []byte) error {
	temp, err := os.CreateTemp(dir, ".replacement-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, filepath.Join(dir, name))
}
