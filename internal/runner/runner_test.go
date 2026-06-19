package runner_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestPrepareTaskLayoutCreatesTaskLocalDirectories(t *testing.T) {
	root := t.TempDir()

	layout, err := runner.PrepareTaskLayout(root, "task-123", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	wantRoot := filepath.Join(root, "task-123")
	if layout.TaskRoot != wantRoot {
		t.Fatalf("expected task root %q, got %q", wantRoot, layout.TaskRoot)
	}
	expectedDirs := []string{
		layout.TaskRoot,
		layout.Workdir,
		layout.RuntimeHome,
		layout.ProviderHome,
		layout.Artifacts,
		layout.Logs,
	}
	for _, dir := range expectedDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected directory %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", dir)
		}
	}
	if layout.ProviderHome != filepath.Join(wantRoot, "runtime-home", "codex") {
		t.Fatalf("expected codex provider home, got %q", layout.ProviderHome)
	}
}

func TestProjectRuntimeConfigWritesGeneratedConfigWithoutMutatingHostConfig(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-123", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	hostConfig := filepath.Join(root, "host-config.json")
	if err := os.WriteFile(hostConfig, []byte(`{"model":"host"}`), 0o600); err != nil {
		t.Fatalf("write host config: %v", err)
	}

	profile := runtimeprofile.Profile{
		ID:       "profile-1",
		Name:     "Codex",
		Provider: runtimeprofile.ProviderCodex,
		Fields: runtimeprofile.Fields{
			BinaryPath: "/usr/local/bin/codex",
			Model:      "gpt-5",
			Endpoint:   "https://api.example.test/v1",
			CustomArgs: []string{"--dangerously-bypass-approvals-and-sandbox"},
			Env:        map[string]string{"CODEX_ENV": "test"},
		},
	}

	projection, err := runner.ProjectRuntimeConfig(layout, profile, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("project config: %v", err)
	}
	configPath := filepath.Join(layout.ProviderHome, "config.toml")
	if projection.ConfigPath != configPath {
		t.Fatalf("expected provider-local config.toml path, got %q", projection.ConfigPath)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read projected config: %v", err)
	}
	config := string(raw)
	if !strings.Contains(config, `model = "gpt-5"`) {
		t.Fatalf("expected model in config.toml, got %s", config)
	}
	if !strings.Contains(config, `base_url = "https://api.example.test/v1"`) {
		t.Fatalf("expected endpoint in config.toml, got %s", config)
	}
	if projection.Config["provider"] != "codex" {
		t.Fatalf("expected provider codex in preview, got %#v", projection.Config["provider"])
	}

	hostRaw, err := os.ReadFile(hostConfig)
	if err != nil {
		t.Fatalf("read host config: %v", err)
	}
	if string(hostRaw) != `{"model":"host"}` {
		t.Fatalf("host config was mutated: %s", string(hostRaw))
	}
}

func TestBuildSandboxCommandConstructsContainerLaunchWithoutExecution(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-123", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout:         layout,
		Provider:       runtimeprofile.ProviderCodex,
		Image:          "pentest-kali:local",
		RuntimeCommand: []string{"codex", "run", "--json"},
	})
	if err != nil {
		t.Fatalf("build command: %v", err)
	}

	if command.Program != "docker" {
		t.Fatalf("expected docker program, got %q", command.Program)
	}
	expectedArgs := []string{
		"run",
		"--rm",
		"-i",
		"--add-host=host.docker.internal:host-gateway",
		"-v",
		layout.TaskRoot + ":/task",
		"-w",
		"/task/workdir",
		"-e",
		"PENTEST_TASK_ROOT=/task",
		"-e",
		"CODEX_HOME=/task/runtime-home/codex",
		"pentest-kali:local",
		"codex",
		"run",
		"--json",
	}
	if !reflect.DeepEqual(command.Args, expectedArgs) {
		t.Fatalf("unexpected sandbox args:\nwant %#v\ngot  %#v", expectedArgs, command.Args)
	}
}

func TestBuildSandboxCommandUsesAbsoluteBindMountForRelativeTaskRoot(t *testing.T) {
	t.Chdir(t.TempDir())

	layout, err := runner.PrepareTaskLayout("runs", "task-relative", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout:         layout,
		Provider:       runtimeprofile.ProviderPi,
		Image:          "pentest-kali:local",
		RuntimeCommand: []string{"pi", "goal"},
	})
	if err != nil {
		t.Fatalf("build command: %v", err)
	}

	absoluteTaskRoot, err := filepath.Abs(layout.TaskRoot)
	if err != nil {
		t.Fatalf("absolute task root: %v", err)
	}
	wantMount := absoluteTaskRoot + ":/task"
	for i, arg := range command.Args {
		if arg == "-v" && i+1 < len(command.Args) {
			if command.Args[i+1] != wantMount {
				t.Fatalf("expected absolute bind mount %q, got %q", wantMount, command.Args[i+1])
			}
			return
		}
	}
	t.Fatalf("expected volume mount in docker args: %#v", command.Args)
}

func TestHostRunnerRequiresExplicitActivationOrYOLO(t *testing.T) {
	err := runner.ValidateActivation(runner.ActivationRequest{Runner: runner.RunnerHost})
	if !errors.Is(err, runner.ErrHostRunnerRequiresActivation) {
		t.Fatalf("expected host activation error, got %v", err)
	}

	if err := runner.ValidateActivation(runner.ActivationRequest{
		Runner:        runner.RunnerHost,
		HostActivated: true,
	}); err != nil {
		t.Fatalf("expected activated host runner to pass: %v", err)
	}

	if err := runner.ValidateActivation(runner.ActivationRequest{
		Runner: runner.RunnerHost,
		YOLO:   true,
	}); err != nil {
		t.Fatalf("expected yolo host runner to pass: %v", err)
	}
}

func TestSandboxFailureDoesNotFallbackToHostRunner(t *testing.T) {
	selected, err := runner.SelectAfterSandboxFailure(runner.FallbackRequest{
		Requested:     runner.RunnerSandbox,
		HostAvailable: true,
	})
	if !errors.Is(err, runner.ErrSandboxDoesNotFallbackToHost) {
		t.Fatalf("expected no-fallback error, got %v", err)
	}
	if selected != runner.RunnerSandbox {
		t.Fatalf("expected selected runner to remain sandbox, got %q", selected)
	}
}
