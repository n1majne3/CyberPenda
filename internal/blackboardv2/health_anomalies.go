package blackboardv2

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"pentest/internal/blackboardv2grammar"
)

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
			Message:  fmt.Sprintf("Runtime Snapshot reached the 32K warning threshold (%d estimated tokens). Review and consolidate the Working Graph before it grows further; do not truncate or auto-merge. %s", projection.EstimatedTokens, completeNote),
		}}
	case AttentionRequired:
		return []HealthAnomaly{{
			Code:     "attention_required",
			Severity: HealthSeverityCritical,
			Message:  fmt.Sprintf("Runtime Snapshot reached the 64K consolidation-required threshold (%d estimated tokens). Review and consolidate the Working Graph before the next run; do not truncate or auto-merge. %s", projection.EstimatedTokens, completeNote),
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
		fromType, fromOK := typeByKey[from]
		toType, toOK := typeByKey[to]
		if !fromOK || !toOK {
			// Dangling edges are reported by relationshipIntegrityAnomalies with
			// surviving-key preference; skip duplicate emission here.
			continue
		}
		if rule, known := blackboardv2grammar.Lookup(rel); !known || !rule.Allows(fromType, toType) {
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
