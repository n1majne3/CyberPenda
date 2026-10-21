package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDispatchCLIRegressions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("平台边界样本使用 POSIX shell")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("调度器回归测试需要 python3")
	}
	for _, source := range []string{"internal/hostedcontroller/assets", "internal/skill/builtins/assets"} {
		t.Run(source, func(t *testing.T) {
			path, err := filepath.Abs(filepath.Join("..", source, "ctf-orchestrator/scripts/dispatch.py"))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(python, "dispatch_regression_test.py")
			cmd.Env = append(os.Environ(), "DISPATCH_SCRIPT="+path)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("调度器行为验证失败: %v\n%s", err, output)
			}
		})
	}
}

func TestHostedPiControlProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Hosted Pi uses a Unix socket")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Hosted Pi control tests require Node.js")
	}
	cmd := exec.Command(node, "--test", "hosted_pi_control_test.mjs")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Hosted Pi control failed: %v\n%s", err, output)
	}
}
