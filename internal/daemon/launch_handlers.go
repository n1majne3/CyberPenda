package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeprofile"
)

func (server *Server) handleResolveLaunchRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Provider          runtimeprofile.Provider `json:"provider"`
		ModelProviderID   string                  `json:"model_provider_id"`
		ModelOverride     string                  `json:"model_override,omitempty"`
		ModelProviderName string                  `json:"model_provider_name,omitempty"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	providerName := input.ModelProviderName
	if providerName == "" && server.modelProviders != nil && input.ModelProviderID != "" {
		if provider, err := server.modelProviders.Get(input.ModelProviderID); err == nil {
			providerName = provider.Name
		}
	}

	resolution, err := server.profiles.ResolveLaunchProfile(runtimeprofile.LaunchSelection{
		Provider:        input.Provider,
		ModelProviderID: input.ModelProviderID,
		ModelOverride:   input.ModelOverride,
	}, providerName)
	if err != nil {
		if errors.Is(err, modelprovider.ErrNotFound) {
			writeError(response, http.StatusBadRequest, err.Error())
			return
		}
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(response, http.StatusOK, struct {
		ProfileID string                    `json:"profile_id"`
		Profile   runtimeprofile.Profile    `json:"profile"`
		Created   bool                      `json:"created"`
	}{
		ProfileID: resolution.Profile.ID,
		Profile:   runtimeprofile.SanitizeProfile(resolution.Profile),
		Created:   resolution.Created,
	})
}