package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestProductionProviderSessionFactoryRejectsHostNonCodex(t *testing.T) {
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{})
	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{Name: "claude", Program: "claude", Args: []string{"-p", "goal"}})
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Task: task.Task{ID: "t"}, Continuation: task.TaskContinuation{ID: "c"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost, LegacyAdapter: legacy,
	})
	if err == nil || !strings.Contains(err.Error(), "codex only") {
		t.Fatalf("error = %v", err)
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
