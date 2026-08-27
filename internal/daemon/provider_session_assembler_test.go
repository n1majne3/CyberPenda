package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

// The assembler seam conformance suite: every Runtime family passes the same
// contract through the shared open flows — deterministic sandbox bridge
// command, family handshake identity, manifest capabilities, and owner-scoped
// rebinding — plus a fake assembler proving the shared finish without any
// family-specific behavior.

func serveFactoryProtocol(t *testing.T, input *io.PipeReader, output *io.PipeWriter, respond func(method string, params json.RawMessage) string) {
	t.Helper()
	go func() {
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			var request runtime.SandboxBridgeRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				continue
			}
			result := respond(request.Method, request.Params)
			if result == "" {
				result = `{"ok":true}`
			}
			_, _ = io.WriteString(output, `{"jsonrpc":"2.0","id":"`+request.ID+`","result":`+result+"}\n")
		}
	}()
}

type setupRPCErrorBridge struct {
	method     string
	mu         sync.Mutex
	calls      []string
	closed     chan struct{}
	terminated chan struct{}
}

func (b *setupRPCErrorBridge) Send(_ context.Context, request runtime.SandboxBridgeRequest) (runtime.SandboxBridgeResponse, error) {
	b.mu.Lock()
	b.calls = append(b.calls, request.Method)
	b.mu.Unlock()
	if request.Method == b.method {
		return runtime.SandboxBridgeResponse{ID: request.ID, Error: json.RawMessage(`{"code":-32000,"message":"private provider setup detail"}`)}, nil
	}
	return runtime.SandboxBridgeResponse{ID: request.ID, Result: json.RawMessage(`{}`)}, nil
}

func (b *setupRPCErrorBridge) Close(context.Context) error { return nil }
func (b *setupRPCErrorBridge) Closed() <-chan struct{}     { return b.closed }
func (b *setupRPCErrorBridge) Terminated() <-chan struct{} { return b.terminated }

func (b *setupRPCErrorBridge) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func TestProviderSessionAssemblersRejectJSONRPCSetupErrors(t *testing.T) {
	registry := runtimeplugin.MustBuiltinRegistry()
	tests := []struct {
		name      string
		provider  runtimeprofile.Provider
		method    string
		assembler providerSessionAssembler
	}{
		{name: "codex initialize", provider: runtimeprofile.ProviderCodex, method: "initialize", assembler: codexAssembler{plugins: registry, bridge: "bridge"}},
		{name: "claude initialize", provider: runtimeprofile.ProviderClaudeCode, method: "claude/initialize", assembler: claudeAssembler{plugins: registry}},
		{name: "pi state", provider: runtimeprofile.ProviderPi, method: "pi/get_state", assembler: piAssembler{plugins: registry}},
		{name: "hermes initialize", provider: runtimeprofile.ProviderHermes, method: "initialize", assembler: hermesAssembler{plugins: registry}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bridge := &setupRPCErrorBridge{method: test.method, closed: make(chan struct{}), terminated: make(chan struct{})}
			_, err := test.assembler.Setup(context.Background(), bridge, providerSessionAssembly{request: ProviderSessionLaunchRequest{
				Provider: test.provider, Continuation: owner.Continuation{ID: "continuation-1", OwnerID: "owner-1"},
				Facts: ProviderSessionLaunchFacts{Workdir: "/work"},
			}})
			if err == nil || !strings.Contains(err.Error(), "JSON-RPC") {
				t.Fatalf("setup error = %v, want bounded JSON-RPC failure", err)
			}
			if bridge.callCount() != 1 {
				t.Fatalf("setup calls = %d, want fail closed on first RPC error", bridge.callCount())
			}
			if strings.Contains(err.Error(), "private provider setup detail") {
				t.Fatalf("setup error leaked provider detail: %v", err)
			}
		})
	}
}

