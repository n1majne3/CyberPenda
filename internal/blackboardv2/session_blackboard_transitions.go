package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"pentest/internal/blackboardv2grammar"
)

func sessionApplyTransition(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if err != nil {
		return revision, "", 0, false, err
	}
	if change.Version != existing.version {
		return revision, "", 0, false, sessionVersionConflict(path+".version", change.Key, change.Version, existing.version)
	}
	var terminal any
	switch existing.typ {
	case "entity":
		if change.Status != "retired" {
			return revision, "", 0, false, semanticError("semantic_validation", "Entity transition status must be retired", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		next := existing.record.entityRecord()
		next.Status = "retired"
		terminal = next
	case "objective":
		if change.Status != "resolved" && change.Status != "abandoned" {
			return revision, "", 0, false, semanticError("semantic_validation", "Objective transition status must be resolved or abandoned", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		if change.Status == "resolved" {
			satisfied, err := sessionHasIncomingSatisfies(ctx, tx, sessionID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !satisfied {
				return revision, "", 0, false, semanticError("semantic_validation", "resolved Objective requires a current incoming satisfies relationship", path+".status", nil)
			}
		}
		if change.Status == "abandoned" {
			invalidated, err := sessionHasIncomingInvalidates(ctx, tx, sessionID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !invalidated {
				return revision, "", 0, false, semanticError("semantic_validation", "abandoned Objective requires a current incoming invalidates relationship", path+".status", nil)
			}
		}
		next := existing.record.objectiveRecord()
		next.Status, next.ResolutionSummary = change.Status, change.ResolutionSummary
		terminal = next
	case "attempt":
		if !isOneOf(change.Status, "succeeded", "failed", "blocked", "inconclusive") {
			return revision, "", 0, false, semanticError("semantic_validation", "Attempt transition status is not terminal", path+".status", nil)
		}
		if err := validateSemanticText(change.Summary, path+".summary"); err != nil {
			return revision, "", 0, false, err
		}
		next := existing.record.attemptRecord()
		next.Status, next.Summary = change.Status, change.Summary
		terminal = next
	case "fact":
		if change.Status != "tentative" && change.Status != "confirmed" {
			return revision, "", 0, false, semanticError("semantic_validation", "Fact transition status must be tentative or confirmed", path+".status", nil)
		}
		next := existing.record.factRecord()
		if next.Confidence == change.Status {
			return revision, change.Key, existing.version, false, nil
		}
		next.Confidence = change.Status
		if err := validateSessionFactRecord(next, path+".status"); err != nil {
			return revision, "", 0, false, err
		}
		return sessionReplaceCurrentRecord(ctx, tx, sessionID, revision, existing, next, now)
	default:
		return revision, "", 0, false, sessionCapabilityError(path+".key", "Project-only Blackboard record type is unavailable to a Session")
	}
	return sessionTerminalizeRecord(ctx, tx, sessionID, revision, existing, terminal, now)
}

func sessionApplySupersede(ctx context.Context, tx *sql.Tx, sessionID string, revision, index int, change Change, createdThisBatch map[string]bool, now string) (int, string, int, RelationVersionTuple, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Replacement, path+".replacement"); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if err := validateKey(change.Replaced, path+".replaced"); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if change.Replacement == change.Replaced {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "supersede requires different replacement and replaced records", path+".replaced", nil)
	}
	replacement, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Replacement)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replacement), path+".replacement", map[string]any{"key": change.Replacement})
	}
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	replaced, err := loadSessionCurrentRecord(ctx, tx, sessionID, change.Replaced)
	if errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replaced), path+".replaced", map[string]any{"key": change.Replaced})
	}
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	rule, _ := blackboardv2grammar.Lookup("supersedes")
	if !rule.Allows(replacement.typ, replaced.typ) || !isOneOf(replacement.typ, "entity", "objective", "fact") {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "Session supersede requires two current records of the same supported type", path, nil)
	}
	replacementVersion := change.ReplacementVersion
	if replacementVersion == 0 {
		if !createdThisBatch[change.Replacement] || replacement.version != 1 {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "replacement_version may be omitted only for a version 1 replacement created earlier in the same batch", path+".replacement_version", nil)
		}
		replacementVersion = 1
	}
	if replacementVersion != replacement.version {
		return revision, "", 0, RelationVersionTuple{}, false, sessionVersionConflict(path+".replacement_version", change.Replacement, replacementVersion, replacement.version)
	}
	if change.ReplacedVersion != replaced.version {
		return revision, "", 0, RelationVersionTuple{}, false, sessionVersionConflict(path+".replaced_version", change.Replaced, change.ReplacedVersion, replaced.version)
	}
	var terminal any
	switch replaced.typ {
	case "entity":
		next := replaced.record.entityRecord()
		next.Status = "superseded"
		terminal = next
	case "objective":
		next := replaced.record.objectiveRecord()
		next.Status = "superseded"
		terminal = next
	case "fact":
		next := replaced.record.factRecord()
		// Session Facts retain their Session-local semantic identity while the
		// terminal history version records that the fact was deprecated. The
		// terminal history shape is shared with Project Facts; it is never a
		// current Session record and therefore bypasses current-record validation.
		next.Confidence = "deprecated"
		terminal = next
	}
	nextRevision, key, nextVersion, changed, err := sessionTerminalizeRecord(ctx, tx, sessionID, revision, replaced, terminal, now)
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_session_relationship_history
		(session_id, from_key, relation, to_key, version, reason, recorded_at)
		VALUES (?, ?, 'supersedes', ?, 1, '', ?)`, sessionID, change.Replacement, change.Replaced, now); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("store Session supersedes history: %w", err)
	}
	return nextRevision, key, nextVersion, RelationVersionTuple{change.Replacement, "supersedes", change.Replaced, 1}, changed, nil
}

func sessionTerminalizeRecord(ctx context.Context, tx *sql.Tx, sessionID string, revision int, existing storedRecord, terminal any, now string) (int, string, int, bool, error) {
	currentJSON, err := json.Marshal(existing.record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode current Session %s: %w", existing.typ, err)
	}
	terminalJSON, err := json.Marshal(terminal)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode terminal Session %s: %w", existing.typ, err)
	}
	nextVersion := existing.version + 1
	nextRevision, err := sessionIncrementRevision(ctx, tx, sessionID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	for version, encoded := range map[int]string{existing.version: string(currentJSON), nextVersion: string(terminalJSON)} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_session_record_history
			(session_id, key, version, type, record_json, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?)`, sessionID, existing.key, version, existing.typ, encoded, now); err != nil {
			return revision, "", 0, false, fmt.Errorf("store terminal Session %s history: %w", existing.typ, err)
		}
	}
	if err := sessionMoveCurrentRelationshipsToHistory(ctx, tx, sessionID, existing.key, now); err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blackboard_v2_session_records WHERE session_id=? AND key=?`, sessionID, existing.key); err != nil {
		return revision, "", 0, false, fmt.Errorf("remove terminal Session %s: %w", existing.typ, err)
	}
	return nextRevision, existing.key, nextVersion, true, nil
}

func sessionValidateFinalTerminalAttempts(ctx context.Context, tx *sql.Tx, sessionID string, attempts map[string]terminalAttemptValidation) error {
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		validation := attempts[key]
		var tested int
		if err := tx.QueryRowContext(ctx, `
			SELECT (SELECT COUNT(*) FROM blackboard_v2_session_relationships WHERE session_id=? AND from_key=? AND relation='tests') +
			       (SELECT COUNT(*) FROM blackboard_v2_session_relationship_history WHERE session_id=? AND from_key=? AND relation='tests')`, sessionID, key, sessionID, key).Scan(&tested); err != nil {
			return fmt.Errorf("count Session tested targets: %w", err)
		}
		if tested == 0 {
			return semanticError("semantic_validation", "terminal Attempt requires at least one tested target", validation.path, map[string]any{"key": key})
		}
		if validation.status != "succeeded" {
			continue
		}
		var outcomes []string
		rows, err := tx.QueryContext(ctx, `
			SELECT to_key FROM blackboard_v2_session_relationships WHERE session_id=? AND from_key=? AND relation='produced'
			UNION SELECT to_key FROM blackboard_v2_session_relationship_history WHERE session_id=? AND from_key=? AND relation='produced'
			ORDER BY to_key ASC`, sessionID, key, sessionID, key)
		if err != nil {
			return fmt.Errorf("read Session produced outcomes: %w", err)
		}
		for rows.Next() {
			var outcome string
			if err := rows.Scan(&outcome); err != nil {
				rows.Close()
				return fmt.Errorf("scan Session produced outcome: %w", err)
			}
			outcomes = append(outcomes, outcome)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate Session produced outcomes: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close Session produced outcomes: %w", err)
		}
		found := false
		for _, outcome := range outcomes {
			if _, err := loadSessionCurrentRecord(ctx, tx, sessionID, outcome); err == nil {
				found = true
				break
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			replacement, err := sessionProducedOutcomeHasCurrentReplacement(ctx, tx, sessionID, outcome)
			if err != nil {
				return err
			}
			if replacement {
				found = true
				break
			}
		}
		if !found {
			return semanticError("semantic_validation", "succeeded Attempt requires a current produced outcome", validation.path, map[string]any{"key": key})
		}
	}
	return nil
}

func sessionProducedOutcomeHasCurrentReplacement(ctx context.Context, tx *sql.Tx, sessionID, outcome string) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM blackboard_v2_session_relationship_history AS supersession
			JOIN blackboard_v2_session_records AS replacement
			  ON replacement.session_id = supersession.session_id AND replacement.key = supersession.from_key
			JOIN blackboard_v2_session_record_history AS replaced
			  ON replaced.session_id = supersession.session_id AND replaced.key = supersession.to_key
			WHERE supersession.session_id = ? AND supersession.relation = 'supersedes' AND supersession.to_key = ?
			  AND replacement.type = replaced.type
		)`, sessionID, outcome,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check Session produced outcome replacement: %w", err)
	}
	return exists != 0, nil
}
