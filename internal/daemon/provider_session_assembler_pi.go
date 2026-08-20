package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
)

// piAssembler opens persistent Pi headless RPC sessions. HostSessionBridge
// speaks CyberPenda JSON-RPC while Pi speaks native RPC, so Host Pi always
// retains the piWire translation boundary: the bridge executable owns the Pi
// child after "--". Projected PI_CODING_AGENT_DIR / session dir env and
// launch-ready credentials must survive from Config Projection.
type piAssembler struct {
	plugins *runtimeplugin.Registry
	// sandboxBridge is the sandbox-local provider-bridge path used inside
	// container create argv.
	sandboxBridge string
	// resolveHostBridge returns the validated host piWire bridge path.
	resolveHostBridge func() (string, error)
}

func (a piAssembler) HostCommand(assembly providerSessionAssembly) (string, []string, error) {
	if err := requireHostPiProjectedEnv(assembly.env); err != nil {
		return "", nil, err
	}
	providerBinary, err := assembly.providerBinary(a.plugins, assembly.request.Provider)
	if err != nil {
		return "", nil, err
	}
	// Program is the explicit host bridge executable; Pi is the child after "--".
	program, err := a.resolveHostBridge()
	if err != nil {
		return "", nil, err
	}
	args := []string{"--provider", "pi", "--", providerBinary, "--mode", "rpc"}
	if sessionPath := strings.TrimSpace(assembly.request.Continuation.NativeSessionPath); sessionPath != "" {
		args = append(args, "--session", sessionPath)
	} else {
		// Pi creates its durable session lazily. Supplying a stable owner-scoped
		// id makes the pre-launch get_state handshake deterministic and keeps
		// later Continuations on the same native session.
		sessionID := strings.TrimSpace(assembly.request.Continuation.NativeSessionID)
		if sessionID == "" {
			sessionID = providerSessionOwnerID(assembly.request)
		}
		args = append(args, "--session-id", sessionID)
	}
	args = append(args, assembly.facts().CustomArgs...)
	return program, args, nil
}

func (a piAssembler) SandboxCommand(assembly providerSessionAssembly) ([]string, error) {
	providerBinary, err := assembly.providerBinary(a.plugins, assembly.request.Provider)
	if err != nil {
		return nil, err
	}
	args := []string{a.sandboxBridge, "--provider", "pi", "--", providerBinary, "--mode", "rpc"}
	if sessionPath := strings.TrimSpace(assembly.request.Continuation.NativeSessionPath); sessionPath != "" {
		args = append(args, "--session", sessionPath)
	} else {
		sessionID := strings.TrimSpace(assembly.request.Continuation.NativeSessionID)
		if sessionID == "" {
			sessionID = providerSessionOwnerID(assembly.request)
		}
		args = append(args, "--session-id", sessionID)
	}
	args = append(args, assembly.facts().CustomArgs...)
	return args, nil
}

func (a piAssembler) Setup(ctx context.Context, bridge productionBridgeTransport, assembly providerSessionAssembly) (providerSessionSetup, error) {
	setupResponse, err := bridge.Send(ctx, runtime.SandboxBridgeRequest{ID: "setup:state", Method: "pi/get_state", Params: json.RawMessage(`{}`)})
	if err != nil {
		return providerSessionSetup{}, err
	}
	var state struct {
		SessionID   string `json:"session_id"`
		SessionPath string `json:"session_path"`
	}
	if err := json.Unmarshal(setupResponse.Result, &state); err == nil {
		// ok
	}
	sessionID, sessionPath := strings.TrimSpace(state.SessionID), strings.TrimSpace(state.SessionPath)
	if durable := strings.TrimSpace(assembly.request.Continuation.NativeSessionID); durable != "" && sessionID != "" && durable != sessionID {
		return providerSessionSetup{}, fmt.Errorf("provider session resume identity changed")
	}
	if sessionID == "" {
		return providerSessionSetup{}, fmt.Errorf("provider session identity unavailable")
	}
	capabilities, err := manifestSessionCapabilities(a.plugins, assembly.request.Provider)
	if err != nil {
		return providerSessionSetup{}, err
	}
	// Adapter conformance owns assisted conclusion; see the Codex assembler.
	capabilities.AssistedConclusion = true
	return providerSessionSetup{
		Session: runtime.NewPiProviderSession(runtime.PiProviderSessionConfig{
			Transport: bridge, SessionID: sessionID, Capabilities: capabilities,
		}),
		NativePath: sessionPath,
	}, nil
}

func (piAssembler) HostStartError(_ string, err error) error { return err }

// HostTeardown removes owner-scoped Pi session files and projected
// credentials after process-group teardown on Stop, failure, and daemon
// shutdown. models.json (non-secret) is retained for diagnostics until the
// next projection.
func (a piAssembler) HostTeardown(assembly providerSessionAssembly) func(context.Context) {
	agentDir := strings.TrimSpace(assembly.env["PI_CODING_AGENT_DIR"])
	if agentDir == "" {
		return nil
	}
	return func(context.Context) {
		_ = os.Remove(filepath.Join(agentDir, "auth.json"))
		_ = os.RemoveAll(filepath.Join(agentDir, "sessions"))
	}
}

var _ providerSessionAssembler = piAssembler{}

func requireHostPiProjectedEnv(env map[string]string) error {
	if strings.TrimSpace(env["PI_CODING_AGENT_DIR"]) == "" {
		return fmt.Errorf("host pi requires projected PI_CODING_AGENT_DIR")
	}
	if strings.TrimSpace(env["PI_CODING_AGENT_SESSION_DIR"]) == "" {
		return fmt.Errorf("host pi requires projected PI_CODING_AGENT_SESSION_DIR")
	}
	return nil
}