func TestProviderSessionAssemblerSandboxConformance(t *testing.T) {
	claudeFullHandshake := `{"session_id":"conf-claude","status":"ready","capabilities":{"persistent_session":true,"send_turn":true,"interrupt_turn":true,"normalized_tool_events":true,"normalized_turn_events":true,"attempt_result":true,"assisted_conclusion":true}}`
	cases := []struct {
		provider        runtimeprofile.Provider
		respond         func(method string, params json.RawMessage) string
		wantBridge      []string
		wantSessionID   string
		wantInTurnSteer bool
	}{
		{
			provider: runtimeprofile.ProviderCodex,
			respond: func(method string, _ json.RawMessage) string {
				switch method {
				case "initialize":
					return `{"userAgent":"codex_cli_rs/0.149.0"}`
				case "thread/start":
					return `{"thread":{"id":"conf-codex"}}`
				}
				return ""
			},
			wantBridge:    []string{"sandbox:test", "/usr/local/bin/pentest-provider-bridge", "--provider", "codex", "--", "codex", "app-server"},
			wantSessionID: "conf-codex", wantInTurnSteer: true,
		},
		{
			provider: runtimeprofile.ProviderClaudeCode,
			respond: func(method string, _ json.RawMessage) string {
				if method == "claude/initialize" {
					return claudeFullHandshake
				}
				return ""
			},
			wantBridge:    []string{"sandbox:test", "/usr/local/bin/pentest-claude-sdk-bridge", "--cwd", "/task/workdir"},
			wantSessionID: "conf-claude",
		},
		{
			provider: runtimeprofile.ProviderPi,
			respond: func(method string, _ json.RawMessage) string {
				if method == "pi/get_state" {
					return `{"session_id":"conf-pi","session_path":"/sessions/conf-pi.jsonl"}`
				}
				return ""
			},
			wantBridge:    []string{"sandbox:test", "/usr/local/bin/pentest-provider-bridge", "--provider", "pi", "--", "pi", "--mode", "rpc"},
			wantSessionID: "conf-pi", wantInTurnSteer: true,
		},
		{
			provider: runtimeprofile.ProviderHermes,
			respond: func(method string, _ json.RawMessage) string {
				if method == "session/new" {
					return `{"sessionId":"conf-hermes"}`
				}
				return ""
			},
			wantBridge:    []string{"sandbox:test", "hermes", "--yolo", "acp"},
			wantSessionID: "conf-hermes",
		},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider), func(t *testing.T) {
			docker := newProductionFactoryDocker()
			factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{Docker: docker})
			serveFactoryProtocol(t, docker.inputR, docker.outputW, tc.respond)

			providerToken := string(tc.provider)
			if providerToken == "claude_code" {
				providerToken = "claude"
			}
			request := ProviderSessionLaunchRequest{
				Owner:         owner.NewTaskContract("conformance-"+string(tc.provider), "project-test", ""),
				Continuation:  owner.Continuation{ID: "c1", OwnerID: "conformance-" + string(tc.provider)},
				Provider:      tc.provider,
				Runner:        task.RunnerSandbox,
				LegacyAdapter: runtime.NewDockerSandboxAdapter(runtime.DockerSandboxConfig{Name: string(tc.provider), Image: "sandbox:test", CreateArgs: []string{"create", "sandbox:test", providerToken, "goal"}}),
				Facts:         ProviderSessionLaunchFacts{Workdir: "/task/workdir"},
			}
			binding, err := factory.Open(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			defer binding.Session.Close(context.Background())
			if binding.Session.SessionID() != tc.wantSessionID {
				t.Fatalf("session id = %q, want %q", binding.Session.SessionID(), tc.wantSessionID)
			}
			capabilities := binding.Session.Capabilities()
			if !capabilities.PersistentSession || !capabilities.SendTurn || !capabilities.InterruptTurn ||
				!capabilities.InterruptThenReplace || !capabilities.PermissionResponse || !capabilities.ResumeSession {
				t.Fatalf("capabilities = %#v, want the manifest persistent-session set", capabilities)
			}
			if !capabilities.AssistedConclusion {
				t.Fatalf("capabilities = %#v, want assisted conclusion projected for %s", capabilities, tc.provider)
			}
			if capabilities.InTurnSteer != tc.wantInTurnSteer {
				t.Fatalf("in-turn steer = %v, want %v", capabilities.InTurnSteer, tc.wantInTurnSteer)
			}
			if err := validateAssistedConclusionBinding(binding); err != nil {
				t.Fatalf("assisted binding validation failed: %v", err)
			}

			docker.mu.Lock()
			created := append([]string(nil), docker.createArgs...)
			docker.mu.Unlock()
			if got, want := strings.Join(created, " "), strings.Join(tc.wantBridge, " "); !strings.HasPrefix(got, "create "+want) && !strings.Contains(got, want) {
				t.Fatalf("create args = %q, want bridge argv %q", got, want)
			}

			// A later Continuation of the same owner rebinds the same session
			// without creating another bridge container.
			rebind := request
			rebind.Continuation = owner.Continuation{ID: "c2", OwnerID: "conformance-" + string(tc.provider)}
			rebound, err := factory.Open(context.Background(), rebind)
			if err != nil {
				t.Fatal(err)
			}
			if rebound.Session != binding.Session {
				t.Fatalf("second Open replaced the %s session", tc.provider)
			}
		})
	}
}

