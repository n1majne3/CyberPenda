package blackboardv2

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

const (
	pentestReportSchema  = "pentest-report/v2"
	ctfSolutionSchema    = "ctf-solution/v2"
	reportMarkdownSchema = "report-markdown/v2"
)

// PentestReportProjection is the deterministic, current semantic report
// allowlist. It excludes storage identity, integrity hashes, Trusted Origin,
// and execution history. Human-readable Blackboard Keys on Finding/Fact/
// Evidence items are allowed so report consumers can load current detail/history.
type PentestReportProjection struct {
	Schema              string          `json:"schema"`
	Project             ReportProject   `json:"project"`
	ConfirmedFindings   []ReportFinding `json:"confirmed_findings"`
	UnconfirmedFindings []ReportFinding `json:"unconfirmed_findings"`
	ConfirmedFacts      []ReportFact    `json:"confirmed_facts"`
	TentativeFacts      []ReportFact    `json:"tentative_facts"`
}

// CTFSolutionProjection is the deterministic CTF consumer allowlist. Solved
// state is derived only from current verified flag Solutions. Hashes, Trusted
// Origin, Goals, and provenance are intentionally omitted. Blackboard Keys on
// Solution/Fact/Evidence items are allowed for detail/history navigation.
type CTFSolutionProjection struct {
	Schema         string           `json:"schema"`
	Project        ReportProject    `json:"project"`
	Solved         bool             `json:"solved"`
	VerifiedFlags  []ReportSolution `json:"verified_flags"`
	CandidateFlags []ReportSolution `json:"candidate_flags"`
	Answers        []ReportSolution `json:"answers"`
	Procedures     []ReportSolution `json:"procedures"`
	ConfirmedFacts []ReportFact     `json:"confirmed_facts"`
	TentativeFacts []ReportFact     `json:"tentative_facts"`
	Evidence       []ReportEvidence `json:"evidence"`
}

// ReportSolution is the report-safe current Solution allowlist.
type ReportSolution struct {
	Key                 string `json:"key"`
	Kind                string `json:"kind"`
	Status              string `json:"status"`
	Summary             string `json:"summary"`
	Value               string `json:"value,omitempty"`
	VerificationSummary string `json:"verification_summary,omitempty"`
}

// ReportMarkdown is the operator markdown deliverable body (format=markdown).
type ReportMarkdown struct {
	Schema   string `json:"schema"`
	Markdown string `json:"markdown"`
}

// NewReportMarkdown wraps deterministic report Markdown in the closed v2 schema.
func NewReportMarkdown(markdown string) ReportMarkdown {
	return ReportMarkdown{Schema: reportMarkdownSchema, Markdown: markdown}
}

// ReportProject contains only display semantics needed by the deliverable.
type ReportProject struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ReportFinding is the report-safe Finding detail and its explicit current
// support. Storage metadata stays internal; the Blackboard Key is included for
// navigation.
type ReportFinding struct {
	Key             string           `json:"key"`
	Title           string           `json:"title"`
	Status          string           `json:"status"`
	Severity        string           `json:"severity,omitempty"`
	CVSSVersion     string           `json:"cvss_version,omitempty"`
	CVSSVector      string           `json:"cvss_vector,omitempty"`
	CVSSPending     bool             `json:"cvss_pending"`
	Target          string           `json:"target,omitempty"`
	Description     string           `json:"description,omitempty"`
	Proof           string           `json:"proof,omitempty"`
	Impact          string           `json:"impact,omitempty"`
	Recommendation  string           `json:"recommendation,omitempty"`
	SupportingFacts []ReportFact     `json:"supporting_facts"`
	Contradictions  []ReportFact     `json:"contradictions"`
	Evidence        []ReportEvidence `json:"evidence"`
}

// ReportFact is the report-safe current Project Fact allowlist. The Blackboard
// Key is included so consumers can load current detail/history for every Fact.
type ReportFact struct {
	Key         string `json:"key"`
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Body        string `json:"body,omitempty"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status"`
}

