package runtime_test

import (
	"fmt"
	"os"
	"runtime"
	"testing"
)

const alwaysRunningContainerCLIEnv = "CYBERPENDA_TEST_CONTAINER_ALWAYS_RUNNING"

func TestMain(m *testing.M) {
	if os.Getenv(alwaysRunningContainerCLIEnv) == "1" {
		fmt.Println("true")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// skipUnlessPOSIXProcessDoubles skips tests whose process doubles (fake docker
// CLI, fake runtime CLIs, bridge programs) are #!/bin/sh scripts. Windows
// cannot execute extension-less shell scripts, so these doubles exist only on
// POSIX platforms; the daemon code paths they exercise are GOOS-independent.
func skipUnlessPOSIXProcessDoubles(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process double is a #!/bin/sh script; not executable on Windows")
	}
}
