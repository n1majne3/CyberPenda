package runner_test

import (
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/credential"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
	"pentest/internal/store"
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
	if env["PI_CODING_AGENT_SESSION_DIR"] != "/task/runtime-home/pi/agent/sessions" {
		t.Fatalf("expected pi session dir in sandbox, got %#v", env["PI_CODING_AGENT_SESSION_DIR"])
	}
	if env["PI_HOME"] != "" {
		t.Fatalf("expected PI_HOME to be unset, got %#v", env["PI_HOME"])
	}
}

func TestLaunchProcessEnvWithCredentialsInjectsPiInlineAPIKey(t *testing.T) {
	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-inline", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	profile := runtimeprofile.Profile{
		Provider: runtimeprofile.ProviderPi,
		Fields: runtimeprofile.Fields{
			Env: map[string]string{
				"PI_PROVIDER_ID": "custom",
			},
			APIKeys: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-inline",
			},
		},
	}

	env, err := runner.LaunchProcessEnvWithCredentials(layout, profile, true, runner.TaskContext{}, runner.ProjectionRequest{})
	if err != nil {
		t.Fatalf("launch env: %v", err)
	}
	if env["ANTHROPIC_API_KEY"] != "sk-ant-inline" {
		t.Fatalf("expected inline Pi API key in process env, got %#v", env["ANTHROPIC_API_KEY"])
	}
	if env["PI_PROVIDER_ID"] != "custom" {
		t.Fatalf("expected profile env in process env, got %#v", env["PI_PROVIDER_ID"])
	}
}

func TestLaunchProcessEnvWithCredentialsInjectsPiCredentialRefAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-bound")

	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	creds := credential.NewService(db)
	if _, err := creds.Upsert("anthropic-key", credential.ScopeGlobal, "", credential.Source{
		Kind:  credential.SourceEnv,
		Value: "ANTHROPIC_API_KEY",
	}, false); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}

	root := t.TempDir()
	layout, err := runner.PrepareTaskLayout(root, "task-pi-ref", runtimeprofile.ProviderPi)
	if err != nil {
		t.Fatalf("prepare layout: %v", err)
	}

	env, err := runner.LaunchProcessEnvWithCredentials(
		layout,
		runtimeprofile.Profile{
			Provider: runtimeprofile.ProviderPi,
			Fields: runtimeprofile.Fields{
				CredentialRefs: []string{"anthropic-key"},
			},
		},
		true,
		runner.TaskContext{},
		runner.ProjectionRequest{
			ProjectID:   "project-1",
			Credentials: creds,
		},
	)
	if err != nil {
		t.Fatalf("launch env: %v", err)
	}
	if env["ANTHROPIC_API_KEY"] != "sk-ant-bound" {
		t.Fatalf("expected bound Pi API key in process env, got %#v", env["ANTHROPIC_API_KEY"])
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
	for _, want := range []string{
		"PI_CODING_AGENT_DIR=/task/runtime-home/pi/agent",
		"PI_CODING_AGENT_SESSION_DIR=/task/runtime-home/pi/agent/sessions",
		"HOME=/task/runtime-home/pi",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in sandbox args, got %q", want, joined)
		}
	}
}
