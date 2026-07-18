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

func TestProductionProviderSessionFactoryRejectsProvidersWithoutNativeSettlement(t *testing.T) {
	for _, provider := range []runtimeprofile.Provider{runtimeprofile.ProviderClaudeCode, runtimeprofile.ProviderPi} {
		t.Run(string(provider), func(t *testing.T) {
			factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: newProductionFactoryDocker()})
			_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{Task: task.Task{ID: "task-1"}, Continuation: task.TaskContinuation{ID: "c-1"}, Provider: provider, Runner: task.RunnerSandbox})
			var unsupported *runtime.UnsupportedProviderSessionCapabilityError
			if !errors.As(err, &unsupported) || unsupported.Capability != runtime.ProviderSessionCapabilityInterruptThenReplace {
				t.Fatalf("error = %v, want interrupt_then_replace capability error", err)
			}
		})
	}
}
