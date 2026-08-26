package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

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
	var err error
	if request.URL.Query().Get("detach") == "true" {
		err = server.modelProviders.DeleteDetaching(request.PathValue("id"))
	} else {
		err = server.modelProviders.Delete(request.PathValue("id"))
	}
	if err != nil {
		if errors.Is(err, modelprovider.ErrInUse) {
			server.writeModelProviderInUse(response, request.PathValue("id"))
			return
		}
		writeModelProviderError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// writeModelProviderInUse answers a blocked delete with the referencing
// profile names so the client can offer an explicit detach delete.
func (server *Server) writeModelProviderInUse(response http.ResponseWriter, id string) {
	profiles, err := server.modelProviders.ReferencingProfiles(id)
	if err != nil {
		writeError(response, http.StatusConflict, modelprovider.ErrInUse.Error())
		return
	}
	writeJSON(response, http.StatusConflict, struct {
		Error    string   `json:"error"`
		Profiles []string `json:"profiles"`
	}{
		Error:    modelprovider.ErrInUse.Error(),
		Profiles: profiles,
	})
}

func (server *Server) handleRefreshModelProviderModels(response http.ResponseWriter, request *http.Request) {
	provider, err := server.modelProviders.Get(request.PathValue("id"))
	if err != nil {
		writeModelProviderError(response, err)
		return
	}
	client := server.modelRefreshClient
	if client == nil {
		client = modelprovider.NewCatalogHTTPClient()
	}
	if value, ok := server.materializeModelProviderCredential(provider.APIKeyEnv); ok {
		updated, err := server.modelProviders.RefreshModelsWithKey(request.Context(), provider.ID, client, value)
		if err != nil {
			writeModelProviderError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, updated)
		return
	}
	updated, err := server.modelProviders.RefreshModels(request.Context(), provider.ID, client)
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

func (server *Server) handleGetModelCapabilityCache(response http.ResponseWriter, request *http.Request) {
	refreshedAt, count := time.Time{}, 0
	if server.capabilityCache != nil {
		refreshedAt, count = server.capabilityCache.Status()
	}
	payload := struct {
		RefreshedAt string `json:"refreshed_at,omitempty"`
		EntryCount  int    `json:"entry_count"`
	}{EntryCount: count}
	if !refreshedAt.IsZero() {
		payload.RefreshedAt = refreshedAt.Format(time.RFC3339Nano)
	}
	writeJSON(response, http.StatusOK, payload)
}

func (server *Server) handleLookupModelCapabilityCache(response http.ResponseWriter, request *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	limits := map[string]modelprovider.CatalogLimits{}
	if server.capabilityCache != nil {
		for _, id := range input.IDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if got, ok := server.capabilityCache.Lookup(id); ok {
				limits[id] = got
			}
		}
	}
	writeJSON(response, http.StatusOK, struct {
		Limits map[string]modelprovider.CatalogLimits `json:"limits"`
	}{Limits: limits})
}

func (server *Server) handleRefreshModelCapabilityCache(response http.ResponseWriter, request *http.Request) {
	if server.capabilityCache == nil {
		writeError(response, http.StatusInternalServerError, "model capability cache is unavailable")
		return
	}
	if err := server.capabilityCache.Refresh(request.Context()); err != nil {
		writeError(response, http.StatusBadGateway, err.Error())
		return
	}
	server.handleGetModelCapabilityCache(response, request)
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
	case errors.Is(err, modelprovider.ErrInvalidCatalogLimits):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, modelprovider.ErrInUse):
		writeError(response, http.StatusConflict, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, err.Error())
	}
}
