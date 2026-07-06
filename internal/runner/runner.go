// Package runner owns execution-boundary preparation for task runtimes. It
// prepares task-local directories, projects generated runtime config, and
// builds launch commands; it does not run pentest tools itself.
package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/modelprovider"
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
	SkillsRoot   string `json:"skills_root"`
	Artifacts    string `json:"artifacts"`
	Logs         string `json:"logs"`
}

// ConfigProjection describes generated runtime config written into a
// task-local provider home.
type ConfigProjection struct {
	ConfigPath      string                  `json:"config_path"`
	Config          map[string]any          `json:"config"`
	ResolvedProfile runtimeprofile.Profile  `json:"-"`
	ModelSnapshot   *modelprovider.Snapshot `json:"-"`
}

// Command is a launch command that can be executed later by the harness or an
// external worker. Building it is intentionally side-effect free.
type Command struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

// SandboxNetworkMode controls the Docker network boundary used by sandbox
// launches.
type SandboxNetworkMode string

const (
	// SandboxNetworkDefault leaves Docker networking at its default bridge
	// behavior.
	SandboxNetworkDefault SandboxNetworkMode = ""
	// SandboxNetworkHostProxyOnly selects a pre-created internal Docker network
	// that should only expose host-provided targets and proxies.
	SandboxNetworkHostProxyOnly SandboxNetworkMode = "host_proxy_only"
)

const HostProxyOnlySandboxNetworkName = "pentest-host-proxy-only"

// SandboxCommandRequest contains the data needed to construct a Kali sandbox
// container create command without starting the container.
type SandboxCommandRequest struct {
	Layout          Layout
	Provider        runtimeprofile.Provider
	Image           string
	ContainerCLI    string
	ContainerIDFile string
	RuntimeCommand  []string
	ProcessEnv      map[string]string
	NetworkMode     SandboxNetworkMode
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
// task_root/workdir, task_root/runtime-home/<provider>, task_root/skills,
// task_root/artifacts, and task_root/logs.
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
		SkillsRoot:   filepath.Join(taskRoot, "skills"),
		Artifacts:    filepath.Join(taskRoot, "artifacts"),
		Logs:         filepath.Join(taskRoot, "logs"),
	}
	for _, dir := range []string{layout.Workdir, layout.ProviderHome, layout.SkillsRoot, layout.Artifacts, layout.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Layout{}, fmt.Errorf("prepare %s: %w", dir, err)
		}
	}
	return layout, nil
}

// BuildSandboxCommand constructs a container create command for a task-local
// runtime. It does not execute the command; the runtime harness owns start,
// stop/kill, logs, wait, and rm for the created container.
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
	taskRoot, err := filepath.Abs(request.Layout.TaskRoot)
	if err != nil {
		return Command{}, fmt.Errorf("resolve task root: %w", err)
	}
	args := []string{
		"create",
		"-i",
	}
	if strings.TrimSpace(request.ContainerIDFile) != "" {
		args = append(args, "--cidfile", request.ContainerIDFile)
	}
	args = append(args,
		"--add-host=host.docker.internal:host-gateway",
		"-v",
		taskRoot+":/task",
		"-w",
		"/task/workdir",
		"-e",
		"PENTEST_TASK_ROOT=/task",
	)
	if request.NetworkMode == SandboxNetworkHostProxyOnly {
		args = append(args, "--network", HostProxyOnlySandboxNetworkName)
	}
	for key, value := range request.ProcessEnv {
		if strings.TrimSpace(key) == "" {
			continue
		}
		args = append(args, "-e", key+"="+value)
	}
	for _, env := range sandboxProviderEnv(request.Provider, providerDir) {
		args = append(args, "-e", env.Name+"="+env.Value)
	}
	args = append(args, image)
	args = append(args, request.RuntimeCommand...)
	return Command{Program: program, Args: args}, nil
}

type sandboxEnvVar struct {
	Name  string
	Value string
}

func sandboxProviderEnv(provider runtimeprofile.Provider, providerDir string) []sandboxEnvVar {
	if provider == runtimeprofile.ProviderPi {
		return []sandboxEnvVar{{Name: "PI_CODING_AGENT_DIR", Value: "/task/runtime-home/" + providerDir + "/agent"}}
	}
	providerHome := "/task/runtime-home/" + providerDir
	if provider == runtimeprofile.ProviderClaudeCode {
		// Claude Code stores resumable conversations under HOME; keep it inside
		// the task mount so sandbox container rebuilds can find native sessions.
		return []sandboxEnvVar{
			{Name: providerHomeEnv(provider), Value: providerHome},
			{Name: "HOME", Value: providerHome},
		}
	}
	return []sandboxEnvVar{{Name: providerHomeEnv(provider), Value: providerHome}}
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
		return "PI_CODING_AGENT_DIR"
	default:
		return "RUNTIME_HOME"
	}
}
