package blackboardv2

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

const healthSchema = "blackboard-health/v2"

type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusAttention HealthStatus = "attention"
	HealthStatusWarning   HealthStatus = "warning"
	HealthStatusCritical  HealthStatus = "critical"
)

type HealthSeverity string

const (
	HealthSeverityInfo     HealthSeverity = "info"
	HealthSeverityWarning  HealthSeverity = "warning"
	HealthSeverityCritical HealthSeverity = "critical"
)

// SemanticHealth is the closed blackboard-health/v2 operator DTO.
// It is a deterministic read-only diagnosis derived from current semantic
// state and exact Runtime Snapshot bytes when available. It never mutates
// knowledge, never truncates Snapshot completeness, and never blocks launch.
type SemanticHealth struct {
	Schema    string          `json:"schema"`
	Revision  int             `json:"revision"`
	Status    HealthStatus    `json:"status"`
	Attention HealthAttention `json:"attention"`
	Anomalies []HealthAnomaly `json:"anomalies"`
	// Proposals remains an empty array for response compatibility.
	Proposals [0]struct{} `json:"proposals"`
}

// HealthAttention reports Snapshot budget measurement and consolidation
// guidance. When the canonical Runtime Snapshot cannot be projected due to
// corruption, Complete is false and Bytes measure a health-safe diagnostic
// encoding of all persisted records and relationship rows.
type HealthAttention struct {
	Bytes                 int                  `json:"bytes"`
	EstimatedTokens       int                  `json:"estimated_tokens"`
	State                 AttentionBudgetState `json:"state"`
	Complete              bool                 `json:"complete"`
	Launchable            bool                 `json:"launchable"`
	ConsolidationOffered  bool                 `json:"consolidation_offered"`
	ConsolidationRequired bool                 `json:"consolidation_required"`
}

type HealthAnomaly struct {
	Code        string         `json:"code"`
	Severity    HealthSeverity `json:"severity"`
	Message     string         `json:"message"`
	SubjectKey  string         `json:"subject_key,omitempty"`
	RelatedKeys []string       `json:"related_keys,omitempty"`
}

// ProjectSemanticHealth derives the current Project's semantic health DTO.
// Corruption that makes the canonical Runtime Snapshot unreadable is reported
// as anomalies with a diagnostic attention measurement; it is never a hard
// validation failure (HTTP 422).
func (s *Service) ProjectSemanticHealth(ctx context.Context, projectID string) (SemanticHealth, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SemanticHealth{}, fmt.Errorf("begin semantic health: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return SemanticHealth{}, err
	}

	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return SemanticHealth{}, err
	}

	// Load records without relationship validation so integrity failures are
	// diagnosable rather than hard errors.
	base, typeByKey, evidenceByKey, err := s.loadHealthRecordsTx(ctx, tx, projectID, revision)
	if err != nil {
		return SemanticHealth{}, err
	}
	rawRelations, storedRelations, err := loadAllRelationshipsForHealth(ctx, tx, projectID)
	if err != nil {
		return SemanticHealth{}, err
	}

	anomalies := make([]HealthAnomaly, 0)
	redirectAnomalies, err := redirectIntegrityAnomalies(ctx, tx, projectID, typeByKey)
	if err != nil {
		return SemanticHealth{}, err
	}
	anomalies = append(anomalies, redirectAnomalies...)
	anomalies = append(anomalies, relationshipIntegrityAnomalies(storedRelations, typeByKey)...)

	// Prefer exact canonical Runtime Snapshot measurement when projection is
	// valid. Otherwise measure a health-safe diagnostic encoding that includes
	// every persisted record and relationship row (including dangling/invalid).
	canonicalOK := false
	var projection RuntimeSnapshotProjection
	analysisSnapshot := base
	analysisSnapshot.Relations = rawRelations
	if snapshot, snapErr := s.runtimeSnapshotTx(ctx, tx, projectID); snapErr == nil {
		if measured, measureErr := projectRuntimeSnapshot(snapshot); measureErr == nil {
			canonicalOK = true
			projection = measured
			analysisSnapshot = snapshot
		}
	}
	if !canonicalOK {
		measured, measureErr := projectRuntimeSnapshot(analysisSnapshot)
		if measureErr != nil {
			return SemanticHealth{}, measureErr
		}
		projection = measured
	}

	anomalies = append(anomalies, attentionAnomalies(projection, canonicalOK)...)
	anomalies = append(anomalies, snapshotSemanticAnomalies(analysisSnapshot)...)
	evidenceAnomalies, err := s.evidenceIntegrityAnomalies(projectID, evidenceByKey, analysisSnapshot)
	if err != nil {
		return SemanticHealth{}, err
	}
	anomalies = append(anomalies, evidenceAnomalies...)
	anomalies = dedupeHealthAnomalies(anomalies)
	sortHealthAnomalies(anomalies)

	offered := projection.AttentionState == AttentionWarning || projection.AttentionState == AttentionRequired
	required := projection.AttentionState == AttentionRequired
	attention := HealthAttention{
		Bytes:                 projection.ByteCount,
		EstimatedTokens:       projection.EstimatedTokens,
		State:                 projection.AttentionState,
		Complete:              canonicalOK,
		Launchable:            true,
		ConsolidationOffered:  offered,
		ConsolidationRequired: required,
	}
	return SemanticHealth{
		Schema:    healthSchema,
		Revision:  revision,
		Status:    healthStatusFromAnomalies(anomalies),
		Attention: attention,
		Anomalies: anomalies,
		Proposals: [0]struct{}{},
	}, nil
}

