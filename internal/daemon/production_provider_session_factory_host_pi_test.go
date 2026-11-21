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

func TestProductionProviderSessionFactoryOpensHostPiRPCViaWireBridge(t *testing.T) {
	// Host Pi must retain the piWire translation boundary: HostSessionBridge
	// speaks CyberPenda JSON-RPC to the explicit bridge executable, which owns
	// the Pi native RPC child.
	starter := newProductionFactoryHostStarter()
	bridgePath := "/opt/cyberpenda/bin/pentest-provider-bridge"
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter: starter, HostBridgeCommand: bridgePath,
	})
	legacy := testProviderLaunch{
		Name: "pi", Program: "/usr/local/bin/pi",
		Args:    []string{"--provider", "primary", "--model", "m1", "--mode", "json", "--debug", "inspect target"},
		Workdir: "/tmp/task-workdir",
		Env: map[string]string{
			"PI_CODING_AGENT_DIR":         "/tmp/task-workdir/runtime-home/pi/agent",
			"PI_CODING_AGENT_SESSION_DIR": "/tmp/task-workdir/runtime-home/pi/agent/sessions",
			"OPENAI_API_KEY":              "sk-projected-1",
			"ANTHROPIC_API_KEY":           "sk-projected-2",
		},
	}
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
		Owner: owner.NewTaskContract("host-pi-task", "project-test", ""), Continuation: owner.Continuation{ID: "host-pi-cont", OwnerID: "host-pi-task"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{ProviderBinary: "/usr/local/bin/pi", CustomArgs: []string{"--debug"}, Workdir: "/tmp/task-workdir"}),
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

	rebound, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("host-pi-task", "project-test", ""), Continuation: owner.Continuation{ID: "host-pi-cont-2", OwnerID: "host-pi-task"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/tmp/task-workdir"}),
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
	legacy := testProviderLaunch{
		Name: "pi", Program: "pi", Args: []string{"--mode", "json", "goal"}, Workdir: "/work",
		Env: map[string]string{
			"PI_CODING_AGENT_DIR":         agentDir,
			"PI_CODING_AGENT_SESSION_DIR": filepath.Join(agentDir, "sessions"),
		},
	}
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
		Owner: owner.NewTaskContract("host-pi-clean", "project-test", ""), Continuation: owner.Continuation{ID: "c1", OwnerID: "host-pi-clean"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/work"}),
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
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostBridgeCommand: filepath.Join(t.TempDir(), "missing-bridge"),
	})
	legacy := testProviderLaunch{
		Name: "pi", Program: "pi", Args: []string{"--mode", "json", "goal"}, Workdir: "/work",
		Env: map[string]string{
			"PI_CODING_AGENT_DIR":         "/tmp/agent",
			"PI_CODING_AGENT_SESSION_DIR": "/tmp/agent/sessions",
		},
	}
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("t", "project-test", ""), Continuation: owner.Continuation{ID: "c", OwnerID: "t"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{}),
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
	legacy := testProviderLaunch{
		Name: "pi", Program: "pi", Args: []string{"goal"}, Workdir: "/work",
		Env: map[string]string{},
	}
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("t", "project-test", ""), Continuation: owner.Continuation{ID: "c", OwnerID: "t"},
		Provider: runtimeprofile.ProviderPi, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{}),
	})
	if err == nil || !strings.Contains(err.Error(), "PI_CODING_AGENT_DIR") {
		t.Fatalf("error = %v", err)
	}
}

func TestHostCodexReservedConfigOverridesStayRejectedByValidateCustomArgs(t *testing.T) {
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
	if err := runtimeprofile.ValidateCustomArgs(runtimeprofile.ProviderCodex, []string{"-c", "foo=bar", "--json"}); err != nil {
		t.Fatalf("ValidateCustomArgs must allow non-conflicting -c foo=bar: %v", err)
	}
}
