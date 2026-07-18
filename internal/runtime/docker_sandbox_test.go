package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/task"
)

func TestDockerSandboxAdapterPullsMissingImageBeforeCreateAndStreamsProgress(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	pulledPath := filepath.Join(dir, "pulled")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1 $2\" = \"image inspect\" ]; then echo 'Error response from daemon: No such image: registry.example/cyberpenda:test' >&2; exit 1; fi\n" +
		"if [ \"$1\" = \"pull\" ]; then\n" +
		"  echo 'downloading layer one'\n" +
		"  echo 'OPENAI_API_KEY=sk-abcdefghijklmnop' >&2\n" +
		"  touch " + shellQuote(pulledPath) + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"create\" ]; then\n" +
		"  [ -f " + shellQuote(pulledPath) + " ] || exit 19\n" +
		"  echo ctr-pulled\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"start\" ]; then echo sandbox-started; exit 0; fi\n" +
		"if [ \"$1\" = \"rm\" ]; then exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	type recordedEvent struct {
		kind    task.EventKind
		payload task.EventPayload
	}
	var mu sync.Mutex
	var events []recordedEvent
	var logs []runtime.DockerSandboxLogEvent
	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "registry.example/cyberpenda:test",
		CreateArgs:   []string{"create", "-i", "registry.example/cyberpenda:test", "codex", "run"},
		Log: func(event runtime.DockerSandboxLogEvent) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, event)
		},
	})
	err := adapter.Run(context.Background(), "sandbox task", func(kind task.EventKind, payload task.EventPayload) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, recordedEvent{kind: kind, payload: payload})
	})
	if err != nil {
		t.Fatalf("run docker sandbox: %v", err)
	}

	rawCommands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	commands := string(rawCommands)
	inspectAt := strings.Index(commands, "image inspect registry.example/cyberpenda:test")
	pullAt := strings.Index(commands, "pull registry.example/cyberpenda:test")
	createAt := strings.Index(commands, "create -i registry.example/cyberpenda:test")
	if inspectAt < 0 || pullAt < 0 || createAt < 0 || !(inspectAt < pullAt && pullAt < createAt) {
		t.Fatalf("expected inspect, pull, create ordering, got:\n%s", commands)
	}

	mu.Lock()
	defer mu.Unlock()
	startedAt, stdoutAt, stderrAt, completedAt := -1, -1, -1, -1
	for i, event := range events {
		phase, _ := event.payload["phase"].(string)
		text, _ := event.payload["text"].(string)
		switch {
		case event.kind == task.EventKindLifecycle && phase == "image_pull_started":
			startedAt = i
			if event.payload["image"] != "registry.example/cyberpenda:test" {
				t.Fatalf("expected lifecycle image field, got %#v", event.payload)
			}
		case event.kind == task.EventKindRuntimeOutput && text == "downloading layer one":
			stdoutAt = i
		case event.kind == task.EventKindRuntimeOutput && strings.Contains(text, "OPENAI_API_KEY="):
			stderrAt = i
			if strings.Contains(text, "sk-abcdefghijklmnop") {
				t.Fatalf("runtime output leaked pull secret: %q", text)
			}
		case event.kind == task.EventKindLifecycle && phase == "image_pull_completed":
			completedAt = i
		}
	}
	if startedAt < 0 || stdoutAt < 0 || stderrAt < 0 || completedAt < 0 || !(startedAt < stdoutAt && startedAt < stderrAt && stdoutAt < completedAt && stderrAt < completedAt) {
		t.Fatalf("expected pull lifecycle to bracket streamed output, got %#v", events)
	}
	if len(logs) < 4 || logs[0].Phase != "image_pull_started" || logs[len(logs)-1].Phase != "image_pull_completed" {
		t.Fatalf("expected pull lifecycle and progress logs, got %#v", logs)
	}
	for _, event := range logs {
		if strings.Contains(event.Text, "sk-abcdefghijklmnop") {
			t.Fatalf("daemon log callback leaked pull secret: %#v", event)
		}
	}
}

func TestDockerSandboxAdapterUsesCachedImageWithoutPulling(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1 $2\" = \"image inspect\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"pull\" ]; then exit 23; fi\n" +
		"if [ \"$1\" = \"create\" ]; then echo ctr-cached; exit 0; fi\n" +
		"if [ \"$1\" = \"start\" ]; then echo sandbox-started; exit 0; fi\n" +
		"if [ \"$1\" = \"rm\" ]; then exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	var mu sync.Mutex
	var phases []string
	var logs []runtime.DockerSandboxLogEvent
	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "registry.example/cached:test",
		CreateArgs:   []string{"create", "registry.example/cached:test", "codex", "run"},
		Log: func(event runtime.DockerSandboxLogEvent) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, event)
		},
	})
	err := adapter.Run(context.Background(), "sandbox task", func(_ task.EventKind, payload task.EventPayload) {
		if phase, ok := payload["phase"].(string); ok {
			mu.Lock()
			defer mu.Unlock()
			phases = append(phases, phase)
		}
	})
	if err != nil {
		t.Fatalf("run docker sandbox: %v", err)
	}

	rawCommands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	commands := string(rawCommands)
	if !strings.Contains(commands, "image inspect registry.example/cached:test") {
		t.Fatalf("expected local image inspection, got:\n%s", commands)
	}
	if strings.Contains(commands, "pull registry.example/cached:test") {
		t.Fatalf("cached image must not contact registry, got:\n%s", commands)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, phase := range phases {
		if strings.HasPrefix(phase, "image_pull_") {
			t.Fatalf("cached image must not emit pull lifecycle events, got %v", phases)
		}
	}
	if len(logs) != 0 {
		t.Fatalf("cached image must not emit pull log callbacks, got %#v", logs)
	}
}

