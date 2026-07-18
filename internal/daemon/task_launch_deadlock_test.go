package daemon_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"pentest/internal/daemon"
	"pentest/internal/runtimeprofile"
)

const piModelProviderLaunchHelper = "PENTEST_TEST_PI_MODEL_PROVIDER_LAUNCH_HELPER"

func TestLaunchPiTaskWithModelProviderReturnsWithoutDeadlock(t *testing.T) {
	if os.Getenv(piModelProviderLaunchHelper) == "1" {
		runPiModelProviderLaunch(t)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLaunchPiTaskWithModelProviderReturnsWithoutDeadlock$", "-test.count=1")
	command.Env = append(os.Environ(), piModelProviderLaunchHelper+"=1")
	output, err := command.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("POST /tasks deadlocked after preflight passed; helper exceeded 2s\n%s", output)
	}
	if err != nil {
		t.Fatalf("launch helper failed: %v\n%s", err, output)
	}
}

func runPiModelProviderLaunch(t *testing.T) {
	root := t.TempDir()
	containerCLI := filepath.Join(root, "fake-docker")
	if err := os.WriteFile(containerCLI, []byte(`#!/bin/sh
case "$1" in
  create) echo ctr-pi-model-provider ;;
  start|rm|stop|kill) exit 0 ;;
  *) exit 0 ;;
esac
`), 0o700); err != nil {
		t.Fatalf("write fake container CLI: %v", err)
	}

	server := newDaemonWithConfig(t, daemon.Config{
		Version:              "test-version",
		DBPath:               filepath.Join(root, "pentest.db"),
		RuntimeRoot:          filepath.Join(root, "runs"),
		SandboxImage:         "cyberpenda-sandbox:test",
		ContainerCLI:         containerCLI,
		DisableBuiltinSkills: true,
	})
	providerID := createModelProvider(t, server, `{
		"name":"Deadlock Provider",
		"endpoints":[{"protocol":"anthropic_messages","base_url":"https://api.example.test/v1"}],
		"catalog":{"manual":["claude-test"],"default_model":"claude-test"}
	}`)
	putBinding(t, server, "/api/credential-bindings", `{
		"credential_ref":"DEADLOCK_PROVIDER_API_KEY",
		"source":{"kind":"literal","value":"test-key"}
	}`)
	projectID := createProject(t, server, `{"name":"Deadlock Project","scope":{"domains":["example.test"]}}`)
	profileID := createLocalRuntimeProfile(t, server, "Pi Deadlock", runtimeprofile.ProviderPi, runtimeprofile.Fields{
		ModelProviderID: providerID,
	})

	taskID := createTask(t, server, projectID, `{
		"goal":"inspect example.test",
		"runtime_profile_id":`+quoteJSON(profileID)+`,
		"runner":"sandbox"
	}`)
	waitForTaskStatus(t, server, projectID, taskID, "completed")
}
