package integration_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithPentestdLiveStartsDaemonForCommand(t *testing.T) {
	root := repoRoot(t)
	port := freeTCPPort(t)
	daemonURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	marker := filepath.Join(t.TempDir(), "health.json")

	cmd := exec.Command(
		"bash",
		filepath.Join(root, "scripts", "with-pentestd-live.sh"),
		"bash",
		"-c",
		`curl -sf "$PENTEST_DAEMON_URL/health" > "$PENTEST_SMOKE_MARKER"`,
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PENTEST_DAEMON_URL="+daemonURL,
		fmt.Sprintf("PENTEST_LISTEN_ADDR=127.0.0.1:%d", port),
		"PENTEST_DB="+filepath.Join(t.TempDir(), "pentest.db"),
		"PENTEST_RUNTIME_ROOT="+filepath.Join(t.TempDir(), "runs"),
		"PENTEST_DAEMON_LOG="+filepath.Join(t.TempDir(), "pentestd.log"),
		"PENTEST_SMOKE_MARKER="+marker,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("with-pentestd-live.sh failed: %v\n%s", err, output)
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("expected child command to write health marker: %v", err)
	}
	if !strings.Contains(string(body), `"status":`) {
		t.Fatalf("expected daemon health JSON in marker, got %q", body)
	}
}

func TestGitHubSmokeWorkflowsRunWithDaemonWrapper(t *testing.T) {
	root := repoRoot(t)
	assertFileContains(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "scripts/with-pentestd-live.sh make smoke-sandbox-mcp")
	assertFileContains(t, filepath.Join(root, ".github", "workflows", "smoke-runtime-nightly.yml"), "scripts/with-pentestd-live.sh make smoke-runtime-tasks")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free tcp port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func assertFileContains(t *testing.T, path string, needle string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(body), needle) {
		t.Fatalf("%s does not contain %q", path, needle)
	}
}
