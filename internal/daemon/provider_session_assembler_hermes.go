package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
)

// hermesAssembler opens persistent Hermes ACP sessions. The durable process is
// the Hermes binary in non-interactive ACP mode; session identity is an ACP
// session created or loaded per durable native session ID.
type hermesAssembler struct {
	plugins *runtimeplugin.Registry
}

func (a hermesAssembler) HostCommand(assembly providerSessionAssembly) (string, []string, error) {
	if strings.TrimSpace(assembly.env["HERMES_HOME"]) == "" {
		return "", nil, fmt.Errorf("host Hermes Runtime requires projected HERMES_HOME")
	}
	providerBinary, err := assembly.providerBinary(a.plugins, assembly.request.Provider)
	if err != nil {
		return "", nil, err
	}
	return providerBinary, []string{"--yolo", "acp"}, nil
}

func (a hermesAssembler) SandboxCommand(assembly providerSessionAssembly) ([]string, error) {
	providerBinary, err := assembly.providerBinary(a.plugins, assembly.request.Provider)
	if err != nil {
		return nil, err
	}
	return []string{providerBinary, "--yolo", "acp"}, nil
}

func (a hermesAssembler) Setup(ctx context.Context, bridge productionBridgeTransport, assembly providerSessionAssembly) (providerSessionSetup, error) {
	// Hermes ACP InitializeRequest requires integer protocolVersion.
	if _, err := sendProviderSetupRPC(ctx, bridge, runtime.SandboxBridgeRequest{
		ID: "setup:initialize", Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":1,"clientInfo":{"name":"cyberpenda","version":"1"}}`),
	}); err != nil {
		return providerSessionSetup{}, err
	}

	sessionID := strings.TrimSpace(assembly.request.Continuation.NativeSessionID)
	method := "session/new"
	params, err := json.Marshal(map[string]any{"cwd": assembly.facts().Workdir, "mcpServers": []any{}})
	if err != nil {
		return providerSessionSetup{}, err
	}
	if sessionID != "" {
		method = "session/load"
		params, err = json.Marshal(map[string]any{"sessionId": sessionID, "cwd": assembly.facts().Workdir, "mcpServers": []any{}})
		if err != nil {
			return providerSessionSetup{}, err
		}
	}
	setupResponse, err := sendProviderSetupRPC(ctx, bridge, runtime.SandboxBridgeRequest{ID: "setup:session", Method: method, Params: params})
	if err != nil {
		return providerSessionSetup{}, err
	}
	var state struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(setupResponse.Result, &state)
	if created := strings.TrimSpace(state.SessionID); created != "" {
		sessionID = created
	}
	if sessionID == "" {
		return providerSessionSetup{}, fmt.Errorf("provider session identity unavailable")
	}

	capabilities, err := manifestSessionCapabilities(a.plugins, assembly.request.Provider)
	if err != nil {
		return providerSessionSetup{}, err
	}
	return providerSessionSetup{
		Session: runtime.NewHermesProviderSession(runtime.HermesProviderSessionConfig{
			Transport:    bridge,
			SessionID:    sessionID,
			Capabilities: capabilities,
			HermesHome:   hermesHomeFromAdapter(assembly.request.LegacyAdapter),
		}),
	}, nil
}

func (hermesAssembler) HostStartError(program string, err error) error {
	return fmt.Errorf("Hermes ACP unavailable at %s: %w", program, err)
}

func hermesHomeFromAdapter(adapter runtime.Adapter) string {
	return runtime.ProjectedHermesHome(adapter)
}

func (hermesAssembler) HostTeardown(providerSessionAssembly) func(context.Context) { return nil }

var _ providerSessionAssembler = hermesAssembler{}
