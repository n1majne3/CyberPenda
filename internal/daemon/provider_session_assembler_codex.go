package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
)

// codexAssembler opens persistent Codex App Server sessions. The durable
// process is the provider binary in app-server mode; non-conflicting Runtime
// Custom Args survive assembly; session identity is an App Server thread that
// resumes by durable thread ID.
type codexAssembler struct {
	plugins *runtimeplugin.Registry
	bridge  string
}

func (a codexAssembler) HostCommand(assembly providerSessionAssembly) (string, []string, error) {
	providerBinary, err := assembly.providerBinary(a.plugins, assembly.request.Provider)
	if err != nil {
		return "", nil, err
	}
	args := []string{"app-server"}
	args = append(args, assembly.facts().CustomArgs...)
	return providerBinary, args, nil
}

func (a codexAssembler) SandboxCommand(assembly providerSessionAssembly) ([]string, error) {
	providerBinary, err := assembly.providerBinary(a.plugins, assembly.request.Provider)
	if err != nil {
		return nil, err
	}
	return []string{a.bridge, "--provider", "codex", "--", providerBinary, "app-server"}, nil
}

func (a codexAssembler) Setup(ctx context.Context, bridge productionBridgeTransport, assembly providerSessionAssembly) (providerSessionSetup, error) {
	if _, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: "setup:initialize", Method: "initialize", Params: json.RawMessage(`{"clientInfo":{"name":"cyberpenda","version":"dev"}}`)}); err != nil {
		return providerSessionSetup{}, err
	}
	cwd := assembly.facts().Workdir
	setupMethod, setupID, setupParams := "thread/start", "setup:thread", json.RawMessage(fmt.Sprintf(`{"cwd":%q}`, cwd))
	if durableThreadID := strings.TrimSpace(assembly.request.Continuation.NativeSessionID); durableThreadID != "" {
		setupMethod, setupID, setupParams = "thread/resume", "setup:thread-resume", json.RawMessage(fmt.Sprintf(`{"threadId":%q,"cwd":%q}`, durableThreadID, cwd))
	}
	setupResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: setupID, Method: setupMethod, Params: setupParams})
	if err != nil {
		return providerSessionSetup{}, err
	}
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(setupResponse.Result, &threadResult); err != nil {
		return providerSessionSetup{}, fmt.Errorf("provider session thread response invalid")
	}
	threadID := strings.TrimSpace(threadResult.Thread.ID)
	if threadID == "" {
		threadID = strings.TrimSpace(threadResult.ID)
	}
	if durableThreadID := strings.TrimSpace(assembly.request.Continuation.NativeSessionID); durableThreadID != "" && threadID != durableThreadID {
		return providerSessionSetup{}, fmt.Errorf("provider session resume identity changed")
	}
	if threadID == "" {
		return providerSessionSetup{}, fmt.Errorf("provider session thread identity unavailable")
	}
	capabilities, err := manifestSessionCapabilities(a.plugins, assembly.request.Provider)
	if err != nil {
		return providerSessionSetup{}, err
	}
	// The manifest owns the shared capability set; adapter conformance owns
	// assisted conclusion, which the manifest must not claim for production
	// providers before it is proven.
	capabilities.AssistedConclusion = true
	return providerSessionSetup{
		Session: runtime.NewCodexProviderSession(runtime.CodexProviderSessionConfig{
			Transport: bridge, SessionID: threadID, Capabilities: capabilities,
		}),
		NativePath: strings.TrimSpace(assembly.request.Continuation.NativeSessionPath),
	}, nil
}

func (codexAssembler) HostStartError(_ string, err error) error { return err }

func (codexAssembler) HostTeardown(providerSessionAssembly) func(context.Context) { return nil }

var _ providerSessionAssembler = codexAssembler{}
