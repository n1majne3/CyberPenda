package blackboard

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"pentest/internal/project"
)

const (
	pentestReportRendererVersion = "pentest_markdown_v1"
	ctfSolutionRendererVersion   = "ctf_solution_markdown_v1"
)

// PentestReportRequest selects the deterministic Pentest report projection.
type PentestReportRequest struct {
	IncludeUnconfirmed       bool
	IncludeTentativeFacts    bool
	IncludeOutOfScopeContext bool
	IncludeUnresolvedWork    bool
	ScopeContext             string // current | task:TASK_ID
	EvidenceDetail           string // summary | index
	Format                   string // json | markdown
}

// CTFSolutionRequest selects the deterministic CTF solution projection.
type CTFSolutionRequest struct {
	IncludeCandidates bool
	IncludeProcedure  bool
	Format            string // json | markdown
}

// ReportMarkdownV1 wraps deterministic Markdown bytes for report projections.
type ReportMarkdownV1 struct {
	Source   ReportSourceV1 `json:"source"`
	Markdown string         `json:"markdown"`
}

// ReportSourceV1 is the shared source envelope for report deliverables.
type ReportSourceV1 struct {
	ProjectID       string `json:"project_id"`
	ProjectName     string `json:"project_name"`
	GraphRevision   int    `json:"graph_revision"`
	StateHash       string `json:"state_hash"`
	SourceHash      string `json:"source_hash"`
	ScopeContext    string `json:"scope_context,omitempty"`
	RendererVersion string `json:"renderer_version"`
}

type ReportRunnerSummaryV1 struct {
	Sandbox int `json:"sandbox"`
	Host    int `json:"host"`
}

type ReportContributingTaskV1 struct {
	TaskID        string        `json:"task_id"`
	Goal          string        `json:"goal"`
	Status        string        `json:"status"`
	Runner        string        `json:"runner"`
	ScopeSnapshot project.Scope `json:"scope_snapshot"`
	CrossTask     bool          `json:"cross_task,omitempty"`
}

type ReportEngagementV1 struct {
	Description       string                     `json:"description"`
	Scope             project.Scope              `json:"scope"`
	TestingLimits     []string                   `json:"testing_limits"`
	ScopeNotes        *string                    `json:"scope_notes"`
	ContributingTasks []ReportContributingTaskV1 `json:"contributing_tasks"`
	RunnerSummary     ReportRunnerSummaryV1      `json:"runner_summary"`
}

type ReportSummaryV1 struct {
	ConfirmedFindings    int            `json:"confirmed_findings"`
	UnconfirmedFindings  int            `json:"unconfirmed_findings"`
	SeverityCounts       map[string]int `json:"severity_counts"`
	ConfirmedFacts       int            `json:"confirmed_facts"`
	TentativeFacts       int            `json:"tentative_facts"`
	EvidenceAvailable    int            `json:"evidence_available"`
	EvidenceMissing      int            `json:"evidence_missing"`
	UnresolvedObjectives int            `json:"unresolved_objectives"`
}

type ReportEvidenceRefV1 struct {
	ID           string `json:"id"`
	StableKey    string `json:"stable_key"`
	ArtifactType string `json:"artifact_type"`
	MediaType    string `json:"media_type,omitempty"`
	Summary      string `json:"summary"`
	Status       string `json:"status"`
	SHA256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Basename     string `json:"basename,omitempty"`
}

type ReportFindingV1 struct {
	Finding                NodeRefV1             `json:"finding"`
	Title                  string                `json:"title"`
	Status                 string                `json:"status"`
	Severity               string                `json:"severity"`
	CVSSVersion            string                `json:"cvss_version,omitempty"`
	CVSSVector             string                `json:"cvss_vector,omitempty"`
	Target                 string                `json:"target,omitempty"`
	Description            string                `json:"description,omitempty"`
	Proof                  string                `json:"proof,omitempty"`
	Impact                 string                `json:"impact,omitempty"`
	Recommendation         string                `json:"recommendation,omitempty"`
	AboutEntities          []NodeRefV1           `json:"about_entities"`
	SupportingFacts        []NodeRefV1           `json:"supporting_facts"`
	SupportingObservations []NodeRefV1           `json:"supporting_observations"`
	Contradictions         []NodeRefV1           `json:"contradictions"`
	Evidence               []ReportEvidenceRefV1 `json:"evidence"`
	Provenance             []ProvenanceSummaryV1 `json:"provenance"`
	CrossTask              bool                  `json:"cross_task,omitempty"`
}

type ReportTruthItemV1 struct {
	Fact        NodeRefV1             `json:"fact"`
	Category    string                `json:"category"`
	Summary     string                `json:"summary"`
	Confidence  string                `json:"confidence"`
	ScopeStatus string                `json:"scope_status"`
	Provenance  []ProvenanceSummaryV1 `json:"provenance"`
}

type ReportCurrentTruthV1 struct {
	Confirmed  []ReportTruthItemV1 `json:"confirmed"`
	Tentative  []ReportTruthItemV1 `json:"tentative"`
	OutOfScope []ReportTruthItemV1 `json:"out_of_scope"`
}

type ReportExplicitPathV1 struct {
	Finding NodeRefV1   `json:"finding"`
	Nodes   []NodeRefV1 `json:"nodes"`
	Edges   []string    `json:"edges"`
}

type ReportUnresolvedWorkV1 struct {
	FalsePositives []ReportFindingV1 `json:"false_positives"`
	Objectives     []NodeRefV1       `json:"objectives"`
}

// PentestReportV1 is the deterministic pentest deliverable semantic model.
type PentestReportV1 struct {
	ReportVersion       string                  `json:"report_version"`
	Source              ReportSourceV1          `json:"source"`
	Engagement          ReportEngagementV1      `json:"engagement"`
	Summary             ReportSummaryV1         `json:"summary"`
	ConfirmedFindings   []ReportFindingV1       `json:"confirmed_findings"`
	UnconfirmedFindings []ReportFindingV1       `json:"unconfirmed_findings"`
	CurrentTruth        ReportCurrentTruthV1    `json:"current_truth"`
	ExplicitPaths       []ReportExplicitPathV1  `json:"explicit_paths"`
	EvidenceIndex       []ReportEvidenceRefV1   `json:"evidence_index"`
	UnresolvedWork      *ReportUnresolvedWorkV1 `json:"unresolved_work"`
	ProvenanceSummary   []ProvenanceSummaryV1   `json:"provenance_summary"`
	Limitations         []string                `json:"limitations"`
}

// CTFSolutionEntryV1 is one Solution record in the CTF deliverable.
type CTFSolutionEntryV1 struct {
	Solution            NodeRefV1             `json:"solution"`
	Kind                string                `json:"kind"`
	Status              string                `json:"status"`
	Summary             string                `json:"summary"`
	Value               string                `json:"value,omitempty"`
	VerificationSummary string                `json:"verification_summary,omitempty"`
	Provenance          []ProvenanceSummaryV1 `json:"provenance"`
}

// CTFSolutionHealthV1 flags operator-visible solution conflicts.
type CTFSolutionHealthV1 struct {
	ConflictingVerifiedFlags bool `json:"conflicting_verified_flags"`
	MissingEvidence          bool `json:"missing_evidence"`
}

