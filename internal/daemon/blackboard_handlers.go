package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/blackboard"
	"pentest/internal/project"
)

func (server *Server) handleUpsertProjectFact(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	factKey := request.PathValue("fact_key")
	if projectID == "" || factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	var input struct {
		FactKey     string                 `json:"fact_key"`
		Category    string                 `json:"category"`
		Summary     string                 `json:"summary"`
		Body        string                 `json:"body"`
		Confidence  blackboard.Confidence  `json:"confidence"`
		ScopeStatus blackboard.ScopeStatus `json:"scope_status"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.FactKey == "" {
		input.FactKey = factKey
	}
	if input.FactKey != factKey {
		writeError(response, http.StatusBadRequest, "fact key path and body must match")
		return
	}

	fact, err := server.facts.UpsertFact(blackboard.UpsertFactRequest{
		ProjectID:   projectID,
		FactKey:     input.FactKey,
		Category:    input.Category,
		Summary:     input.Summary,
		Body:        input.Body,
		Confidence:  input.Confidence,
		ScopeStatus: input.ScopeStatus,
	})
	if err != nil {
		writeFactError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, fact)
}

func (server *Server) handleFactIndex(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	facts, err := server.facts.FactIndex(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list fact index")
		return
	}
	if facts == nil {
		facts = []blackboard.FactIndexEntry{}
	}
	writeJSON(response, http.StatusOK, struct {
		Facts []blackboard.FactIndexEntry `json:"facts"`
	}{
		Facts: facts,
	})
}

func (server *Server) handleGetProjectFact(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	factKey := request.PathValue("fact_key")
	if projectID == "" || factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	fact, err := server.facts.GetFact(projectID, factKey)
	if err != nil {
		if errors.Is(err, blackboard.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project fact")
		return
	}
	writeJSON(response, http.StatusOK, fact)
}

func (server *Server) handleProjectFactVersions(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	factKey := request.PathValue("fact_key")
	if projectID == "" || factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}
	if _, err := server.projects.Get(projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	versions, err := server.facts.FactVersions(projectID, factKey)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list fact versions")
		return
	}
	if versions == nil {
		versions = []blackboard.FactVersion{}
	}
	writeJSON(response, http.StatusOK, struct {
		Versions []blackboard.FactVersion `json:"versions"`
	}{
		Versions: versions,
	})
}

func writeFactError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, blackboard.ErrMissingFactKey), errors.Is(err, blackboard.ErrMissingSummary):
		writeError(response, http.StatusBadRequest, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "fact operation failed")
	}
}