func TestDockerSandboxAdapterPreservesCreatePathOnOpaqueImageInspectFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1 $2\" = \"image inspect\" ]; then exit 1; fi\n" +
		"if [ \"$1\" = \"pull\" ]; then echo unexpected-pull >&2; exit 23; fi\n" +
		"if [ \"$1\" = \"create\" ]; then echo ctr-opaque-inspect; exit 0; fi\n" +
		"if [ \"$1\" = \"start\" ]; then echo sandbox-started; exit 0; fi\n" +
		"if [ \"$1\" = \"rm\" ]; then exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	var phases []string
	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "registry.example/opaque:test",
		CreateArgs:   []string{"create", "registry.example/opaque:test", "codex", "run"},
	})
	err := adapter.Run(context.Background(), "sandbox task", func(_ task.EventKind, payload task.EventPayload) {
		if phase, ok := payload["phase"].(string); ok {
			phases = append(phases, phase)
		}
	})
	if err != nil {
		t.Fatalf("opaque inspect failure must preserve create path, got %v", err)
	}

	rawCommands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	commands := string(rawCommands)
	if strings.Contains(commands, "pull registry.example/opaque:test") {
		t.Fatalf("opaque inspect failure must not trigger pull, got:\n%s", commands)
	}
	if !strings.Contains(commands, "create registry.example/opaque:test") {
		t.Fatalf("opaque inspect failure must preserve create, got:\n%s", commands)
	}
	for _, phase := range phases {
		if strings.HasPrefix(phase, "image_pull_") {
			t.Fatalf("opaque inspect failure must not emit pull lifecycle events, got %v", phases)
		}
	}
}

func TestDockerSandboxAdapterReportsPullFailureWithoutCreatingContainer(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1 $2\" = \"image inspect\" ]; then echo 'Error response from daemon: No such object: registry.example/missing:test' >&2; exit 1; fi\n" +
		"if [ \"$1\" = \"pull\" ]; then echo 'registry unavailable' >&2; exit 42; fi\n" +
		"if [ \"$1\" = \"create\" ]; then echo unexpected-create; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	type recordedEvent struct {
		kind    task.EventKind
		payload task.EventPayload
	}
	var mu sync.Mutex
	var events []recordedEvent
	var logs []runtime.DockerSandboxLogEvent
	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "registry.example/missing:test",
		CreateArgs:   []string{"create", "registry.example/missing:test", "codex", "run"},
		Log: func(event runtime.DockerSandboxLogEvent) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, event)
		},
	})
	err := adapter.Run(context.Background(), "sandbox task", func(kind task.EventKind, payload task.EventPayload) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, recordedEvent{kind: kind, payload: payload})
	})
	if err == nil || !strings.Contains(err.Error(), "pull sandbox image") || !strings.Contains(err.Error(), "registry.example/missing:test") {
		t.Fatalf("expected useful image pull error, got %v", err)
	}

	rawCommands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read docker log: %v", readErr)
	}
	if strings.Contains(string(rawCommands), "create registry.example/missing:test") {
		t.Fatalf("container must not be created after pull failure, got:\n%s", string(rawCommands))
	}

	mu.Lock()
	defer mu.Unlock()
	startedAt, progressAt, failedAt := -1, -1, -1
	for i, event := range events {
		phase, _ := event.payload["phase"].(string)
		text, _ := event.payload["text"].(string)
		switch {
		case event.kind == task.EventKindLifecycle && phase == "image_pull_started":
			startedAt = i
		case event.kind == task.EventKindRuntimeOutput && text == "registry unavailable":
			progressAt = i
		case event.kind == task.EventKindLifecycle && phase == "image_pull_failed":
			failedAt = i
			if event.payload["image"] != "registry.example/missing:test" {
				t.Fatalf("expected failed lifecycle image field, got %#v", event.payload)
			}
		}
	}
	if startedAt < 0 || progressAt < 0 || failedAt < 0 || !(startedAt < progressAt && progressAt < failedAt) {
		t.Fatalf("expected started, progress, failed ordering, got %#v", events)
	}
	if len(logs) < 3 || logs[0].Phase != "image_pull_started" || logs[len(logs)-1].Phase != "image_pull_failed" {
		t.Fatalf("expected failure mirrored to log callback, got %#v", logs)
	}
}

func TestDockerSandboxAdapterCancelsImagePullWithoutCreatingContainer(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1 $2\" = \"image inspect\" ]; then echo 'Error response from daemon: No such image: registry.example/cancel:test' >&2; exit 1; fi\n" +
		"if [ \"$1\" = \"pull\" ]; then echo pull-waiting; while :; do :; done; fi\n" +
		"if [ \"$1\" = \"create\" ]; then echo unexpected-create; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var phases []string
	adapter := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name:         "codex",
		ContainerCLI: docker,
		Image:        "registry.example/cancel:test",
		CreateArgs:   []string{"create", "registry.example/cancel:test", "codex", "run"},
	})
	err := adapter.Run(ctx, "sandbox task", func(kind task.EventKind, payload task.EventPayload) {
		if phase, ok := payload["phase"].(string); ok {
			mu.Lock()
			phases = append(phases, phase)
			mu.Unlock()
		}
		if kind == task.EventKindRuntimeOutput && payload["text"] == "pull-waiting" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	rawCommands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read docker log: %v", readErr)
	}
	if strings.Contains(string(rawCommands), "create registry.example/cancel:test") {
		t.Fatalf("container must not be created after canceled pull, got:\n%s", string(rawCommands))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(phases) < 2 || phases[0] != "image_pull_started" || phases[len(phases)-1] != "image_pull_failed" {
		t.Fatalf("expected canceled pull lifecycle, got %v", phases)
	}
}
