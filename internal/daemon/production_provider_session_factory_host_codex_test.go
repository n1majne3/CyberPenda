package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

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
	legacy := testProviderLaunch{
		Name: "codex", Program: "/opt/codex", Args: []string{"exec", "--model", "gpt-test", "--json", "--strict-mode", "inspect target"},
		Workdir: "/tmp/task-workdir", Env: map[string]string{"CODEX_HOME": "/tmp/codex-home"},
	}
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			result := `{"ok":true}`
			switch request.Method {
			case "thread/start":
				params := string(request.Params)
				if !strings.Contains(params, "/tmp/task-workdir") ||
					!strings.Contains(params, `"approvalPolicy":"never"`) ||
					!strings.Contains(params, `"sandbox":"danger-full-access"`) {
					result = `{"error":"non-interactive sandbox missing"}`
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
		Owner: owner.NewTaskContract("host-task-1", "project-test", ""), Continuation: owner.Continuation{ID: "host-continuation-1", OwnerID: "host-task-1"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{ProviderBinary: "/opt/codex", CustomArgs: []string{"--json", "--strict-mode"}, Workdir: "/tmp/task-workdir"}),
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

	rebound, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("host-task-1", "project-test", ""), Continuation: owner.Continuation{ID: "host-continuation-2", OwnerID: "host-task-1"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/tmp/task-workdir"}),
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
	legacy := testProviderLaunch{
		Name: "codex", Program: "codex", Args: []string{"exec", "goal"}, Workdir: "/work",
	}
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
		Owner: owner.NewTaskContract("host-resume", "project-test", ""), Continuation: owner.Continuation{ID: "c1", OwnerID: "host-resume", NativeSessionID: "host-durable"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/work"}),
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
	legacy := testProviderLaunch{Name: "codex", Program: "codex", Args: []string{"exec", "goal"}, Workdir: "/work"}
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
		Owner: owner.NewTaskContract("host-kill", "project-test", ""), Continuation: owner.Continuation{ID: "c1", OwnerID: "host-kill"},
		Provider: runtimeprofile.ProviderCodex, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/work"}),
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
	legacy := testProviderLaunch{Name: "fake", Program: "fake", Args: []string{"goal"}, Workdir: "/work"}
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("t", "project-test", ""), Continuation: owner.Continuation{ID: "c", OwnerID: "t"},
		Provider: runtimeprofile.ProviderFake, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{}),
	})
	if err == nil || !strings.Contains(err.Error(), "codex, claude_code, and pi only") {
		t.Fatalf("error = %v", err)
	}
}
