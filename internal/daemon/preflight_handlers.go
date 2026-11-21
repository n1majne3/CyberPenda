package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"pentest/internal/preflight"
	"pentest/internal/project"
	"pentest/internal/runtimeconfig"
	"pentest/internal/runtimeprofile"
	"pentest/internal/task"
)

func (server *Server) handlePreflight(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}

	var input struct {
		RuntimeProfileID        string           `json:"runtime_profile_id"`
		RuntimePluginID         string           `json:"runtime_plugin_id,omitempty"`
		ModelProviderID         string           `json:"model_provider_id,omitempty"`
		Model                   string           `json:"model,omitempty"`
		ModelOverride           string           `json:"model_override,omitempty"`
		ReasoningEffort         string           `json:"reasoning_effort,omitempty"`
		Runner                  string           `json:"runner"`
		RunControls             task.RunControls `json:"run_controls"`
		CredentialRefsToResolve []string         `json:"credential_refs"`
		HostActivated           bool             `json:"host_activated"`
		HostBrowserEnabled      bool             `json:"host_browser_enabled"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	defaulted, err := server.applyTaskLaunchDefaults(projectID, input.RuntimeProfileID, task.Runner(input.Runner))
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project defaults")
		return
	}

	hostActivated := input.RunControls.HostActivated || input.HostActivated
	if _, err := normalizeLaunchReasoningEffort(input.ReasoningEffort); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = strings.TrimSpace(input.ModelOverride)
	}
	resolvedConfiguration, err := server.resolveLaunchConfiguration(runtimeconfig.LaunchSelection{
		RuntimeProfileID: input.RuntimeProfileID, RuntimePluginID: input.RuntimePluginID,
		ModelProviderID: input.ModelProviderID, Model: model,
		ReasoningEffort: input.ReasoningEffort, Runner: string(defaulted.runner),
		HostBrowserEnabled: input.HostBrowserEnabled,
	}, projectID)
	var resolvedProfile *runtimeprofile.Profile
	if err == nil {
		resolvedProfile = &resolvedConfiguration.Profile
	} else if strings.TrimSpace(defaulted.runtimeProfileID) == "" {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	result := server.preflight.Run(request.Context(), preflight.Request{
		RuntimeProfileID:        defaulted.runtimeProfileID,
		Profile:                 resolvedProfile,
		LaunchModelOverride:     model,
		ProjectID:               projectID,
		CredentialRefsToResolve: input.CredentialRefsToResolve,
		Runner:                  string(defaulted.runner),
		HostActivated:           hostActivated,
		ProjectKind:             defaulted.project.Kind,
		ScopeCapabilities:       append([]string(nil), defaulted.project.Scope.Capabilities...),
		ContainerCLI:            task.ResolveContainerCLI(input.RunControls.ContainerCLI, server.containerCLI),
		SandboxVPNTun:           input.RunControls.SandboxVPNTun,
		SandboxNetwork:          input.RunControls.SandboxNetwork,
		RuntimeRoot:             server.runtimeRoot,
	})
	server.logPreflightCustomArgConflict(defaulted.runtimeProfileID, result)
	server.checkHostBrowser(&result, input.HostBrowserEnabled, resolvedConfiguration.Profile.Provider)

	writeJSON(response, http.StatusOK, result)
}
