package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDockerRemovalWaitsForConcurrentRemovalToFinish(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "attempt")
	cli := removalRetryFixture(t, "if [ ! -f "+strconv.Quote(count)+" ]; then\n"+
		"  touch "+strconv.Quote(count)+"\n"+
		"  echo 'Error response from daemon: removal of container ctr is already in progress' >&2\n"+
		"  exit 1\nfi\n"+
		"echo 'Error: No such container: ctr' >&2\nexit 1\n")
	if err := RemoveDockerContainer(cli, "ctr"); err != nil {
		t.Fatalf("concurrent removal blocked cleanup: %v", err)
	}
}

func TestDockerBridgeRemovalCleansContainerAfterCanceledSetup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cli := removalRetryFixture(t, "touch "+strconv.Quote(marker)+"\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := (DockerCLISandboxBridgeDocker{ContainerCLI: cli}).Remove(ctx, "ctr")
	if err != nil {
		t.Fatalf("canceled setup prevented owned container cleanup: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("canceled setup left its container without a removal attempt: %v", err)
	}
}

func TestDockerRemovalRetainsBusyErrorAtDeadline(t *testing.T) {
	cli := removalRetryFixture(t, "echo 'Error response from daemon: removal of container ctr is already in progress' >&2\nexit 1\n")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := removeDockerContainer(ctx, cli, "ctr")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("cleanup must fail with its deadline and last Docker error: %v", err)
	}
}

func TestDockerConcurrentRemovalWithRealEngine(t *testing.T) {
	image := os.Getenv("CYBERPENDA_DOCKER_CLEANUP_TEST_IMAGE")
	if image == "" {
		t.Skip("set CYBERPENDA_DOCKER_CLEANUP_TEST_IMAGE to an installed Sandbox image")
	}
	for trial := range 8 {
		t.Run(strconv.Itoa(trial), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "docker", "create", "--network", "none", "--entrypoint", "/bin/sh", image, "-c", "sleep 30").CombinedOutput()
			if err != nil {
				t.Fatalf("create isolated container: %v: %s", err, output)
			}
			id := strings.TrimSpace(string(output))
			defer func() {
				cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
				defer done()
				if err := removeDockerContainer(cleanup, "docker", id); err != nil {
					t.Errorf("clean up owned container: %v", err)
				}
			}()
			if output, err := exec.CommandContext(ctx, "docker", "start", id).CombinedOutput(); err != nil {
				t.Fatalf("start isolated container: %v: %s", err, output)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var workers sync.WaitGroup
			for index := range 2 {
				workers.Go(func() {
					<-start
					requestContext := ctx
					if index == 0 {
						var cancelRequest context.CancelFunc
						requestContext, cancelRequest = context.WithCancel(ctx)
						cancelRequest()
					}
					results <- (DockerCLISandboxBridgeDocker{}).Remove(requestContext, id)
				})
			}
			close(start)
			workers.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Errorf("concurrent cleanup: %v", err)
				}
			}
			output, err = exec.CommandContext(ctx, "docker", "inspect", id).CombinedOutput()
			if detail := strings.ToLower(string(output)); err == nil || (!strings.Contains(detail, "no such object") && !strings.Contains(detail, "no such container")) {
				t.Fatalf("removal not confirmed: %v: %s", err, output)
			}
			t.Logf("both cleanup calls confirmed absence of %s", id)
		})
	}
}

func removalRetryFixture(t *testing.T, script string) string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("requires a POSIX process fixture")
	}
	cli := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return cli
}
