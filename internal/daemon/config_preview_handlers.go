package daemon

import (
	"errors"
	"net/http"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/modelprovider"
	"pentest/internal/runner"
	"pentest/internal/runtimeprofile"
)

func (server *Server) previewProjectionRequest(profiles ...runtimeprofile.Profile) runner.ProjectionRequest {
	snapshot, err := server.snapshotGlobalModelProviders()
	if err != nil {
		snapshot = runner.CloneGlobalModelProviderSnapshot(nil)
	}
	// Story 3: the daemon operator token must never reach editor text, so
	// the preview request carries no AuthToken and no credential values.
	// Credential-derived env keys render as redacted placeholders derived
	// from binding metadata only.
	req := runner.ProjectionRequest{
		ModelProviders:              server.modelProviders,
		RuntimePlugins:              server.runtimePlugins,
		GlobalModelProviderSnapshot: snapshot,
		DaemonAddr:                  server.listenAddr,
		CredentialEnvNames:          server.credentialEnvNames(),
		CapabilityCache:             server.capabilityCache,
	}
	// Resolve the preview ModelSnapshot so the editor shows every env the
	// launch projection materializes, including the Model Provider API-key
	// env (Story 2/16).
	if len(profiles) > 0 {
		profile := profiles[0]
		if strings.TrimSpace(profile.Fields.ModelProviderID) != "" && server.modelProviders != nil {
			if resolved, err := modelprovider.Resolve(modelprovider.ResolveRequest{
				Profile:         profile,
				Providers:       server.modelProviders,
				Plugins:         server.runtimePlugins,
				CapabilityCache: server.capabilityCache,
			}); err == nil && resolved.ModelProviderID != "" {
				req.ModelSnapshot = &resolved
			}
		}
	}
	return req
}

// credentialEnvNames lists the env var names global credential bindings
// project under. Metadata only — never secret values.
func (server *Server) credentialEnvNames() []string {
	bindings, err := server.creds.ListGlobal()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Disabled {
			continue
		}
		envName, err := credential.DestinationEnv(binding.Source)
		if err != nil || envName == "" {
			continue
		}
		names = append(names, envName)
	}
	return names
}

// handleMergedConfigPreview answers the final merged result the runtime
// receives: the provider-native projected config deep-merged with the
// profile's Custom Config File overlay (structured fields win conflicts).
func (server *Server) handleMergedConfigPreview(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}
	profile, err := server.profiles.Get(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return
	}
	merged, err := runner.MergedProjectedConfigWith(profile.Provider, profile, server.previewProjectionRequest(profile))
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"provider": string(profile.Provider),
		"merged":   merged,
	})
}

// handleProjectedConfig answers the provider-native seed the config editor
// opens on: a complete, realistic file derived from structured fields,
// redacted, with the stored Custom Config File carried alongside.
func (server *Server) handleProjectedConfig(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		writeError(response, http.StatusNotFound, "runtime profile not found")
		return
	}
	profile, err := server.profiles.Get(id)
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load runtime profile")
		return
	}
	text, err := runner.ProjectedConfigTextWith(profile.Provider, profile, server.previewProjectionRequest(profile))
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"provider":           string(profile.Provider),
		"format":             runner.OverlayFormat(profile.Provider),
		"text":               text,
		"custom_config_file": profile.Fields.CustomConfigFile,
	})
}
