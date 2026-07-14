package runtime

import (
	"context"
	"errors"
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
	Name            string
	ContainerCLI    string
	CreateArgs      []string
	RequiredNetwork *DockerNetworkRequirement
}

// DockerNetworkRequirement describes a daemon-managed Docker network that
// must exist with the expected isolation properties before a sandbox starts.
type DockerNetworkRequirement struct {
	Name     string
	Driver   string
	Internal bool
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

func (a *dockerSandboxAdapter) recordRuntimeLineMetadata(line string) {
	metadata := NativeSessionMetadataFromRuntimeLine(line)
	if metadata.NativeSessionID == "" && metadata.NativeSessionPath == "" && metadata.ContainerID == "" {
		return
	}
	a.mu.Lock()
	record := a.record
	a.mu.Unlock()
	if record != nil {
		_ = record(metadata)
	}
}

func (a *dockerSandboxAdapter) Run(ctx context.Context, goal string, emit func(task.EventKind, task.EventPayload)) error {
	cli := strings.TrimSpace(a.config.ContainerCLI)
	if cli == "" {
		cli = "docker"
	}
	if len(a.config.CreateArgs) == 0 || a.config.CreateArgs[0] != "create" {
		return fmt.Errorf("docker sandbox adapter requires docker create args")
	}
	if err := ensureDockerNetwork(ctx, cli, a.config.RequiredNetwork); err != nil {
		return err
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
	runDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		select {
		case <-ctx.Done():
		case <-runDone:
			return
		}
		if err := StopDockerContainer(cli, containerID, dockerStopGrace); err != nil {
			emit(task.EventKindLifecycle, task.EventPayload{
				"phase":        "stop_failed",
				"adapter":      a.Name(),
				"container_id": containerID,
				"error":        err.Error(),
			})
		}
	}()
	defer func() {
		if ctx.Err() != nil {
			<-stopDone
		} else {
			close(runDone)
			<-stopDone
		}
		if err := RemoveDockerContainer(cli, containerID); err != nil {
			emit(task.EventKindLifecycle, task.EventPayload{
				"phase":        "cleanup_failed",
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
		ScanOutputWithObserver(stdout, "stdout", maxRuntimeOutputLineBytes, a.recordRuntimeLineMetadata, emit)
	}()
	go func() {
		defer wg.Done()
		ScanOutputWithObserver(stderr, "stderr", maxRuntimeOutputLineBytes, a.recordRuntimeLineMetadata, emit)
	}()
	wg.Wait()
	waitErr := start.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("sandbox container failed: %w", waitErr)
	}
	return nil
}

func ensureDockerNetwork(ctx context.Context, cli string, requirement *DockerNetworkRequirement) error {
	if requirement == nil {
		return nil
	}
	name := strings.TrimSpace(requirement.Name)
	if name == "" {
		return fmt.Errorf("required docker network name is empty")
	}
	driver := strings.TrimSpace(requirement.Driver)
	if driver == "" {
		driver = "bridge"
	}

	exists, actualDriver, actualInternal := inspectDockerNetwork(ctx, cli, name)
	if exists {
		return validateDockerNetwork(name, driver, requirement.Internal, actualDriver, actualInternal)
	}

	args := []string{"network", "create", "--driver", driver}
	if requirement.Internal {
		args = append(args, "--internal")
	}
	args = append(args, name)
	if output, err := exec.CommandContext(ctx, cli, args...).CombinedOutput(); err != nil {
		// Another task may have created the network between inspect and create.
		exists, actualDriver, actualInternal = inspectDockerNetwork(ctx, cli, name)
		if exists {
			return validateDockerNetwork(name, driver, requirement.Internal, actualDriver, actualInternal)
		}
		return fmt.Errorf("create required docker network %q: %w: %s", name, err, strings.TrimSpace(string(output)))
	}

	exists, actualDriver, actualInternal = inspectDockerNetwork(ctx, cli, name)
	if !exists {
		return fmt.Errorf("required docker network %q was not found after creation", name)
	}
	return validateDockerNetwork(name, driver, requirement.Internal, actualDriver, actualInternal)
}

func inspectDockerNetwork(ctx context.Context, cli, name string) (bool, string, bool) {
	output, err := exec.CommandContext(ctx, cli, "network", "inspect", "--format", "{{.Driver}}|{{.Internal}}", name).Output()
	if err != nil {
		return false, "", false
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(parts) != 2 {
		return true, strings.TrimSpace(string(output)), false
	}
	return true, strings.TrimSpace(parts[0]), strings.EqualFold(strings.TrimSpace(parts[1]), "true")
}

func validateDockerNetwork(name, expectedDriver string, expectedInternal bool, actualDriver string, actualInternal bool) error {
	if actualDriver != expectedDriver || actualInternal != expectedInternal {
		return fmt.Errorf(
			"required docker network %q has unsafe configuration: expected driver=%s internal=%t, got driver=%s internal=%t",
			name,
			expectedDriver,
			expectedInternal,
			actualDriver,
			actualInternal,
		)
	}
	return nil
}

// StopDockerContainer requests a docker container stop, escalating to kill when
// the graceful stop command fails or times out.
func StopDockerContainer(containerCLI, containerID string, grace time.Duration) error {
	cli := strings.TrimSpace(containerCLI)
	if cli == "" {
		cli = "docker"
	}
	id := strings.TrimSpace(containerID)
	if id == "" {
		return nil
	}
	if err := runDockerContainerCommand(cli, grace, "stop", id); err == nil || isMissingDockerContainerError(err) {
		return nil
	}
	err := runDockerContainerCommand(cli, grace, "kill", id)
	if isMissingDockerContainerError(err) {
		return nil
	}
	return err
}

// RemoveDockerContainer force-removes a docker container. Missing containers
// are treated as a successful cleanup by Docker itself.
func RemoveDockerContainer(containerCLI, containerID string) error {
	cli := strings.TrimSpace(containerCLI)
	if cli == "" {
		cli = "docker"
	}
	id := strings.TrimSpace(containerID)
	if id == "" {
		return nil
	}
	err := runDockerContainerCommand(cli, 0, "rm", "-f", id)
	if isMissingDockerContainerError(err) {
		return nil
	}
	return err
}

type dockerContainerCommandError struct {
	err    error
	output string
}

func (e dockerContainerCommandError) Error() string {
	return e.err.Error()
}

func runDockerContainerCommand(containerCLI string, timeout time.Duration, args ...string) error {
	var cmd *exec.Cmd
	if timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd = exec.CommandContext(ctx, containerCLI, args...)
	} else {
		cmd = exec.Command(containerCLI, args...)
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return dockerContainerCommandError{err: err, output: string(output)}
}

func isMissingDockerContainerError(err error) bool {
	if err == nil {
		return false
	}
	var commandErr dockerContainerCommandError
	if errors.As(err, &commandErr) {
		text := strings.ToLower(commandErr.output)
		return strings.Contains(text, "no such container") || strings.Contains(text, "no such object")
	}
	return false
}
