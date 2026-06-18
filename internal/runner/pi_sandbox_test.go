package runner_test

import (
	"strings"
	"testing"

	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func TestWrapSandboxPiCommandInstallsPiWhenMissing(t *testing.T) {
	wrapped, err := runner.WrapSandboxPiCommand(
		[]string{"pi", "--model", "mimo-v2.5-pro", "-p", "smoke goal"},
		map[string]string{},
	)
	if err != nil {
		t.Fatalf("wrap command: %v", err)
	}
	if len(wrapped) != 3 || wrapped[0] != "sh" || wrapped[1] != "-c" {
		t.Fatalf("expected sh -c wrapper, got %#v", wrapped)
	}
	script := wrapped[2]
	for _, want := range []string{
		"command -v pi",
		"npm install -g '@earendil-works/pi-coding-agent'",
		"/task/logs/pi-bootstrap.log",
		"exec pi '--model' 'mimo-v2.5-pro' '-p' 'smoke goal'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected script to contain %q, got:\n%s", want, script)
		}
	}
}

func TestWrapSandboxPiCommandHonorsPackageOverride(t *testing.T) {
	wrapped, err := runner.WrapSandboxPiCommand(
		[]string{"pi", "goal"},
		map[string]string{
			"PENTEST_PI_NPM_PACKAGE": "@example/pi",
			"PENTEST_PI_NPM_VERSION": "1.2.3",
		},
	)
	if err != nil {
		t.Fatalf("wrap command: %v", err)
	}
	if !strings.Contains(wrapped[2], "npm install -g '@example/pi@1.2.3'") {
		t.Fatalf("expected custom package install, got:\n%s", wrapped[2])
	}
}

func TestLaunchProcessEnvSetsPiCodingAgentDirInSandbox(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	env := runner.LaunchProcessEnv(layout, runtimeprofile.Profile{Provider: runtimeprofile.ProviderPi}, true, runner.TaskContext{})
	if env["PI_CODING_AGENT_DIR"] != "/task/runtime-home/pi/agent" {
		t.Fatalf("expected pi agent dir in sandbox, got %#v", env["PI_CODING_AGENT_DIR"])
	}
	if env["PI_HOME"] != "" {
		t.Fatalf("expected PI_HOME to be unset, got %#v", env["PI_HOME"])
	}
}

func TestBuildSandboxCommandSetsPiCodingAgentDir(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	command, err := runner.BuildSandboxCommand(runner.SandboxCommandRequest{
		Layout:         layout,
		Provider:       runtimeprofile.ProviderPi,
		Image:          "pentest-kali:local",
		RuntimeCommand: []string{"sh", "-c", "exec pi goal"},
	})
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	joined := strings.Join(command.Args, " ")
	if !strings.Contains(joined, "PI_CODING_AGENT_DIR=/task/runtime-home/pi/agent") {
		t.Fatalf("expected pi agent dir env in sandbox args, got %q", joined)
	}
}