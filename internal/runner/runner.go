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
	ErrHostRunnerRequiresActivation = errors.New("host runner requires explicit activation")
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
	// SandboxNetworkHostProxyOnly selects a daemon-managed, egress-filtered
	// Docker bridge that only exposes host-provided targets and proxies.
	SandboxNetworkHostProxyOnly SandboxNetworkMode = "host_proxy_only"
)

const HostProxyOnlySandboxNetworkName = "pentest-host-proxy-only"

const hostProxyOnlyEntrypoint = "/usr/local/bin/pentest-host-proxy-only"

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
	// ReadOnlyTaskFiles are task-root-relative mandatory inputs remounted over
	// the writable task volume with Docker's read-only bind option.
	ReadOnlyTaskFiles []string
	// ReadOnlyTaskDirs keep pathname-based atomic replacements visible inside
	// the sandbox while preventing the Runtime from changing the directory.
	ReadOnlyTaskDirs []string
}

type ActivationRequest struct {
	Runner        Runner
	HostActivated bool
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
	args := []string{"create"}
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
	for _, relativePath := range request.ReadOnlyTaskFiles {
		clean := filepath.Clean(relativePath)
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Command{}, fmt.Errorf("read-only task file must stay under task root: %q", relativePath)
		}
		source := filepath.Join(taskRoot, clean)
		target := "/task/" + filepath.ToSlash(clean)
		args = append(args, "--mount", "type=bind,src="+source+",dst="+target+",readonly")
	}
	for _, relativePath := range request.ReadOnlyTaskDirs {
		clean, source, err := confinedReadOnlyTaskDir(taskRoot, relativePath)
		if err != nil {
			return Command{}, err
		}
		target := "/task/" + filepath.ToSlash(clean)
		args = append(args, "--mount", "type=bind,src="+source+",dst="+target+",readonly")
	}
	if request.NetworkMode == SandboxNetworkHostProxyOnly {
		args = append(args,
			"--network", HostProxyOnlySandboxNetworkName,
			"--cap-add", "NET_ADMIN",
		)
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
	if request.NetworkMode == SandboxNetworkHostProxyOnly {
		args = append(args, hostProxyOnlyEntrypoint)
	}
	args = append(args, request.RuntimeCommand...)
	return Command{Program: program, Args: args}, nil
}

func confinedReadOnlyTaskDir(taskRoot, relativePath string) (string, string, error) {
	clean := filepath.Clean(relativePath)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.Contains(relativePath, `\`) {
		return "", "", fmt.Errorf("read-only task directory must stay under task root: %q", relativePath)
	}
	current := taskRoot
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", "", fmt.Errorf("inspect read-only task directory %q: %w", relativePath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("read-only task directory contains a symbolic link: %q", relativePath)
		}
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("read-only task directory is not a directory: %q", relativePath)
	}
	return clean, current, nil
}

type sandboxEnvVar struct {
	Name  string
	Value string
}

func sandboxProviderEnv(provider runtimeprofile.Provider, providerDir string) []sandboxEnvVar {
	if provider == runtimeprofile.ProviderPi {
		providerHome := "/task/runtime-home/" + providerDir
		return []sandboxEnvVar{
			{Name: "PI_CODING_AGENT_DIR", Value: providerHome + "/agent"},
			{Name: "PI_CODING_AGENT_SESSION_DIR", Value: providerHome + "/agent/sessions"},
			{Name: "HOME", Value: providerHome},
		}
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
	if request.Runner == RunnerHost && !request.HostActivated {
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
