package runtime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// DockerCLISandboxBridgeDocker is the production Docker CLI transport. The
// attached command uses -a and -i only; no API exposes attach or exec above
// this daemon-owned lifecycle seam.
type DockerCLISandboxBridgeDocker struct {
	ContainerCLI string
}

func (d DockerCLISandboxBridgeDocker) cli() string {
	if cli := strings.TrimSpace(d.ContainerCLI); cli != "" {
		return cli
	}
	return "docker"
}

func (d DockerCLISandboxBridgeDocker) Create(ctx context.Context, args []string) (string, error) {
	out, err := exec.CommandContext(ctx, d.cli(), args...).Output()
	if err != nil {
		return "", dockerExitError(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// dockerExitError enriches a failed docker CLI error with the captured stderr
// so the daemon log shows the real reason ("No such image", "network not
// found", daemon connectivity) instead of a bare "exit status 1". The original
// *exec.ExitError stays in the unwrap chain for errors.Is/As callers.
func dockerExitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if detail := strings.TrimSpace(string(exitErr.Stderr)); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
	}
	return err
}

func (d DockerCLISandboxBridgeDocker) Start(_ context.Context, containerID string) (SandboxBridgeIO, error) {
	// The attach process is Task-owned and must outlive any request or
	// Continuation context passed to Start. Close owns its termination.
	cmd := exec.Command(d.cli(), DockerSandboxBridgeStartArgs(containerID)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return SandboxBridgeIO{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return SandboxBridgeIO{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return SandboxBridgeIO{}, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return SandboxBridgeIO{}, err
	}
	return SandboxBridgeIO{Stdin: stdin, Stdout: stdout, Diagnostics: stderr, Wait: cmd.Wait}, nil
}

// DockerSandboxBridgeStartArgs exposes the security-sensitive attach argv to
// regression tests. It deliberately contains interactive attach but no TTY.
func DockerSandboxBridgeStartArgs(containerID string) []string {
	return []string{"start", "-a", "-i", strings.TrimSpace(containerID)}
}

func (d DockerCLISandboxBridgeDocker) Stop(_ context.Context, containerID string) error {
	return StopDockerContainer(d.cli(), containerID, dockerStopGrace)
}

func (d DockerCLISandboxBridgeDocker) Remove(_ context.Context, containerID string) error {
	return RemoveDockerContainer(d.cli(), containerID)
}