// TestProviderSessionAssemblerManifestIsCapabilitySource proves the factory
// reads assisted-conclusion capability from the Runtime Plugin manifest for
// Codex, Pi, and Hermes, while Claude keeps its handshake gate.
func TestProviderSessionAssemblerManifestIsCapabilitySource(t *testing.T) {
	registry := runtimeplugin.MustBuiltinRegistry()
	for _, provider := range []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex, runtimeprofile.ProviderClaudeCode,
		runtimeprofile.ProviderPi, runtimeprofile.ProviderHermes,
	} {
		capabilities, err := manifestSessionCapabilities(registry, provider)
		if err != nil {
			t.Fatal(err)
		}
		if !capabilities.PersistentSession || !capabilities.SendTurn ||
			!capabilities.InterruptTurn || !capabilities.InterruptThenReplace ||
			!capabilities.PermissionResponse || !capabilities.ResumeSession {
			t.Fatalf("manifest capabilities for %s = %#v, want the persistent-session set", provider, capabilities)
		}
		if capabilities.AssistedConclusion {
			t.Fatalf("manifest for %s must not claim assisted conclusion; adapter conformance proves it", provider)
		}
	}
	// The factory is the adapter-conformance layer that proves assisted
	// conclusion for the implemented families.
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{})
	for _, provider := range []runtimeprofile.Provider{
		runtimeprofile.ProviderCodex, runtimeprofile.ProviderPi, runtimeprofile.ProviderHermes,
	} {
		if !factory.SupportsAssistedConclusion(provider) {
			t.Fatalf("factory did not project adapter-proven assisted support for %s", provider)
		}
	}
}

// fakeAssembler is the test double for the assembler seam: it scripts one
// command and one setup result so the shared open and finish flows stay
// testable without any Runtime family behavior.
type fakeAssembler struct {
	mu        sync.Mutex
	program   string
	args      []string
	setupErr  error
	teardowns int
}

func (a *fakeAssembler) HostCommand(providerSessionAssembly) (string, []string, error) {
	return a.program, a.args, nil
}

func (a *fakeAssembler) SandboxCommand(providerSessionAssembly) ([]string, error) {
	return append([]string{a.program}, a.args...), nil
}

func (a *fakeAssembler) Setup(_ context.Context, _ productionBridgeTransport, _ providerSessionAssembly) (providerSessionSetup, error) {
	if a.setupErr != nil {
		return providerSessionSetup{}, a.setupErr
	}
	session := runtime.NewFakeProviderSession(runtime.FakeProviderSessionConfig{
		SessionID: "fake-asm-session",
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InterruptTurn: true, InterruptThenReplace: true,
			PermissionResponse: true, ResumeSession: true, AssistedConclusion: true,
		},
	})
	return providerSessionSetup{Session: session}, nil
}

func (a *fakeAssembler) HostStartError(_ string, err error) error { return err }

func (a *fakeAssembler) HostTeardown(providerSessionAssembly) func(context.Context) {
	return func(context.Context) {
		a.mu.Lock()
		a.teardowns++
		a.mu.Unlock()
	}
}

