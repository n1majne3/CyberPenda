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
	legacy := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{
		Name: "codex", Image: "sandbox:test",
		CreateArgs: []string{"create", "-e", "CODEX_HOME=/task/runtime-home/codex", "sandbox:test", "codex", "exec", "--model", "gpt-test", "goal"},
	})
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
		Task: task.Task{ID: "task-1"}, Continuation: task.TaskContinuation{ID: "continuation-1"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerSandbox, LegacyAdapter: legacy,
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
	legacy := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{Name: "codex", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "codex", "exec", "goal"}})
	methods := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(docker.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			methods <- request.Method
			result := `{"ok":true}`
			if request.Method == "thread/resume" {
				result = `{"thread":{"id":"thread-durable"}}`
			}
			_, _ = io.WriteString(docker.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task:         task.Task{ID: "task-restart"},
		Continuation: task.TaskContinuation{ID: "continuation-fresh", NativeSessionID: "thread-durable", NativeSessionPath: "/sessions/thread-durable.jsonl"},
		Provider:     runtimeprofile.ProviderCodex, Runner: task.RunnerSandbox, LegacyAdapter: legacy,
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
	legacy := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{Name: "codex", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "codex", "exec", "goal"}})
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
		Task:         task.Task{ID: "task-restart-mismatch"},
		Continuation: task.TaskContinuation{ID: "continuation-fresh", NativeSessionID: "thread-durable"},
		Provider:     runtimeprofile.ProviderCodex, Runner: task.RunnerSandbox, LegacyAdapter: legacy,
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
	legacy := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{Name: "claude_code", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "claude", "--model", "claude-test", "--settings", "/task/runtime-home/claude/settings.json", "--print", "goal"}})
	methods := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(docker.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			methods <- request.Method
			_, _ = io.WriteString(docker.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":{"session_id":"claude-durable","status":"ready"}}`+"\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "task-claude"}, Continuation: task.TaskContinuation{ID: "continuation-claude", NativeSessionID: "claude-durable"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerSandbox, LegacyAdapter: legacy,
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
	legacy := runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{Name: "pi", Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", "pi", "--mode", "json", "goal"}})
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
		Task: task.Task{ID: "task-pi"}, Continuation: task.TaskContinuation{ID: "continuation-pi"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerSandbox, LegacyAdapter: legacy,
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

type productionFactoryHostStarter struct {
	mu      sync.Mutex
	specs   []runtime.HostProcessSpec
	inputR  *io.PipeReader
	inputW  *io.PipeWriter
	outputR *io.PipeReader
	outputW *io.PipeWriter
	diagR   *io.PipeReader
	diagW   *io.PipeWriter
	pgid    int
	killed  bool
}

func newProductionFactoryHostStarter() *productionFactoryHostStarter {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	diagR, diagW := io.Pipe()
	return &productionFactoryHostStarter{inputR: inR, inputW: inW, outputR: outR, outputW: outW, diagR: diagR, diagW: diagW, pgid: 4242}
}

func (s *productionFactoryHostStarter) Start(_ context.Context, spec runtime.HostProcessSpec) (runtime.HostProcessHandle, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	s.mu.Unlock()
	return runtime.HostProcessHandle{
		IO:             runtime.SandboxBridgeIO{Stdin: s.inputW, Stdout: s.outputR, Diagnostics: s.diagR, Wait: func() error { return nil }},
		ProcessGroupID: s.pgid,
		KillProcessGroup: func(context.Context) error {
			s.mu.Lock()
			s.killed = true
			s.mu.Unlock()
			_ = s.inputR.Close()
			_ = s.outputW.Close()
			_ = s.diagW.Close()
			return nil
		},
	}, nil
}

func (s *productionFactoryHostStarter) lastSpec() runtime.HostProcessSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) == 0 {
		return runtime.HostProcessSpec{}
	}
	return s.specs[len(s.specs)-1]
}

func TestProductionProviderSessionFactoryOpensHostCodexAppServer(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "codex", Program: "/opt/codex", Args: []string{"exec", "--model", "gpt-test", "--json", "--strict-mode", "inspect target"},
		Workdir: "/tmp/task-workdir", Env: map[string]string{"CODEX_HOME": "/tmp/codex-home"},
	})
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"ok":true}`
			switch request.Method {
			case "thread/start":
				if !strings.Contains(string(request.Params), "/tmp/task-workdir") {
					result = `{"error":"cwd missing"}`
				} else {
					result = `{"thread":{"id":"host-thread-1"}}`
				}
			case "turn/start":
				result = `{"turnId":"turn-1"}`
			}
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-task-1"}, Continuation: task.TaskContinuation{ID: "host-continuation-1"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "host-thread-1" || binding.Adapter == nil {
		t.Fatalf("binding = %#v", binding)
	}
	adapter, ok := binding.Adapter.(*runtime.ProviderSessionRunAdapter)
	if !ok {
		t.Fatalf("adapter type = %T", binding.Adapter)
	}
	var recordedMu sync.Mutex
	var recorded runtime.NativeSessionMetadata
	adapter.SetMetadataRecorder(func(meta runtime.NativeSessionMetadata) error {
		recordedMu.Lock()
		recorded = meta
		recordedMu.Unlock()
		return nil
	})
	// Drive one launch turn so metadata (including durable host process-group
	// identity for daemon-restart cleanup) is recorded.
	runCtx, runCancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- adapter.Run(runCtx, "inspect target", func(task.EventKind, task.EventPayload) {}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recordedMu.Lock()
		gotID, gotContainer := recorded.NativeSessionID, recorded.ContainerID
		recordedMu.Unlock()
		if gotID == "host-thread-1" && gotContainer == "host-pgid:4242" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runCancel()
	<-runDone
	recordedMu.Lock()
	finalMeta := recorded
	recordedMu.Unlock()
	if finalMeta.NativeSessionID != "host-thread-1" || finalMeta.ContainerID != "host-pgid:4242" {
		t.Fatalf("recorded host metadata = %#v", finalMeta)
	}
	spec := starter.lastSpec()
	if spec.Program != "/opt/codex" || len(spec.Args) == 0 || spec.Args[0] != "app-server" {
		t.Fatalf("host process spec = %#v", spec)
	}
	if spec.Workdir != "/tmp/task-workdir" || spec.Env["CODEX_HOME"] != "/tmp/codex-home" {
		t.Fatalf("host process workdir/env = %#v", spec)
	}
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "--strict-mode") || !strings.Contains(joined, "--json") {
		t.Fatalf("non-conflicting custom args not preserved: %q", joined)
	}
	if strings.Contains(joined, "gpt-test") || strings.Contains(joined, "inspect target") {
		t.Fatalf("structured model/goal leaked into app-server args: %q", joined)
	}

	// Same Task reuses the bound session without starting another process.
	rebound, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-task-1"}, Continuation: task.TaskContinuation{ID: "host-continuation-2"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Session != binding.Session {
		t.Fatal("host factory replaced Task session on second Open")
	}
	starter.mu.Lock()
	starts := len(starter.specs)
	starter.mu.Unlock()
	if starts != 1 {
		t.Fatalf("host process starts = %d, want 1", starts)
	}
}

func TestProductionProviderSessionFactoryResumesHostCodexThread(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "codex", Program: "codex", Args: []string{"exec", "goal"}, Workdir: "/work",
	})
	methods := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			methods <- request.Method
			result := `{"ok":true}`
			if request.Method == "thread/resume" {
				result = `{"thread":{"id":"host-durable"}}`
			}
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-resume"}, Continuation: task.TaskContinuation{ID: "c1", NativeSessionID: "host-durable"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "host-durable" {
		t.Fatalf("session id = %q", binding.Session.SessionID())
	}
	select {
	case method := <-methods:
		if method != "initialize" {
			t.Fatalf("first method = %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initialize")
	}
	select {
	case method := <-methods:
		if method != "thread/resume" {
			t.Fatalf("second method = %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thread/resume")
	}
}

func TestProductionProviderSessionFactoryHostCloseKillsProcessGroup(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{Name: "codex", Program: "codex", Args: []string{"exec", "goal"}, Workdir: "/work"})
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"ok":true}`
			if request.Method == "thread/start" {
				result = `{"thread":{"id":"t1"}}`
			}
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-kill"}, Continuation: task.TaskContinuation{ID: "c1"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	starter.mu.Lock()
	killed := starter.killed
	starter.mu.Unlock()
	if !killed {
		t.Fatal("host close did not kill process group")
	}
	if _, ok := factory.hostBridges.Get("host-kill"); ok {
		t.Fatal("host bridge registry retained closed Task")
	}
}

