package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// StopConfirmation confirms runtime-owned resources have exited after the
// adapter process has returned.
type StopConfirmation func(timeout time.Duration) error

// ReadContainerIDFile reads a Docker cidfile written by the sandbox runner.
func ReadContainerIDFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// DockerContainerStopConfirmation returns a confirmation hook that waits until
// the cidfile container is no longer running. A missing cidfile means docker
// exited before creating a container.
func DockerContainerStopConfirmation(containerCLI, cidFile string) StopConfirmation {
	return func(timeout time.Duration) error {
		return ConfirmDockerContainerExited(containerCLI, cidFile, timeout)
	}
}

// ConfirmDockerContainerExited confirms a docker container has stopped or has
// already been removed by docker run --rm.
func ConfirmDockerContainerExited(containerCLI, cidFile string, timeout time.Duration) error {
	id, err := ReadContainerIDFile(cidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read container id: %w", err)
	}
	if id == "" {
		return nil
	}
	if strings.TrimSpace(containerCLI) == "" {
		containerCLI = "docker"
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		stopped, err := dockerContainerStopped(ctx, containerCLI, id)
		if err != nil {
			return err
		}
		if stopped {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("container %s did not stop before timeout", id)
		case <-ticker.C:
		}
	}
}

func dockerContainerStopped(ctx context.Context, containerCLI, id string) (bool, error) {
	cmd := exec.CommandContext(ctx, containerCLI, "inspect", "-f", "{{.State.Running}}", id)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return true, nil
		}
		return false, fmt.Errorf("inspect container %s: %w", id, err)
	}
	return strings.TrimSpace(string(out)) == "false", nil
}
