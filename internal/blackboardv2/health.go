package blackboardv2

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"pentest/internal/blackboardv2grammar"
)

const healthSchema = "blackboard-health/v2"

// HealthStatus is the maximum anomaly severity for one Project.
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusAttention HealthStatus = "attention"
	HealthStatusWarning   HealthStatus = "warning"
	HealthStatusCritical  HealthStatus = "critical"
)

// HealthSeverity ranks one actionable anomaly.
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
	Schema    string           `json:"schema"`
	Revision  int              `json:"revision"`
	Status    HealthStatus     `json:"status"`
	Attention HealthAttention  `json:"attention"`
	Anomalies []HealthAnomaly  `json:"anomalies"`
	Proposals []HealthProposal `json:"proposals"`
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

// HealthAnomaly is one concise, actionable semantic health finding.
type HealthAnomaly struct {
	Code        string         `json:"code"`
	Severity    HealthSeverity `json:"severity"`
	Message     string         `json:"message"`
	SubjectKey  string         `json:"subject_key,omitempty"`
	RelatedKeys []string       `json:"related_keys,omitempty"`
}

// HealthProposal is an approval-required operator action suggested by health.
// Health never mutates state and never schedules work; proposals are guidance only.
type HealthProposal struct {
	Code             string `json:"code"`
	Action           string `json:"action"`
	ApprovalRequired bool   `json:"approval_required"`
	Required         bool   `json:"required"`
}

