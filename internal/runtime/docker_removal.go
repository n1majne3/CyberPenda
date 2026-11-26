package runtime

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

func removeDockerContainer(parent context.Context, containerCLI, containerID string) error {
	cli, id := strings.TrimSpace(containerCLI), strings.TrimSpace(containerID)
	if id == "" {
		return nil
	}
	if cli == "" {
		cli = "docker"
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var pending error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, pending)
		}
		output, err := exec.CommandContext(ctx, cli, "rm", "-f", id).CombinedOutput()
		if err == nil {
			return nil
		}
		commandErr := dockerContainerCommandError{err: err, output: string(output)}
		if isMissingDockerContainerError(commandErr) {
			return nil
		}
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), pending, commandErr)
		}
		detail := strings.ToLower(string(output))
		if !strings.Contains(detail, "removal of container ") || !strings.Contains(detail, "is already in progress") {
			return commandErr
		}
		pending = commandErr
		// A concurrent remover is not confirmation that removal has finished.
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), pending)
		case <-timer.C:
		}
	}
}