func TestProductionProviderSessionFactoryRejectsUnsupportedHostProvider(t *testing.T) {
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{Name: "fake", Program: "fake", Args: []string{"goal"}, Workdir: "/work"})
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "t"}, Continuation: task.TaskContinuation{ID: "c"},
		Provider: runtimeprofile.ProviderFake, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err == nil || !strings.Contains(err.Error(), "codex, claude_code, and pi only") {
		t.Fatalf("error = %v", err)
	}
}

func TestProductionProviderSessionFactoryOpensHostClaudeSDKBridge(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter:            starter,
		ClaudeSDKBridgeCommand: "/opt/pentest/pentest-claude-sdk-bridge",
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "claude_code", Program: "/opt/claude",
		Args: []string{
			"--model", "claude-test",
			"--settings", "/tmp/claude-home/settings.json",
			"--strict-mcp-config", "--mcp-config", "/tmp/task-workdir/.mcp.json",
			"-p", "--output-format", "stream-json", "--verbose",
			"--dangerously-skip-permissions", "--permission-mode", "bypassPermissions",
			"--add-dir", "/tmp/extra",
			"inspect target",
		},
		Workdir: "/tmp/task-workdir",
		Env: map[string]string{
			"CLAUDE_HOME":          "/tmp/claude-home",
			"ANTHROPIC_API_KEY":    "sk-test",
			"ANTHROPIC_AUTH_TOKEN": "token-test",
		},
	})
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"session_id":"host-claude-1","status":"ready"}`
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-claude-1"}, Continuation: task.TaskContinuation{ID: "c1"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost, LegacyAdapter: legacy,
		RuntimeConfig: map[string]any{"launch_model_override": "claude-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "host-claude-1" || binding.Adapter == nil {
		t.Fatalf("binding = %#v", binding)
	}
	if !binding.Session.Capabilities().PersistentSession || !binding.Session.Capabilities().InterruptThenReplace {
		t.Fatalf("capabilities = %#v", binding.Session.Capabilities())
	}

	adapter, ok := binding.Adapter.(*runtime.ProviderSessionRunAdapter)
	if !ok {
		t.Fatalf("adapter type = %T", binding.Adapter)
	}
	var recordedMu sync.Mutex
	var recorded runtime.NativeSessionMetadata
	adapter.SetMetadataRecorder(func(meta runtime.NativeSessionMetadata) error {
		recordedMu.Lock()
		recorded = meta
		recordedMu.Unlock()
		return nil
	})
	runCtx, runCancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- adapter.Run(runCtx, "inspect target", func(task.EventKind, task.EventPayload) {}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recordedMu.Lock()
		gotID, gotContainer := recorded.NativeSessionID, recorded.ContainerID
		recordedMu.Unlock()
		if gotID == "host-claude-1" && gotContainer == "host-pgid:4242" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runCancel()
	<-runDone
	recordedMu.Lock()
	finalMeta := recorded
	recordedMu.Unlock()
	if finalMeta.NativeSessionID != "host-claude-1" || finalMeta.ContainerID != "host-pgid:4242" {
		t.Fatalf("recorded host Claude metadata = %#v", finalMeta)
	}

	spec := starter.lastSpec()
	// Explicit packaged bridge command — not the Claude CLI and not repo Node sources.
	if spec.Program != "/opt/pentest/pentest-claude-sdk-bridge" {
		t.Fatalf("host Claude program = %q, want packaged SDK bridge", spec.Program)
	}
	if strings.Contains(spec.Program, "cmd/pentest-claude-sdk-bridge") || strings.HasSuffix(spec.Program, "main.mjs") {
		t.Fatalf("must not launch repo-relative Node bridge source: %q", spec.Program)
	}
	joined := strings.Join(spec.Args, " ")
	for _, want := range []string{
		"--cwd /tmp/task-workdir",
		"--model claude-test",
		"--settings /tmp/claude-home/settings.json",
		"--add-dir /tmp/extra",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bridge argv missing %q: %q", want, joined)
		}
	}
	// Structured one-shot / non-interactive helpers must not leak; MCP/settings
	// survive via projected settings path and CLAUDE_HOME env, not CLI re-print.
	for _, leak := range []string{"inspect target", "-p", "--print", "stream-json", "--verbose", "dangerously-skip-permissions", "--mcp-config"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("one-shot helper leaked into SDK bridge argv (%q): %q", leak, joined)
		}
	}
	if spec.Workdir != "/tmp/task-workdir" {
		t.Fatalf("workdir = %q", spec.Workdir)
	}
	if spec.Env["CLAUDE_HOME"] != "/tmp/claude-home" || spec.Env["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Fatalf("projected env not preserved: %#v", spec.Env)
	}

	// Same Task reuses one Query process.
	rebound, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-claude-1"}, Continuation: task.TaskContinuation{ID: "c2"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Session != binding.Session {
		t.Fatal("host Claude factory replaced Task session on second Open")
	}
	starter.mu.Lock()
	starts := len(starter.specs)
	starter.mu.Unlock()
	if starts != 1 {
		t.Fatalf("host Claude process starts = %d, want 1", starts)
	}
}

func TestProductionProviderSessionFactoryResumesHostClaudeQuery(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter:            starter,
		ClaudeSDKBridgeCommand: "/usr/local/bin/pentest-claude-sdk-bridge",
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "claude_code", Program: "claude",
		Args:    []string{"--model", "claude-test", "--settings", "/home/settings.json", "-p", "goal"},
		Workdir: "/work",
	})
	methods := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			methods <- request.Method
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":{"session_id":"claude-durable","status":"ready"}}`+"\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task:         task.Task{ID: "host-claude-resume"},
		Continuation: task.TaskContinuation{ID: "c1", NativeSessionID: "claude-durable"},
		Provider:     runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "claude-durable" {
		t.Fatalf("session id = %q", binding.Session.SessionID())
	}
	select {
	case method := <-methods:
		if method != "claude/initialize" {
			t.Fatalf("setup method = %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for claude/initialize")
	}
	spec := starter.lastSpec()
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "--resume claude-durable") {
		t.Fatalf("resume missing from bridge argv: %q", joined)
	}
}

