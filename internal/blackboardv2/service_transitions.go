package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type terminalAttemptValidation struct {
	status string
	path   string
}

func applyTransition(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
		}
		return revision, "", 0, false, err
	}
	if change.Version != existing.version {
		return revision, "", 0, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			path+".version",
			map[string]any{
				"key":              change.Key,
				"expected_version": float64(change.Version),
				"current_version":  float64(existing.version),
				"next_action":      "read_current_record",
			},
		)
	}

	switch existing.typ {
	case "entity":
		if change.Status != "retired" {
			return revision, "", 0, false, semanticError("semantic_validation", "Entity transition status must be retired", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		terminal := existing.record.entityRecord()
		terminal.Status = "retired"
		return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
	case "objective":
		if change.Status != "resolved" && change.Status != "abandoned" {
			return revision, "", 0, false, semanticError("semantic_validation", "Objective transition status must be resolved or abandoned", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		if change.Status == "resolved" {
			satisfied, err := hasIncomingSatisfies(ctx, tx, projectID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !satisfied {
				return revision, "", 0, false, semanticError("semantic_validation", "resolved Objective requires a current incoming satisfies relationship", path+".status", nil)
			}
		}
		if change.Status == "abandoned" {
			invalidated, err := hasIncomingInvalidates(ctx, tx, projectID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !invalidated {
				return revision, "", 0, false, semanticError("semantic_validation", "abandoned Objective requires a current incoming invalidates relationship", path+".status", nil)
			}
		}
		terminal := existing.record.objectiveRecord()
		terminal.Status = change.Status
		terminal.ResolutionSummary = change.ResolutionSummary
		return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
	case "attempt":
		if !isOneOf(change.Status, "succeeded", "failed", "blocked", "inconclusive") {
			if change.Status == "interrupted" {
				return revision, "", 0, false, semanticError("semantic_validation", "Runtime interruption is reconciled by the server", path+".status", nil)
			}
			return revision, "", 0, false, semanticError("semantic_validation", "Attempt transition status is not terminal", path+".status", nil)
		}
		if err := validateSemanticText(change.Summary, path+".summary"); err != nil {
			return revision, "", 0, false, err
		}
		terminal := existing.record.attemptRecord()
		terminal.Status = change.Status
		terminal.Summary = change.Summary
		return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
	case "fact":
		if !isOneOf(change.Status, "tentative", "confirmed") {
			return revision, "", 0, false, semanticError("semantic_validation", "Fact transition status must be tentative or confirmed", path+".status", nil)
		}
		current := existing.record.factRecord()
		if current.Confidence == change.Status {
			return revision, change.Key, existing.version, false, nil
		}
		next := current
		next.Confidence = change.Status
		if err := validateFactRecord(next, path+".status"); err != nil {
			return revision, "", 0, false, err
		}
		return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
	case "finding":
		if change.Status == "false_positive" {
			if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
				return revision, "", 0, false, err
			}
			hasMeaning, err := hasCurrentFindingInvalidationMeaning(ctx, tx, projectID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !hasMeaning {
				return revision, "", 0, false, semanticError("semantic_validation", "false-positive Finding requires a current contradicting Fact that preserves reusable invalidation meaning", path+".status", nil)
			}
			terminal := existing.record.findingOutputRecord()
			terminal.Status = "false_positive"
			terminal.ResolutionSummary = change.ResolutionSummary
			return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
		}
		if change.Status != "confirmed" {
			return revision, "", 0, false, semanticError("semantic_validation", "Finding transition status must be confirmed or false_positive", path+".status", nil)
		}
		current := existing.record.findingOutputRecord()
		if current.Status == "confirmed" {
			return revision, change.Key, existing.version, false, nil
		}
		if current.Status != "unconfirmed" {
			return revision, "", 0, false, semanticError("semantic_validation", "only an unconfirmed Finding can be confirmed", path+".status", nil)
		}
		next := current
		next.Status = "confirmed"
		if err := validateFindingOutputRecord(next, path+".status"); err != nil {
			return revision, "", 0, false, err
		}
		return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
	case "solution":
		return applySolutionTransition(ctx, tx, projectID, revision, path, existing, change, now)
	case "evidence":
		if change.Status != "missing" {
			return revision, "", 0, false, semanticError("semantic_validation", "Evidence lifecycle transition must be missing", path+".status", nil)
		}
		if err := validateSemanticText(change.Summary, path+".summary"); err != nil {
			return revision, "", 0, false, err
		}
		current := existing.record.evidenceRecord()
		next := current
		next.Status = "missing"
		next.Summary = change.Summary
		if current.Status == next.Status && current.Summary == next.Summary {
			return revision, change.Key, existing.version, false, nil
		}
		return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "record type does not support this transition", path+".key", map[string]any{"key": change.Key, "type": existing.typ})
	}
}

func (s *Service) runtimeFactConfirmationHasBasis(ctx context.Context, tx *sql.Tx, projectID, continuationID, factKey string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT source.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.relation = 'supports' AND rel.to_key = ? AND source.type = 'fact'
		ORDER BY source.key ASC`,
		projectID, factKey,
	)
	if err != nil {
		return false, fmt.Errorf("read supporting Facts for confirmation: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan supporting Fact for confirmation: %w", err)
		}
		var source FactRecord
		if err := decodeJSON([]byte(raw), &source); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode supporting Fact for confirmation: %w", err)
		}
		if source.Confidence == "confirmed" {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("iterate supporting Facts for confirmation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close supporting Facts for confirmation: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT source.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.relation = 'evidences' AND rel.to_key = ? AND source.type = 'evidence'
		ORDER BY source.key ASC`,
		projectID, factKey,
	)
	if err != nil {
		return false, fmt.Errorf("read Evidence for confirmation: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan Evidence for confirmation: %w", err)
		}
		var source EvidenceRecord
		if err := decodeJSON([]byte(raw), &source); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode Evidence for confirmation: %w", err)
		}
		if source.Status == "available" {
			valid, err := s.evidenceIntegrityValid(projectID, source)
			if err != nil {
				rows.Close()
				return false, err
			}
			if valid {
				rows.Close()
				return true, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("iterate Evidence for confirmation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close Evidence for confirmation: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT attempt.record_json, origin.continuation_id
		FROM blackboard_v2_relationship_history AS rel
		JOIN blackboard_v2_attempt_origins AS origin
		  ON origin.project_id = rel.project_id AND origin.key = rel.from_key
		JOIN blackboard_v2_record_history AS attempt
		  ON attempt.project_id = rel.project_id AND attempt.key = rel.from_key AND attempt.type = 'attempt'
		WHERE rel.project_id = ? AND rel.relation = 'produced' AND rel.to_key = ?
		ORDER BY rel.from_key ASC, attempt.version ASC`,
		projectID, factKey,
	)
	if err != nil {
		return false, fmt.Errorf("read producing Attempts for confirmation: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw, originContinuationID string
		if err := rows.Scan(&raw, &originContinuationID); err != nil {
			return false, fmt.Errorf("scan producing Attempt for confirmation: %w", err)
		}
		var attempt AttemptRecord
		if err := decodeJSON([]byte(raw), &attempt); err != nil {
			return false, fmt.Errorf("decode producing Attempt for confirmation: %w", err)
		}
		if originContinuationID == continuationID && attempt.Status == "succeeded" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate producing Attempts for confirmation: %w", err)
	}
	return false, nil
}

func validateFinalTerminalAttempts(ctx context.Context, tx *sql.Tx, projectID string, attempts map[string]terminalAttemptValidation) error {
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		validation := attempts[key]
		currentTests, err := currentOutgoingRelationshipCount(ctx, tx, projectID, key, "tests")
		if err != nil {
			return err
		}
		historicalTests, err := historicalOutgoingRelationshipCount(ctx, tx, projectID, key, "tests")
		if err != nil {
			return err
		}
		if currentTests+historicalTests == 0 {
			return semanticError("semantic_validation", "terminal Attempt requires at least one tested target", validation.path, map[string]any{"key": key})
		}
		if validation.status != "succeeded" {
			continue
		}
		hasReusableOutcome, err := succeededAttemptHasReusableProducedOutcome(ctx, tx, projectID, key)
		if err != nil {
			return err
		}
		if !hasReusableOutcome {
			return semanticError("semantic_validation", "succeeded Attempt requires a current produced outcome or current same-type replacement", validation.path, map[string]any{"key": key})
		}
	}
	return nil
}

func succeededAttemptHasReusableProducedOutcome(ctx context.Context, tx *sql.Tx, projectID, attemptKey string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT to_key
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND from_key = ? AND relation = 'produced'
		UNION
		SELECT to_key
		FROM blackboard_v2_relationship_history
		WHERE project_id = ? AND from_key = ? AND relation = 'produced'
		ORDER BY to_key ASC`,
		projectID, attemptKey, projectID, attemptKey,
	)
	if err != nil {
		return false, fmt.Errorf("read succeeded Attempt produced outcomes: %w", err)
	}
	defer rows.Close()
	targets := make([]string, 0)
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return false, fmt.Errorf("scan succeeded Attempt produced outcome: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate succeeded Attempt produced outcomes: %w", err)
	}
	if len(targets) == 0 {
		return false, nil
	}
	for _, target := range targets {
		if _, err := loadCurrentRecord(ctx, tx, projectID, target); err == nil {
			return true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		var replacementExists int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM blackboard_v2_relationship_history AS supersession
				JOIN blackboard_v2_records AS replacement
				  ON replacement.project_id = supersession.project_id AND replacement.key = supersession.from_key
				JOIN blackboard_v2_record_history AS replaced
				  ON replaced.project_id = supersession.project_id AND replaced.key = supersession.to_key
				WHERE supersession.project_id = ? AND supersession.relation = 'supersedes' AND supersession.to_key = ?
				  AND replacement.type = replaced.type
			)`,
			projectID, target,
		).Scan(&replacementExists); err != nil {
			return false, fmt.Errorf("check produced outcome replacement: %w", err)
		}
		if replacementExists != 0 {
			return true, nil
		}
	}
	return false, nil
}

func terminalizeRecord(ctx context.Context, tx *sql.Tx, projectID string, revision int, existing storedRecord, terminal any, now string) (int, string, int, bool, error) {
	currentJSON, err := json.Marshal(existing.record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode current %s record: %w", existing.typ, err)
	}
	terminalJSON, err := json.Marshal(terminal)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode terminal %s record: %w", existing.typ, err)
	}
	nextVersion := existing.version + 1
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, existing.key, existing.version, existing.typ, string(currentJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store current %s history: %w", existing.typ, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, existing.key, nextVersion, existing.typ, string(terminalJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store terminal %s history: %w", existing.typ, err)
	}
	if err := moveCurrentRelationshipsToHistory(ctx, tx, projectID, existing.key, now); err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_records
		WHERE project_id = ? AND key = ?`,
		projectID, existing.key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("remove terminal %s current record: %w", existing.typ, err)
	}
	return nextRevision, existing.key, nextVersion, true, nil
}
