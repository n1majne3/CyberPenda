package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
	Image           string
	CreateArgs      []string
	RequiredNetwork *DockerNetworkRequirement
	Log             func(DockerSandboxLogEvent)
	// SecretValues holds resolved credential values (e.g. from the launch env)
	// used only to redact runtime output by exact match. It is never passed to
	// the container; the container receives credentials via CreateArgs -e flags.
	SecretValues []string
}

// DockerSandboxLogEvent mirrors image-pull lifecycle and progress to the
// daemon without coupling the runtime package to a concrete logger.
type DockerSandboxLogEvent struct {
	Phase  string
	Image  string
	Stream string
	Text   string
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

// DockerSandboxCreateArgs returns the docker create argv (excluding the
// container CLI) for adapters built by NewDockerSandboxAdapter. Security
// regression tests use this to inspect sandbox launch argv and process env
// without executing containers. Pi session-tail wrappers are unwrapped.
func DockerSandboxCreateArgs(adapter Adapter) ([]string, bool) {
	for adapter != nil {
		switch a := adapter.(type) {
		case *dockerSandboxAdapter:
			out := make([]string, len(a.config.CreateArgs))
			copy(out, a.config.CreateArgs)
			return out, true
		case *piSessionTailAdapter:
			adapter = a.inner
		default:
			return nil, false
		}
	}
	return nil, false
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
	redactor := adapters.NewRedactor(a.config.SecretValues)
	var emitMu sync.Mutex
	safeEmit := func(kind task.EventKind, payload task.EventPayload) {
		emitMu.Lock()
		defer emitMu.Unlock()
		emit(kind, redactor.Redact(payload))
	}
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
	if err := a.ensureDockerImage(ctx, cli, safeEmit); err != nil {
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
	safeEmit(task.EventKindLifecycle, task.EventPayload{
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
			safeEmit(task.EventKindLifecycle, task.EventPayload{
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
			safeEmit(task.EventKindLifecycle, task.EventPayload{
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
	safeEmit(task.EventKindLifecycle, adapters.Redact(task.EventPayload{
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
		ScanOutputWithObserver(stdout, "stdout", maxRuntimeOutputLineBytes, a.recordRuntimeLineMetadata, safeEmit)
	}()
	go func() {
		defer wg.Done()
		ScanOutputWithObserver(stderr, "stderr", maxRuntimeOutputLineBytes, a.recordRuntimeLineMetadata, safeEmit)
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

func (a *dockerSandboxAdapter) ensureDockerImage(ctx context.Context, cli string, emit func(task.EventKind, task.EventPayload)) error {
	image := strings.TrimSpace(a.config.Image)
	if image == "" {
		return fmt.Errorf("docker sandbox adapter requires an explicit image")
	}
	inspectOutput, inspectErr := exec.CommandContext(ctx, cli, "image", "inspect", image).CombinedOutput()
	if inspectErr == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !dockerImageInspectReportsMissing(inspectOutput) {
		return nil
	}

	a.emitImagePullLifecycle(emit, "image_pull_started", image, "")
	pull := exec.CommandContext(ctx, cli, "pull", image)
	stdout, err := pull.StdoutPipe()
	if err != nil {
		return a.failImagePull(emit, image, fmt.Errorf("open docker pull stdout: %w", err))
	}
	stderr, err := pull.StderrPipe()
	if err != nil {
		return a.failImagePull(emit, image, fmt.Errorf("open docker pull stderr: %w", err))
	}
	if err := pull.Start(); err != nil {
		return a.failImagePull(emit, image, fmt.Errorf("start docker pull: %w", err))
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.scanImagePullOutput(stdout, "stdout", emit)
	}()
	go func() {
		defer wg.Done()
		a.scanImagePullOutput(stderr, "stderr", emit)
	}()
	wg.Wait()
	waitErr := pull.Wait()
	if err := ctx.Err(); err != nil {
		return a.failImagePull(emit, image, err)
	}
	if waitErr != nil {
		return a.failImagePull(emit, image, fmt.Errorf("docker pull failed: %w", waitErr))
	}
	a.emitImagePullLifecycle(emit, "image_pull_completed", image, "")
	return nil
}

func dockerImageInspectReportsMissing(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "no such image") || strings.Contains(text, "no such object")
}

func (a *dockerSandboxAdapter) scanImagePullOutput(reader io.Reader, stream string, emit func(task.EventKind, task.EventPayload)) {
	br := bufio.NewReader(reader)
	for {
		line, truncated, err := ReadBoundedLine(br, maxRuntimeOutputLineBytes)
		if line != "" {
			payload := task.EventPayload{
				"stream": stream,
				"text":   line,
			}
			if truncated {
				payload["truncated"] = true
			}
			a.emitImagePullProgress(emit, payload)
		}
		if err != nil {
			if err != io.EOF {
				a.emitImagePullProgress(emit, task.EventPayload{
					"stream": stream,
					"text":   "read docker pull " + stream + ": " + err.Error(),
				})
			}
			return
		}
	}
}

func (a *dockerSandboxAdapter) emitImagePullProgress(emit func(task.EventKind, task.EventPayload), payload task.EventPayload) {
	safe := adapters.Redact(payload)
	emit(task.EventKindRuntimeOutput, safe)
	if a.config.Log != nil {
		a.config.Log(DockerSandboxLogEvent{
			Phase:  "image_pull_progress",
			Image:  redactedDockerString(a.config.Image),
			Stream: payloadString(safe, "stream"),
			Text:   payloadString(safe, "text"),
		})
	}
}

func (a *dockerSandboxAdapter) emitImagePullLifecycle(emit func(task.EventKind, task.EventPayload), phase, image, detail string) {
	payload := task.EventPayload{
		"phase":   phase,
		"adapter": a.Name(),
		"image":   image,
	}
	if detail != "" {
		payload["error"] = detail
	}
	safe := adapters.Redact(payload)
	emit(task.EventKindLifecycle, safe)
	if a.config.Log != nil {
		a.config.Log(DockerSandboxLogEvent{
			Phase: phase,
			Image: payloadString(safe, "image"),
			Text:  payloadString(safe, "error"),
		})
	}
}

func (a *dockerSandboxAdapter) failImagePull(emit func(task.EventKind, task.EventPayload), image string, err error) error {
	safeImage := redactedDockerString(image)
	wrapped := fmt.Errorf("pull sandbox image %q: %w", safeImage, err)
	a.emitImagePullLifecycle(emit, "image_pull_failed", image, wrapped.Error())
	return wrapped
}

func redactedDockerString(value string) string {
	return payloadString(adapters.Redact(task.EventPayload{"value": value}), "value")
}

func payloadString(payload task.EventPayload, key string) string {
	value, _ := payload[key].(string)
	return value
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
