package runner_test

import (
	"errors"
	"os"
	"os/exec"
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

func TestBuildSandboxCommandMountsPinnedBlackboardAndScopeReadOnly(t *testing.T) {
	layout, err := runner.PrepareTaskLayout(t.TempDir(), "task-pinned", runtimeprofile.ProviderCodex)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}
	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout: layout, Provider: runtimeprofile.ProviderCodex, RuntimeCommand: []string{"codex", "exec"},
		ReadOnlyTaskFiles: []string{"workdir/.pentest/blackboard.json", "workdir/.pentest/scope.json"},
	})
	if err != nil {
		t.Fatalf("build sandbox command: %v", err)
	}
	joined := strings.Join(command.Args, " ")
	for _, file := range []string{"blackboard.json", "scope.json"} {
		if !strings.Contains(joined, "dst=/task/workdir/.pentest/"+file+",readonly") {
			t.Fatalf("%s is not mounted read-only: %v", file, command.Args)
		}
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
		Layout:          layout,
		Provider:        runtimeprofile.ProviderCodex,
		Image:           "pentest-kali:local",
		ContainerIDFile: filepath.Join(layout.Logs, "container.cid"),
		RuntimeCommand:  []string{"codex", "run", "--json"},
	})
	if err != nil {
		t.Fatalf("build command: %v", err)
	}

	if command.Program != "docker" {
		t.Fatalf("expected docker program, got %q", command.Program)
	}
	expectedArgs := []string{
		"create",
		"--cidfile",
		filepath.Join(layout.Logs, "container.cid"),
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
	if containsString(command.Args, "-i") {
		t.Fatalf("sandbox containers must not keep stdin open; got args %#v", command.Args)
	}
}

func TestBuildSandboxCommandSetsClaudeHomeToPersistentRuntimeHome(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-claude", runtimeprofile.ProviderClaudeCode)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout:         layout,
		Provider:       runtimeprofile.ProviderClaudeCode,
		Image:          "pentest-kali:local",
		RuntimeCommand: []string{"claude", "--resume", "sess-123", "continue"},
	})
	if err != nil {
		t.Fatalf("build command: %v", err)
	}

	env := sandboxEnvArgs(command.Args)
	for _, want := range []string{
		"CLAUDE_HOME=/task/runtime-home/claude",
		"HOME=/task/runtime-home/claude",
	} {
		if !containsString(env, want) {
			t.Fatalf("expected sandbox env to contain %q, got env=%#v args=%#v", want, env, command.Args)
		}
	}
}

func sandboxEnvArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" {
			out = append(out, args[i+1])
			i++
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBuildSandboxCommandUsesHostProxyOnlyNetworkWhenRequested(t *testing.T) {
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
		NetworkMode:    runner.SandboxNetworkHostProxyOnly,
	})
	if err != nil {
		t.Fatalf("build command: %v", err)
	}

	joined := strings.Join(command.Args, " ")
	for _, want := range []string{
		"--network pentest-host-proxy-only",
		"--add-host=host.docker.internal:host-gateway",
		"--cap-add NET_ADMIN",
		"pentest-kali:local /usr/local/bin/pentest-host-proxy-only codex run --json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected sandbox args to contain %q, got %v", want, command.Args)
		}
	}
}

func TestHostProxyOnlyEntrypointRestrictsEgressBeforeRuntime(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	fakeCommand := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> \"$COMMAND_LOG\"\n"
	for _, name := range []string{"iptables", "ip6tables", "setpriv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(fakeCommand), 0o700); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	getent := "#!/bin/sh\necho '0.250.250.254 STREAM host.docker.internal'\n"
	if err := os.WriteFile(filepath.Join(dir, "getent"), []byte(getent), 0o700); err != nil {
		t.Fatalf("write fake getent: %v", err)
	}

	script := filepath.Join("..", "..", "docker", "pentest-sandbox", "host-proxy-only-entrypoint.sh")
	command := exec.Command("sh", script, "codex", "run", "--json")
	command.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "COMMAND_LOG="+logPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run host-proxy-only entrypoint: %v: %s", err, output)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	logText := string(raw)
	for _, want := range []string{
		"iptables -w -F OUTPUT",
		"iptables -w -A OUTPUT -o lo -j ACCEPT",
		"iptables -w -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"iptables -w -A OUTPUT -d 0.250.250.254/32 -j ACCEPT",
		"iptables -w -P OUTPUT DROP",
		"ip6tables -w -P OUTPUT DROP",
		"setpriv --bounding-set=-net_admin --inh-caps=-net_admin --ambient-caps=-net_admin -- codex run --json",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected entrypoint command %q, got log:\n%s", want, logText)
		}
	}
	if strings.Index(logText, "iptables -w -P OUTPUT DROP") > strings.Index(logText, "setpriv ") {
		t.Fatalf("expected firewall before capability drop and runtime exec, got log:\n%s", logText)
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

func TestHostRunnerRequiresExplicitActivation(t *testing.T) {
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
