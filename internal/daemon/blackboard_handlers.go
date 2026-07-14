package daemon

import (
	"errors"
	"net/http"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardcompat"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/report"
)

// serveLegacyRead dispatches a legacy compatibility read through
// BlackboardReadService when the graph store is active (read contract §18). It
// writes the legacy-shaped result with ETag/If-None-Match handling and returns
// true; when the graph store is not yet active it returns false so the caller
// falls back to the legacy-table path.
func (server *Server) serveLegacyRead(response http.ResponseWriter, request *http.Request, readRequest blackboard.ReadRequest) bool {
	if server.reads == nil {
		return false
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		if kind := blackboardcompat.ReadCallKind(readRequest.Kind); kind != "" {
			if err := server.compatibility.RejectRetiredRead(request.Context(), kind); err != nil {
				writeCompatibilityError(response, err)
				return true
			}
			if err := server.compatibility.RecordUse(request.Context(), blackboardcompat.Use{ProjectID: readRequest.ProjectID, Transport: blackboardcompat.TransportHTTP, Kind: kind, Mode: blackboardcompat.UseModeRead}); err != nil {
				writeCompatibilityError(response, err)
				return true
			}
		}
	}
	readRequest.ProtocolVersion = blackboard.BlackboardReadProtocolVersion
	envelope, err := server.reads.Read(request.Context(), readRequest)
	if err != nil {
		writeBlackboardReadError(response, err)
		return true
	}
	etag := `"` + envelope.ProjectionHash + `"`
	response.Header().Set("ETag", etag)
	response.Header().Set("Cache-Control", "private, no-cache")
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return true
	}
	writeJSON(response, http.StatusOK, envelope.Result)
	return true
}