// CTFSolutionV1 is the deterministic CTF solution deliverable semantic model.
type CTFSolutionV1 struct {
	SolutionVersion     string                `json:"solution_version"`
	Source              ReportSourceV1        `json:"source"`
	Solved              bool                  `json:"solved"`
	PrimaryVerifiedFlag *CTFSolutionEntryV1   `json:"primary_verified_flag"`
	VerifiedFlags       []CTFSolutionEntryV1  `json:"verified_flags"`
	CandidateFlags      []CTFSolutionEntryV1  `json:"candidate_flags"`
	Answers             []CTFSolutionEntryV1  `json:"answers"`
	Procedures          []CTFSolutionEntryV1  `json:"procedures"`
	SupportingFacts     []ReportTruthItemV1   `json:"supporting_facts"`
	Evidence            []ReportEvidenceRefV1 `json:"evidence"`
	GoalsSatisfied      []NodeRefV1           `json:"goals_satisfied"`
	ProvenanceSummary   []ProvenanceSummaryV1 `json:"provenance_summary"`
	Health              CTFSolutionHealthV1   `json:"health"`
}

func buildPentestReport(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, projectKind string, request PentestReportRequest) (any, error) {
	if projectKind != project.KindPentest {
		return nil, readValidationError(ErrCodeProjectKindMismatch, "PentestReportV1 is valid only for project_kind=pentest", "kind")
	}
	opts, err := normalizePentestReportRequest(request)
	if err != nil {
		return nil, err
	}

	var name, description, scopeRaw string
	if err := tx.QueryRowContext(ctx, `SELECT name,description,scope_json FROM projects WHERE id=?`, snapshot.ProjectID).Scan(&name, &description, &scopeRaw); err != nil {
		return nil, fmt.Errorf("read Project report metadata: %w", err)
	}
	scope, err := decodeProjectScope(scopeRaw)
	if err != nil {
		return nil, err
	}
	selectedTaskID := ""
	if strings.HasPrefix(opts.ScopeContext, "task:") {
		selectedTaskID = strings.TrimPrefix(opts.ScopeContext, "task:")
		taskScope, err := loadTaskScopeSnapshot(ctx, tx, snapshot.ProjectID, selectedTaskID)
		if err != nil {
			return nil, err
		}
		scope = taskScope
	}

	byID := map[string]NodeRecord{}
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}

	confirmedFindings := []ReportFindingV1{}
	unconfirmedFindings := []ReportFindingV1{}
	falsePositives := []ReportFindingV1{}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeFinding || node.Disposition != DispositionMain {
			continue
		}
		finding, err := projectReportFinding(ctx, tx, snapshot, byID, node, selectedTaskID)
		if err != nil {
			return nil, err
		}
		switch finding.Status {
		case "confirmed":
			confirmedFindings = append(confirmedFindings, finding)
		case "unconfirmed":
			unconfirmedFindings = append(unconfirmedFindings, finding)
		case "false_positive":
			falsePositives = append(falsePositives, finding)
		}
	}
	sortReportFindings(confirmedFindings)
	sortReportFindings(unconfirmedFindings)
	sortReportFindings(falsePositives)
	if !opts.IncludeUnconfirmed {
		unconfirmedFindings = []ReportFindingV1{}
	}

	truth := ReportCurrentTruthV1{Confirmed: []ReportTruthItemV1{}, Tentative: []ReportTruthItemV1{}, OutOfScope: []ReportTruthItemV1{}}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeProjectFact || node.Disposition != DispositionMain {
			continue
		}
		confidence := stringProp(node.PropertyMap, "confidence")
		if confidence != "confirmed" && confidence != "tentative" {
			continue
		}
		scopeStatus := stringProp(node.PropertyMap, "scope_status")
		item, err := projectReportTruthItem(ctx, tx, node)
		if err != nil {
			return nil, err
		}
		switch {
		case scopeStatus == "out_of_scope":
			if opts.IncludeOutOfScopeContext {
				truth.OutOfScope = append(truth.OutOfScope, item)
			}
		case confidence == "confirmed":
			truth.Confirmed = append(truth.Confirmed, item)
		case confidence == "tentative" && opts.IncludeTentativeFacts:
			truth.Tentative = append(truth.Tentative, item)
		}
	}
	sortReportTruth(truth.Confirmed)
	sortReportTruth(truth.Tentative)
	sortReportTruth(truth.OutOfScope)

	reliedEvidenceIDs := map[string]bool{}
	for _, finding := range confirmedFindings {
		for _, evidence := range finding.Evidence {
			reliedEvidenceIDs[evidence.ID] = true
		}
	}
	for _, finding := range unconfirmedFindings {
		for _, evidence := range finding.Evidence {
			reliedEvidenceIDs[evidence.ID] = true
		}
	}

	evidenceIndex := []ReportEvidenceRefV1{}
	available, missing := 0, 0
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeEvidenceArtifact || node.Disposition != DispositionMain {
			continue
		}
		ref := projectEvidenceRef(node)
		switch ref.Status {
		case "available":
			available++
		case "missing":
			missing++
		}
		if opts.EvidenceDetail == "index" || reliedEvidenceIDs[node.ID] {
			evidenceIndex = append(evidenceIndex, ref)
		}
	}
	sort.Slice(evidenceIndex, func(i, j int) bool {
		if evidenceIndex[i].StableKey != evidenceIndex[j].StableKey {
			return evidenceIndex[i].StableKey < evidenceIndex[j].StableKey
		}
		return evidenceIndex[i].ID < evidenceIndex[j].ID
	})

	paths := buildExplicitPaths(snapshot, byID, confirmedFindings)

	objectives := []NodeRefV1{}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeExplorationObjective || node.Disposition != DispositionMain {
			continue
		}
		if stringProp(node.PropertyMap, "status") == "open" {
			objectives = append(objectives, nodeRefForNode(node))
		}
	}
	sort.Slice(objectives, func(i, j int) bool {
		if objectives[i].StableKey != objectives[j].StableKey {
			return objectives[i].StableKey < objectives[j].StableKey
		}
		return objectives[i].ID < objectives[j].ID
	})

	contributing, provenanceSummary, runners, err := collectReportContribution(ctx, tx, snapshot, append(append([]ReportFindingV1{}, confirmedFindings...), unconfirmedFindings...), append(append([]ReportTruthItemV1{}, truth.Confirmed...), append(truth.Tentative, truth.OutOfScope...)...), selectedTaskID)
	if err != nil {
		return nil, err
	}

	severityCounts := map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0}
	for _, finding := range confirmedFindings {
		if _, ok := severityCounts[finding.Severity]; ok {
			severityCounts[finding.Severity]++
		}
	}

	var unresolved *ReportUnresolvedWorkV1
	if opts.IncludeUnresolvedWork {
		unresolved = &ReportUnresolvedWorkV1{FalsePositives: falsePositives, Objectives: objectives}
	}

	var notes *string
	if strings.TrimSpace(scope.Notes) != "" {
		value := scope.Notes
		notes = &value
	}
	testingLimits := append([]string(nil), scope.TestingLimits...)
	if testingLimits == nil {
		testingLimits = []string{}
	}

	report := PentestReportV1{
		ReportVersion: "pentest_report_v1",
		Source: ReportSourceV1{
			ProjectID:       snapshot.ProjectID,
			ProjectName:     name,
			GraphRevision:   snapshot.GraphRevision,
			StateHash:       snapshot.StateHash,
			ScopeContext:    opts.ScopeContext,
			RendererVersion: pentestReportRendererVersion,
		},
		Engagement: ReportEngagementV1{
			Description:       description,
			Scope:             scope,
			TestingLimits:     testingLimits,
			ScopeNotes:        notes,
			ContributingTasks: contributing,
			RunnerSummary:     runners,
		},
		Summary: ReportSummaryV1{
			ConfirmedFindings:    len(confirmedFindings),
			UnconfirmedFindings:  len(unconfirmedFindings),
			SeverityCounts:       severityCounts,
			ConfirmedFacts:       len(truth.Confirmed),
			TentativeFacts:       len(truth.Tentative),
			EvidenceAvailable:    available,
			EvidenceMissing:      missing,
			UnresolvedObjectives: len(objectives),
		},
		ConfirmedFindings:   confirmedFindings,
		UnconfirmedFindings: unconfirmedFindings,
		CurrentTruth:        truth,
		ExplicitPaths:       paths,
		EvidenceIndex:       evidenceIndex,
		UnresolvedWork:      unresolved,
		ProvenanceSummary:   provenanceSummary,
		Limitations: []string{
			"Report conclusions are derived only from the graph Blackboard and durable Project/Task/Scope context.",
			"Produced artifacts without an active evidences edge are not presented as proof.",
			"Missing Evidence remains listed without invented proof.",
		},
	}
	report.Source.SourceHash, err = hashPentestReportSource(report, opts)
	if err != nil {
		return nil, err
	}

	if opts.Format == "markdown" {
		return ReportMarkdownV1{Source: report.Source, Markdown: renderPentestReportMarkdown(report, opts)}, nil
	}
	return report, nil
}

