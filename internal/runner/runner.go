// Package runner owns execution-boundary preparation for task runtimes. It
// prepares task-local directories, projects generated runtime config, and
// builds launch commands; it does not run pentest tools itself.
package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// Runner aliases the task runner vocabulary so runner-boundary code and task
// launch records cannot drift.
type Runner = task.Runner

const (
	RunnerSandbox = task.RunnerSandbox
	RunnerHost    = task.RunnerHost
)

var (
	ErrHostRunnerRequiresActivation = errors.New("host runner requires explicit activation or YOLO mode")
	ErrSandboxDoesNotFallbackToHost = errors.New("sandbox runner failure must not fallback to host runner")
)

// Layout is the task-local filesystem boundary prepared before a runtime
// continuation starts.
type Layout struct {
	TaskRoot     string `json:"task_root"`
	Workdir      string `json:"workdir"`
	RuntimeHome  string `json:"runtime_home"`
	ProviderHome string `json:"provider_home"`
	Artifacts    string `json:"artifacts"`
	Logs         string `json:"logs"`
}

// ConfigProjection describes generated runtime config written into a
// task-local provider home.
type ConfigProjection struct {
	ConfigPath string         `json:"config_path"`
	Config     map[string]any `json:"config"`
}

// Command is a launch command that can be executed later by the harness or an
// external worker. Building it is intentionally side-effect free.
type Command struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

// SandboxCommandRequest contains the data needed to construct a Kali sandbox
// launch command without starting the container.
type SandboxCommandRequest struct {
	Layout         Layout
	Provider       runtimeprofile.Provider
	Image          string
	ContainerCLI   string
	RuntimeCommand []string
}

type ActivationRequest struct {
	Runner        Runner
	HostActivated bool
	YOLO          bool
}

type FallbackRequest struct {
	Requested     Runner
	HostAvailable bool
}

// PrepareTaskLayout creates the task-local directory layout:
// task_root/workdir, task_root/runtime-home/<provider>, task_root/artifacts,
// and task_root/logs.
func PrepareTaskLayout(rootDir, taskID string, provider runtimeprofile.Provider) (Layout, error) {
	if strings.TrimSpace(rootDir) == "" {
		return Layout{}, fmt.Errorf("runner root is required")
	}
	if strings.TrimSpace(taskID) == "" {
		return Layout{}, fmt.Errorf("task id is required")
	}
	providerDir := providerHomeDir(provider)
	if providerDir == "" {
		return Layout{}, fmt.Errorf("runtime provider is required")
	}

	taskRoot := filepath.Join(rootDir, taskID)
	layout := Layout{
		TaskRoot:     taskRoot,
		Workdir:      filepath.Join(taskRoot, "workdir"),
		RuntimeHome:  filepath.Join(taskRoot, "runtime-home"),
		ProviderHome: filepath.Join(taskRoot, "runtime-home", providerDir),
		Artifacts:    filepath.Join(taskRoot, "artifacts"),
		Logs:         filepath.Join(taskRoot, "logs"),
	}
	for _, dir := range []string{layout.Workdir, layout.ProviderHome, layout.Artifacts, layout.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Layout{}, fmt.Errorf("prepare %s: %w", dir, err)
		}
	}
	return layout, nil
}

// ProjectRuntimeConfig writes generated runtime profile config into the
// task-local provider home. It never writes back to host runtime config.
func ProjectRuntimeConfig(layout Layout, profile runtimeprofile.Profile) (ConfigProjection, error) {
	if strings.TrimSpace(layout.ProviderHome) == "" {
		return ConfigProjection{}, fmt.Errorf("provider home is required")
	}
	if err := os.MkdirAll(layout.ProviderHome, 0o700); err != nil {
		return ConfigProjection{}, fmt.Errorf("prepare provider home: %w", err)
	}

	config := runtimeprofile.GeneratedConfig(profile)
	configPath := filepath.Join(layout.ProviderHome, "config.json")
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return ConfigProjection{}, fmt.Errorf("encode runtime config: %w", err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		return ConfigProjection{}, fmt.Errorf("write runtime config: %w", err)
	}
	return ConfigProjection{ConfigPath: configPath, Config: config}, nil
}

// BuildSandboxCommand constructs a container launch command for a task-local
// runtime. It does not execute the command.
func BuildSandboxCommand(request SandboxCommandRequest) (Command, error) {
	if strings.TrimSpace(request.Layout.TaskRoot) == "" {
		return Command{}, fmt.Errorf("task root is required")
	}
	if len(request.RuntimeCommand) == 0 {
		return Command{}, fmt.Errorf("runtime command is required")
	}
	providerDir := providerHomeDir(request.Provider)
	if providerDir == "" {
		return Command{}, fmt.Errorf("runtime provider is required")
	}

	program := request.ContainerCLI
	if program == "" {
		program = "docker"
	}
	image := request.Image
	if image == "" {
		image = "kalilinux/kali-rolling"
	}
	args := []string{
		"run",
		"--rm",
		"-i",
		"-v",
		request.Layout.TaskRoot + ":/task",
		"-w",
		"/task/workdir",
		"-e",
		providerHomeEnv(request.Provider) + "=/task/runtime-home/" + providerDir,
		"-e",
		"PENTEST_TASK_ROOT=/task",
		image,
	}
	args = append(args, request.RuntimeCommand...)
	return Command{Program: program, Args: args}, nil
}

func ValidateActivation(request ActivationRequest) error {
	if request.Runner == RunnerHost && !request.HostActivated && !request.YOLO {
		return ErrHostRunnerRequiresActivation
	}
	return nil
}

func SelectAfterSandboxFailure(request FallbackRequest) (Runner, error) {
	if request.Requested == RunnerSandbox && request.HostAvailable {
		return RunnerSandbox, ErrSandboxDoesNotFallbackToHost
	}
	return request.Requested, nil
}

func providerHomeDir(provider runtimeprofile.Provider) string {
	switch provider {
	case runtimeprofile.ProviderClaudeCode:
		return "claude"
	default:
		return string(provider)
	}
}

func providerHomeEnv(provider runtimeprofile.Provider) string {
	switch provider {
	case runtimeprofile.ProviderCodex:
		return "CODEX_HOME"
	case runtimeprofile.ProviderClaudeCode:
		return "CLAUDE_HOME"
	case runtimeprofile.ProviderPi:
		return "PI_HOME"
	default:
		return "RUNTIME_HOME"
	}
}
