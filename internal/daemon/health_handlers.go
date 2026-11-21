package daemon

import (
	"net/http"

	"pentest/internal/runner"
)

func (server *Server) handleHealth(response http.ResponseWriter, request *http.Request) {
	payload := struct {
		Version  string `json:"version"`
		Database struct {
			Status string `json:"status"`
		} `json:"database"`
		Runner struct {
			RuntimeRoot  string `json:"runtime_root"`
			SandboxImage string `json:"sandbox_image"`
			ContainerCLI string `json:"container_cli"`
			EngineKind   string `json:"engine_kind,omitempty"`
			EngineName   string `json:"engine_name,omitempty"`
		} `json:"runner"`
	}{
		Version: server.version,
	}
	payload.Database.Status = "ok"
	payload.Runner.RuntimeRoot = server.runtimeRoot
	payload.Runner.SandboxImage = server.sandboxImage
	payload.Runner.ContainerCLI = server.containerCLI
	if payload.Runner.ContainerCLI == "" {
		payload.Runner.ContainerCLI = "docker"
	}
	if info, err := runner.DetectEngine(request.Context(), payload.Runner.ContainerCLI, nil); err == nil {
		payload.Runner.EngineKind = string(info.Kind)
		payload.Runner.EngineName = info.Name
	} else {
		// If the configured CLI is missing, surface the first available engine so
		// the launch UI can default to Podman when only Podman is installed.
		for _, candidate := range []string{"podman", "docker"} {
			if candidate == payload.Runner.ContainerCLI {
				continue
			}
			if info, err := runner.DetectEngine(request.Context(), candidate, nil); err == nil {
				payload.Runner.EngineKind = string(info.Kind)
				payload.Runner.EngineName = info.Name
				payload.Runner.ContainerCLI = candidate
				break
			}
		}
	}

	writeJSON(response, http.StatusOK, payload)
}
