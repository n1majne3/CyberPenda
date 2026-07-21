//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// KillHostProcessGroup terminates the host process identified by pgid.
// Windows stores the root process PID in ProcessGroupID; full process-tree
// reaping requires Job Objects and is not implemented here.
func KillHostProcessGroup(ctx context.Context, pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid host process group id")
	}
	proc, err := os.FindProcess(pgid)
	if err != nil {
		return nil
	}
	if err := proc.Kill(); err != nil {
		// Process may already have exited.
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// Start launches a host process. ProcessGroupID is the root PID; Close kills
// that process rather than a Unix process group.
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
	pid := cmd.Process.Pid
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitOnce sync.Once
	var waitErr error
	wait := func() error {
		waitOnce.Do(func() {
			waitErr = <-waitCh
		})
		return waitErr
	}
	killGroup := func(ctx context.Context) error {
		_ = cmd.Process.Kill()
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
			_ = cmd.Process.Kill()
			<-done
			return ctx.Err()
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-done
			return nil
		}
	}
	return HostProcessHandle{
		IO: SandboxBridgeIO{
			Stdin: stdin, Stdout: stdout, Diagnostics: stderr, Wait: wait,
		},
		ProcessGroupID:   pid,
		KillProcessGroup: killGroup,
	}, nil
}
