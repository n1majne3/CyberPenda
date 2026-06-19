package daemon

import (
	"net/http"

	"pentest/internal/runtimeextension"
)

func (server *Server) handleListRuntimeExtensions(response http.ResponseWriter, request *http.Request) {
	extensions := server.runtimeExtensions.List()
	if extensions == nil {
		extensions = []runtimeextension.Extension{}
	}
	writeJSON(response, http.StatusOK, struct {
		Extensions []runtimeextension.Extension `json:"extensions"`
	}{
		Extensions: extensions,
	})
}

func (server *Server) handleListRuntimeExtensionCatalog(response http.ResponseWriter, request *http.Request) {
	items, errs := runtimeextension.FetchDefaultCatalog(request.Context())
	if items == nil {
		items = []runtimeextension.CatalogItem{}
	}
	if errs == nil {
		errs = []runtimeextension.CatalogSourceError{}
	}
	writeJSON(response, http.StatusOK, struct {
		Items  []runtimeextension.CatalogItem        `json:"items"`
		Errors []runtimeextension.CatalogSourceError `json:"errors,omitempty"`
	}{
		Items:  items,
		Errors: errs,
	})
}

func (server *Server) handleGetRuntimeExtension(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("extension_id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime extension not found")
		return
	}
	extension, ok := server.runtimeExtensions.Get(id)
	if !ok {
		writeError(response, http.StatusNotFound, "runtime extension not found")
		return
	}
	writeJSON(response, http.StatusOK, extension)
}
