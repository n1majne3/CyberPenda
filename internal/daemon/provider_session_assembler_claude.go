package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
)

// claudeAssembler opens persistent Claude Agent SDK Query sessions. The
// durable process is the version-pinned packaged SDK bridge — never
// repo-relative Node sources, an on-demand npm install, or the interactive
// CLI. Projected settings and the durable session ID flow as structured flags;
// non-conflicting Runtime Custom Args survive argv assembly. Assisted
// conclusion capability is proven by the runtime handshake, not assumed.
type claudeAssembler struct {
	plugins    *runtimeplugin.Registry
	sdkCommand string
}

func (a claudeAssembler) sdkBridgeCommand() (string, error) {
	program := strings.TrimSpace(a.sdkCommand)
	if program == "" {
		return "", fmt.Errorf("Claude SDK bridge command is not configured")
	}
	return program, nil
}

// bridgeArgs builds the SDK bridge argv from structured facts: the
// bridge-visible cwd, effective model, projected settings, durable resume
// identity, and Runtime Custom Args.
func (a claudeAssembler) bridgeArgs(assembly providerSessionAssembly) []string {
	facts := assembly.facts()
	args := []string{"--cwd", facts.Workdir}
	if model := strings.TrimSpace(facts.Model); model != "" {
		args = append(args, "--model", model)
	}
	if settings := strings.TrimSpace(facts.SettingsPath); settings != "" {
		args = append(args, "--settings", settings)
	}
	if durableSessionID := strings.TrimSpace(assembly.request.Continuation.NativeSessionID); durableSessionID != "" {
		args = append(args, "--resume", durableSessionID)
	}
	args = append(args, facts.CustomArgs...)
	return args
}

func (a claudeAssembler) HostCommand(assembly providerSessionAssembly) (string, []string, error) {
	program, err := a.sdkBridgeCommand()
	if err != nil {
		return "", nil, err
	}
	return program, a.bridgeArgs(assembly), nil
}

func (a claudeAssembler) SandboxCommand(assembly providerSessionAssembly) ([]string, error) {
	// The SDK bridge is an executable in the sandbox image. It owns the
	// long-lived Query and does not invoke the Claude CLI's private protocol.
	program, err := a.sdkBridgeCommand()
	if err != nil {
		return nil, err
	}
	return append([]string{program}, a.bridgeArgs(assembly)...), nil
}

func (a claudeAssembler) Setup(ctx context.Context, bridge productionBridgeTransport, assembly providerSessionAssembly) (providerSessionSetup, error) {
	setupResponse, err := sendProviderSetupRPC(ctx, bridge, runtime.SandboxBridgeRequest{ID: "setup:initialize", Method: "claude/initialize", Params: json.RawMessage(`{}`)})
	if err != nil {
		return providerSessionSetup{}, err
	}
	var state struct {
		SessionID    string                   `json:"session_id"`
		Capabilities claudeBridgeCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(setupResponse.Result, &state); err != nil || strings.TrimSpace(state.SessionID) == "" {
		return providerSessionSetup{}, fmt.Errorf("provider session identity unavailable")
	}
	sessionID := strings.TrimSpace(state.SessionID)
	if durable := strings.TrimSpace(assembly.request.Continuation.NativeSessionID); durable != "" && durable != sessionID {
		return providerSessionSetup{}, fmt.Errorf("provider session resume identity changed")
	}
	capabilities, err := manifestSessionCapabilities(a.plugins, assembly.request.Provider)
	if err != nil {
		return providerSessionSetup{}, err
	}
	return providerSessionSetup{
		Session: runtime.NewClaudeCodeProviderSession(runtime.ClaudeCodeProviderSessionConfig{
			Transport: bridge, SessionID: sessionID, Capabilities: capabilities,
		}),
	}, nil
}

func (claudeAssembler) HostStartError(program string, err error) error {
	return fmt.Errorf("Claude SDK bridge unavailable at %s: %w", program, err)
}

func (claudeAssembler) HostTeardown(providerSessionAssembly) func(context.Context) { return nil }

var _ providerSessionAssembler = claudeAssembler{}
