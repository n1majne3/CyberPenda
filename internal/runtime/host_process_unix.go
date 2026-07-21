//go:build unix

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// KillHostProcessGroup terminates every process in the group. Used by live
// bridge Close and by daemon-restart reconciliation of durable host metadata.
func KillHostProcessGroup(ctx context.Context, pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid host process group id")
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(-pgid, 0); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return ctx.Err()
		case <-timer.C:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return nil
		case <-ticker.C:
		}
	}
}

// Start launches a host process with Setpgid so Close can signal the entire
// descendant tree.
func (LocalHostProcessStarter) Start(_ context.Context, spec HostProcessSpec) (HostProcessHandle, error) {
	if strings.TrimSpace(spec.Program) == "" {
		return HostProcessHandle{}, fmt.Errorf("host process program is required")
	}
	cmd := exec.Command(spec.Program, spec.Args...)
	cmd.Dir = spec.Workdir
	cmd.Env = os.Environ()
	for key, value := range spec.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return HostProcessHandle{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return HostProcessHandle{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return HostProcessHandle{}, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return HostProcessHandle{}, err
	}
	pgid := cmd.Process.Pid
	if actual, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		pgid = actual
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitOnce sync.Once
	var waitErr error
	wait := func() error {
		waitOnce.Do(func() {
			waitErr = <-waitCh
			// Root exit must still reap descendants that may outlive the leader.
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		})
		return waitErr
	}
	killGroup := func(ctx context.Context) error {
		// Best-effort graceful stop, then force the whole group.
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = wait()
			close(done)
		}()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
			return ctx.Err()
		case <-timer.C:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
			return nil
		}
	}
	return HostProcessHandle{
		IO: SandboxBridgeIO{
			Stdin: stdin, Stdout: stdout, Diagnostics: stderr, Wait: wait,
		},
		ProcessGroupID:   pgid,
		KillProcessGroup: killGroup,
	}, nil
}
