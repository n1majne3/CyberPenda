package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
)

func (server *Server) handleListModelProviders(response http.ResponseWriter, request *http.Request) {
	providers, err := server.modelProviders.List()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list model providers")
		return
	}
	if providers == nil {
		providers = []modelprovider.Provider{}
	}
	writeJSON(response, http.StatusOK, struct {
		Providers []modelprovider.Provider `json:"providers"`
	}{Providers: providers})
}

func (server *Server) handleCreateModelProvider(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name      string                   `json:"name"`
		BaseURL   string                   `json:"base_url"`
		Protocols []modelprovider.Protocol `json:"protocols"`
		Endpoints []modelprovider.Endpoint `json:"endpoints"`
		Catalog   modelprovider.Catalog    `json:"catalog"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	created, err := server.modelProviders.Create(modelprovider.CreateRequest{
		Name:      input.Name,
		BaseURL:   input.BaseURL,
		Protocols: input.Protocols,
		Endpoints: input.Endpoints,
		Catalog:   input.Catalog,
	})
	if err != nil {
		writeModelProviderError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, created)
}

func (server *Server) handleGetModelProvider(response http.ResponseWriter, request *http.Request) {
	found, err := server.modelProviders.Get(request.PathValue("id"))
	if err != nil {
		writeModelProviderError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, found)
}

func (server *Server) handleUpdateModelProvider(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Name      *string                   `json:"name"`
		BaseURL   *string                   `json:"base_url"`
		Protocols *[]modelprovider.Protocol `json:"protocols"`
		Endpoints *[]modelprovider.Endpoint `json:"endpoints"`
		Catalog   *modelprovider.Catalog    `json:"catalog"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updated, err := server.modelProviders.Update(request.PathValue("id"), modelprovider.UpdateRequest{
		Name:      input.Name,
		BaseURL:   input.BaseURL,
		Protocols: input.Protocols,
		Endpoints: input.Endpoints,
		Catalog:   input.Catalog,
	})
	if err != nil {
		writeModelProviderError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func (server *Server) handleDeleteModelProvider(response http.ResponseWriter, request *http.Request) {
	err := server.modelProviders.Delete(request.PathValue("id"))
	if err != nil {
		writeModelProviderError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleRefreshModelProviderModels(response http.ResponseWriter, request *http.Request) {
	provider, err := server.modelProviders.Get(request.PathValue("id"))
	if err != nil {
		writeModelProviderError(response, err)
		return
	}
	client := server.modelRefreshClient
	if client == nil {
		client = http.DefaultClient
	}
	if value, ok := server.materializeModelProviderCredential(provider.APIKeyEnv); ok {
		updated, err := server.modelProviders.RefreshModelsWithKey(context.Background(), provider.ID, client, value)
		if err != nil {
			writeModelProviderError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, updated)
		return
	}
	updated, err := server.modelProviders.RefreshModels(context.Background(), provider.ID, client)
	if err != nil {
		writeModelProviderError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func (server *Server) materializeModelProviderCredential(envName string) (string, bool) {
	resolution, err := server.creds.Resolve(envName, "")
	if err != nil || !resolution.Found || resolution.Disabled || resolution.Source == nil {
		return "", false
	}
	value, err := credential.Materialize(*resolution.Source)
	if err != nil {
		return "", false
	}
	if value == "" {
		return "", false
	}
	return value, true
}

func writeModelProviderError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modelprovider.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, modelprovider.ErrMissingName),
		errors.Is(err, modelprovider.ErrMissingBaseURL),
		errors.Is(err, modelprovider.ErrInvalidProtocol),
		errors.Is(err, modelprovider.ErrDuplicateEndpointProtocol),
		errors.Is(err, modelprovider.ErrInvalidEndpointBaseURL):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, modelprovider.ErrInUse):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, err.Error())
	}
}