func TestProductionProviderSessionFactoryHostClaudeCloseKillsProcessGroup(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter:            starter,
		ClaudeSDKBridgeCommand: "/usr/local/bin/pentest-claude-sdk-bridge",
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "claude_code", Program: "claude", Args: []string{"-p", "goal"}, Workdir: "/work",
	})
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":{"session_id":"c1","status":"ready"}}`+"\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-claude-kill"}, Continuation: task.TaskContinuation{ID: "c1"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	starter.mu.Lock()
	killed := starter.killed
	starter.mu.Unlock()
	if !killed {
		t.Fatal("host Claude close did not kill process group")
	}
	if _, ok := factory.hostBridges.Get("host-claude-kill"); ok {
		t.Fatal("host bridge registry retained closed Claude Task")
	}
}

func TestProductionProviderSessionFactoryHostClaudeBridgeUnavailableIsClear(t *testing.T) {
	// Production starter with a missing packaged bridge path must fail closed
	// with an explicit unavailable error — never a silent one-shot CLI path.
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		ClaudeSDKBridgeCommand: filepath.Join(t.TempDir(), "missing-pentest-claude-sdk-bridge"),
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "claude_code", Program: "claude", Args: []string{"-p", "goal"}, Workdir: t.TempDir(),
	})
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-claude-missing"}, Continuation: task.TaskContinuation{ID: "c1"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err == nil {
		t.Fatal("expected clear error when packaged Claude SDK bridge is unavailable")
	}
	if !strings.Contains(err.Error(), "Claude SDK bridge unavailable") {
		t.Fatalf("error = %v, want Claude SDK bridge unavailable", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "npm install") || strings.Contains(err.Error(), "main.mjs") {
		t.Fatalf("must not suggest repo Node/npm fallback: %v", err)
	}
	if _, ok := factory.hostBridges.Get("host-claude-missing"); ok {
		t.Fatal("failed open retained host bridge ownership")
	}
}