// TestProviderSessionAssemblerFakeDrivesSharedFinish proves the shared host
// flow through the seam alone: bind, family setup, shared finish with restart
// metadata, owner-scoped rebind, and family teardown on close.
func TestProviderSessionAssemblerFakeDrivesSharedFinish(t *testing.T) {
	starter := newProductionFactoryHostStarter()
	serveFactoryProtocol(t, starter.inputR, starter.outputW, func(string, json.RawMessage) string { return "" })
	factory := NewProductionProviderSessionFactory(ProductionProviderSessionFactoryConfig{HostStarter: starter})
	fake := &fakeAssembler{program: "fake-asm", args: []string{"--persistent"}}
	factory.assemblers[runtimeprofile.Provider("fake-asm")] = fake

	legacy := runtime.NewCommandAdapter(runtime.CommandAdapterConfig{Name: "fake-asm", Program: "fake-asm", Args: []string{"goal"}, Workdir: "/work"})
	makeRequest := func(continuationID string) ProviderSessionLaunchRequest {
		return ProviderSessionLaunchRequest{
			Owner:         owner.NewTaskContract("fake-asm-task", "project-test", ""),
			Continuation:  owner.Continuation{ID: continuationID, OwnerID: "fake-asm-task"},
			Provider:      runtimeprofile.Provider("fake-asm"),
			Runner:        task.RunnerHost,
			LegacyAdapter: legacy,
			Facts:         ProviderSessionLaunchFacts{Workdir: "/work"},
		}
	}
	binding, err := factory.Open(context.Background(), makeRequest("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if binding.Session.SessionID() != "fake-asm-session" {
		t.Fatalf("session = %q, want the fake setup identity", binding.Session.SessionID())
	}
	spec := starter.lastSpec()
	if spec.Program != "fake-asm" || len(spec.Args) != 1 || spec.Args[0] != "--persistent" {
		t.Fatalf("host spec = %#v, want the fake assembler command", spec)
	}
	rebound, err := factory.Open(context.Background(), makeRequest("c2"))
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Session != binding.Session {
		t.Fatal("shared finish did not rebind the owner-scoped session")
	}
	if err := binding.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	teardowns := fake.teardowns
	fake.mu.Unlock()
	if teardowns != 1 {
		t.Fatalf("family teardown runs = %d, want 1 after close", teardowns)
	}
}

type codexSetupTestBridge struct {
	initialize json.RawMessage
	closed     chan struct{}
	terminated chan struct{}
}

func (b *codexSetupTestBridge) Send(_ context.Context, request runtime.SandboxBridgeRequest) (runtime.SandboxBridgeResponse, error) {
	switch request.Method {
	case "initialize":
		return runtime.SandboxBridgeResponse{ID: request.ID, Result: b.initialize}, nil
	case "thread/start":
		return runtime.SandboxBridgeResponse{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"thread-version"}}`)}, nil
	default:
		return runtime.SandboxBridgeResponse{ID: request.ID, Result: json.RawMessage(`{}`)}, nil
	}
}

func (b *codexSetupTestBridge) Close(context.Context) error { return nil }
func (b *codexSetupTestBridge) Closed() <-chan struct{}     { return b.closed }
func (b *codexSetupTestBridge) Terminated() <-chan struct{} { return b.terminated }

func TestCodexAssemblerAdvertisesManifestSteerUntilWireNegotiation(t *testing.T) {
	for _, test := range []struct {
		name       string
		initialize string
		wantSteer  bool
	}{
		{name: "current version", initialize: `{"userAgent":"codex_cli_rs/0.149.0"}`, wantSteer: true},
		{name: "older version", initialize: `{"userAgent":"codex_cli_rs/0.148.9"}`, wantSteer: true},
		{name: "unknown version", initialize: `{"userAgent":"codex app-server"}`, wantSteer: true},
		{name: "future response shape", initialize: `{}`, wantSteer: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := runtimeplugin.MustBuiltinRegistry()
			bridge := &codexSetupTestBridge{
				initialize: json.RawMessage(test.initialize),
				closed:     make(chan struct{}), terminated: make(chan struct{}),
			}
			setup, err := (codexAssembler{plugins: registry, bridge: "bridge"}).Setup(context.Background(), bridge, providerSessionAssembly{
				request: ProviderSessionLaunchRequest{
					Provider:     runtimeprofile.ProviderCodex,
					Continuation: owner.Continuation{ID: "continuation-1", OwnerID: "task-1"},
					Facts:        ProviderSessionLaunchFacts{Workdir: "/work"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			capabilities := setup.Session.Capabilities()
			if capabilities.InTurnSteer != test.wantSteer {
				t.Fatalf("InTurnSteer = %v, want %v", capabilities.InTurnSteer, test.wantSteer)
			}
			if !capabilities.InterruptThenReplace {
				t.Fatal("wire negotiation removed interrupt_then_replace fallback")
			}
		})
	}
}