const (
	proposalCodeConsolidationReasonTask = "consolidation_reason_task"
	proposalActionStartReasonTask       = "start_reason_task"
)

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
	redirectAnomalies, err := redirectIntegrityAnomalies(ctx, tx, projectID)
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
	proposals := make([]HealthProposal, 0)
	if offered {
		proposals = append(proposals, HealthProposal{
			Code:             proposalCodeConsolidationReasonTask,
			Action:           proposalActionStartReasonTask,
			ApprovalRequired: true,
			Required:         required,
		})
	}
	return SemanticHealth{
		Schema:    healthSchema,
		Revision:  revision,
		Status:    healthStatusFromAnomalies(anomalies),
		Attention: attention,
		Anomalies: anomalies,
		Proposals: proposals,
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

func relationshipIntegrityAnomalies(stored []persistedRelationship, typeByKey map[string]string) []HealthAnomaly {
	anomalies := make([]HealthAnomaly, 0)
	// Cycle detection only among edges that still have both endpoint types.
	cycleCandidates := make([]persistedRelationship, 0, len(stored))
	for _, relationship := range stored {
		fromType, fromOK := typeByKey[relationship.from]
		toType, toOK := typeByKey[relationship.to]
		if !fromOK || !toOK {
			subject, related := survivingRelationshipEndpoints(relationship.from, relationship.to, fromOK, toOK)
			anomalies = append(anomalies, HealthAnomaly{
				Code:        "dangling_relationship",
				Severity:    HealthSeverityCritical,
				Message:     fmt.Sprintf("Current %s relationship references a non-current endpoint.", relationship.relation),
				SubjectKey:  subject,
				RelatedKeys: related,
			})
			continue
		}
		// Prefer live type map over join metadata when present.
		if fromType == "" {
			fromType = relationship.fromType
		}
		if toType == "" {
			toType = relationship.toType
		}
		relationship.fromType, relationship.toType = fromType, toType
		rule, known := blackboardv2grammar.Lookup(relationship.relation)
		invalid := !known || relationship.relation == "supersedes" || relationship.from == relationship.to || !rule.Allows(fromType, toType)
		if !invalid {
			// Reason constraints still classify as invalid relationship integrity.
			// Reconstruct reason from stored edges is not available here; reasons
			// are validated when building cycle candidates only for known edges.
		}
		if invalid {
			anomalies = append(anomalies, HealthAnomaly{
				Code:        "invalid_relationship",
				Severity:    HealthSeverityCritical,
				Message:     fmt.Sprintf("Current %s relationship violates the closed endpoint grammar (%s → %s).", relationship.relation, fromType, toType),
				SubjectKey:  relationship.from,
				RelatedKeys: []string{relationship.to},
			})
			continue
		}
		cycleCandidates = append(cycleCandidates, relationship)
	}
	// Second pass: reason-length / reason-required violations need the raw reason.
	// Those are emitted from stored tuples via scan of the original load path.
	// Cycles among valid-endpoint edges.
	if relation, cyclic := persistedRelationshipCycle(cycleCandidates); cyclic {
		anomalies = append(anomalies, HealthAnomaly{
			Code:     "relationship_cycle",
			Severity: HealthSeverityCritical,
			Message:  fmt.Sprintf("Current %s relationships form a cycle that the closed grammar forbids.", relation),
		})
	}
	return anomalies
}

func survivingRelationshipEndpoints(from, to string, fromOK, toOK bool) (subject string, related []string) {
	switch {
	case fromOK && !toOK:
		return from, []string{to}
	case !fromOK && toOK:
		return to, []string{from}
	default:
		return from, []string{to}
	}
}

func attentionAnomalies(projection RuntimeSnapshotProjection, canonicalComplete bool) []HealthAnomaly {
	completeNote := "Complete Snapshot remains launchable."
	if !canonicalComplete {
		completeNote = "Canonical Runtime Snapshot is unavailable due to integrity issues; diagnostic attention remains launchable."
	}
	switch projection.AttentionState {
	case AttentionAboveTarget:
		return []HealthAnomaly{{
			Code:     "attention_above_target",
			Severity: HealthSeverityInfo,
			Message:  fmt.Sprintf("Runtime Snapshot is above the 16K healthy target (%d estimated tokens). %s", projection.EstimatedTokens, completeNote),
		}}
	case AttentionWarning:
		return []HealthAnomaly{{
			Code:     "attention_warning",
			Severity: HealthSeverityWarning,
			Message:  fmt.Sprintf("Runtime Snapshot reached the 32K warning threshold (%d estimated tokens). Offer an approval-required Reason Task for consolidation; do not truncate or auto-merge. %s", projection.EstimatedTokens, completeNote),
		}}
	case AttentionRequired:
		return []HealthAnomaly{{
			Code:     "attention_required",
			Severity: HealthSeverityCritical,
			Message:  fmt.Sprintf("Runtime Snapshot reached the 64K consolidation-required threshold (%d estimated tokens). Start an approval-required Reason Task for consolidation; do not truncate or auto-merge. %s", projection.EstimatedTokens, completeNote),
		}}
	default:
		return nil
	}
}

func snapshotSemanticAnomalies(snapshot RuntimeSnapshot) []HealthAnomaly {
	anomalies := make([]HealthAnomaly, 0)
	typeByKey := map[string]string{}
	statusByKey := map[string]string{}
	for key, row := range snapshot.Work.Objectives {
		typeByKey[key] = "objective"
		statusByKey[key] = row.Status
	}
	for key, row := range snapshot.Work.Attempts {
		typeByKey[key] = "attempt"
		statusByKey[key] = row.Status
	}
	for key, row := range snapshot.Knowledge.Entities {
		typeByKey[key] = "entity"
		statusByKey[key] = row.Status
	}
	for key, row := range snapshot.Knowledge.Facts {
		typeByKey[key] = "fact"
		statusByKey[key] = row.Confidence
	}
	for key, row := range snapshot.Knowledge.Findings {
		typeByKey[key] = "finding"
		statusByKey[key] = row.Status
	}
	for key, row := range snapshot.Knowledge.Solutions {
		typeByKey[key] = "solution"
		statusByKey[key] = row.Status
	}
	for key, row := range snapshot.Knowledge.Evidence {
		typeByKey[key] = "evidence"
		statusByKey[key] = row.Status
	}

	// Index relationships by Blackboard Key for stranded / satisfaction checks.
	testsFromAttempt := map[string][]string{}
	testsToObjective := map[string][]string{}
	satisfiesToObjective := map[string][]string{}
	evidencesFrom := map[string][]string{}
	contradicts := make([][2]string, 0)

	for _, relation := range snapshot.Relations {
		from, rel, to, ok := relationParts(relation)
		if !ok {
			continue
		}
		// Endpoint presence + closed grammar integrity on current Snapshot edges.
		fromType, fromOK := typeByKey[from]
		toType, toOK := typeByKey[to]
		if !fromOK || !toOK {
			// Dangling edges are reported by relationshipIntegrityAnomalies with
			// surviving-key preference; skip duplicate emission here.
			continue
		}
		if rule, known := blackboardv2grammar.Lookup(rel); !known || !rule.Allows(fromType, toType) {
			// Invalid grammar is reported by relationshipIntegrityAnomalies.
			continue
		}
		// Reason violations (oversized/required) still surface as invalid.
		var reason string
		if len(relation) >= 4 {
			reason, _ = relation[3].(string)
		}
		if violation := blackboardv2grammar.ReasonViolation(rel, reason); violation != "" {
			anomalies = append(anomalies, HealthAnomaly{
				Code:        "invalid_relationship",
				Severity:    HealthSeverityCritical,
				Message:     fmt.Sprintf("Current %s relationship has an invalid reason (%s).", rel, violation),
				SubjectKey:  from,
				RelatedKeys: []string{to},
			})
			continue
		}
		switch rel {
		case "tests":
			testsFromAttempt[from] = append(testsFromAttempt[from], to)
			if toType == "objective" {
				testsToObjective[to] = append(testsToObjective[to], from)
			}
		case "satisfies":
			if toType == "objective" {
				satisfiesToObjective[to] = append(satisfiesToObjective[to], from)
			}
		case "evidences":
			evidencesFrom[from] = append(evidencesFrom[from], to)
		case "contradicts":
			contradicts = append(contradicts, [2]string{from, to})
		}
	}

	// Stranded open work.
	for _, key := range sortedKeys(snapshot.Work.Objectives) {
		if len(testsToObjective[key]) == 0 {
			anomalies = append(anomalies, HealthAnomaly{
				Code:       "stranded_objective",
				Severity:   HealthSeverityWarning,
				Message:    "Open Objective has no open Attempt currently testing it.",
				SubjectKey: key,
			})
		}
		if sources := satisfiesToObjective[key]; len(sources) > 0 {
			related := append([]string(nil), sources...)
			sort.Strings(related)
			anomalies = append(anomalies, HealthAnomaly{
				Code:        "objective_satisfied_but_open",
				Severity:    HealthSeverityWarning,
				Message:     "Open Objective is already satisfied by current knowledge and should be resolved or abandoned.",
				SubjectKey:  key,
				RelatedKeys: related,
			})
		}
	}
	for _, key := range sortedKeys(snapshot.Work.Attempts) {
		if len(testsFromAttempt[key]) == 0 {
			anomalies = append(anomalies, HealthAnomaly{
				Code:       "stranded_attempt",
				Severity:   HealthSeverityWarning,
				Message:    "Open Attempt has no current tests relationship.",
				SubjectKey: key,
			})
		}
	}

	// Missing Evidence, escalated when it supports a confirmed/verified conclusion.
	for _, key := range sortedKeys(snapshot.Knowledge.Evidence) {
		row := snapshot.Knowledge.Evidence[key]
		if row.Status != "missing" {
			continue
		}
		severity := HealthSeverityWarning
		related := append([]string(nil), evidencesFrom[key]...)
		sort.Strings(related)
		for _, target := range related {
			switch typeByKey[target] {
			case "finding":
				if statusByKey[target] == "confirmed" {
					severity = HealthSeverityCritical
				}
			case "solution":
				if statusByKey[target] == "verified" {
					severity = HealthSeverityCritical
				}
			case "fact":
				if statusByKey[target] == "confirmed" {
					severity = HealthSeverityCritical
				}
			}
		}
		message := "Evidence Artifact is missing its managed payload."
		if severity == HealthSeverityCritical {
			message = "Missing Evidence currently supports a confirmed or verified conclusion."
		}
		anomalies = append(anomalies, HealthAnomaly{
			Code:        "missing_evidence",
			Severity:    severity,
			Message:     message,
			SubjectKey:  key,
			RelatedKeys: related,
		})
	}

	// Unresolved contradictions among live conclusions.
	seenContradiction := map[string]bool{}
	for _, pair := range contradicts {
		from, to := pair[0], pair[1]
		canon := from + "\x00" + to
		if from > to {
			canon = to + "\x00" + from
		}
		if seenContradiction[canon] {
			continue
		}
		seenContradiction[canon] = true
		severity := HealthSeverityWarning
		if isConfirmedConclusion(typeByKey[from], statusByKey[from]) && isConfirmedConclusion(typeByKey[to], statusByKey[to]) {
			severity = HealthSeverityCritical
		}
		related := []string{from, to}
		sort.Strings(related)
		anomalies = append(anomalies, HealthAnomaly{
			Code:        "unresolved_contradiction",
			Severity:    severity,
			Message:     "Current contradicts relationship remains unresolved between live conclusions.",
			SubjectKey:  related[0],
			RelatedKeys: related,
		})
	}

	return anomalies
}

func (s *Service) evidenceIntegrityAnomalies(projectID string, evidenceRecords []healthEvidenceRecord, snapshot RuntimeSnapshot) ([]HealthAnomaly, error) {
	if len(evidenceRecords) == 0 {
		return nil, nil
	}
	typeByKey := map[string]string{}
	statusByKey := map[string]string{}
	for key, row := range snapshot.Knowledge.Facts {
		typeByKey[key] = "fact"
		statusByKey[key] = row.Confidence
	}
	for key, row := range snapshot.Knowledge.Findings {
		typeByKey[key] = "finding"
		statusByKey[key] = row.Status
	}
	for key, row := range snapshot.Knowledge.Solutions {
		typeByKey[key] = "solution"
		statusByKey[key] = row.Status
	}
	evidencesFrom := map[string][]string{}
	for _, relation := range snapshot.Relations {
		from, rel, to, ok := relationParts(relation)
		if !ok || rel != "evidences" {
			continue
		}
		evidencesFrom[from] = append(evidencesFrom[from], to)
	}

	anomalies := make([]HealthAnomaly, 0)
	for _, item := range evidenceRecords {
		// Status-marked missing is reported separately as missing_evidence.
		if item.record.Status != "available" {
			continue
		}
		valid, err := s.evidenceIntegrityValid(projectID, item.record)
		if err != nil {
			return nil, err
		}
		if valid {
			continue
		}
		related := append([]string(nil), evidencesFrom[item.key]...)
		sort.Strings(related)
		severity := HealthSeverityWarning
		for _, target := range related {
			if isConfirmedConclusion(typeByKey[target], statusByKey[target]) {
				severity = HealthSeverityCritical
				break
			}
		}
		message := "Managed Evidence payload is missing or fails digest verification while status is still available."
		if severity == HealthSeverityCritical {
			message = "Managed Evidence payload is missing or fails digest verification while supporting a confirmed or verified conclusion."
		}
		anomalies = append(anomalies, HealthAnomaly{
			Code:        "evidence_integrity",
			Severity:    severity,
			Message:     message,
			SubjectKey:  item.key,
			RelatedKeys: related,
		})
	}
	return anomalies, nil
}

func isConfirmedConclusion(recordType, status string) bool {
	switch recordType {
	case "fact":
		return status == "confirmed"
	case "finding":
		return status == "confirmed"
	case "solution":
		return status == "verified"
	default:
		return false
	}
}

func relationParts(relation RelationshipTuple) (from, rel, to string, ok bool) {
	if len(relation) < 3 {
		return "", "", "", false
	}
	from, _ = relation[0].(string)
	rel, _ = relation[1].(string)
	to, _ = relation[2].(string)
	if from == "" || rel == "" || to == "" {
		return "", "", "", false
	}
	return from, rel, to, true
}

func redirectIntegrityAnomalies(ctx context.Context, tx *sql.Tx, projectID string) ([]HealthAnomaly, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT source_key, canonical_key
		FROM blackboard_v2_key_redirects
		WHERE project_id = ?
		ORDER BY source_key ASC, canonical_key ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("read Blackboard Key Redirects for health: %w", err)
	}
	defer rows.Close()

	type redirect struct{ source, canonical string }
	redirects := make([]redirect, 0)
	sourceSet := map[string]bool{}
	for rows.Next() {
		var item redirect
		if err := rows.Scan(&item.source, &item.canonical); err != nil {
			return nil, fmt.Errorf("scan Blackboard Key Redirect for health: %w", err)
		}
		redirects = append(redirects, item)
		sourceSet[item.source] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Blackboard Key Redirects for health: %w", err)
	}

	anomalies := make([]HealthAnomaly, 0)
	for _, item := range redirects {
		var canonicalExists int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM blackboard_v2_records WHERE project_id = ? AND key = ?
			)`,
			projectID, item.canonical,
		).Scan(&canonicalExists); err != nil {
			return nil, fmt.Errorf("check redirect canonical for health: %w", err)
		}
		if canonicalExists == 0 {
			anomalies = append(anomalies, HealthAnomaly{
				Code:        "redirect_integrity",
				Severity:    HealthSeverityCritical,
				Message:     "Blackboard Key Redirect points at a missing canonical record.",
				SubjectKey:  item.source,
				RelatedKeys: []string{item.canonical},
			})
			continue
		}
		if sourceSet[item.canonical] {
			anomalies = append(anomalies, HealthAnomaly{
				Code:        "redirect_integrity",
				Severity:    HealthSeverityCritical,
				Message:     "Blackboard Key Redirect forms a chain instead of a single hop.",
				SubjectKey:  item.source,
				RelatedKeys: []string{item.canonical},
			})
		}
		var sourceExists int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM blackboard_v2_records WHERE project_id = ? AND key = ?
			)`,
			projectID, item.source,
		).Scan(&sourceExists); err != nil {
			return nil, fmt.Errorf("check redirect source for health: %w", err)
		}
		if sourceExists != 0 {
			anomalies = append(anomalies, HealthAnomaly{
				Code:        "redirect_integrity",
				Severity:    HealthSeverityCritical,
				Message:     "Blackboard Key Redirect source still has a current record.",
				SubjectKey:  item.source,
				RelatedKeys: []string{item.canonical},
			})
		}
	}
	return anomalies, nil
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
