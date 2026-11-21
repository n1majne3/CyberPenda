package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"pentest/internal/blackboardv2grammar"
)

func applySupersede(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, createdThisBatch map[string]bool, now string) (int, string, int, RelationVersionTuple, bool, error) {
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
	replacement, err := loadCurrentRecord(ctx, tx, projectID, change.Replacement)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replacement), path+".replacement", map[string]any{"key": change.Replacement})
		}
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	replaced, err := loadCurrentRecord(ctx, tx, projectID, change.Replaced)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replaced), path+".replaced", map[string]any{"key": change.Replaced})
		}
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	supersedesRule, _ := blackboardv2grammar.Lookup("supersedes")
	if !supersedesRule.Allows(replacement.typ, replaced.typ) {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "supersede requires two current records of the same supersedable type", path, map[string]any{"replacement_type": replacement.typ, "replaced_type": replaced.typ})
	}
	replacementVersion := change.ReplacementVersion
	if replacementVersion == 0 {
		if !createdThisBatch[change.Replacement] || replacement.version != 1 {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "replacement_version may be omitted only for a version 1 replacement created earlier in the same batch", path+".replacement_version", nil)
		}
		replacementVersion = 1
	}
	if replacementVersion != replacement.version {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Replacement),
			path+".replacement_version",
			map[string]any{"key": change.Replacement, "expected_version": float64(replacementVersion), "current_version": float64(replacement.version), "next_action": "read_current_record"},
		)
	}
	if change.ReplacedVersion != replaced.version {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Replaced),
			path+".replaced_version",
			map[string]any{"key": change.Replaced, "expected_version": float64(change.ReplacedVersion), "current_version": float64(replaced.version), "next_action": "read_current_record"},
		)
	}
	var terminal any
	switch replaced.typ {
	case "entity":
		record := replaced.record.entityRecord()
		record.Status = "superseded"
		terminal = record
	case "objective":
		record := replaced.record.objectiveRecord()
		record.Status = "superseded"
		terminal = record
	case "evidence":
		record := replaced.record.evidenceRecord()
		record.Status = "superseded"
		terminal = record
	case "fact":
		record := replaced.record.factRecord()
		record.Confidence = "deprecated"
		terminal = record
	case "finding":
		record := replaced.record.findingOutputRecord()
		record.Status = "superseded"
		terminal = record
	case "solution":
		record := replaced.record.solutionRecord()
		record.Status = "superseded"
		terminal = record
	}
	nextRevision, key, nextVersion, changed, err := terminalizeRecord(ctx, tx, projectID, revision, replaced, terminal, now)
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
		VALUES (?, ?, 'supersedes', ?, 1, '', ?)`,
		projectID, change.Replacement, change.Replaced, now,
	); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("store supersedes relationship history: %w", err)
	}
	tuple := RelationVersionTuple{change.Replacement, "supersedes", change.Replaced, 1}
	return nextRevision, key, nextVersion, tuple, changed, nil
}

func resolveKeyRedirect(ctx context.Context, tx *sql.Tx, projectID, key string) (string, bool, error) {
	var canonical string
	err := tx.QueryRowContext(ctx, `
		SELECT canonical_key
		FROM blackboard_v2_key_redirects
		WHERE project_id = ? AND source_key = ?`,
		projectID, key,
	).Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return key, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve Blackboard v2 key redirect: %w", err)
	}
	var chained int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blackboard_v2_key_redirects
			WHERE project_id = ? AND source_key = ?
		)`, projectID, canonical,
	).Scan(&chained); err != nil {
		return "", false, fmt.Errorf("validate Blackboard v2 key redirect: %w", err)
	}
	if chained != 0 {
		return "", false, fmt.Errorf("invalid Blackboard v2 key redirect chain for %q", key)
	}
	return canonical, true, nil
}

func resolveChangeRedirects(ctx context.Context, tx *sql.Tx, projectID string, change Change) (Change, error) {
	resolve := func(key string) (string, error) {
		canonical, _, err := resolveKeyRedirect(ctx, tx, projectID, key)
		return canonical, err
	}
	var err error
	switch change.Op {
	case "update", "transition":
		change.Key, err = resolve(change.Key)
	case "relate", "unrelate":
		if change.From, err = resolve(change.From); err == nil {
			change.To, err = resolve(change.To)
		}
	case "supersede":
		if change.Replacement, err = resolve(change.Replacement); err == nil {
			change.Replaced, err = resolve(change.Replaced)
		}
	}
	return change, err
}

func historicalKeyExists(ctx context.Context, tx *sql.Tx, projectID, key string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blackboard_v2_record_history WHERE project_id = ? AND key = ?
		)`, projectID, key,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check Blackboard v2 historical key: %w", err)
	}
	return exists == 1, nil
}
