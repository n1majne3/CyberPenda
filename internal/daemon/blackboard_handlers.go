package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"pentest/internal/blackboard"
	"pentest/internal/project"
	"pentest/internal/report"
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

func (server *Server) handleUpsertFactRelation(response http.ResponseWriter, request *http.Request) {
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
		TargetFactKey string `json:"target_fact_key"`
		Relation      string `json:"relation"`
		Summary       string `json:"summary"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	relation, err := server.facts.UpsertFactRelation(blackboard.UpsertFactRelationRequest{
		ProjectID:     projectID,
		SourceFactKey: factKey,
		TargetFactKey: input.TargetFactKey,
		Relation:      input.Relation,
		Summary:       input.Summary,
	})
	if err != nil {
		writeFactError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, relation)
}

func (server *Server) handleFactRelations(response http.ResponseWriter, request *http.Request) {
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

	relations, err := server.facts.FactRelations(projectID, factKey)
	if err != nil {
		writeFactError(response, err)
		return
	}
	if relations == nil {
		relations = []blackboard.FactRelation{}
	}
	writeJSON(response, http.StatusOK, struct {
		Relations []blackboard.FactRelation `json:"relations"`
	}{
		Relations: relations,
	})
}

func (server *Server) handleUpsertFinding(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	findingKey := request.PathValue("finding_key")
	if projectID == "" || findingKey == "" {
		writeError(response, http.StatusNotFound, "finding not found")
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
		Title          string                   `json:"title"`
		Description    string                   `json:"description"`
		Status         blackboard.FindingStatus `json:"status"`
		Target         string                   `json:"target"`
		Proof          string                   `json:"proof"`
		Impact         string                   `json:"impact"`
		Recommendation string                   `json:"recommendation"`
		CVSSVersion    string                   `json:"cvss_version"`
		CVSSVector     string                   `json:"cvss_vector"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	finding, err := server.facts.UpsertFinding(blackboard.UpsertFindingRequest{
		ProjectID:      projectID,
		FindingKey:     findingKey,
		Title:          input.Title,
		Description:    input.Description,
		Status:         input.Status,
		Target:         input.Target,
		Proof:          input.Proof,
		Impact:         input.Impact,
		Recommendation: input.Recommendation,
		CVSSVersion:    input.CVSSVersion,
		CVSSVector:     input.CVSSVector,
	})
	if err != nil {
		writeFactError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, finding)
}

func (server *Server) handleListFindings(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}
	findings, err := server.facts.ListFindings(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list findings")
		return
	}
	if findings == nil {
		findings = []blackboard.Finding{}
	}
	writeJSON(response, http.StatusOK, struct {
		Findings []blackboard.Finding `json:"findings"`
	}{
		Findings: findings,
	})
}

func (server *Server) handleFindingVersions(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	findingKey := request.PathValue("finding_key")
	if projectID == "" || findingKey == "" {
		writeError(response, http.StatusNotFound, "finding not found")
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

	versions, err := server.facts.FindingVersions(projectID, findingKey)
	if err != nil {
		writeFactError(response, err)
		return
	}
	if versions == nil {
		versions = []blackboard.FindingVersion{}
	}
	writeJSON(response, http.StatusOK, struct {
		Versions []blackboard.FindingVersion `json:"versions"`
	}{
		Versions: versions,
	})
}

func (server *Server) handleListEvidence(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}
	evidence, err := server.facts.ListEvidence(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list evidence")
		return
	}
	if evidence == nil {
		evidence = []blackboard.EvidenceArtifact{}
	}
	writeJSON(response, http.StatusOK, struct {
		Evidence []blackboard.EvidenceArtifact `json:"evidence"`
	}{
		Evidence: evidence,
	})
}

func (server *Server) handleAttachEvidence(response http.ResponseWriter, request *http.Request) {
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

	var input struct {
		EvidenceKey  string                        `json:"evidence_key"`
		AttachToType blackboard.EvidenceAttachType `json:"attach_to_type"`
		AttachToKey  string                        `json:"attach_to_key"`
		ArtifactType string                        `json:"artifact_type"`
		SourcePath   string                        `json:"source_path"`
		SHA256       string                        `json:"sha256"`
		Summary      string                        `json:"summary"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid JSON body")
		return
	}

	artifact, err := server.facts.AttachEvidence(blackboard.AttachEvidenceRequest{
		ProjectID:    projectID,
		EvidenceKey:  input.EvidenceKey,
		AttachToType: input.AttachToType,
		AttachToKey:  input.AttachToKey,
		ArtifactType: input.ArtifactType,
		SourcePath:   input.SourcePath,
		SHA256:       input.SHA256,
		Summary:      input.Summary,
	})
	if err != nil {
		writeFactError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, artifact)
}

func (server *Server) handleReportTrigger(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}
	found, err := server.projects.Get(projectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(response, http.StatusNotFound, err.Error())
			return
		}
		writeError(response, http.StatusInternalServerError, "load project")
		return
	}

	// A request may name a task to anchor runner and scope context. With a
	// task id we render the full report derived from stored state; without one
	// we fall back to the inventory stub.
	var input struct {
		TaskID string `json:"task_id"`
	}
	if request.ContentLength > 0 {
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			writeError(response, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	if input.TaskID != "" {
		generator := report.NewGenerator(server.facts, server.tasks)
		out, err := generator.Generate(report.Request{ProjectID: projectID, TaskID: input.TaskID})
		if err != nil {
			writeError(response, http.StatusInternalServerError, "generate report")
			return
		}
		writeJSON(response, http.StatusOK, out)
		return
	}

	factCount, err := server.facts.CountFacts(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count facts")
		return
	}
	findingCount, err := server.facts.CountFindings(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count findings")
		return
	}
	evidenceCount, err := server.facts.CountEvidence(projectID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "count evidence")
		return
	}

	writeJSON(response, http.StatusOK, report.GenerateStub(found, report.Counts{
		Facts:    factCount,
		Findings: findingCount,
		Evidence: evidenceCount,
	}))
}

func writeFactError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, blackboard.ErrMissingFactKey), errors.Is(err, blackboard.ErrMissingSummary), errors.Is(err, blackboard.ErrMissingTargetFactKey), errors.Is(err, blackboard.ErrMissingRelation), errors.Is(err, blackboard.ErrMissingFindingKey), errors.Is(err, blackboard.ErrMissingFindingTitle), errors.Is(err, blackboard.ErrConfirmedFindingIncomplete), errors.Is(err, blackboard.ErrMissingEvidenceKey), errors.Is(err, blackboard.ErrMissingEvidenceTarget), errors.Is(err, blackboard.ErrMissingArtifactType), errors.Is(err, blackboard.ErrUnsupportedEvidenceTarget):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, blackboard.ErrNotFound):
		writeError(response, http.StatusNotFound, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "fact operation failed")
	}
}