func buildCTFSolutionReport(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, projectKind string, request CTFSolutionRequest) (any, error) {
	if projectKind != project.KindCTFChallenge {
		return nil, readValidationError(ErrCodeProjectKindMismatch, "CTFSolutionV1 is valid only for project_kind=ctf_challenge", "kind")
	}
	opts := normalizeCTFSolutionRequest(request)

	var name string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id=?`, snapshot.ProjectID).Scan(&name); err != nil {
		return nil, fmt.Errorf("read CTF Project name: %w", err)
	}
	byID := map[string]NodeRecord{}
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}

	verified := []CTFSolutionEntryV1{}
	candidates := []CTFSolutionEntryV1{}
	answers := []CTFSolutionEntryV1{}
	procedures := []CTFSolutionEntryV1{}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeSolution || node.Disposition != DispositionMain {
			continue
		}
		entry, err := projectCTFSolutionEntry(ctx, tx, node)
		if err != nil {
			return nil, err
		}
		switch entry.Kind {
		case "flag":
			switch entry.Status {
			case "verified":
				verified = append(verified, entry)
			case "candidate":
				if opts.IncludeCandidates {
					candidates = append(candidates, entry)
				}
			}
		case "answer":
			answers = append(answers, entry)
		case "procedure":
			if opts.IncludeProcedure {
				procedures = append(procedures, entry)
			}
		}
	}
	sortCTFEntries(verified)
	sortCTFEntries(candidates)
	sortCTFEntries(answers)
	sortCTFEntries(procedures)

	var primary *CTFSolutionEntryV1
	if len(verified) > 0 {
		value := verified[0]
		primary = &value
	}

	goals := []NodeRefV1{}
	seenGoals := map[string]bool{}
	for _, flag := range verified {
		for _, edge := range snapshot.Edges {
			if edge.State != "active" || edge.EdgeType != EdgeTypeSatisfies || edge.FromNodeID != flag.Solution.ID {
				continue
			}
			goal, ok := byID[edge.ToNodeID]
			if !ok || seenGoals[goal.ID] {
				continue
			}
			seenGoals[goal.ID] = true
			goals = append(goals, nodeRefForNode(goal))
		}
	}
	sort.Slice(goals, func(i, j int) bool {
		if goals[i].StableKey != goals[j].StableKey {
			return goals[i].StableKey < goals[j].StableKey
		}
		return goals[i].ID < goals[j].ID
	})

	supportingFacts := []ReportTruthItemV1{}
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeProjectFact || node.Disposition != DispositionMain {
			continue
		}
		confidence := stringProp(node.PropertyMap, "confidence")
		if confidence != "confirmed" && confidence != "tentative" {
			continue
		}
		linked := false
		for _, edge := range snapshot.Edges {
			if edge.State != "active" || edge.EdgeType != EdgeTypeSupports {
				continue
			}
			if edge.FromNodeID != node.ID {
				continue
			}
			to, ok := byID[edge.ToNodeID]
			if !ok || to.NodeType != NodeTypeSolution {
				continue
			}
			status := stringProp(to.PropertyMap, "status")
			if status == "verified" || status == "candidate" {
				linked = true
				break
			}
		}
		if !linked {
			continue
		}
		item, err := projectReportTruthItem(ctx, tx, node)
		if err != nil {
			return nil, err
		}
		supportingFacts = append(supportingFacts, item)
	}
	sortReportTruth(supportingFacts)

	evidence := []ReportEvidenceRefV1{}
	missingEvidence := false
	for _, node := range snapshot.Nodes {
		if node.NodeType != NodeTypeEvidenceArtifact || node.Disposition != DispositionMain {
			continue
		}
		for _, edge := range snapshot.Edges {
			if edge.State != "active" || edge.EdgeType != EdgeTypeEvidences || edge.FromNodeID != node.ID {
				continue
			}
			to, ok := byID[edge.ToNodeID]
			if !ok || to.NodeType != NodeTypeSolution {
				continue
			}
			status := stringProp(to.PropertyMap, "status")
			if status != "verified" && status != "candidate" {
				continue
			}
			ref := projectEvidenceRef(node)
			if ref.Status == "missing" {
				missingEvidence = true
			}
			evidence = append(evidence, ref)
			break
		}
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].StableKey != evidence[j].StableKey {
			return evidence[i].StableKey < evidence[j].StableKey
		}
		return evidence[i].ID < evidence[j].ID
	})

	distinctValues := map[string]bool{}
	for _, flag := range verified {
		distinctValues[flag.Value] = true
	}

	provenanceSummary := []ProvenanceSummaryV1{}
	seenProv := map[string]bool{}
	for _, entry := range append(append(append([]CTFSolutionEntryV1{}, verified...), candidates...), append(answers, procedures...)...) {
		for _, p := range entry.Provenance {
			key := provenanceKey(p)
			if seenProv[key] {
				continue
			}
			seenProv[key] = true
			provenanceSummary = append(provenanceSummary, p)
		}
	}
	sortProvenance(provenanceSummary)

	solution := CTFSolutionV1{
		SolutionVersion: "ctf_solution_v1",
		Source: ReportSourceV1{
			ProjectID:       snapshot.ProjectID,
			ProjectName:     name,
			GraphRevision:   snapshot.GraphRevision,
			StateHash:       snapshot.StateHash,
			RendererVersion: ctfSolutionRendererVersion,
		},
		Solved:              len(verified) > 0,
		PrimaryVerifiedFlag: primary,
		VerifiedFlags:       verified,
		CandidateFlags:      candidates,
		Answers:             answers,
		Procedures:          procedures,
		SupportingFacts:     supportingFacts,
		Evidence:            evidence,
		GoalsSatisfied:      goals,
		ProvenanceSummary:   provenanceSummary,
		Health: CTFSolutionHealthV1{
			ConflictingVerifiedFlags: len(distinctValues) > 1,
			MissingEvidence:          missingEvidence,
		},
	}
	var err error
	solution.Source.SourceHash, err = hashCTFSolutionSource(solution, opts)
	if err != nil {
		return nil, err
	}
	if opts.Format == "markdown" {
		return ReportMarkdownV1{Source: solution.Source, Markdown: renderCTFSolutionMarkdown(solution, opts)}, nil
	}
	return solution, nil
}

type normalizedPentestReportRequest struct {
	IncludeUnconfirmed       bool
	IncludeTentativeFacts    bool
	IncludeOutOfScopeContext bool
	IncludeUnresolvedWork    bool
	ScopeContext             string
	EvidenceDetail           string
	Format                   string
}

// pentestSourceOptions are the semantic selection options that affect
// source_hash. Format is intentionally excluded so JSON and Markdown of the
// same report share one source_hash.
type pentestSourceOptions struct {
	IncludeUnconfirmed       bool   `json:"include_unconfirmed"`
	IncludeTentativeFacts    bool   `json:"include_tentative_facts"`
	IncludeOutOfScopeContext bool   `json:"include_out_of_scope_context"`
	IncludeUnresolvedWork    bool   `json:"include_unresolved_work"`
	ScopeContext             string `json:"scope_context"`
	EvidenceDetail           string `json:"evidence_detail"`
}

func normalizePentestReportRequest(request PentestReportRequest) (normalizedPentestReportRequest, error) {
	out := normalizedPentestReportRequest{
		IncludeUnconfirmed:       request.IncludeUnconfirmed,
		IncludeTentativeFacts:    request.IncludeTentativeFacts,
		IncludeOutOfScopeContext: request.IncludeOutOfScopeContext,
		IncludeUnresolvedWork:    request.IncludeUnresolvedWork,
		ScopeContext:             request.ScopeContext,
		EvidenceDetail:           request.EvidenceDetail,
		Format:                   request.Format,
	}
	// Contract defaults apply when the request is fully empty.
	empty := !request.IncludeUnconfirmed && !request.IncludeTentativeFacts && !request.IncludeOutOfScopeContext &&
		!request.IncludeUnresolvedWork && request.ScopeContext == "" && request.EvidenceDetail == "" && request.Format == ""
	if empty {
		out.IncludeUnconfirmed = true
		out.IncludeTentativeFacts = true
		out.IncludeOutOfScopeContext = true
	}
	if out.ScopeContext == "" {
		out.ScopeContext = "current"
	}
	if out.ScopeContext != "current" && !strings.HasPrefix(out.ScopeContext, "task:") {
		return normalizedPentestReportRequest{}, readValidationError(ErrCodeInvalidQuery, "scope_context must be current or task:TASK_ID", "scope_context")
	}
	if strings.HasPrefix(out.ScopeContext, "task:") && strings.TrimPrefix(out.ScopeContext, "task:") == "" {
		return normalizedPentestReportRequest{}, readValidationError(ErrCodeInvalidQuery, "scope_context task id is required", "scope_context")
	}
	if out.EvidenceDetail == "" {
		out.EvidenceDetail = "summary"
	}
	if out.EvidenceDetail != "summary" && out.EvidenceDetail != "index" {
		return normalizedPentestReportRequest{}, readValidationError(ErrCodeInvalidQuery, "evidence_detail must be summary or index", "evidence_detail")
	}
	if out.Format == "" {
		out.Format = "json"
	}
	if out.Format != "json" && out.Format != "markdown" {
		return normalizedPentestReportRequest{}, readValidationError(ErrCodeInvalidQuery, "format must be json or markdown", "format")
	}
	return out, nil
}

func (opts normalizedPentestReportRequest) sourceOptions() pentestSourceOptions {
	return pentestSourceOptions{
		IncludeUnconfirmed:       opts.IncludeUnconfirmed,
		IncludeTentativeFacts:    opts.IncludeTentativeFacts,
		IncludeOutOfScopeContext: opts.IncludeOutOfScopeContext,
		IncludeUnresolvedWork:    opts.IncludeUnresolvedWork,
		ScopeContext:             opts.ScopeContext,
		EvidenceDetail:           opts.EvidenceDetail,
	}
}

type normalizedCTFSolutionRequest struct {
	IncludeCandidates bool
	IncludeProcedure  bool
	Format            string
}

func normalizeCTFSolutionRequest(request CTFSolutionRequest) normalizedCTFSolutionRequest {
	out := normalizedCTFSolutionRequest{
		IncludeCandidates: request.IncludeCandidates,
		IncludeProcedure:  request.IncludeProcedure,
		Format:            request.Format,
	}
	// Contract defaults include candidates and procedure unless explicitly disabled
	// via zero values on a fully empty request.
	if !request.IncludeCandidates && !request.IncludeProcedure && request.Format == "" {
		out.IncludeCandidates = true
		out.IncludeProcedure = true
	}
	if out.Format == "" {
		out.Format = "json"
	}
	return out
}

func decodeProjectScope(raw string) (project.Scope, error) {
	var scope project.Scope
	if strings.TrimSpace(raw) == "" {
		return scope, nil
	}
	if err := json.Unmarshal([]byte(raw), &scope); err != nil {
		return project.Scope{}, fmt.Errorf("decode Project scope: %w", err)
	}
	return scope, nil
}

func loadTaskScopeSnapshot(ctx context.Context, tx *sql.Tx, projectID, taskID string) (project.Scope, error) {
	var scopeJSON string
	var foundProject string
	err := tx.QueryRowContext(ctx, `SELECT project_id,scope_snapshot_json FROM tasks WHERE id=?`, taskID).Scan(&foundProject, &scopeJSON)
	if errorsIsNoRows(err) {
		return project.Scope{}, readValidationError(ErrCodeInvalidQuery, "task does not exist in this Project", "scope_context")
	}
	if err != nil {
		return project.Scope{}, fmt.Errorf("read Task Scope Snapshot: %w", err)
	}
	if foundProject != projectID {
		return project.Scope{}, readValidationError(ErrCodeInvalidQuery, "task does not belong to this Project", "scope_context")
	}
	return decodeProjectScope(scopeJSON)
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func projectReportFinding(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, byID map[string]NodeRecord, node NodeRecord, selectedTaskID string) (ReportFindingV1, error) {
	status := stringProp(node.PropertyMap, "status")
	vector := stringProp(node.PropertyMap, "cvss_vector")
	severity := deriveSeverity(vector)
	if status != "confirmed" && severity == "pending" {
		severity = "pending"
	}
	finding := ReportFindingV1{
		Finding:                nodeRefForNode(node),
		Title:                  stringProp(node.PropertyMap, "title"),
		Status:                 status,
		Severity:               severity,
		CVSSVersion:            stringProp(node.PropertyMap, "cvss_version"),
		CVSSVector:             vector,
		Target:                 stringProp(node.PropertyMap, "target"),
		Description:            stringProp(node.PropertyMap, "description"),
		Proof:                  stringProp(node.PropertyMap, "proof"),
		Impact:                 stringProp(node.PropertyMap, "impact"),
		Recommendation:         stringProp(node.PropertyMap, "recommendation"),
		AboutEntities:          []NodeRefV1{},
		SupportingFacts:        []NodeRefV1{},
		SupportingObservations: []NodeRefV1{},
		Contradictions:         []NodeRefV1{},
		Evidence:               []ReportEvidenceRefV1{},
		Provenance:             []ProvenanceSummaryV1{},
	}
	for _, edge := range snapshot.Edges {
		if edge.State != "active" {
			continue
		}
		switch edge.EdgeType {
		case EdgeTypeAbout:
			if edge.FromNodeID == node.ID {
				if other, ok := byID[edge.ToNodeID]; ok {
					finding.AboutEntities = append(finding.AboutEntities, nodeRefForNode(other))
				}
			}
		case EdgeTypeSupports:
			if edge.ToNodeID == node.ID {
				if other, ok := byID[edge.FromNodeID]; ok {
					switch other.NodeType {
					case NodeTypeProjectFact:
						finding.SupportingFacts = append(finding.SupportingFacts, nodeRefForNode(other))
					case NodeTypeObservation:
						finding.SupportingObservations = append(finding.SupportingObservations, nodeRefForNode(other))
					}
				}
			}
		case EdgeTypeContradicts:
			if edge.ToNodeID == node.ID {
				if other, ok := byID[edge.FromNodeID]; ok {
					finding.Contradictions = append(finding.Contradictions, nodeRefForNode(other))
				}
			}
		case EdgeTypeEvidences:
			if edge.ToNodeID == node.ID {
				if other, ok := byID[edge.FromNodeID]; ok && other.NodeType == NodeTypeEvidenceArtifact {
					finding.Evidence = append(finding.Evidence, projectEvidenceRef(other))
				}
			}
		}
	}
	sortNodeRefs(finding.AboutEntities)
	sortNodeRefs(finding.SupportingFacts)
	sortNodeRefs(finding.SupportingObservations)
	sortNodeRefs(finding.Contradictions)
	sort.Slice(finding.Evidence, func(i, j int) bool {
		if finding.Evidence[i].StableKey != finding.Evidence[j].StableKey {
			return finding.Evidence[i].StableKey < finding.Evidence[j].StableKey
		}
		return finding.Evidence[i].ID < finding.Evidence[j].ID
	})
	prov, err := loadCompactNodeProvenance(ctx, tx, snapshot.ProjectID, node.ID, node.Version)
	if err != nil {
		return ReportFindingV1{}, err
	}
	finding.Provenance = prov
	if selectedTaskID != "" {
		finding.CrossTask = !provenanceTouchesTask(prov, selectedTaskID)
	}
	return finding, nil
}

func projectReportTruthItem(ctx context.Context, tx *sql.Tx, node NodeRecord) (ReportTruthItemV1, error) {
	prov, err := loadCompactNodeProvenance(ctx, tx, node.ProjectID, node.ID, node.Version)
	if err != nil {
		return ReportTruthItemV1{}, err
	}
	return ReportTruthItemV1{
		Fact:        nodeRefForNode(node),
		Category:    stringProp(node.PropertyMap, "category"),
		Summary:     stringProp(node.PropertyMap, "summary"),
		Confidence:  stringProp(node.PropertyMap, "confidence"),
		ScopeStatus: stringProp(node.PropertyMap, "scope_status"),
		Provenance:  prov,
	}, nil
}

func projectEvidenceRef(node NodeRecord) ReportEvidenceRefV1 {
	managed := stringProp(node.PropertyMap, "managed_path")
	basename := ""
	if managed != "" && !strings.HasPrefix(managed, "missing://") {
		basename = filepath.Base(managed)
	}
	return ReportEvidenceRefV1{
		ID:           node.ID,
		StableKey:    node.StableKey,
		ArtifactType: stringProp(node.PropertyMap, "artifact_type"),
		MediaType:    stringProp(node.PropertyMap, "media_type"),
		Summary:      stringProp(node.PropertyMap, "summary"),
		Status:       stringProp(node.PropertyMap, "status"),
		SHA256:       stringProp(node.PropertyMap, "sha256"),
		SizeBytes:    int64Property(node.PropertyMap, "size_bytes"),
		Basename:     basename,
	}
}

func projectCTFSolutionEntry(ctx context.Context, tx *sql.Tx, node NodeRecord) (CTFSolutionEntryV1, error) {
	prov, err := loadCompactNodeProvenance(ctx, tx, node.ProjectID, node.ID, node.Version)
	if err != nil {
		return CTFSolutionEntryV1{}, err
	}
	return CTFSolutionEntryV1{
		Solution:            nodeRefForNode(node),
		Kind:                stringProp(node.PropertyMap, "kind"),
		Status:              stringProp(node.PropertyMap, "status"),
		Summary:             stringProp(node.PropertyMap, "summary"),
		Value:               stringProp(node.PropertyMap, "value"),
		VerificationSummary: stringProp(node.PropertyMap, "verification_summary"),
		Provenance:          prov,
	}, nil
}

func loadCompactNodeProvenance(ctx context.Context, tx *sql.Tx, projectID, nodeID string, version int) ([]ProvenanceSummaryV1, error) {
	// Prefer updated provenance; include created when different.
	updated, err := loadNodeVersionProvenance(ctx, tx, projectID, nodeID, version)
	if err != nil {
		return nil, err
	}
	out := []ProvenanceSummaryV1{updated.Provenance}
	if version > 1 {
		created, err := loadNodeVersionProvenance(ctx, tx, projectID, nodeID, 1)
		if err != nil {
			return nil, err
		}
		if provenanceKey(created.Provenance) != provenanceKey(updated.Provenance) {
			out = append([]ProvenanceSummaryV1{created.Provenance}, out...)
		}
	}
	return out, nil
}

func collectReportContribution(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, findings []ReportFindingV1, facts []ReportTruthItemV1, selectedTaskID string) ([]ReportContributingTaskV1, []ProvenanceSummaryV1, ReportRunnerSummaryV1, error) {
	taskIDs := map[string]bool{}
	provenanceSummary := []ProvenanceSummaryV1{}
	seenProv := map[string]bool{}
	runners := ReportRunnerSummaryV1{}
	runnerSeen := map[string]bool{}

	addProv := func(items []ProvenanceSummaryV1) {
		for _, p := range items {
			key := provenanceKey(p)
			if !seenProv[key] {
				seenProv[key] = true
				provenanceSummary = append(provenanceSummary, p)
			}
			if p.TaskID != nil && *p.TaskID != "" {
				taskIDs[*p.TaskID] = true
			}
			if p.Runner != nil && *p.Runner != "" && !runnerSeen[*p.Runner] {
				runnerSeen[*p.Runner] = true
				switch *p.Runner {
				case "host":
					runners.Host++
				case "sandbox":
					runners.Sandbox++
				}
			}
		}
	}
	for _, finding := range findings {
		addProv(finding.Provenance)
	}
	for _, fact := range facts {
		addProv(fact.Provenance)
	}
	sortProvenance(provenanceSummary)

	// Also count runners from all Task rows that contributed conclusions.
	contributing := []ReportContributingTaskV1{}
	for taskID := range taskIDs {
		var goal, status, runner, scopeJSON string
		var projectID string
		err := tx.QueryRowContext(ctx, `SELECT project_id,goal,status,runner,scope_snapshot_json FROM tasks WHERE id=?`, taskID).Scan(&projectID, &goal, &status, &runner, &scopeJSON)
		if errorsIsNoRows(err) {
			continue
		}
		if err != nil {
			return nil, nil, ReportRunnerSummaryV1{}, fmt.Errorf("read contributing Task: %w", err)
		}
		if projectID != snapshot.ProjectID {
			continue
		}
		scope, err := decodeProjectScope(scopeJSON)
		if err != nil {
			return nil, nil, ReportRunnerSummaryV1{}, err
		}
		if !runnerSeen[runner] {
			runnerSeen[runner] = true
			switch runner {
			case "host":
				runners.Host++
			case "sandbox":
				runners.Sandbox++
			}
		}
		contributing = append(contributing, ReportContributingTaskV1{
			TaskID:        taskID,
			Goal:          goal,
			Status:        status,
			Runner:        runner,
			ScopeSnapshot: scope,
			CrossTask:     selectedTaskID != "" && taskID != selectedTaskID,
		})
	}
	sort.Slice(contributing, func(i, j int) bool {
		if contributing[i].TaskID != contributing[j].TaskID {
			return contributing[i].TaskID < contributing[j].TaskID
		}
		return contributing[i].Goal < contributing[j].Goal
	})
	return contributing, provenanceSummary, runners, nil
}

func buildExplicitPaths(snapshot GraphSnapshot, byID map[string]NodeRecord, findings []ReportFindingV1) []ReportExplicitPathV1 {
	// Minimal deterministic path: Entity/Fact -about/supports/evidences-> Finding.
	paths := []ReportExplicitPathV1{}
	pathEdges := map[EdgeType]bool{
		EdgeTypeAbout: true, EdgeTypeSupports: true, EdgeTypeEvidences: true,
		EdgeTypeDerivedFrom: true, EdgeTypeLeadsTo: true, EdgeTypeProduced: true, EdgeTypeSatisfies: true,
	}
	for _, finding := range findings {
		for _, edge := range snapshot.Edges {
			if edge.State != "active" || !pathEdges[edge.EdgeType] {
				continue
			}
			var startID string
			if edge.ToNodeID == finding.Finding.ID {
				startID = edge.FromNodeID
			} else if edge.FromNodeID == finding.Finding.ID {
				startID = edge.ToNodeID
			} else {
				continue
			}
			start, ok := byID[startID]
			if !ok {
				continue
			}
			// Prefer paths that start at Entity or confirmed ProjectFact.
			if start.NodeType != NodeTypeEntity && !(start.NodeType == NodeTypeProjectFact && stringProp(start.PropertyMap, "confidence") == "confirmed") {
				continue
			}
			paths = append(paths, ReportExplicitPathV1{
				Finding: finding.Finding,
				Nodes:   []NodeRefV1{nodeRefForNode(start), finding.Finding},
				Edges:   []string{string(edge.EdgeType)},
			})
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Finding.StableKey != paths[j].Finding.StableKey {
			return paths[i].Finding.StableKey < paths[j].Finding.StableKey
		}
		if len(paths[i].Nodes) != len(paths[j].Nodes) {
			return len(paths[i].Nodes) < len(paths[j].Nodes)
		}
		if paths[i].Nodes[0].StableKey != paths[j].Nodes[0].StableKey {
			return paths[i].Nodes[0].StableKey < paths[j].Nodes[0].StableKey
		}
		return strings.Join(paths[i].Edges, ",") < strings.Join(paths[j].Edges, ",")
	})
	if len(paths) > 100 {
		paths = paths[:100]
	}
	return paths
}

func sortReportFindings(items []ReportFindingV1) {
	sort.Slice(items, func(i, j int) bool {
		si := severityRank(items[i].Severity)
		sj := severityRank(items[j].Severity)
		if si != sj {
			return si < sj
		}
		if items[i].Target != items[j].Target {
			return items[i].Target < items[j].Target
		}
		if items[i].Title != items[j].Title {
			return items[i].Title < items[j].Title
		}
		if items[i].Finding.StableKey != items[j].Finding.StableKey {
			return items[i].Finding.StableKey < items[j].Finding.StableKey
		}
		return items[i].Finding.ID < items[j].Finding.ID
	})
}

func sortReportTruth(items []ReportTruthItemV1) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		if items[i].Fact.StableKey != items[j].Fact.StableKey {
			return items[i].Fact.StableKey < items[j].Fact.StableKey
		}
		return items[i].Fact.ID < items[j].Fact.ID
	})
}

func sortCTFEntries(items []CTFSolutionEntryV1) {
	statusRank := map[string]int{"verified": 0, "candidate": 1, "rejected": 2, "superseded": 3}
	sort.Slice(items, func(i, j int) bool {
		if statusRank[items[i].Status] != statusRank[items[j].Status] {
			return statusRank[items[i].Status] < statusRank[items[j].Status]
		}
		if items[i].Solution.StableKey != items[j].Solution.StableKey {
			return items[i].Solution.StableKey < items[j].Solution.StableKey
		}
		return items[i].Solution.ID < items[j].Solution.ID
	})
}

func sortNodeRefs(items []NodeRefV1) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].StableKey != items[j].StableKey {
			return items[i].StableKey < items[j].StableKey
		}
		return items[i].ID < items[j].ID
	})
}

func sortProvenance(items []ProvenanceSummaryV1) {
	sort.Slice(items, func(i, j int) bool {
		return provenanceKey(items[i]) < provenanceKey(items[j])
	})
}

func provenanceKey(p ProvenanceSummaryV1) string {
	taskID, contID, runner, profile := "", "", "", ""
	if p.TaskID != nil {
		taskID = *p.TaskID
	}
	if p.ContinuationID != nil {
		contID = *p.ContinuationID
	}
	if p.Runner != nil {
		runner = *p.Runner
	}
	if p.RuntimeProfileID != nil {
		profile = *p.RuntimeProfileID
	}
	return strings.Join([]string{string(p.ActorType), p.ActorID, taskID, contID, runner, profile, p.RecordedAt, strconv.Itoa(p.SourceEventCount)}, "|")
}

func provenanceTouchesTask(items []ProvenanceSummaryV1, taskID string) bool {
	for _, p := range items {
		if p.TaskID != nil && *p.TaskID == taskID {
			return true
		}
	}
	return false
}

func int64Property(properties map[string]any, key string) int64 {
	value, ok := properties[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		n, _ := typed.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(typed, 10, 64)
		return n
	default:
		return 0
	}
}

func hashPentestReportSource(report PentestReportV1, opts normalizedPentestReportRequest) (string, error) {
	payload := struct {
		ReportVersion string                  `json:"report_version"`
		ProjectID     string                  `json:"project_id"`
		ProjectName   string                  `json:"project_name"`
		Description   string                  `json:"description"`
		GraphRevision int                     `json:"graph_revision"`
		StateHash     string                  `json:"state_hash"`
		Options       pentestSourceOptions    `json:"options"`
		Engagement    ReportEngagementV1      `json:"engagement"`
		Summary       ReportSummaryV1         `json:"summary"`
		Confirmed     []ReportFindingV1       `json:"confirmed_findings"`
		Unconfirmed   []ReportFindingV1       `json:"unconfirmed_findings"`
		Truth         ReportCurrentTruthV1    `json:"current_truth"`
		Paths         []ReportExplicitPathV1  `json:"explicit_paths"`
		Evidence      []ReportEvidenceRefV1   `json:"evidence_index"`
		Unresolved    *ReportUnresolvedWorkV1 `json:"unresolved_work"`
		Provenance    []ProvenanceSummaryV1   `json:"provenance_summary"`
		Limitations   []string                `json:"limitations"`
		Renderer      string                  `json:"renderer_version"`
	}{
		ReportVersion: report.ReportVersion,
		ProjectID:     report.Source.ProjectID,
		ProjectName:   report.Source.ProjectName,
		Description:   report.Engagement.Description,
		GraphRevision: report.Source.GraphRevision,
		StateHash:     report.Source.StateHash,
		Options:       opts.sourceOptions(),
		Engagement:    report.Engagement,
		Summary:       report.Summary,
		Confirmed:     report.ConfirmedFindings,
		Unconfirmed:   report.UnconfirmedFindings,
		Truth:         report.CurrentTruth,
		Paths:         report.ExplicitPaths,
		Evidence:      report.EvidenceIndex,
		Unresolved:    report.UnresolvedWork,
		Provenance:    report.ProvenanceSummary,
		Limitations:   report.Limitations,
		Renderer:      report.Source.RendererVersion,
	}
	data, err := canonicalJSON(payload)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(framedHash("CyberPenda.Blackboard.PentestReportSource.v1", data)), nil
}

func hashCTFSolutionSource(solution CTFSolutionV1, opts normalizedCTFSolutionRequest) (string, error) {
	payload := struct {
		SolutionVersion string                       `json:"solution_version"`
		ProjectID       string                       `json:"project_id"`
		ProjectName     string                       `json:"project_name"`
		GraphRevision   int                          `json:"graph_revision"`
		StateHash       string                       `json:"state_hash"`
		Options         normalizedCTFSolutionRequest `json:"options"`
		Body            CTFSolutionV1                `json:"body"`
		Renderer        string                       `json:"renderer_version"`
	}{
		SolutionVersion: solution.SolutionVersion,
		ProjectID:       solution.Source.ProjectID,
		ProjectName:     solution.Source.ProjectName,
		GraphRevision:   solution.Source.GraphRevision,
		StateHash:       solution.Source.StateHash,
		Options:         opts,
		Body:            solution,
		Renderer:        solution.Source.RendererVersion,
	}
	// Clear recursive source hash before hashing.
	payload.Body.Source.SourceHash = ""
	data, err := canonicalJSON(payload)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(framedHash("CyberPenda.Blackboard.CTFSolutionSource.v1", data)), nil
}

func renderPentestReportMarkdown(report PentestReportV1, opts normalizedPentestReportRequest) string {
	var b strings.Builder
	write := func(parts ...string) {
		for _, part := range parts {
			b.WriteString(part)
		}
	}
	writeln := func(parts ...string) {
		write(parts...)
		b.WriteByte('\n')
	}

	writeln("# Pentest Report")
	writeln()
	writeln("## Source")
	writeln()
	writeln("- **Project:** ", escapeMarkdown(report.Source.ProjectName), " (`", escapeMarkdown(report.Source.ProjectID), "`)")
	writeln("- **Graph revision:** ", strconv.Itoa(report.Source.GraphRevision))
	writeln("- **State hash:** `", escapeMarkdown(report.Source.StateHash), "`")
	writeln("- **Source hash:** `", escapeMarkdown(report.Source.SourceHash), "`")
	writeln("- **Scope context:** ", escapeMarkdown(report.Source.ScopeContext))
	writeln("- **Renderer:** ", escapeMarkdown(report.Source.RendererVersion))
	writeln()

	writeln("## Engagement Context and Scope")
	writeln()
	if report.Engagement.Description != "" {
		writeln(escapeMarkdown(report.Engagement.Description))
		writeln()
	}
	writeScopeMarkdown(&b, report.Engagement.Scope)
	writeln("- **Runner summary:** sandbox=", strconv.Itoa(report.Engagement.RunnerSummary.Sandbox), ", host=", strconv.Itoa(report.Engagement.RunnerSummary.Host))
	if report.Engagement.RunnerSummary.Host > 0 {
		writeln("- **Host runner:** host Runner contributions are present in this report.")
	}
	if len(report.Engagement.ContributingTasks) == 0 {
		writeln()
		writeln("_No contributing Tasks recorded._")
	} else {
		writeln()
		writeln("### Contributing Tasks")
		writeln()
		for _, task := range report.Engagement.ContributingTasks {
			writeln("- `", escapeMarkdown(task.TaskID), "` (", escapeMarkdown(task.Runner), "): ", escapeMarkdown(task.Goal))
		}
	}
	writeln()

	writeln("## Executive Summary")
	writeln()
	writeln("- Confirmed findings: ", strconv.Itoa(report.Summary.ConfirmedFindings))
	writeln("- Unconfirmed findings: ", strconv.Itoa(report.Summary.UnconfirmedFindings))
	writeln("- Severity counts: critical=", strconv.Itoa(report.Summary.SeverityCounts["critical"]),
		", high=", strconv.Itoa(report.Summary.SeverityCounts["high"]),
		", medium=", strconv.Itoa(report.Summary.SeverityCounts["medium"]),
		", low=", strconv.Itoa(report.Summary.SeverityCounts["low"]))
	writeln("- Confirmed facts: ", strconv.Itoa(report.Summary.ConfirmedFacts))
	writeln("- Tentative facts: ", strconv.Itoa(report.Summary.TentativeFacts))
	writeln("- Evidence available: ", strconv.Itoa(report.Summary.EvidenceAvailable))
	writeln("- Evidence missing: ", strconv.Itoa(report.Summary.EvidenceMissing))
	writeln("- Unresolved objectives: ", strconv.Itoa(report.Summary.UnresolvedObjectives))
	writeln()

	writeln("## Confirmed Findings")
	writeln()
	writeFindingsMarkdown(&b, report.ConfirmedFindings)
	writeln()

	if opts.IncludeUnconfirmed {
		writeln("## Unconfirmed Findings")
		writeln()
		writeFindingsMarkdown(&b, report.UnconfirmedFindings)
		writeln()
	}

	writeln("## Confirmed Current Truth")
	writeln()
	writeTruthMarkdown(&b, report.CurrentTruth.Confirmed)
	writeln()

	if opts.IncludeTentativeFacts {
		writeln("## Tentative Context")
		writeln()
		writeTruthMarkdown(&b, report.CurrentTruth.Tentative)
		writeln()
	}
	if opts.IncludeOutOfScopeContext {
		writeln("## Out-of-Scope Context")
		writeln()
		writeTruthMarkdown(&b, report.CurrentTruth.OutOfScope)
		writeln()
	}

	writeln("## Explicit Evidence Paths")
	writeln()
	if len(report.ExplicitPaths) == 0 {
		writeln("_No explicit paths recorded._")
	} else {
		for _, path := range report.ExplicitPaths {
			nodes := make([]string, 0, len(path.Nodes))
			for _, node := range path.Nodes {
				nodes = append(nodes, node.StableKey)
			}
			writeln("- ", escapeMarkdown(strings.Join(nodes, " -> ")), " [", escapeMarkdown(strings.Join(path.Edges, ", ")), "]")
		}
	}
	writeln()

	writeln("## Evidence Index")
	writeln()
	if len(report.EvidenceIndex) == 0 {
		writeln("_No Evidence recorded._")
	} else {
		for _, evidence := range report.EvidenceIndex {
			writeln("- `", escapeMarkdown(evidence.StableKey), "` (", escapeMarkdown(evidence.Status), "): ", escapeMarkdown(evidence.Summary))
		}
	}
	writeln()

	if opts.IncludeUnresolvedWork && report.UnresolvedWork != nil {
		writeln("## Unresolved Work")
		writeln()
		writeln("### False Positives (audit only)")
		writeln()
		writeFindingsMarkdown(&b, report.UnresolvedWork.FalsePositives)
		writeln()
	}

	writeln("## Provenance and Execution Context")
	writeln()
	if len(report.ProvenanceSummary) == 0 {
		writeln("_No provenance recorded._")
	} else {
		for _, p := range report.ProvenanceSummary {
			runner := ""
			if p.Runner != nil {
				runner = *p.Runner
			}
			taskID := ""
			if p.TaskID != nil {
				taskID = *p.TaskID
			}
			writeln("- actor=", escapeMarkdown(string(p.ActorType)), "/", escapeMarkdown(p.ActorID),
				" task=", escapeMarkdown(taskID), " runner=", escapeMarkdown(runner),
				" recorded_at=", escapeMarkdown(p.RecordedAt))
		}
	}
	writeln()

	writeln("## Limitations")
	writeln()
	for _, item := range report.Limitations {
		writeln("- ", escapeMarkdown(item))
	}
	// Exactly one trailing LF is added by the final writeln above; ensure no extras.
	out := b.String()
	out = strings.TrimRight(out, "\n") + "\n"
	return out
}

func renderCTFSolutionMarkdown(solution CTFSolutionV1, opts normalizedCTFSolutionRequest) string {
	var b strings.Builder
	writeln := func(parts ...string) {
		for _, part := range parts {
			b.WriteString(part)
		}
		b.WriteByte('\n')
	}
	writeln("# CTF Solution")
	writeln()
	writeln("## Challenge and Source")
	writeln()
	writeln("- **Challenge:** ", escapeMarkdown(solution.Source.ProjectName), " (`", escapeMarkdown(solution.Source.ProjectID), "`)")
	writeln("- **Graph revision:** ", strconv.Itoa(solution.Source.GraphRevision))
	writeln("- **State hash:** `", escapeMarkdown(solution.Source.StateHash), "`")
	writeln("- **Source hash:** `", escapeMarkdown(solution.Source.SourceHash), "`")
	writeln()
	writeln("## Solved Status")
	writeln()
	if solution.Solved {
		writeln("Solved: true")
	} else {
		writeln("Solved: false")
	}
	writeln()
	writeln("## Verified Flag or Flags")
	writeln()
	writeCTFEntriesMarkdown(&b, solution.VerifiedFlags)
	writeln()
	if opts.IncludeCandidates {
		writeln("## Candidate Flags")
		writeln()
		writeCTFEntriesMarkdown(&b, solution.CandidateFlags)
		writeln()
	}
	writeln("## Answers")
	writeln()
	writeCTFEntriesMarkdown(&b, solution.Answers)
	writeln()
	if opts.IncludeProcedure {
		writeln("## Procedure")
		writeln()
		writeCTFEntriesMarkdown(&b, solution.Procedures)
		writeln()
	}
	writeln("## Supporting Facts")
	writeln()
	writeTruthMarkdown(&b, solution.SupportingFacts)
	writeln()
	writeln("## Evidence")
	writeln()
	if len(solution.Evidence) == 0 {
		writeln("_No Evidence recorded._")
	} else {
		for _, evidence := range solution.Evidence {
			writeln("- `", escapeMarkdown(evidence.StableKey), "` (", escapeMarkdown(evidence.Status), "): ", escapeMarkdown(evidence.Summary))
		}
	}
	writeln()
	writeln("## Provenance")
	writeln()
	if len(solution.ProvenanceSummary) == 0 {
		writeln("_No provenance recorded._")
	} else {
		for _, p := range solution.ProvenanceSummary {
			writeln("- actor=", escapeMarkdown(string(p.ActorType)), "/", escapeMarkdown(p.ActorID), " recorded_at=", escapeMarkdown(p.RecordedAt))
		}
	}
	out := b.String()
	out = strings.TrimRight(out, "\n") + "\n"
	return out
}

func writeScopeMarkdown(b *strings.Builder, scope project.Scope) {
	writeList := func(label string, values []string) {
		if len(values) == 0 {
			return
		}
		b.WriteString("- **")
		b.WriteString(label)
		b.WriteString(":** ")
		b.WriteString(escapeMarkdown(strings.Join(values, ", ")))
		b.WriteByte('\n')
	}
	writeList("In-scope domains", scope.Domains)
	writeList("In-scope IPs", scope.IPs)
	writeList("In-scope CIDRs", scope.CIDRs)
	writeList("In-scope URLs", scope.URLs)
	writeList("In-scope ports", scope.Ports)
	writeList("Excluded", scope.Excluded)
	writeList("Testing limits", scope.TestingLimits)
	if strings.TrimSpace(scope.Notes) != "" {
		b.WriteString("- **Scope notes:** ")
		b.WriteString(escapeMarkdown(scope.Notes))
		b.WriteByte('\n')
	}
}

func writeFindingsMarkdown(b *strings.Builder, findings []ReportFindingV1) {
	if len(findings) == 0 {
		b.WriteString("_No findings recorded._\n")
		return
	}
	for _, finding := range findings {
		b.WriteString("### ")
		b.WriteString(escapeMarkdown(finding.Title))
		b.WriteString("\n\n")
		b.WriteString("- **Status:** ")
		b.WriteString(escapeMarkdown(finding.Status))
		b.WriteByte('\n')
		b.WriteString("- **Severity/CVSS:** ")
		b.WriteString(escapeMarkdown(finding.Severity))
		if finding.CVSSVector != "" {
			b.WriteString(" (`")
			b.WriteString(escapeMarkdown(finding.CVSSVector))
			b.WriteString("`)")
		}
		b.WriteByte('\n')
		if finding.Target != "" {
			b.WriteString("- **Target:** ")
			b.WriteString(escapeMarkdown(finding.Target))
			b.WriteByte('\n')
		}
		if finding.Description != "" {
			b.WriteString("- **Description:** ")
			b.WriteString(escapeMarkdown(finding.Description))
			b.WriteByte('\n')
		}
		if finding.Proof != "" {
			b.WriteString("- **Proof:** ")
			b.WriteString(escapeMarkdown(finding.Proof))
			b.WriteByte('\n')
		}
		if finding.Impact != "" {
			b.WriteString("- **Impact:** ")
			b.WriteString(escapeMarkdown(finding.Impact))
			b.WriteByte('\n')
		}
		if finding.Recommendation != "" {
			b.WriteString("- **Recommendation:** ")
			b.WriteString(escapeMarkdown(finding.Recommendation))
			b.WriteByte('\n')
		}
		if len(finding.AboutEntities) > 0 {
			b.WriteString("- **Related entities:** ")
			b.WriteString(escapeMarkdown(joinRefKeys(finding.AboutEntities)))
			b.WriteByte('\n')
		}
		if len(finding.SupportingFacts) > 0 || len(finding.SupportingObservations) > 0 {
			b.WriteString("- **Supporting facts/observations:** ")
			b.WriteString(escapeMarkdown(joinRefKeys(append(append([]NodeRefV1{}, finding.SupportingFacts...), finding.SupportingObservations...))))
			b.WriteByte('\n')
		}
		if len(finding.Contradictions) > 0 {
			b.WriteString("- **Contradictions:** ")
			b.WriteString(escapeMarkdown(joinRefKeys(finding.Contradictions)))
			b.WriteByte('\n')
		}
		if len(finding.Evidence) == 0 {
			b.WriteString("- **Evidence:** _none_\n")
		} else {
			b.WriteString("- **Evidence:**\n")
			for _, evidence := range finding.Evidence {
				b.WriteString("  - `")
				b.WriteString(escapeMarkdown(evidence.StableKey))
				b.WriteString("` (")
				b.WriteString(escapeMarkdown(evidence.Status))
				b.WriteString("): ")
				b.WriteString(escapeMarkdown(evidence.Summary))
				b.WriteByte('\n')
			}
		}
		if len(finding.Provenance) > 0 {
			b.WriteString("- **Provenance:**\n")
			for _, p := range finding.Provenance {
				runner := ""
				if p.Runner != nil {
					runner = *p.Runner
				}
				b.WriteString("  - actor=")
				b.WriteString(escapeMarkdown(string(p.ActorType)))
				b.WriteString("/")
				b.WriteString(escapeMarkdown(p.ActorID))
				if runner != "" {
					b.WriteString(" runner=")
					b.WriteString(escapeMarkdown(runner))
				}
				b.WriteByte('\n')
			}
		}
		b.WriteByte('\n')
	}
}