func TestHostClaudeCustomArgsPreservesNonConflicting(t *testing.T) {
	got := hostClaudeCustomArgs([]string{
		"--model", "claude-test",
		"--settings", "/tmp/settings.json",
		"--strict-mcp-config", "--mcp-config", "/tmp/.mcp.json",
		"-p", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--permission-mode", "bypassPermissions",
		"--add-dir", "/extra", "--debug",
		"inspect goal",
	})
	want := []string{"--add-dir", "/extra", "--debug"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("custom args = %#v, want %#v", got, want)
	}
}

func TestHostClaudeReservedFlagsStayRejectedByValidateCustomArgs(t *testing.T) {
	reserved := [][]string{
		{"--model", "claude-x"},
		{"--effort", "high"},
	}
	for _, args := range reserved {
		err := runtimeprofile.ValidateCustomArgs(runtimeprofile.ProviderClaudeCode, args)
		if err == nil {
			t.Fatalf("ValidateCustomArgs must reject %v", args)
		}
		var conflict *runtimeprofile.CustomArgConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("ValidateCustomArgs(%v) = %T %v, want CustomArgConflictError", args, err, err)
		}
	}
	if err := runtimeprofile.ValidateCustomArgs(runtimeprofile.ProviderClaudeCode, []string{"--add-dir", "/extra"}); err != nil {
		t.Fatalf("ValidateCustomArgs must allow non-conflicting --add-dir: %v", err)
	}
}

