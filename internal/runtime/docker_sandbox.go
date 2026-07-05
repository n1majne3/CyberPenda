package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"pentest/internal/adapters"
	"pentest/internal/task"
)

const dockerStopGrace = 2 * time.Second

// DockerSandboxConfig describes a daemon-owned sandbox container runtime.
// CreateArgs must be a docker create argv, excluding the container CLI itself.
type DockerSandboxConfig struct {
	Name         string
	ContainerCLI string
	CreateArgs   []string
}

type dockerSandboxAdapter struct {
	config      DockerSandboxConfig
	mu          sync.Mutex
	record      func(NativeSessionMetadata) error
	containerID string
}

// NewDockerSandboxAdapter returns an adapter that owns the created container
// lifecycle instead of relying on foreground docker run cancellation.
func NewDockerSandboxAdapter(config DockerSandboxConfig) Adapter {
	return &dockerSandboxAdapter{config: config}
}

func (a *dockerSandboxAdapter) Name() string {
	if strings.TrimSpace(a.config.Name) != "" {
		return a.config.Name
	}
	return "docker"
}

func (a *dockerSandboxAdapter) SetMetadataRecorder(record func(NativeSessionMetadata) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record = record
}

func (a *dockerSandboxAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	cli := strings.TrimSpace(a.config.ContainerCLI)
	if cli == "" {
		cli = "docker"
	}
	if len(a.config.CreateArgs) == 0 || a.config.CreateArgs[0] != "create" {
		return fmt.Errorf("docker sandbox adapter requires docker create args")
	}

	create := exec.CommandContext(ctx, cli, a.config.CreateArgs...)
	rawID, err := create.Output()
	if err != nil {
		return fmt.Errorf("create sandbox container: %w", err)
	}
	containerID := strings.TrimSpace(string(rawID))
	if containerID == "" {
		return fmt.Errorf("create sandbox container returned empty id")
	}
	a.mu.Lock()
	a.containerID = containerID
	record := a.record
	a.mu.Unlock()
	if record != nil {
		if err := record(NativeSessionMetadata{ContainerID: containerID}); err != nil {
			return fmt.Errorf("record sandbox container id: %w", err)
		}
	}
	emit(task.EventKindLifecycle, task.EventPayload{
		"phase":        "container_created",
		"adapter":      a.Name(),
		"container_id": containerID,
	})

	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		<-ctx.Done()
		if err := stopThenKillContainer(cli, containerID, dockerStopGrace); err != nil {
			emit(task.EventKindLifecycle, task.EventPayload{
				"phase":        "stop_failed",
				"adapter":      a.Name(),
				"container_id": containerID,
				"error":        err.Error(),
			})
		}
	}()

	start := exec.CommandContext(ctx, cli, "start", "-a", containerID)
	stdout, err := start.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open sandbox stdout: %w", err)
	}
	stderr, err := start.StderrPipe()
	if err != nil {
		return fmt.Errorf("open sandbox stderr: %w", err)
	}
	emit(task.EventKindLifecycle, adapters.Redact(task.EventPayload{
		"phase":        "container_starting",
		"adapter":      a.Name(),
		"container_id": containerID,
		"program":      cli,
		"args":         []string{"start", "-a", containerID},
	}))
	if err := start.Start(); err != nil {
		return fmt.Errorf("start sandbox container: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ScanOutput(stdout, "stdout", maxRuntimeOutputLineBytes, emit)
	}()
	go func() {
		defer wg.Done()
		ScanOutput(stderr, "stderr", maxRuntimeOutputLineBytes, emit)
	}()
	wg.Wait()
	waitErr := start.Wait()
	if ctx.Err() != nil {
		<-stopDone
	}
	if err := removeContainer(cli, containerID); err != nil {
		emit(task.EventKindLifecycle, task.EventPayload{
			"phase":        "cleanup_failed",
			"adapter":      a.Name(),
			"container_id": containerID,
			"error":        err.Error(),
		})
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("sandbox container failed: %w", waitErr)
	}
	return nil
}

func stopThenKillContainer(containerCLI, containerID string, grace time.Duration) error {
	stopCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := exec.CommandContext(stopCtx, containerCLI, "stop", containerID).Run(); err == nil {
		return nil
	}
	killCtx, killCancel := context.WithTimeout(context.Background(), grace)
	defer killCancel()
	if err := exec.CommandContext(killCtx, containerCLI, "kill", containerID).Run(); err != nil {
		return err
	}
	return nil
}

func removeContainer(containerCLI, containerID string) error {
	return exec.Command(containerCLI, "rm", "-f", containerID).Run()
}
