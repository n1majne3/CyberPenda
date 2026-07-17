package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

const pentestReportSchema = "pentest-report/v2"

// PentestReportProjection is the deterministic, current semantic report
// allowlist. It excludes Blackboard Keys, storage identity, integrity hashes,
// Trusted Origin, and execution history.
type PentestReportProjection struct {
	Schema              string          `json:"schema"`
	Project             ReportProject   `json:"project"`
	ConfirmedFindings   []ReportFinding `json:"confirmed_findings"`
	UnconfirmedFindings []ReportFinding `json:"unconfirmed_findings"`
	ConfirmedFacts      []ReportFact    `json:"confirmed_facts"`
	TentativeFacts      []ReportFact    `json:"tentative_facts"`
}

// ReportProject contains only display semantics needed by the deliverable.
type ReportProject struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ReportFinding is the report-safe Finding detail and its explicit current
// support. Relationship keys and storage metadata stay internal.
type ReportFinding struct {
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

// ReportFact is the report-safe current Project Fact allowlist.
type ReportFact struct {
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Body        string `json:"body,omitempty"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status"`
}

// ReportEvidence contains semantic evidence description only. Paths, digests,
// sizes, and execution bindings are intentionally excluded.
type ReportEvidence struct {
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
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PentestReportProjection{}, fmt.Errorf("begin v2 Pentest report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var projectName, projectDescription, projectKind string
	if err := tx.QueryRowContext(ctx, `SELECT name, description, kind FROM projects WHERE id = ?`, projectID).Scan(&projectName, &projectDescription, &projectKind); err != nil {
		if err == sql.ErrNoRows {
			return PentestReportProjection{}, semanticError("not_found", "Project was not found", "", nil)
		}
		return PentestReportProjection{}, fmt.Errorf("read report Project: %w", err)
	}
	if projectKind != "pentest" {
		return PentestReportProjection{}, semanticError("project_kind_mismatch", "Pentest reports require a Pentest Project", "", nil)
	}
	if err := validateAllConfirmedFindings(ctx, tx, projectID); err != nil {
		return PentestReportProjection{}, err
	}

	report := PentestReportProjection{
		Schema:            pentestReportSchema,
		Project:           ReportProject{Name: projectName, Description: projectDescription},
		ConfirmedFindings: []ReportFinding{}, UnconfirmedFindings: []ReportFinding{},
		ConfirmedFacts: []ReportFact{}, TentativeFacts: []ReportFact{},
	}
	findings := make(map[string]*reportFindingState)
	rows, err := tx.QueryContext(ctx, `
		SELECT key, type, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type IN ('fact', 'finding')
		ORDER BY key ASC`, projectID)
	if err != nil {
		return PentestReportProjection{}, fmt.Errorf("read report conclusions: %w", err)
	}
	for rows.Next() {
		var key, typ, raw string
		if err := rows.Scan(&key, &typ, &raw); err != nil {
			rows.Close()
			return PentestReportProjection{}, fmt.Errorf("scan report conclusion: %w", err)
		}
		switch typ {
		case "fact":
			var fact FactRecord
			if err := json.Unmarshal([]byte(raw), &fact); err != nil {
				rows.Close()
				return PentestReportProjection{}, fmt.Errorf("decode report Fact: %w", err)
			}
			item := reportFact(fact)
			if fact.Confidence == "confirmed" {
				report.ConfirmedFacts = append(report.ConfirmedFacts, item)
			} else {
				report.TentativeFacts = append(report.TentativeFacts, item)
			}
		case "finding":
			var finding findingOutputRecord
			if err := json.Unmarshal([]byte(raw), &finding); err != nil {
				rows.Close()
				return PentestReportProjection{}, fmt.Errorf("decode report Finding: %w", err)
			}
			findings[key] = &reportFindingState{key: key, record: finding, report: ReportFinding{
				Title: finding.Title, Status: finding.Status, Severity: finding.Severity,
				CVSSVersion: finding.CVSSVersion, CVSSVector: finding.CVSSVector, CVSSPending: finding.CVSSPending,
				Target: finding.Target, Description: finding.Description, Proof: finding.Proof,
				Impact: finding.Impact, Recommendation: finding.Recommendation,
				SupportingFacts: []ReportFact{}, Contradictions: []ReportFact{}, Evidence: []ReportEvidence{},
			}}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PentestReportProjection{}, fmt.Errorf("iterate report conclusions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return PentestReportProjection{}, fmt.Errorf("close report conclusions: %w", err)
	}

	relRows, err := tx.QueryContext(ctx, `
		SELECT rel.to_key, rel.relation, source.type, source.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.relation IN ('supports', 'contradicts', 'evidences')
		ORDER BY rel.to_key ASC, rel.relation ASC, source.key ASC`, projectID)
	if err != nil {
		return PentestReportProjection{}, fmt.Errorf("read report support: %w", err)
	}
	for relRows.Next() {
		var target, relation, sourceType, raw string
		if err := relRows.Scan(&target, &relation, &sourceType, &raw); err != nil {
			relRows.Close()
			return PentestReportProjection{}, fmt.Errorf("scan report support: %w", err)
		}
		finding := findings[target]
		if finding == nil {
			continue
		}
		switch sourceType {
		case "fact":
			var fact FactRecord
			if err := json.Unmarshal([]byte(raw), &fact); err != nil {
				relRows.Close()
				return PentestReportProjection{}, fmt.Errorf("decode report supporting Fact: %w", err)
			}
			if relation == "supports" {
				finding.report.SupportingFacts = append(finding.report.SupportingFacts, reportFact(fact))
			} else if relation == "contradicts" {
				finding.report.Contradictions = append(finding.report.Contradictions, reportFact(fact))
			}
		case "evidence":
			if relation != "evidences" {
				continue
			}
			var evidence EvidenceRecord
			if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
				relRows.Close()
				return PentestReportProjection{}, fmt.Errorf("decode report Evidence: %w", err)
			}
			finding.report.Evidence = append(finding.report.Evidence, ReportEvidence{
				Status: evidence.Status, ArtifactType: evidence.ArtifactType, Summary: evidence.Summary,
				MediaType: evidence.MediaType, CapturedAt: evidence.CapturedAt,
			})
		}
	}
	if err := relRows.Err(); err != nil {
		relRows.Close()
		return PentestReportProjection{}, fmt.Errorf("iterate report support: %w", err)
	}
	if err := relRows.Close(); err != nil {
		return PentestReportProjection{}, fmt.Errorf("close report support: %w", err)
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
		return ordered[i].key < ordered[j].key
	})
	for _, finding := range ordered {
		if finding.record.Status == "confirmed" {
			report.ConfirmedFindings = append(report.ConfirmedFindings, finding.report)
		} else if finding.record.Status == "unconfirmed" {
			report.UnconfirmedFindings = append(report.UnconfirmedFindings, finding.report)
		}
	}
	return report, nil
}

func reportFact(fact FactRecord) ReportFact {
	return ReportFact{Category: fact.Category, Summary: fact.Summary, Body: fact.Body, Confidence: fact.Confidence, ScopeStatus: fact.ScopeStatus}
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