func (server *Server) handleUpsertProjectFact(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	factKey := request.PathValue("fact_key")
	if !server.requireCompatibilityProject(response, request, projectID) {
		return
	}
	if factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}

	var input struct {
		FactKey         string                 `json:"fact_key"`
		Category        string                 `json:"category"`
		Summary         string                 `json:"summary"`
		Body            string                 `json:"body"`
		Confidence      blackboard.Confidence  `json:"confidence"`
		ScopeStatus     blackboard.ScopeStatus `json:"scope_status"`
		ExpectedVersion *int                   `json:"expected_version,omitempty"`
		IdempotencyKey  string                 `json:"idempotency_key,omitempty"`
	}
	if !server.decodeCompatibilityJSON(response, request, &input) {
		return
	}
	if input.FactKey == "" {
		input.FactKey = factKey
	}
	if input.FactKey != factKey {
		if server.compatibility != nil {
			writeCompatibilityError(response, projectinterface.ValidationError(projectinterface.ErrCodeInvalidRequest, "fact key path and body must match", "fact_key"))
		} else {
			writeError(response, http.StatusBadRequest, "fact key path and body must match")
		}
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		idempotencyKey := request.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			idempotencyKey = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: idempotencyKey,
			ExpectedVersion: input.ExpectedVersion,
			Fact: &blackboardcompat.FactWrite{
				FactKey: input.FactKey, Category: input.Category, Summary: input.Summary,
				Body: input.Body, Confidence: string(input.Confidence), ScopeStatus: string(input.ScopeStatus),
			},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
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
	if !server.requireProject(response, projectID) {
		return
	}

	// ?include_deprecated=1 surfaces deprecated facts alongside Current Truth.
	// Default omits them, matching the dashboard counts and runtime context.
	includeDeprecated := request.URL.Query().Get("include_deprecated") == "1"
	if server.serveLegacyRead(response, request, blackboard.ReadRequest{ProjectID: projectID, Kind: blackboard.ReadKindLegacyFactIndexV1, LegacyFactIndex: &blackboard.LegacyFactIndexRequest{IncludeDeprecated: &includeDeprecated}}) {
		return
	}
	opts := blackboard.FactIndexOptions{IncludeDeprecated: includeDeprecated}

	facts, err := server.facts.FactIndex(projectID, opts)
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
	if !server.requireProject(response, projectID) {
		return
	}
	if factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}

	if server.serveLegacyRead(response, request, blackboard.ReadRequest{ProjectID: projectID, Kind: blackboard.ReadKindLegacyFactDetailV1, LegacyFactDetail: &blackboard.LegacyFactDetailRequest{FactKey: factKey}}) {
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

// handleMergeFacts governs a Fact Merge: the source fact key is consolidated
// into the canonical key and becomes an alias of it (CONTEXT.md). The route is
// project-scoped like every other blackboard route.
func (server *Server) handleMergeFacts(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireCompatibilityProject(response, request, projectID) {
		return
	}

	var input struct {
		SourceFactKey    string `json:"source_fact_key"`
		CanonicalFactKey string `json:"canonical_fact_key"`
		IdempotencyKey   string `json:"idempotency_key,omitempty"`
	}
	if !server.decodeCompatibilityJSON(response, request, &input) {
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		key := request.Header.Get("Idempotency-Key")
		if key == "" {
			key = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallMergeFacts, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: key,
			FactMerge: &blackboardcompat.MergeWrite{SourceKey: input.SourceFactKey, CanonicalKey: input.CanonicalFactKey},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
		return
	}

	if err := server.facts.MergeFacts(blackboard.MergeFactsRequest{
		ProjectID:        projectID,
		SourceFactKey:    input.SourceFactKey,
		CanonicalFactKey: input.CanonicalFactKey,
	}); err != nil {
		switch {
		case errors.Is(err, blackboard.ErrSelfMerge), errors.Is(err, blackboard.ErrMissingFactKey):
			writeError(response, http.StatusBadRequest, err.Error())
		case errors.Is(err, blackboard.ErrNotFound):
			writeError(response, http.StatusNotFound, err.Error())
		default:
			writeError(response, http.StatusInternalServerError, "merge facts")
		}
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Merged bool `json:"merged"`
	}{Merged: true})
}

// handleMergeFindings governs a Finding Merge: the source finding key is
// consolidated into the canonical key and becomes an alias of it (CONTEXT.md).
func (server *Server) handleMergeFindings(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	if !server.requireCompatibilityProject(response, request, projectID) {
		return
	}

	var input struct {
		SourceFindingKey    string `json:"source_finding_key"`
		CanonicalFindingKey string `json:"canonical_finding_key"`
		IdempotencyKey      string `json:"idempotency_key,omitempty"`
	}
	if !server.decodeCompatibilityJSON(response, request, &input) {
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		key := request.Header.Get("Idempotency-Key")
		if key == "" {
			key = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallMergeFindings, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: key,
			FindingMerge: &blackboardcompat.MergeWrite{SourceKey: input.SourceFindingKey, CanonicalKey: input.CanonicalFindingKey},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
		return
	}

	if err := server.facts.MergeFindings(blackboard.MergeFindingsRequest{
		ProjectID:           projectID,
		SourceFindingKey:    input.SourceFindingKey,
		CanonicalFindingKey: input.CanonicalFindingKey,
	}); err != nil {
		switch {
		case errors.Is(err, blackboard.ErrSelfMerge), errors.Is(err, blackboard.ErrMissingFindingKey):
			writeError(response, http.StatusBadRequest, err.Error())
		case errors.Is(err, blackboard.ErrNotFound):
			writeError(response, http.StatusNotFound, err.Error())
		default:
			writeError(response, http.StatusInternalServerError, "merge findings")
		}
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Merged bool `json:"merged"`
	}{Merged: true})
}

func (server *Server) handleProjectFactVersions(response http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("id")
	factKey := request.PathValue("fact_key")
	if !server.requireProject(response, projectID) {
		return
	}
	if factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}

	if server.serveLegacyRead(response, request, blackboard.ReadRequest{ProjectID: projectID, Kind: blackboard.ReadKindLegacyFactVersionsV1, LegacyFactVersions: &blackboard.LegacyFactVersionsRequest{FactKey: factKey}}) {
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
	if !server.requireCompatibilityProject(response, request, projectID) {
		return
	}
	if factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}

	var input struct {
		TargetFactKey   string `json:"target_fact_key"`
		Relation        string `json:"relation"`
		Summary         string `json:"summary"`
		ExpectedVersion *int   `json:"expected_version,omitempty"`
		IdempotencyKey  string `json:"idempotency_key,omitempty"`
	}
	if !server.decodeCompatibilityJSON(response, request, &input) {
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		idempotencyKey := request.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			idempotencyKey = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallPutFactRelation, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: idempotencyKey,
			ExpectedVersion: input.ExpectedVersion,
			Relation: &blackboardcompat.FactRelationWrite{
				SourceFactKey: factKey, TargetFactKey: input.TargetFactKey,
				Relation: input.Relation, Summary: input.Summary,
			},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
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
	if !server.requireProject(response, projectID) {
		return
	}
	if factKey == "" {
		writeError(response, http.StatusNotFound, "project fact not found")
		return
	}

	if server.serveLegacyRead(response, request, blackboard.ReadRequest{ProjectID: projectID, Kind: blackboard.ReadKindLegacyFactRelationsV1, LegacyFactRelations: &blackboard.LegacyFactRelationsRequest{FactKey: factKey}}) {
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
	if !server.requireCompatibilityProject(response, request, projectID) {
		return
	}
	if findingKey == "" {
		writeError(response, http.StatusNotFound, "finding not found")
		return
	}

	var input struct {
		Title           string                   `json:"title"`
		Description     string                   `json:"description"`
		Status          blackboard.FindingStatus `json:"status"`
		Target          string                   `json:"target"`
		Proof           string                   `json:"proof"`
		Impact          string                   `json:"impact"`
		Recommendation  string                   `json:"recommendation"`
		CVSSVersion     string                   `json:"cvss_version"`
		CVSSVector      string                   `json:"cvss_vector"`
		ExpectedVersion *int                     `json:"expected_version,omitempty"`
		IdempotencyKey  string                   `json:"idempotency_key,omitempty"`
	}
	if !server.decodeCompatibilityJSON(response, request, &input) {
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		key := request.Header.Get("Idempotency-Key")
		if key == "" {
			key = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallUpsertFinding, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: key, ExpectedVersion: input.ExpectedVersion,
			Finding: &blackboardcompat.FindingWrite{
				FindingKey: findingKey, Title: input.Title, Description: input.Description, Status: string(input.Status),
				Target: input.Target, Proof: input.Proof, Impact: input.Impact, Recommendation: input.Recommendation,
				CVSSVersion: input.CVSSVersion, CVSSVector: input.CVSSVector,
			},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
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
	if !server.requireProject(response, projectID) {
		return
	}
	if server.serveLegacyRead(response, request, blackboard.ReadRequest{ProjectID: projectID, Kind: blackboard.ReadKindLegacyFindingCollectionV1, LegacyFindingCollection: &blackboard.LegacyFindingCollectionRequest{}}) {
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
	if !server.requireProject(response, projectID) {
		return
	}
	if findingKey == "" {
		writeError(response, http.StatusNotFound, "finding not found")
		return
	}

	if server.serveLegacyRead(response, request, blackboard.ReadRequest{ProjectID: projectID, Kind: blackboard.ReadKindLegacyFindingVersionsV1, LegacyFindingVersions: &blackboard.LegacyFindingVersionsRequest{FindingKey: findingKey}}) {
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
	if !server.requireProject(response, projectID) {
		return
	}
	if server.serveLegacyRead(response, request, blackboard.ReadRequest{ProjectID: projectID, Kind: blackboard.ReadKindLegacyEvidenceCollectionV1, LegacyEvidenceCollection: &blackboard.LegacyEvidenceCollectionRequest{}}) {
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
	if !server.requireCompatibilityProject(response, request, projectID) {
		return
	}

	var input struct {
		EvidenceKey     string                        `json:"evidence_key"`
		AttachToType    blackboard.EvidenceAttachType `json:"attach_to_type"`
		AttachToKey     string                        `json:"attach_to_key"`
		ArtifactType    string                        `json:"artifact_type"`
		SourcePath      string                        `json:"source_path"`
		SHA256          string                        `json:"sha256"`
		Summary         string                        `json:"summary"`
		ExpectedVersion *int                          `json:"expected_version,omitempty"`
		IdempotencyKey  string                        `json:"idempotency_key,omitempty"`
	}
	if !server.decodeCompatibilityJSON(response, request, &input) {
		return
	}
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		key := request.Header.Get("Idempotency-Key")
		if key == "" {
			key = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallAttachEvidence, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: key, ExpectedVersion: input.ExpectedVersion,
			Evidence: &blackboardcompat.EvidenceWrite{
				EvidenceKey: input.EvidenceKey, AttachToType: string(input.AttachToType), AttachToKey: input.AttachToKey,
				ArtifactType: input.ArtifactType, SourcePath: input.SourcePath, Summary: input.Summary,
			},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
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
	if server.compatibility != nil {
		setCompatibilityHeaders(response)
	}
	if projectID == "" {
		writeError(response, http.StatusNotFound, "project not found")
		return
	}
	found, err := server.projects.Get(projectID)
	if err != nil {
		if server.compatibility != nil {
			if errors.Is(err, project.ErrNotFound) {
				writeCompatibilityError(response, projectinterface.ValidationError(projectinterface.ErrCodeProjectNotFound, "Project not found", "project_id"))
			} else {
				writeCompatibilityError(response, projectinterface.InternalError("load compatibility Project: "+err.Error()))
			}
			return
		}
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
		TaskID         string `json:"task_id"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}
	if request.ContentLength > 0 {
		if !server.decodeCompatibilityJSON(response, request, &input) {
			return
		}
	}

	// Graph epoch: delegate to PentestReportV1 (read contract §18.5). A task_id
	// selects the task Scope; an absent task_id uses the current Scope. CTF
	// Projects fail project_kind_mismatch rather than rendering a vuln report.
	if server.reads != nil {
		setCompatibilityHeaders(response)
		principal, err := server.requestCompatibilityPrincipal(request, projectID)
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		key := request.Header.Get("Idempotency-Key")
		if key == "" {
			key = input.IdempotencyKey
		}
		result, err := server.compatibility.Call(request.Context(), blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallGenerateReport, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectID, Principal: principal, IdempotencyKey: key,
			Report: &blackboardcompat.ReportWrite{TaskID: input.TaskID},
		})
		if err != nil {
			writeCompatibilityError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result.Payload)
		return
	}

	taskID := input.TaskID
	if taskID == "" {
		tasks, err := server.tasks.ListForProject(projectID)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "list tasks")
			return
		}
		if len(tasks) > 0 {
			taskID = tasks[len(tasks)-1].ID
		}
	}
	if taskID != "" {
		generator := report.NewGenerator(server.facts, server.tasks)
		out, err := generator.Generate(report.Request{ProjectID: projectID, TaskID: taskID})
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
