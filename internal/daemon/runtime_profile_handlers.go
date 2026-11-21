package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/runtimeprofile"
)

func (server *Server) handleListRuntimeProfiles(response http.ResponseWriter, request *http.Request) {
	profiles, err := server.profiles.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list runtime profiles")
		return
	}
	if profiles == nil {
		profiles = []runtimeprofile.Profile{}
	}
	sanitized := make([]runtimeprofile.Profile, len(profiles))
	for i, profile := range profiles {
		sanitized[i] = runtimeprofile.SanitizeProfile(profile)
	}
	writeJSON(response, http.StatusOK, struct {
		Profiles []runtimeprofile.Profile `json:"profiles"`
	}{
		Profiles: sanitized,
	})
}

func (server *Server) handleCreateRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name     string                  `json:"name"`
		Provider runtimeprofile.Provider `json:"provider"`
		Fields   runtimeprofile.Fields   `json:"fields"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	created, err := server.profiles.Create(input.Name, input.Provider, input.Fields)
	if err != nil {
		switch {
		case errors.Is(err, runtimeprofile.ErrMissingName),
			errors.Is(err, runtimeprofile.ErrMissingProvider),
			errors.Is(err, runtimeprofile.ErrUnknownProvider),
			errors.Is(err, runtimeprofile.ErrInvalidReasoningEffort),
			errors.Is(err, runtimeprofile.ErrInvalidCodexMultiAgent),
			errors.Is(err, runtimeprofile.ErrCustomArgConflict):
			server.logCustomArgConflict(input.Provider, input.Fields.CustomArgs, err)
			writeError(response, http.StatusBadRequest, err.Error())
		default:
			writeError(response, http.StatusInternalServerError, "store runtime profile")
		}
		return
	}

	writeJSON(response, http.StatusCreated, runtimeprofile.SanitizeProfile(created))
}

func (server *Server) handleGetRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	found, err := server.profiles.Get(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return
	}

	writeJSON(response, http.StatusOK, runtimeprofile.SanitizeProfile(found))
}

func (server *Server) handleUpdateRuntimeProfile(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	var input struct {
		Name     *string                  `json:"name"`
		Provider *runtimeprofile.Provider `json:"provider"`
		Fields   *runtimeprofile.Fields   `json:"fields"`
		// ConfirmProviderSwitchClearsOverlay confirms discarding a non-empty
		// Custom Config File when switching provider.
		ConfirmProviderSwitchClearsOverlay *bool `json:"confirm_provider_switch_clears_overlay,omitempty"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	name := ""
	if input.Name != nil {
		name = *input.Name
	}
	provider := runtimeprofile.Provider("")
	if input.Provider != nil {
		provider = *input.Provider
	}
	var fields runtimeprofile.Fields
	fieldsTouched := false
	if input.Fields != nil {
		fields = *input.Fields
		fieldsTouched = true
	}
	confirmProviderSwitchClearsOverlay := input.ConfirmProviderSwitchClearsOverlay != nil && *input.ConfirmProviderSwitchClearsOverlay

	updated, err := server.profiles.Update(id, name, provider, fields, fieldsTouched, confirmProviderSwitchClearsOverlay)
	if err != nil {
		var switchErr *runtimeprofile.ProviderSwitchNeedsOverlayClearError
		switch {
		case errors.Is(err, runtimeprofile.ErrNotFound):
			writeError(response, http.StatusNotFound, err.Error())
		case errors.As(err, &switchErr):
			writeJSON(response, http.StatusConflict, map[string]any{
				"error": switchErr.Error(),
				"code":  "provider_switch_needs_overlay_clear",
			})
		case errors.Is(err, runtimeprofile.ErrUnknownProvider),
			errors.Is(err, runtimeprofile.ErrInvalidReasoningEffort),
			errors.Is(err, runtimeprofile.ErrInvalidCodexMultiAgent),
			errors.Is(err, runtimeprofile.ErrCustomArgConflict):
			logProvider := provider
			if logProvider == "" {
				if existing, getErr := server.profiles.Get(id); getErr == nil {
					logProvider = existing.Provider
				}
			}
			logArgs := fields.CustomArgs
			if !fieldsTouched {
				if existing, getErr := server.profiles.Get(id); getErr == nil {
					logArgs = existing.Fields.CustomArgs
				}
			}
			server.logCustomArgConflict(logProvider, logArgs, err)
			writeError(response, http.StatusBadRequest, err.Error())
		default:
			writeError(response, http.StatusInternalServerError, "store runtime profile update")
		}
		return
	}

	writeJSON(response, http.StatusOK, runtimeprofile.SanitizeProfile(updated))
}

// handleImportRuntimeProfileConfig runs one Profile Config Import: parse the
// edited provider-native config text, sync structured-expressible keys, and
// store the remainder as the Custom Config File. Refusals return per-key
// errors with a 400.
func (server *Server) handleImportRuntimeProfileConfig(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}

	var input struct {
		ConfigText string `json:"config_text"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	result, err := server.profiles.ImportConfig(id, runtimeprofile.ImportConfigRequest{
		ConfigText: input.ConfigText,
	})
	if err != nil {
		var refusal *runtimeprofile.ImportConfigError
		if errors.As(err, &refusal) {
			writeJSON(response, http.StatusBadRequest, map[string]any{
				"error": refusal.Error(),
				"keys":  refusal.Errors,
			})
			return
		}
		if errors.Is(err, runtimeprofile.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "import runtime profile config")
		return
	}

	writeJSON(response, http.StatusOK, map[string]any{
		"profile":     runtimeprofile.SanitizeProfile(result.Profile),
		"mapped_keys": result.MappedKeys,
	})
}
