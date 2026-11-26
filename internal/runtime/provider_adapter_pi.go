package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"pentest/internal/runtimeplugin"
)

type PiProviderSessionConfig struct {
	Transport    ProviderSessionTransport
	SessionID    string
	ActiveTurnID string
	Capabilities runtimeplugin.Capabilities
}

type PiProviderSession struct{ *providerSessionAdapter }

func NewPiProviderSession(config PiProviderSessionConfig) *PiProviderSession {
	methods := providerWireMethods{
		send: "pi/prompt", interrupt: "pi/abort", steer: "pi/steer", permission: "pi/permission/respond",
		params: providerParams,
		// Pi applies Runtime Turn Selection through discrete RPC commands.
		// Order is mandatory: model → thinking level → prompt, because
		// set_model can reset thinking level on the provider side.
		prepareSend: piPrepareSendSelection,
		turnID:      func(record map[string]any) string { return providerJSONValue(record, "turn_id", "turnId", "id") },
		sessionID:   identitySession,
	}
	return &PiProviderSession{newProviderSessionAdapter("pi", config.Transport, config.SessionID, config.ActiveTurnID, providerCapabilities(config.Capabilities), methods)}
}

// piPrepareSendSelection issues set_model then set_thinking_level before prompt
// when Model / ModelProviderID / RequestedReasoningEffort are present. Effort
// vocabulary is passed through 1:1; unsupported values surface as provider errors.
func piPrepareSendSelection(ctx context.Context, transport ProviderSessionTransport, wireBaseID, sessionID, turnID string, request ProviderSessionRequest) error {
	if transport == nil {
		return errors.New("provider session transport is required")
	}
	providerID := strings.TrimSpace(request.ModelProviderID)
	model := strings.TrimSpace(request.Model)
	effort := strings.TrimSpace(request.RequestedReasoningEffort)

	if providerID != "" || model != "" {
		params := map[string]any{"session_id": sessionID, "turn_id": turnID}
		if providerID != "" {
			params["provider"] = providerID
		}
		if model != "" {
			params["modelId"] = model
			params["model"] = model
		}
		encoded, err := json.Marshal(params)
		if err != nil {
			return err
		}
		response, err := transport.Send(ctx, SandboxBridgeRequest{
			ID: wireBaseID + ":set_model", Method: "pi/set_model", Params: encoded,
		})
		if err != nil {
			return err
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return &SandboxBridgeRPCError{RequestID: wireBaseID + ":set_model"}
		}
	}
	if effort != "" {
		params := map[string]any{
			"session_id": sessionID,
			"turn_id":    turnID,
			"level":      effort,
		}
		encoded, err := json.Marshal(params)
		if err != nil {
			return err
		}
		response, err := transport.Send(ctx, SandboxBridgeRequest{
			ID: wireBaseID + ":set_thinking_level", Method: "pi/set_thinking_level", Params: encoded,
		})
		if err != nil {
			return err
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return &SandboxBridgeRPCError{RequestID: wireBaseID + ":set_thinking_level"}
		}
	}
	return nil
}
