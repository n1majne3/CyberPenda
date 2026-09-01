package daemon

import (
	"net/http"

	"pentest/internal/runtimeplugin"
)

func (server *Server) handleListRuntimePlugins(response http.ResponseWriter, request *http.Request) {
	plugins := server.runtimePlugins.List()
	if plugins == nil {
		plugins = []runtimeplugin.Plugin{}
	}
	for index := range plugins {
		plugins[index] = server.effectiveRuntimePlugin(plugins[index])
	}
	writeJSON(response, http.StatusOK, struct {
		Plugins []runtimeplugin.Plugin `json:"plugins"`
	}{
		Plugins: plugins,
	})
}

func (server *Server) handleGetRuntimePlugin(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("plugin_id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime plugin not found")
		return
	}
	plugin, ok := server.runtimePlugins.Get(id)
	if !ok {
		writeError(response, http.StatusNotFound, "runtime plugin not found")
		return
	}
	plugin = server.effectiveRuntimePlugin(plugin)
	writeJSON(response, http.StatusOK, plugin)
}

func (server *Server) effectiveRuntimePlugin(plugin runtimeplugin.Plugin) runtimeplugin.Plugin {
	return plugin
}
