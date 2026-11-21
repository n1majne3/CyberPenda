package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestProductionProviderSessionFactoryOpensHostClaudeSDKBridge(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{
		HostStarter:            starter,
		ClaudeSDKBridgeCommand: "/opt/pentest/pentest-claude-sdk-bridge",
	})
	legacy := testProviderLaunch{
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
	}
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
		Owner: owner.NewTaskContract("host-claude-1", "project-test", ""), Continuation: owner.Continuation{ID: "c1", OwnerID: "host-claude-1"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost,
		RuntimeConfig: map[string]any{"launch_model_override": "claude-test"},
		Facts:         legacy.facts(ProviderSessionLaunchFacts{Workdir: "/tmp/task-workdir", Model: "claude-test", SettingsPath: "/tmp/claude-home/settings.json", CustomArgs: []string{"--add-dir", "/tmp/extra"}}),
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

	rebound, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("host-claude-1", "project-test", ""), Continuation: owner.Continuation{ID: "c2", OwnerID: "host-claude-1"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/tmp/task-workdir"}),
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
	legacy := testProviderLaunch{
		Name: "claude_code", Program: "claude",
		Args:    []string{"--model", "claude-test", "--settings", "/home/settings.json", "-p", "goal"},
		Workdir: "/work",
	}
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
		Owner:        owner.NewTaskContract("host-claude-resume", "project-test", ""),
		Continuation: owner.Continuation{ID: "c1", OwnerID: "host-claude-resume", NativeSessionID: "claude-durable"},
		Provider:     runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/work", Model: "claude-test", SettingsPath: "/home/settings.json"}),
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
	legacy := testProviderLaunch{
		Name: "claude_code", Program: "claude", Args: []string{"-p", "goal"}, Workdir: "/work",
	}
	go func() {
		scanner := bufio.NewScanner(starter.inputR)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			_ = json.Unmarshal(scanner.Bytes(), &request)
			_, _ = io.WriteString(starter.outputW, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":{"session_id":"c1","status":"ready"}}`+"\n")
		}
	}()
	binding, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("host-claude-kill", "project-test", ""), Continuation: owner.Continuation{ID: "c1", OwnerID: "host-claude-kill"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost,
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
	legacy := testProviderLaunch{
		Name: "claude_code", Program: "claude", Args: []string{"-p", "goal"}, Workdir: t.TempDir(),
	}
	_, err := factory.Open(context.Background(), ProviderSessionLaunchRequest{
		Owner: owner.NewTaskContract("host-claude-missing", "project-test", ""), Continuation: owner.Continuation{ID: "c1", OwnerID: "host-claude-missing"},
		Provider: runtimeprofile.ProviderClaudeCode, Runner: task.RunnerHost,
		Facts: legacy.facts(ProviderSessionLaunchFacts{Workdir: "/work"}),
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
