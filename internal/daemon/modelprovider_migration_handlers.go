package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/modelprovidermigrate"
	"pentest/internal/runtimeprofile"
)

func (server *Server) handlePreviewModelProviderMigration(response http.ResponseWriter, request *http.Request) {
	preview, err := server.modelProviderMigrator().Preview(request.PathValue("id"))
	if err != nil {
		writeModelProviderMigrationError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, preview)
}

func (server *Server) handleApplyModelProviderMigration(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Action        modelprovidermigrate.Action `json:"action"`
		ProviderID    string                      `json:"provider_id,omitempty"`
		ProviderName  string                      `json:"provider_name,omitempty"`
		MigrateAPIKey bool                        `json:"migrate_api_key"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	result, err := server.modelProviderMigrator().Apply(modelprovidermigrate.ApplyRequest{
		ProfileID:     request.PathValue("id"),
		Action:        input.Action,
		ProviderID:    input.ProviderID,
		ProviderName:  input.ProviderName,
		MigrateAPIKey: input.MigrateAPIKey,
	})
	if err != nil {
		writeModelProviderMigrationError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Profile  runtimeprofile.Profile `json:"profile"`
		Provider any                    `json:"provider"`
	}{
		Profile:  runtimeprofile.SanitizeProfile(result.Profile),
		Provider: result.Provider,
	})
}

func (server *Server) modelProviderMigrator() *modelprovidermigrate.Service {
	return modelprovidermigrate.NewService(server.profiles, server.modelProviders, server.creds, server.runtimePlugins)
}

func writeModelProviderMigrationError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modelprovidermigrate.ErrNotFound),
		errors.Is(err, runtimeprofile.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, modelprovidermigrate.ErrNotEligible),
		errors.Is(err, modelprovidermigrate.ErrMissingProviderID),
		errors.Is(err, modelprovidermigrate.ErrIncompatibleProfile):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, modelprovidermigrate.ErrProviderNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, err.Error())
	}
}