func TestProductionProviderSessionFactoryOpensHostPiRPCViaWireBridge(t *testing.T) {
	// Host Pi must retain the piWire translation boundary: HostSessionBridge
	// speaks CyberPenda JSON-RPC to the explicit bridge executable, which owns
	// the Pi native RPC child.
	starter := newProductionFactoryHostStarter()
	bridgePath := "/opt/cyberpenda/bin/pentest-provider-bridge"
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter: starter, HostBridgeCommand: bridgePath,
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "pi", Program: "/usr/local/bin/pi",
		Args:    []string{"--provider", "primary", "--model", "m1", "--mode", "json", "--debug", "inspect target"},
		Workdir: "/tmp/task-workdir",
		Env: map[string]string{
			"PI_CODING_AGENT_DIR":         "/tmp/task-workdir/runtime-home/pi/agent",
			"PI_CODING_AGENT_SESSION_DIR": "/tmp/task-workdir/runtime-home/pi/agent/sessions",
			"OPENAI_API_KEY":              "sk-projected-1",
			"ANTHROPIC_API_KEY":           "sk-projected-2",
		},
	})
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"ok":true}`
			if request.Method == "pi/get_state" {
				result = `{"session_id":"host-pi-1","session_path":"/tmp/task-workdir/runtime-home/pi/agent/sessions/host-pi-1.jsonl"}`
			}
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-pi-task"}, Continuation: task.TaskContinuation{ID: "host-pi-cont"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	if binding.Session.SessionID() != "host-pi-1" || !binding.Session.Capabilities().InTurnSteer {
		t.Fatalf("binding = %#v capabilities=%#v", binding, binding.Session.Capabilities())
	}
	spec := starter.lastSpec()
	if spec.Program != bridgePath {
		t.Fatalf("host bridge program = %q, want explicit HostBridgeCommand %q", spec.Program, bridgePath)
	}
	joined := strings.Join(append([]string{spec.Program}, spec.Args...), " ")
	if !strings.Contains(joined, bridgePath+" --provider pi -- /usr/local/bin/pi --mode rpc") {
		t.Fatalf("host pi wire bridge argv = %q", joined)
	}
	if strings.Contains(joined, "--mode json") {
		t.Fatalf("one-shot json mode leaked into host pi RPC: %q", joined)
	}
	if !strings.Contains(joined, "--session-id host-pi-task") {
		t.Fatalf("stable session-id missing: %q", joined)
	}
	if !strings.Contains(joined, "--debug") {
		t.Fatalf("non-conflicting custom args not preserved: %q", joined)
	}
	if strings.Contains(joined, "m1") || strings.Contains(joined, "inspect target") || strings.Contains(joined, "primary") {
		t.Fatalf("structured model/provider/goal leaked into RPC args: %q", joined)
	}
	if spec.Env["PI_CODING_AGENT_DIR"] != "/tmp/task-workdir/runtime-home/pi/agent" {
		t.Fatalf("PI_CODING_AGENT_DIR not preserved: %#v", spec.Env)
	}
	if spec.Env["PI_CODING_AGENT_SESSION_DIR"] == "" {
		t.Fatal("PI_CODING_AGENT_SESSION_DIR not preserved")
	}
	if spec.Env["OPENAI_API_KEY"] != "sk-projected-1" || spec.Env["ANTHROPIC_API_KEY"] != "sk-projected-2" {
		t.Fatalf("launch-ready credentials not preserved: %#v", spec.Env)
	}

	// Same Task reuses the bound RPC session without starting another process.
	rebound, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-pi-task"}, Continuation: task.TaskContinuation{ID: "host-pi-cont-2"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Session != binding.Session {
		t.Fatal("host pi factory replaced Task session on second Open")
	}
	starter.mu.Lock()
	starts := len(starter.specs)
	starter.mu.Unlock()
	if starts != 1 {
		t.Fatalf("host process starts = %d, want 1", starts)
	}
}

func TestProductionProviderSessionFactoryHostPiCloseCleansProcessGroupAndArtifacts(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	agentDir := t.TempDir()
	sessionDir := filepath.Join(agentDir, "sessions", "cwd")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(agentDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"providers":{"p":{"key":"sk-secret"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "sess.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter: starter, HostBridgeCommand: "/bridge/pi-wire",
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "pi", Program: "pi", Args: []string{"--mode", "json", "goal"}, Workdir: "/work",
		Env: map[string]string{
			"PI_CODING_AGENT_DIR":         agentDir,
			"PI_CODING_AGENT_SESSION_DIR": filepath.Join(agentDir, "sessions"),
		},
	})
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"session_id":"pi-clean","session_path":` + quoteJSON(sessionPath) + `}`
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-pi-clean"}, Continuation: task.TaskContinuation{ID: "c1"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := binding.Adapter.(*runtime.ProviderSessionRunAdapter)
	if !ok {
		t.Fatalf("adapter type = %T", binding.Adapter)
	}
	var recordedMu sync.Mutex
	var recorded runtime.NativeSessionMetadata
	adapter.SetMetadataRecorder(func(meta runtime.NativeSessionMetadata) error {
		recordedMu.Lock()
		recorded = meta
		recordedMu.Unlock()
		return nil
	})
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(runCtx, "goal", func(task.EventKind, task.EventPayload) {}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recordedMu.Lock()
		gotID, gotContainer := recorded.NativeSessionID, recorded.ContainerID
		recordedMu.Unlock()
		if gotID == "pi-clean" && gotContainer == "host-pgid:4242" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	recordedMu.Lock()
	finalMetadata := recorded
	recordedMu.Unlock()
	if finalMetadata.NativeSessionID != "pi-clean" || finalMetadata.ContainerID != "host-pgid:4242" {
		t.Fatalf("host pi metadata = %#v", finalMetadata)
	}
	if err := binding.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	starter.mu.Lock()
	killed := starter.killed
	starter.mu.Unlock()
	if !killed {
		t.Fatal("host pi close did not kill process group")
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("auth.json must be cleaned after close, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("session files must be cleaned after close, err=%v", err)
	}
	if _, err := os.Stat(modelsPath); err != nil {
		t.Fatalf("models.json should remain for diagnostics: %v", err)
	}
}

func TestProductionProviderSessionFactoryHostPiFailsWhenBridgeUnavailable(t *testing.T) {
	// No HostStarter: production path validates the explicit bridge executable.
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostBridgeCommand: filepath.Join(t.TempDir(), "missing-bridge"),
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "pi", Program: "pi", Args: []string{"--mode", "json", "goal"}, Workdir: "/work",
		Env: map[string]string{
			"PI_CODING_AGENT_DIR":         "/tmp/agent",
			"PI_CODING_AGENT_SESSION_DIR": "/tmp/agent/sessions",
		},
	})
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "t"}, Continuation: task.TaskContinuation{ID: "c"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err == nil || !strings.Contains(err.Error(), "host pi provider bridge is unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestProductionProviderSessionFactoryHostPiFailsWhenProjectedEnvMissing(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter: starter, HostBridgeCommand: "/bridge",
	})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "pi", Program: "pi", Args: []string{"goal"}, Workdir: "/work",
		Env: map[string]string{}, // missing projected Pi dirs
	})
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "t"}, Continuation: task.TaskContinuation{ID: "c"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err == nil || !strings.Contains(err.Error(), "PI_CODING_AGENT_DIR") {
		t.Fatalf("error = %v", err)
	}
}

