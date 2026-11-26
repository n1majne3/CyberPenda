package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pentest/internal/runtime"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/session"
)

func sessionRuntimeInputFromCreate(input createSessionInput) sessionRuntimeInput {
	return sessionRuntimeInput{
		RuntimeProfileID: input.RuntimeProfileID, RuntimePluginID: input.RuntimePluginID, Provider: input.Provider, RuntimeProvider: input.RuntimeProvider,
		ModelProviderID: input.ModelProviderID, Model: input.Model,
		ReasoningEffort: input.ReasoningEffort, Runner: input.Runner, HostActivated: input.HostActivated,
		HostBrowserEnabled: input.HostBrowserEnabled,
		RuntimeConfig:      input.RuntimeConfig,
		ContainerCLI:       input.RunControls.ContainerCLI,
		SandboxNetwork:     input.RunControls.SandboxNetwork,
		SandboxVPNTun:      input.RunControls.SandboxVPNTun,
	}
}

func sessionSelectionFromInput(selection runtime.ProviderSessionRequest, input sessionRuntimeInput) (runtime.ProviderSessionRequest, error) {
	if value := strings.TrimSpace(input.ModelProviderID); value != "" {
		selection.ModelProviderID = value
	}
	if value := input.selectedModel(); value != "" {
		selection.Model = value
	}
	if strings.TrimSpace(input.ReasoningEffort) != "" {
		reasoning, err := runtimeprofile.NormalizeReasoningEffort(input.ReasoningEffort)
		if err != nil {
			return runtime.ProviderSessionRequest{}, err
		}
		selection.RequestedReasoningEffort = string(reasoning)
	}
	return selection, nil
}

func sessionInputChangesTurnSelection(input sessionRuntimeInput) bool {
	return strings.TrimSpace(input.ModelProviderID) != "" || input.selectedModel() != "" || strings.TrimSpace(input.ReasoningEffort) != ""
}

func (server *Server) sessionTurnSelectionConfig(sessionID string, continuation session.Continuation, selection runtime.ProviderSessionRequest) (map[string]any, error) {
	versions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil || len(versions) == 0 {
		if err == nil {
			err = errors.New("runtime configuration snapshot is missing")
		}
		return nil, err
	}
	prior, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return nil, err
	}
	cloned, err := server.cloneSnapshotForTurn(prior, "", runtimeconfig.RuntimeTurnSelection{
		ModelProviderID: selection.ModelProviderID,
		Model:           selection.Model,
		ReasoningEffort: selection.RequestedReasoningEffort,
	})
	if err != nil {
		return nil, err
	}
	return runtimeSnapshotMap(cloned), nil
}

func (server *Server) sessionPiProjectedProviderAllowed(sessionID, providerID string) bool {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return false
	}
	versions, err := server.sessions.RuntimeConfigVersions(sessionID)
	if err != nil || len(versions) == 0 {
		return false
	}
	snapshot, err := decodeRuntimeSnapshot(versions[len(versions)-1].Config)
	if err != nil {
		return false
	}
	for _, projectedID := range stringSliceFromConfig(snapshot.ConfigProjection["projected_model_provider_ids"]) {
		if projectedID == providerID {
			return true
		}
	}
	return false
}

func (server *Server) recordSessionTurnSelection(sessionID string, continuation session.Continuation, selection runtime.ProviderSessionRequest) error {
	config, err := server.sessionTurnSelectionConfig(sessionID, continuation, selection)
	if err != nil {
		return err
	}
	_, err = server.sessions.RecordRuntimeConfig(sessionID, continuation.RuntimeProfileID, config)
	return err
}

func sessionContinuationSelectionConflict(continuation session.Continuation, input sessionRuntimeInput) string {
	if profileID := strings.TrimSpace(input.RuntimeProfileID); profileID != "" && profileID != continuation.RuntimeProfileID {
		return "Session Runtime profile changes require a fresh continuation"
	}
	if provider := input.provider(); provider != "" && string(provider) != continuation.RuntimeProvider {
		return "Session Runtime provider changes require a fresh continuation"
	}
	if requestedRunner := strings.TrimSpace(input.Runner); requestedRunner != "" && requestedRunner != string(continuation.Runner) {
		return "Session Runtime runner changes require a fresh continuation"
	}
	return ""
}

func sessionRequestID(request *http.Request, prefix string) string {
	if value := strings.TrimSpace(request.Header.Get("Idempotency-Key")); value != "" {
		return value
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