func writeTruthMarkdown(b *strings.Builder, items []ReportTruthItemV1) {
	if len(items) == 0 {
		b.WriteString("_No records recorded._\n")
		return
	}
	for _, item := range items {
		b.WriteString("- `")
		b.WriteString(escapeMarkdown(item.Fact.StableKey))
		b.WriteString("` (")
		b.WriteString(escapeMarkdown(item.Confidence))
		b.WriteString("/")
		b.WriteString(escapeMarkdown(item.ScopeStatus))
		b.WriteString("): ")
		b.WriteString(escapeMarkdown(item.Summary))
		b.WriteByte('\n')
	}
}

func writeCTFEntriesMarkdown(b *strings.Builder, items []CTFSolutionEntryV1) {
	if len(items) == 0 {
		b.WriteString("_No records recorded._\n")
		return
	}
	for _, item := range items {
		b.WriteString("- `")
		b.WriteString(escapeMarkdown(item.Solution.StableKey))
		b.WriteString("` (")
		b.WriteString(escapeMarkdown(item.Status))
		b.WriteString("): ")
		b.WriteString(escapeMarkdown(item.Summary))
		if item.Value != "" {
			b.WriteString(" — `")
			b.WriteString(escapeMarkdownCodeSpan(item.Value))
			b.WriteString("`")
		}
		b.WriteByte('\n')
	}
}

func joinRefKeys(items []NodeRefV1) string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.StableKey)
	}
	return strings.Join(keys, ", ")
}

func escapeMarkdownCodeSpan(value string) string {
	// Code spans only need backtick escaping; preserve flag/answer payload bytes.
	return strings.ReplaceAll(value, "`", "\\`")
}

func escapeMarkdown(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"{", "\\{",
		"}", "\\}",
		"[", "\\[",
		"]", "\\]",
		"<", "\\<",
		">", "\\>",
		"|", "\\|",
		"#", "\\#",
	)
	return replacer.Replace(value)
}