// ReportEvidence contains semantic evidence description and its Blackboard Key.
// Paths, digests, sizes, and execution bindings are intentionally excluded.
type ReportEvidence struct {
	Key          string `json:"key"`
	Status       string `json:"status"`
	ArtifactType string `json:"artifact_type"`
	Summary      string `json:"summary"`
	MediaType    string `json:"media_type,omitempty"`
	CapturedAt   string `json:"captured_at,omitempty"`
}

type reportFindingState struct {
	key    string
	record findingOutputRecord
	report ReportFinding
}

// PentestReport reads one atomic current v2 state and assembles a deterministic
// Pentest report projection. It never reads raw Runtime output or history.
func (s *Service) PentestReport(ctx context.Context, projectID string) (PentestReportProjection, error) {
	projection, _, err := s.ProjectPentestReport(ctx, projectID)
	return projection, err
}

// ProjectPentestReport is the operator/HTTP seam: semantic projection plus the
// current Project revision used for conditional ETags. The revision is not part
// of the report body allowlist.
func (s *Service) ProjectPentestReport(ctx context.Context, projectID string) (PentestReportProjection, int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PentestReportProjection{}, 0, fmt.Errorf("begin v2 Pentest report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var projectName, projectDescription, projectKind string
	if err := tx.QueryRowContext(ctx, `SELECT name, description, kind FROM projects WHERE id = ?`, projectID).Scan(&projectName, &projectDescription, &projectKind); err != nil {
		if err == sql.ErrNoRows {
			return PentestReportProjection{}, 0, semanticError("not_found", "Project was not found", "", nil)
		}
		return PentestReportProjection{}, 0, fmt.Errorf("read report Project: %w", err)
	}
	if projectKind != "pentest" {
		return PentestReportProjection{}, 0, semanticError("project_kind_mismatch", "Pentest reports require a Pentest Project", "", nil)
	}
	if err := validateAllConfirmedFindings(ctx, tx, projectID); err != nil {
		return PentestReportProjection{}, 0, err
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return PentestReportProjection{}, 0, err
	}

	report := PentestReportProjection{
		Schema:            pentestReportSchema,
		Project:           ReportProject{Name: projectName, Description: projectDescription},
		ConfirmedFindings: []ReportFinding{}, UnconfirmedFindings: []ReportFinding{},
		ConfirmedFacts: []ReportFact{}, TentativeFacts: []ReportFact{},
	}
	findings := make(map[string]*reportFindingState)
	confirmedFacts := make(map[string]FactRecord)
	// confirmed supporting facts per finding key (fact key set)
	supportingConfirmed := make(map[string]map[string]struct{})
	// evidence keyed by the target they evidence (finding or fact key)
	evidenceByTarget := make(map[string][]ReportEvidence)
	evidenceSeenByTarget := make(map[string]map[string]struct{})

	rows, err := tx.QueryContext(ctx, `
		SELECT key, type, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type IN ('fact', 'finding')
		ORDER BY key ASC`, projectID)
	if err != nil {
		return PentestReportProjection{}, 0, fmt.Errorf("read report conclusions: %w", err)
	}
	for rows.Next() {
		var key, typ, raw string
		if err := rows.Scan(&key, &typ, &raw); err != nil {
			rows.Close()
			return PentestReportProjection{}, 0, fmt.Errorf("scan report conclusion: %w", err)
		}
		switch typ {
		case "fact":
			var fact FactRecord
			if err := decodeJSON([]byte(raw), &fact); err != nil {
				rows.Close()
				return PentestReportProjection{}, 0, fmt.Errorf("decode report Fact: %w", err)
			}
			item := reportFact(key, fact)
			if fact.Confidence == "confirmed" {
				report.ConfirmedFacts = append(report.ConfirmedFacts, item)
				confirmedFacts[key] = fact
			} else {
				report.TentativeFacts = append(report.TentativeFacts, item)
			}
		case "finding":
			var finding findingOutputRecord
			if err := decodeJSON([]byte(raw), &finding); err != nil {
				rows.Close()
				return PentestReportProjection{}, 0, fmt.Errorf("decode report Finding: %w", err)
			}
			findings[key] = &reportFindingState{key: key, record: finding, report: ReportFinding{
				Key: key, Title: finding.Title, Status: finding.Status, Severity: finding.Severity,
				CVSSVersion: finding.CVSSVersion, CVSSVector: finding.CVSSVector, CVSSPending: finding.CVSSPending,
				Target: finding.Target, Description: finding.Description, Proof: finding.Proof,
				Impact: finding.Impact, Recommendation: finding.Recommendation,
				SupportingFacts: []ReportFact{}, Contradictions: []ReportFact{}, Evidence: []ReportEvidence{},
			}}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PentestReportProjection{}, 0, fmt.Errorf("iterate report conclusions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return PentestReportProjection{}, 0, fmt.Errorf("close report conclusions: %w", err)
	}

	relRows, err := tx.QueryContext(ctx, `
		SELECT rel.from_key, rel.to_key, rel.relation, source.type, source.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.relation IN ('supports', 'contradicts', 'evidences')
		ORDER BY rel.to_key ASC, rel.relation ASC, rel.from_key ASC`, projectID)
	if err != nil {
		return PentestReportProjection{}, 0, fmt.Errorf("read report support: %w", err)
	}
	for relRows.Next() {
		var fromKey, target, relation, sourceType, raw string
		if err := relRows.Scan(&fromKey, &target, &relation, &sourceType, &raw); err != nil {
			relRows.Close()
			return PentestReportProjection{}, 0, fmt.Errorf("scan report support: %w", err)
		}
		switch sourceType {
		case "fact":
			finding := findings[target]
			if finding == nil {
				continue
			}
			var fact FactRecord
			if err := decodeJSON([]byte(raw), &fact); err != nil {
				relRows.Close()
				return PentestReportProjection{}, 0, fmt.Errorf("decode report supporting Fact: %w", err)
			}
			if relation == "supports" {
				finding.report.SupportingFacts = append(finding.report.SupportingFacts, reportFact(fromKey, fact))
				if fact.Confidence == "confirmed" {
					if supportingConfirmed[target] == nil {
						supportingConfirmed[target] = make(map[string]struct{})
					}
					supportingConfirmed[target][fromKey] = struct{}{}
				}
			} else if relation == "contradicts" {
				finding.report.Contradictions = append(finding.report.Contradictions, reportFact(fromKey, fact))
			}
		case "evidence":
			if relation != "evidences" {
				continue
			}
			var evidence EvidenceRecord
			if err := decodeJSON([]byte(raw), &evidence); err != nil {
				relRows.Close()
				return PentestReportProjection{}, 0, fmt.Errorf("decode report Evidence: %w", err)
			}
			item := reportEvidence(fromKey, evidence)
			if evidenceSeenByTarget[target] == nil {
				evidenceSeenByTarget[target] = make(map[string]struct{})
			}
			if _, seen := evidenceSeenByTarget[target][fromKey]; seen {
				continue
			}
			evidenceSeenByTarget[target][fromKey] = struct{}{}
			evidenceByTarget[target] = append(evidenceByTarget[target], item)
		}
	}
	if err := relRows.Err(); err != nil {
		relRows.Close()
		return PentestReportProjection{}, 0, fmt.Errorf("iterate report support: %w", err)
	}
	if err := relRows.Close(); err != nil {
		return PentestReportProjection{}, 0, fmt.Errorf("close report support: %w", err)
	}

	// Relationship-derived Evidence: direct evidences on the Finding, plus
	// Evidence that evidences a confirmed Fact supporting that Finding.
	for findingKey, finding := range findings {
		seen := make(map[string]struct{})
		appendEvidence := func(items []ReportEvidence) {
			for _, item := range items {
				if _, ok := seen[item.Key]; ok {
					continue
				}
				seen[item.Key] = struct{}{}
				finding.report.Evidence = append(finding.report.Evidence, item)
			}
		}
		appendEvidence(evidenceByTarget[findingKey])
		for factKey := range supportingConfirmed[findingKey] {
			if _, ok := confirmedFacts[factKey]; !ok {
				continue
			}
			appendEvidence(evidenceByTarget[factKey])
		}
		sort.Slice(finding.report.Evidence, func(i, j int) bool {
			return finding.report.Evidence[i].Key < finding.report.Evidence[j].Key
		})
	}

	ordered := make([]*reportFindingState, 0, len(findings))
	for _, finding := range findings {
		ordered = append(ordered, finding)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i].record, ordered[j].record
		if severityOrder(left.Severity) != severityOrder(right.Severity) {
			return severityOrder(left.Severity) > severityOrder(right.Severity)
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		if left.Title != right.Title {
			return left.Title < right.Title
		}
		// Presentation grouping never merges identities: key breaks ties.
		return ordered[i].key < ordered[j].key
	})
	for _, finding := range ordered {
		if finding.record.Status == "confirmed" {
			report.ConfirmedFindings = append(report.ConfirmedFindings, finding.report)
		} else if finding.record.Status == "unconfirmed" {
			report.UnconfirmedFindings = append(report.UnconfirmedFindings, finding.report)
		}
	}
	return report, revision, nil
}

// CTFSolution reads the current CTF consumer projection. Solved is true only
// when at least one verified flag Solution is current.
func (s *Service) CTFSolution(ctx context.Context, projectID string) (CTFSolutionProjection, error) {
	projection, _, err := s.ProjectCTFSolution(ctx, projectID)
	return projection, err
}

// ProjectCTFSolution is the operator/HTTP seam for the CTF solution deliverable.
func (s *Service) ProjectCTFSolution(ctx context.Context, projectID string) (CTFSolutionProjection, int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CTFSolutionProjection{}, 0, fmt.Errorf("begin v2 CTF solution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var projectName, projectDescription, projectKind string
	if err := tx.QueryRowContext(ctx, `SELECT name, description, kind FROM projects WHERE id = ?`, projectID).Scan(&projectName, &projectDescription, &projectKind); err != nil {
		if err == sql.ErrNoRows {
			return CTFSolutionProjection{}, 0, semanticError("not_found", "Project was not found", "", nil)
		}
		return CTFSolutionProjection{}, 0, fmt.Errorf("read CTF solution Project: %w", err)
	}
	if projectKind != "ctf_challenge" {
		return CTFSolutionProjection{}, 0, semanticError("project_kind_mismatch", "CTF solutions require a CTF Challenge Project", "", nil)
	}
	if err := validateAllVerifiedSolutions(ctx, tx, projectID); err != nil {
		return CTFSolutionProjection{}, 0, err
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return CTFSolutionProjection{}, 0, err
	}

	projection := CTFSolutionProjection{
		Schema:        ctfSolutionSchema,
		Project:       ReportProject{Name: projectName, Description: projectDescription},
		VerifiedFlags: []ReportSolution{}, CandidateFlags: []ReportSolution{},
		Answers: []ReportSolution{}, Procedures: []ReportSolution{},
		ConfirmedFacts: []ReportFact{}, TentativeFacts: []ReportFact{},
		Evidence: []ReportEvidence{},
	}
	// Targets that may carry report Evidence: relevant Solutions and confirmed Facts.
	evidenceTargets := make(map[string]struct{})
	evidenceByKey := make(map[string]ReportEvidence)

	rows, err := tx.QueryContext(ctx, `
		SELECT key, type, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type IN ('solution', 'fact', 'evidence')
		ORDER BY key ASC`, projectID)
	if err != nil {
		return CTFSolutionProjection{}, 0, fmt.Errorf("read CTF solution records: %w", err)
	}
	for rows.Next() {
		var key, typ, raw string
		if err := rows.Scan(&key, &typ, &raw); err != nil {
			rows.Close()
			return CTFSolutionProjection{}, 0, fmt.Errorf("scan CTF solution record: %w", err)
		}
		switch typ {
		case "solution":
			var solution SolutionRecord
			if err := decodeJSON([]byte(raw), &solution); err != nil {
				rows.Close()
				return CTFSolutionProjection{}, 0, fmt.Errorf("decode CTF Solution: %w", err)
			}
			item := ReportSolution{
				Key: key, Kind: solution.Kind, Status: solution.Status, Summary: solution.Summary,
				Value: solution.Value, VerificationSummary: solution.VerificationSummary,
			}
			switch {
			case solution.Kind == "flag" && solution.Status == "verified":
				projection.VerifiedFlags = append(projection.VerifiedFlags, item)
				evidenceTargets[key] = struct{}{}
			case solution.Kind == "flag" && solution.Status == "candidate":
				projection.CandidateFlags = append(projection.CandidateFlags, item)
				evidenceTargets[key] = struct{}{}
			case solution.Kind == "answer":
				projection.Answers = append(projection.Answers, item)
				evidenceTargets[key] = struct{}{}
			case solution.Kind == "procedure":
				projection.Procedures = append(projection.Procedures, item)
				evidenceTargets[key] = struct{}{}
			}
		case "fact":
			var fact FactRecord
			if err := decodeJSON([]byte(raw), &fact); err != nil {
				rows.Close()
				return CTFSolutionProjection{}, 0, fmt.Errorf("decode CTF Fact: %w", err)
			}
			item := reportFact(key, fact)
			if fact.Confidence == "confirmed" {
				projection.ConfirmedFacts = append(projection.ConfirmedFacts, item)
				evidenceTargets[key] = struct{}{}
			} else {
				projection.TentativeFacts = append(projection.TentativeFacts, item)
			}
		case "evidence":
			var evidence EvidenceRecord
			if err := decodeJSON([]byte(raw), &evidence); err != nil {
				rows.Close()
				return CTFSolutionProjection{}, 0, fmt.Errorf("decode CTF Evidence: %w", err)
			}
			evidenceByKey[key] = reportEvidence(key, evidence)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CTFSolutionProjection{}, 0, fmt.Errorf("iterate CTF solution records: %w", err)
	}
	if err := rows.Close(); err != nil {
		return CTFSolutionProjection{}, 0, fmt.Errorf("close CTF solution records: %w", err)
	}

	// CTF Evidence is relationship-derived only: never dump unrelated Evidence.
	if len(evidenceByKey) > 0 && len(evidenceTargets) > 0 {
		relRows, err := tx.QueryContext(ctx, `
			SELECT rel.from_key, rel.to_key
			FROM blackboard_v2_relationships AS rel
			WHERE rel.project_id = ? AND rel.relation = 'evidences'
			ORDER BY rel.from_key ASC`, projectID)
		if err != nil {
			return CTFSolutionProjection{}, 0, fmt.Errorf("read CTF Evidence relationships: %w", err)
		}
		selected := make(map[string]struct{})
		for relRows.Next() {
			var fromKey, toKey string
			if err := relRows.Scan(&fromKey, &toKey); err != nil {
				relRows.Close()
				return CTFSolutionProjection{}, 0, fmt.Errorf("scan CTF Evidence relationship: %w", err)
			}
			if _, ok := evidenceTargets[toKey]; !ok {
				continue
			}
			if _, ok := evidenceByKey[fromKey]; !ok {
				continue
			}
			selected[fromKey] = struct{}{}
		}
		if err := relRows.Err(); err != nil {
			relRows.Close()
			return CTFSolutionProjection{}, 0, fmt.Errorf("iterate CTF Evidence relationships: %w", err)
		}
		if err := relRows.Close(); err != nil {
			return CTFSolutionProjection{}, 0, fmt.Errorf("close CTF Evidence relationships: %w", err)
		}
		keys := make([]string, 0, len(selected))
		for key := range selected {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			projection.Evidence = append(projection.Evidence, evidenceByKey[key])
		}
	}

	projection.Solved = len(projection.VerifiedFlags) > 0
	return projection, revision, nil
}

func reportFact(key string, fact FactRecord) ReportFact {
	return ReportFact{
		Key: key, Category: fact.Category, Summary: fact.Summary, Body: fact.Body,
		Confidence: fact.Confidence, ScopeStatus: fact.ScopeStatus,
	}
}

func reportEvidence(key string, evidence EvidenceRecord) ReportEvidence {
	return ReportEvidence{
		Key: key, Status: evidence.Status, ArtifactType: evidence.ArtifactType, Summary: evidence.Summary,
		MediaType: evidence.MediaType, CapturedAt: evidence.CapturedAt,
	}
}

func severityOrder(value string) int {
	switch value {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "none":
		return 1
	default:
		return 0
	}
}