func TestHostPiCustomArgsPreservesNonConflicting(t *testing.T) {
	got := hostPiCustomArgs([]string{
		"--provider", "primary", "--model", "m1", "--mode", "json",
		"--debug", "--print-config", "inspect goal",
	})
	want := []string{"--debug", "--print-config"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("custom args = %#v, want %#v", got, want)
	}
}

func TestHostCodexCustomArgsPreservesNonConflicting(t *testing.T) {
	got := hostCodexCustomArgs([]string{
		"exec", "--model", "gpt-test", "--dangerously-bypass-approvals-and-sandbox",
		"--json", "--strict-mode", "--flag=value", "inspect goal",
	})
	want := []string{"--json", "--strict-mode", "--flag=value"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("custom args = %#v, want %#v", got, want)
	}
}

func TestHostCodexCustomArgsPreservesNonConflictingConfigOverrides(t *testing.T) {
	// One-shot host argv includes structured --model plus user Custom Args.
	// -c/--config/--config-file/--profile are not harness-owned; non-conflicting
	// forms must survive app-server assembly. Only one-shot subcommands,
	// structured model flags, non-interactive defaults, and the goal drop.
	got := hostCodexCustomArgs([]string{
		"exec", "--model", "gpt-test",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"-c", "foo=bar",
		"--config", "features.foo=true",
		"--config-file", "/tmp/extra.toml",
		"--profile", "operator-profile",
		"-c", "sandbox_workspace_write.network_access=true",
		"--json",
		"--full-auto",
		"inspect goal",
	})
	want := []string{
		"-c", "foo=bar",
		"--config", "features.foo=true",
		"--config-file", "/tmp/extra.toml",
		"--profile", "operator-profile",
		"-c", "sandbox_workspace_write.network_access=true",
		"--json",
		"--full-auto",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("custom args = %#v, want %#v", got, want)
	}
}

