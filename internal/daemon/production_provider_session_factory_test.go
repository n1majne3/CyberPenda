package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

type productionFactoryDocker struct {
	mu         sync.Mutex
	createArgs []string
	inputR     *io.PipeReader
	inputW     *io.PipeWriter
	outputR    *io.PipeReader
	outputW    *io.PipeWriter
	diagR      *io.PipeReader
	diagW      *io.PipeWriter
}

func newProductionFactoryDocker() *productionFactoryDocker {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	diagR, diagW := io.Pipe()
	return &productionFactoryDocker{inputR: inR, inputW: inW, outputR: outR, outputW: outW, diagR: diagR, diagW: diagW}
}

func (d *productionFactoryDocker) Create(_ context.Context, args []string) (string, error) {
	d.mu.Lock()
	d.createArgs = append([]string(nil), args...)
	d.mu.Unlock()
	return "bridge-container", nil
}

func (d *productionFactoryDocker) Start(context.Context, string) (runtime.SandboxBridgeIO, error) {
	return runtime.SandboxBridgeIO{Stdin: d.inputW, Stdout: d.outputR, Diagnostics: d.diagR, Wait: func() error { return nil }}, nil
}

func (d *productionFactoryDocker) Stop(context.Context, string) error {
	_ = d.inputR.Close()
	_ = d.outputW.Close()
	_ = d.diagW.Close()
	return nil
}

func (*productionFactoryDocker) Remove(context.Context, string) error { return nil }

func TestProductionProviderSessionFactoryOpensCodexAppServerBridgeWithoutPTY(t *testing.T) {
	docker := newProductionFactoryDocker()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: docker})
	legacy := testProviderLaunch{
		Name: "codex", Image: "sandbox:test",
		CreateArgs: []string{"create", "-e", "CODEX_HOME=/task/runtime-home/codex", "sandbox:test", "codex", "exec", "--model", "gpt-test", "goal"},
	}
	go func() {
		scanner := bufio.NewScanner(docker.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"ok":true}`
			if request.Method == "thread/start" {
				result = `{"thread":{"id":"thread-live"}}`
			}
			_, _ = io.WriteString(docker.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("task-1", "project-test", ""), Continuation: owner.Continuation{ID: "continuation-1", OwnerID: "task-1"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerSandbox,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/task/workdir"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "thread-live" || binding.Adapter == nil {
		t.Fatalf("binding = %#v", binding)
	}
	docker.mu.Lock()
	args := append([]string(nil), docker.createArgs...)
	docker.mu.Unlock()
	joined := strings.Join(args, " ")
	for _, want := range []string{"create", "sandbox:test /usr/local/bin/pentest-provider-bridge --provider codex -- codex app-server"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("create args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, " -t ") || strings.Contains(joined, " --tty ") {
		t.Fatalf("bridge create allocated a terminal: %q", joined)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- binding.Adapter.Run(ctx, "inspect target", func(task.EventKind, task.EventPayload) {}) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("persistent adapter Run error = %v", err)
	}
}

func TestProductionProviderSessionFactoryResumesDurableCodexThread(t *testing.T) {
	docker := newProductionFactoryDocker()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: docker})
	legacy := testProviderLaunch{Name: "codex", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "codex", "exec", "goal"}}
	methods := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(docker.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			methods <- request.Method
			result := `{"ok":true}`
			if request.Method == "thread/resume" {
				params := string(request.Params)
				if !strings.Contains(params, `"approvalPolicy":"never"`) ||
					!strings.Contains(params, `"sandbox":"danger-full-access"`) {
					result = `{"error":"non-interactive sandbox missing"}`
				} else {
					result = `{"thread":{"id":"thread-durable"}}`
				}
			}
			_, _ = io.WriteString(docker.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner:        owner.NewTaskContract("task-restart", "project-test", ""),
		Continuation: owner.Continuation{ID: "continuation-fresh", OwnerID: "task-restart", NativeSessionID: "thread-durable", NativeSessionPath: "/sessions/thread-durable.jsonl"},
		Provider:     runtimeprofile.ProviderCodex, Runner: task.RunnerSandbox,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/task/workdir"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "thread-durable" {
		t.Fatalf("resumed session id = %q", binding.Session.SessionID())
	}
	select {
	case method := <-methods:
		if method != "initialize" {
			t.Fatalf("first setup method = %q, want initialize", method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initialize")
	}
	select {
	case method := <-methods:
		if method != "thread/resume" {
			t.Fatalf("second setup method = %q, want thread/resume", method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thread/resume")
	}
}

func TestProductionProviderSessionFactoryFailsClosedOnChangedDurableThread(t *testing.T) {
	docker := newProductionFactoryDocker()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: docker})
	legacy := testProviderLaunch{Name: "codex", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "codex", "exec", "goal"}}
	go func() {
		scanner := bufio.NewScanner(docker.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"ok":true}`
			if request.Method == "thread/resume" {
				result = `{"thread":{"id":"thread-other"}}`
			}
			_, _ = io.WriteString(docker.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner:        owner.NewTaskContract("task-restart-mismatch", "project-test", ""),
		Continuation: owner.Continuation{ID: "continuation-fresh", OwnerID: "task-restart-mismatch", NativeSessionID: "thread-durable"},
		Provider:     runtimeprofile.ProviderCodex, Runner: task.RunnerSandbox,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/task/workdir"}),
	})
	if err == nil || !strings.Contains(err.Error(), "resume identity changed") {
		t.Fatalf("resume mismatch error = %v", err)
	}
	docker.mu.Lock()
	created := docker.createArgs
	docker.mu.Unlock()
	if len(created) == 0 {
		t.Fatal("expected bridge container to be created before fail-closed cleanup")
	}
	if _, ok := factory.bridges.Get("task-restart-mismatch"); ok {
		t.Fatal("failed resume retained stale bridge registry ownership")
	}
}