type healthEvidenceRecord struct {
	key    string
	record EvidenceRecord
}

func (s *Service) loadHealthRecordsTx(ctx context.Context, tx *sql.Tx, projectID string, revision int) (RuntimeSnapshot, map[string]string, []healthEvidenceRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT key, type, version, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type IN ('entity', 'objective', 'attempt', 'fact', 'finding', 'solution', 'evidence')
		ORDER BY key ASC`, projectID,
	)
	if err != nil {
		return RuntimeSnapshot{}, nil, nil, fmt.Errorf("read Blackboard v2 health records: %w", err)
	}
	defer rows.Close()

	entities := make(map[string]SnapshotEntity)
	objectives := make(map[string]SnapshotObjective)
	attempts := make(map[string]SnapshotAttempt)
	facts := make(map[string]SnapshotFact)
	findings := make(map[string]SnapshotFinding)
	solutions := make(map[string]SnapshotSolution)
	evidence := make(map[string]SnapshotEvidence)
	typeByKey := map[string]string{}
	evidenceByKey := make([]healthEvidenceRecord, 0)
	for rows.Next() {
		var key, typ, raw string
		var version int
		if err := rows.Scan(&key, &typ, &version, &raw); err != nil {
			return RuntimeSnapshot{}, nil, nil, fmt.Errorf("scan Blackboard v2 health record: %w", err)
		}
		record, err := decodeStoredRecord(typ, raw)
		if err != nil {
			return RuntimeSnapshot{}, nil, nil, fmt.Errorf("decode Blackboard v2 health record: %w", err)
		}
		typeByKey[key] = typ
		switch typ {
		case "entity":
			entity := record.entityRecord()
			entities[key] = SnapshotEntity{
				Version: version, Status: entity.Status, Kind: entity.Kind, Name: entity.Name,
				Locator: entity.Locator, Description: entity.Description, ScopeStatus: entity.ScopeStatus, CredentialRef: entity.CredentialRef,
			}
		case "objective":
			objective := record.objectiveRecord()
			objectives[key] = SnapshotObjective{Version: version, Status: objective.Status, Objective: objective.Objective}
		case "attempt":
			attempt := record.attemptRecord()
			attempts[key] = SnapshotAttempt{Version: version, Status: attempt.Status, Summary: attempt.Summary}
		case "fact":
			fact := record.factRecord()
			facts[key] = SnapshotFact{Version: version, Category: fact.Category, Summary: fact.Summary, Confidence: fact.Confidence, ScopeStatus: fact.ScopeStatus}
		case "finding":
			finding := record.findingOutputRecord()
			findings[key] = SnapshotFinding{
				Version: version, Status: finding.Status, Title: finding.Title, Target: finding.Target,
				Description: finding.Description, Severity: finding.Severity, CVSSPending: finding.CVSSPending,
			}
		case "solution":
			solution := record.solutionRecord()
			solutions[key] = SnapshotSolution{
				Version: version, Status: solution.Status, Kind: solution.Kind, Summary: solution.Summary,
				Value: solution.Value, VerificationSummary: solution.VerificationSummary,
			}
		case "evidence":
			item := record.evidenceRecord()
			evidence[key] = SnapshotEvidence{
				Version: version, Status: item.Status, ArtifactType: item.ArtifactType, Summary: item.Summary,
				MediaType: item.MediaType, CapturedAt: item.CapturedAt,
			}
			evidenceByKey = append(evidenceByKey, healthEvidenceRecord{key: key, record: item})
		}
	}
	if err := rows.Err(); err != nil {
		return RuntimeSnapshot{}, nil, nil, fmt.Errorf("iterate Blackboard v2 health records: %w", err)
	}
	work := SnapshotWork{}
	if len(objectives) != 0 {
		work.Objectives = objectives
	}
	if len(attempts) != 0 {
		work.Attempts = attempts
	}
	knowledge := SnapshotKnowledge{}
	if len(entities) != 0 {
		knowledge.Entities = entities
	}
	if len(facts) != 0 {
		knowledge.Facts = facts
	}
	if len(findings) != 0 {
		knowledge.Findings = findings
	}
	if len(solutions) != 0 {
		knowledge.Solutions = solutions
	}
	if len(evidence) != 0 {
		knowledge.Evidence = evidence
	}
	return RuntimeSnapshot{
		Schema: snapshotSchema, Semantics: snapshotSemantics, Revision: revision,
		Work: work, Knowledge: knowledge, Relations: []RelationshipTuple{},
	}, typeByKey, evidenceByKey, nil
}

// loadAllRelationshipsForHealth returns every persisted relationship row as a
// diagnostic tuple (including dangling/invalid edges) plus typed metadata for
// integrity classification. It never fails for grammar/cycle violations.
func loadAllRelationshipsForHealth(ctx context.Context, tx *sql.Tx, projectID string) ([]RelationshipTuple, []persistedRelationship, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT rel.from_key, rel.relation, rel.to_key, rel.reason, source.type, target.type
		FROM blackboard_v2_relationships AS rel
		LEFT JOIN blackboard_v2_records AS source
		  ON source.project_id=rel.project_id AND source.key=rel.from_key
		LEFT JOIN blackboard_v2_records AS target
		  ON target.project_id=rel.project_id AND target.key=rel.to_key
		WHERE rel.project_id = ?
		ORDER BY rel.from_key ASC, rel.relation ASC, rel.to_key ASC, rel.reason ASC`,
		projectID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("read health relationships: %w", err)
	}
	defer rows.Close()

	tuples := make([]RelationshipTuple, 0)
	stored := make([]persistedRelationship, 0)
	for rows.Next() {
		var from, relation, to, reason string
		var fromType, toType sql.NullString
		if err := rows.Scan(&from, &relation, &to, &reason, &fromType, &toType); err != nil {
			return nil, nil, fmt.Errorf("scan health relationship: %w", err)
		}
		if reason == "" {
			tuples = append(tuples, RelationshipTuple{from, relation, to})
		} else {
			tuples = append(tuples, RelationshipTuple{from, relation, to, reason})
		}
		stored = append(stored, persistedRelationship{
			from: from, relation: relation, to: to,
			fromType: fromType.String, toType: toType.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate health relationships: %w", err)
	}
	return tuples, stored, nil
}

func healthStatusFromAnomalies(anomalies []HealthAnomaly) HealthStatus {
	status := HealthStatusHealthy
	for _, anomaly := range anomalies {
		switch anomaly.Severity {
		case HealthSeverityCritical:
			return HealthStatusCritical
		case HealthSeverityWarning:
			if status != HealthStatusCritical {
				status = HealthStatusWarning
			}
		case HealthSeverityInfo:
			if status == HealthStatusHealthy {
				status = HealthStatusAttention
			}
		}
	}
	return status
}

func dedupeHealthAnomalies(anomalies []HealthAnomaly) []HealthAnomaly {
	if len(anomalies) == 0 {
		return anomalies
	}
	seen := make(map[string]bool, len(anomalies))
	out := make([]HealthAnomaly, 0, len(anomalies))
	for _, anomaly := range anomalies {
		key := anomaly.Code + "\x00" + string(anomaly.Severity) + "\x00" + anomaly.SubjectKey + "\x00" + anomaly.Message + "\x00" + strings.Join(anomaly.RelatedKeys, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, anomaly)
	}
	return out
}

func sortHealthAnomalies(anomalies []HealthAnomaly) {
	sort.SliceStable(anomalies, func(i, j int) bool {
		left, right := anomalies[i], anomalies[j]
		if severityRank(left.Severity) != severityRank(right.Severity) {
			return severityRank(left.Severity) > severityRank(right.Severity)
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.SubjectKey != right.SubjectKey {
			return left.SubjectKey < right.SubjectKey
		}
		if left.Message != right.Message {
			return left.Message < right.Message
		}
		return strings.Join(left.RelatedKeys, "\x00") < strings.Join(right.RelatedKeys, "\x00")
	})
}

func severityRank(severity HealthSeverity) int {
	switch severity {
	case HealthSeverityCritical:
		return 3
	case HealthSeverityWarning:
		return 2
	case HealthSeverityInfo:
		return 1
	default:
		return 0
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