func TestHostCodexAppServerAssemblyPreservesDashCCustomArgs(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{
		Name: "codex", Program: "/opt/codex",
		Args: []string{
			"exec", "--model", "gpt-test",
			"--dangerously-bypass-approvals-and-sandbox",
			"-c", "foo=bar", "--json",
			"inspect target",
		},
		Workdir: "/tmp/task-workdir",
	})
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"ok":true}`
			if request.Method == "thread/start" {
				result = `{"thread":{"id":"host-thread-c"}}`
			}
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "host-task-c"}, Continuation: task.TaskContinuation{ID: "c1"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Session.Close(context.Background())
	spec := starter.lastSpec()
	joined := strings.Join(append([]string{spec.Program}, spec.Args...), " ")
	if !strings.Contains(joined, "app-server") || !strings.Contains(joined, "-c foo=bar") {
		t.Fatalf("host app-server argv lost non-conflicting -c: %q", joined)
	}
	if strings.Contains(joined, "gpt-test") || strings.Contains(joined, "inspect target") {
		t.Fatalf("structured model/goal leaked into app-server argv: %q", joined)
	}
}

func TestHostCodexReservedConfigOverridesStayRejectedByValidateCustomArgs(t *testing.T) {
	// Assembly must not reimplement #148. Reserved model/provider/effort config
	// forms remain fail-closed at the existing validation seam.
	reserved := [][]string{
		{"-c", "model=gpt-5"},
		{"-c", "model_provider=custom"},
		{"-c", "model_reasoning_effort=high"},
		{"--config", "model_reasoning_effort=xhigh"},
	}
	for _, args := range reserved {
		err := runtimeprofile.ValidateCustomArgs(runtimeprofile.ProviderCodex, args)
		if err == nil {
			t.Fatalf("ValidateCustomArgs must reject %v", args)
		}
		var conflict *runtimeprofile.CustomArgConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("ValidateCustomArgs(%v) = %T %v, want CustomArgConflictError", args, err, err)
		}
	}
	// Non-conflicting -c is accepted by the same seam.
	if err := runtimeprofile.ValidateCustomArgs(runtimeprofile.ProviderCodex, []string{"-c", "foo=bar", "--json"}); err != nil {
		t.Fatalf("ValidateCustomArgs must allow non-conflicting -c foo=bar: %v", err)
	}
}