func TestProductionProviderSessionFactoryOpensClaudeAgentSDKBridge(t *testing.T) {
	docker := newProductionFactoryDocker()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: docker})
	legacy := testProviderLaunch{Name: "claude_code", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "claude", "--model", "claude-test", "--settings", "/task/runtime-home/claude/settings.json", "--print", "goal"}}
	methods := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(docker.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			methods <- request.Method
			_, _ = io.WriteString(docker.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":{"session_id":"claude-durable","status":"ready","capabilities":{"persistent_session":true,"send_turn":true,"interrupt_turn":true}}}`+"\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("task-claude", "project-test", ""), Continuation: owner.Continuation{ID: "continuation-claude", OwnerID: "task-claude", NativeSessionID: "claude-durable"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerSandbox,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/task/workdir", Model: "claude-test", SettingsPath: "/task/runtime-home/claude/settings.json"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "claude-durable" || !binding.Session.Capabilities().InterruptThenReplace || binding.Session.Capabilities().InTurnSteer {
		t.Fatalf("Claude binding = %#v capabilities=%#v", binding, binding.Session.Capabilities())
	}
	select {
	case method := <-methods:
		if method != "claude/initialize" {
			t.Fatalf("Claude setup method = %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Claude setup")
	}
	docker.mu.Lock()
	joined := strings.Join(docker.createArgs, " ")
	docker.mu.Unlock()
	if !strings.Contains(joined, "sandbox:test /usr/local/bin/pentest-claude-sdk-bridge --cwd /task/workdir --model claude-test --settings /task/runtime-home/claude/settings.json --resume claude-durable") {
		t.Fatalf("Claude create args = %q", joined)
	}
	if strings.Contains(joined, " -t ") || strings.Contains(joined, " --tty ") {
		t.Fatalf("Claude bridge allocated a terminal: %q", joined)
	}
}

func TestProductionProviderSessionFactoryOpensPiRPCBridge(t *testing.T) {
	docker := newProductionFactoryDocker()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: docker})
	legacy := testProviderLaunch{Name: "pi", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "pi", "--mode", "json", "goal"}}
	methods := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(docker.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			methods <- request.Method
			result := `{"session_id":"pi-session","session_path":"/sessions/pi-session.jsonl"}`
			_, _ = io.WriteString(docker.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("task-pi", "project-test", ""), Continuation: owner.Continuation{ID: "continuation-pi", OwnerID: "task-pi"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerSandbox,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/task/workdir"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "pi-session" || !binding.Session.Capabilities().InTurnSteer {
		t.Fatalf("Pi binding = %#v capabilities=%#v", binding, binding.Session.Capabilities())
	}
	select {
	case method := <-methods:
		if method != "pi/get_state" {
			t.Fatalf("Pi setup method = %q, want pi/get_state", method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Pi setup")
	}
	docker.mu.Lock()
	joined := strings.Join(docker.createArgs, " ")
	docker.mu.Unlock()
	if !strings.Contains(joined, "sandbox:test /usr/local/bin/pentest-provider-bridge --provider pi -- pi --mode rpc") {
		t.Fatalf("Pi create args = %q", joined)
	}
	if strings.Contains(joined, " -t ") || strings.Contains(joined, " --tty ") {
		t.Fatalf("Pi bridge allocated a terminal: %q", joined)
	}
}

func TestProductionProviderSessionFactoryUsesLaunchContainerCLI(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "cli.log")
	podman := filepath.Join(dir, "podman")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + logPath + "\n" +
		"echo 'podman create refused for test' 1>&2\n" +
		"exit 1\n"
	if err := os.WriteFile(podman, []byte(script), 0o700); err != nil {
		t.Fatalf("write podman stub: %v", err)
	}
	// Factory transport defaults to bare "docker" (not on PATH in this test).
	// A regression that ignores the launch CLI fails with exec docker not found
	// instead of reaching the podman stub.
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		Docker: runtime.DockerCLISandboxBridgeDocker{ContainerCLI: "docker"},
	})
	legacy := testProviderLaunch{
		Name: "claude_code", ContainerCLI: podman, Image: "sandbox:test",
		CreateArgs: []string{"create", "-i", "sandbox:test", "claude", "--print"},
	}
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner:        owner.NewSessionContract("session-podman-cli", ""),
		Continuation: owner.Continuation{ID: "continuation-1", OwnerID: "session-podman-cli"},
		Provider:     runtimeprofile.ProviderClaudeCode, Runner: task.RunnerSandbox,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/task/workdir"}),
	})
	if err == nil {
		t.Fatal("expected create failure from podman stub")
	}
	if strings.Contains(err.Error(), `executable file not found`) {
		t.Fatalf("bridge still invoked missing docker binary: %v", err)
	}
	if !strings.Contains(err.Error(), "create sandbox bridge container") {
		t.Fatalf("error = %v, want create sandbox bridge container failure", err)
	}
	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("podman was never invoked: %v", readErr)
	}
	if !strings.Contains(string(raw), "create") {
		t.Fatalf("expected podman create log, got:\n%s", raw)
	}
	if !strings.Contains(err.Error(), "podman create refused for test") {
		t.Fatalf("error = %v, want podman stderr detail", err)
	}
}

type testProviderLaunch struct {
	Name, Program, Image, ContainerCLI, Workdir string
	Args, CreateArgs                            []string
	Env                                         map[string]string
}

func (l testProviderLaunch) facts(facts ProviderSessionLaunchFacts) ProviderSessionLaunchFacts {
	if facts.Workdir == "" {
		facts.Workdir = l.Workdir
	}
	facts.Env = l.Env
	facts.SandboxContainerCLI = l.ContainerCLI
	facts.SandboxCreateArgs = l.CreateArgs
	return facts
}
