package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// CTFSolvedState is derived from current verified flag Solutions.
type CTFSolvedState struct {
	Solved        bool     `json:"solved"`
	VerifiedFlags []string `json:"verified_flags"`
}

// CTFSolvedState derives the reversible solved state without persisting a flag.
func (s *Service) CTFSolvedState(ctx context.Context, projectID string) (CTFSolvedState, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CTFSolvedState{}, fmt.Errorf("begin CTF solved-state read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureCTFProject(ctx, tx, projectID, "project"); err != nil {
		return CTFSolvedState{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT key FROM blackboard_v2_records
		WHERE project_id = ? AND type = 'solution'
		  AND json_extract(record_json, '$.status') = 'verified'
		  AND json_extract(record_json, '$.kind') = 'flag'
		ORDER BY key ASC`, projectID)
	if err != nil {
		return CTFSolvedState{}, fmt.Errorf("read verified flag Solutions: %w", err)
	}
	defer rows.Close()
	flags := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return CTFSolvedState{}, fmt.Errorf("scan verified flag Solution: %w", err)
		}
		flags = append(flags, key)
	}
	if err := rows.Err(); err != nil {
		return CTFSolvedState{}, fmt.Errorf("iterate verified flag Solutions: %w", err)
	}
	return CTFSolvedState{Solved: len(flags) > 0, VerifiedFlags: flags}, nil
}

func applyCreateSolution(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := ensureCTFProject(ctx, tx, projectID, path+".type"); err != nil {
		return revision, "", 0, false, err
	}
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeSolutionRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	if !isOneOf(record.Status, "candidate", "verified") {
		return revision, "", 0, false, semanticError("semantic_validation", "Solution creation status must be candidate or verified", path+".record.status", nil)
	}
	if err := validateSolutionRecord(record, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "solution" && existing.record.solutionRecord() == record {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	return insertCurrentWorkRecord(ctx, tx, projectID, revision, change.Key, "solution", record, now)
}

func applyUpdateSolution(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := ensureCTFProject(ctx, tx, projectID, path+".type"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "solution", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	patch, err := partialSolutionRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	current := existing.record.solutionRecord()
	if current.Status != "candidate" && (patch.Kind != nil || patch.Value != nil || patch.VerificationSummary != nil || solutionClearChangesVerification(change.Clear)) {
		return revision, "", 0, false, semanticError("semantic_validation", "verified Solution kind, value, and verification_summary are immutable", path+".record", nil)
	}
	next := current
	if patch.Kind != nil {
		next.Kind = *patch.Kind
	}
	if patch.Summary != nil {
		next.Summary = *patch.Summary
	}
	if patch.Value != nil {
		next.Value = *patch.Value
	}
	if patch.VerificationSummary != nil {
		next.VerificationSummary = *patch.VerificationSummary
	}
	seen := map[string]bool{}
	for _, field := range change.Clear {
		if seen[field] {
			return revision, "", 0, false, semanticError("semantic_validation", "Solution clear fields must be unique", path+".clear", map[string]any{"field": field})
		}
		seen[field] = true
		switch field {
		case "value":
			if patch.Value != nil {
				return revision, "", 0, false, semanticError("semantic_validation", "Solution value cannot be patched and cleared together", path+".clear", map[string]any{"field": field})
			}
			next.Value = ""
		case "verification_summary":
			if patch.VerificationSummary != nil {
				return revision, "", 0, false, semanticError("semantic_validation", "Solution verification_summary cannot be patched and cleared together", path+".clear", map[string]any{"field": field})
			}
			next.VerificationSummary = ""
		default:
			return revision, "", 0, false, semanticError("semantic_validation", "unsupported Solution clear field", path+".clear", map[string]any{"field": field})
		}
	}
	if err := validateSolutionRecord(next, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if existing.record.solutionRecord() == next {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
}

func solutionClearChangesVerification(clear []string) bool {
	for _, field := range clear {
		if field == "value" || field == "verification_summary" {
			return true
		}
	}
	return false
}

func applySolutionTransition(ctx context.Context, tx *sql.Tx, projectID string, revision int, path string, existing storedRecord, change Change, now string) (int, string, int, bool, error) {
	if err := ensureCTFProject(ctx, tx, projectID, path+".status"); err != nil {
		return revision, "", 0, false, err
	}
	current := existing.record.solutionRecord()
	switch change.Status {
	case "verified":
		if current.Status != "candidate" {
			return revision, "", 0, false, semanticError("semantic_validation", "only a candidate Solution can be verified", path+".status", nil)
		}
		next := current
		next.Status = "verified"
		next.VerificationSummary = change.VerificationSummary
		if err := validateSolutionRecord(next, path+".status"); err != nil {
			return revision, "", 0, false, err
		}
		return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
	case "rejected":
		if !isOneOf(current.Status, "candidate", "verified") {
			return revision, "", 0, false, semanticError("semantic_validation", "only a current candidate or verified Solution can be rejected", path+".status", nil)
		}
		if err := validateConciseText(change.VerificationSummary, path+".verification_summary"); err != nil {
			return revision, "", 0, false, err
		}
		hasMeaning, err := hasCurrentSolutionInvalidationMeaning(ctx, tx, projectID, change.Key)
		if err != nil {
			return revision, "", 0, false, err
		}
		if !hasMeaning {
			return revision, "", 0, false, semanticError("semantic_validation", "rejected Solution requires a current contradicting Fact that preserves reusable invalidation meaning", path+".status", nil)
		}
		terminal := current
		terminal.Status = "rejected"
		terminal.VerificationSummary = change.VerificationSummary
		return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "Solution transition status must be verified or rejected", path+".status", nil)
	}
}

func validateAllVerifiedSolutions(ctx context.Context, tx *sql.Tx, projectID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT key, record_json FROM blackboard_v2_records WHERE project_id = ? AND type = 'solution' ORDER BY key`, projectID)
	if err != nil {
		return fmt.Errorf("read current Solutions for final validation: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("scan current Solution: %w", err)
		}
		var record SolutionRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return fmt.Errorf("decode current Solution: %w", err)
		}
		if !isOneOf(record.Status, "candidate", "verified") {
			return semanticError("semantic_validation", "terminal Solution remained in current Project Knowledge", key, nil)
		}
		if err := validateSolutionRecord(record, key); err != nil {
			return err
		}
	}
	return rows.Err()
}

func hasCurrentSolutionInvalidationMeaning(ctx context.Context, tx *sql.Tx, projectID, solutionKey string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.to_key = ? AND rel.relation = 'contradicts' AND source.type = 'fact')`, projectID, solutionKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check Solution invalidation meaning: %w", err)
	}
	return exists == 1, nil
}

func ensureCTFProject(ctx context.Context, tx *sql.Tx, projectID, path string) error {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id = ?`, projectID).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return semanticError("not_found", "Project was not found", path, nil)
		}
		return fmt.Errorf("read Solution Project kind: %w", err)
	}
	if kind != "ctf_challenge" {
		return semanticError("project_kind_mismatch", "Solutions require a CTF Challenge Project", path, nil)
	}
	return nil
}

func decodeSolutionRecord(raw json.RawMessage) (SolutionRecord, error) {
	var record SolutionRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return SolutionRecord{}, err
	}
	return record, nil
}

func decodeSolutionPatch(raw json.RawMessage) (SolutionPatch, error) {
	var patch SolutionPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return SolutionPatch{}, err
	}
	return patch, nil
}

func completeSolutionRecord(value any, path string) (SolutionRecord, error) {
	switch record := value.(type) {
	case SolutionRecord:
		return record, nil
	case *SolutionRecord:
		if record == nil {
			return SolutionRecord{}, semanticError("semantic_validation", "Solution record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeSolutionRecord(record)
		if err != nil {
			return SolutionRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return SolutionRecord{}, semanticError("semantic_validation", "Solution record has invalid shape", path, nil)
		}
		decoded, err := decodeSolutionRecord(raw)
		if err != nil {
			return SolutionRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	}
}

func partialSolutionRecord(value any, path string) (SolutionPatch, error) {
	var patch SolutionPatch
	switch record := value.(type) {
	case SolutionPatch:
		patch = record
	case *SolutionPatch:
		if record == nil {
			return SolutionPatch{}, semanticError("semantic_validation", "Solution update requires a record", path, nil)
		}
		patch = *record
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return SolutionPatch{}, semanticError("semantic_validation", "Solution update has invalid shape", path, nil)
		}
		decoded, err := decodeSolutionPatch(raw)
		if err != nil {
			return SolutionPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		patch = decoded
	}
	if solutionPatchEmpty(patch) {
		return SolutionPatch{}, semanticError("semantic_validation", "Solution update requires at least one field", path, nil)
	}
	if patch.Kind != nil && strings.TrimSpace(*patch.Kind) == "" {
		return SolutionPatch{}, semanticError("semantic_validation", "Solution kind patch must be non-empty", path+".kind", nil)
	}
	if patch.Summary != nil {
		if err := validateSemanticText(*patch.Summary, path+".summary"); err != nil {
			return SolutionPatch{}, err
		}
	}
	if patch.Value != nil {
		if *patch.Value == "" || !utf8.ValidString(*patch.Value) || *patch.Value != strings.TrimSpace(*patch.Value) {
			return SolutionPatch{}, semanticError("semantic_validation", "Solution value patch must be non-empty valid UTF-8 without surrounding whitespace", path+".value", nil)
		}
	}
	if patch.VerificationSummary != nil {
		if err := validateConciseText(*patch.VerificationSummary, path+".verification_summary"); err != nil {
			return SolutionPatch{}, err
		}
	}
	return patch, nil
}

func solutionPatchEmpty(patch SolutionPatch) bool {
	return patch.Kind == nil && patch.Summary == nil && patch.Value == nil && patch.VerificationSummary == nil
}

func validateSolutionRecord(record SolutionRecord, path string) error {
	if !isOneOf(record.Status, "candidate", "verified", "rejected", "superseded") {
		return semanticError("semantic_validation", "Solution status must be candidate or verified", path+".status", nil)
	}
	if !isOneOf(record.Kind, "answer", "flag", "procedure") {
		return semanticError("semantic_validation", "Solution kind must be answer, flag, or procedure", path+".kind", nil)
	}
	if err := validateSemanticText(record.Summary, path+".summary"); err != nil {
		return err
	}
	if record.Value != "" && (!utf8.ValidString(record.Value) || record.Value != strings.TrimSpace(record.Value)) {
		return semanticError("semantic_validation", "Solution value must be valid UTF-8 without surrounding whitespace", path+".value", nil)
	}
	if record.VerificationSummary != "" {
		if err := validateConciseText(record.VerificationSummary, path+".verification_summary"); err != nil {
			return err
		}
	}
	if record.Status == "verified" {
		if isOneOf(record.Kind, "answer", "flag") && record.Value == "" {
			return semanticError("semantic_validation", "verified answer or flag Solution requires value", path+".value", nil)
		}
		if record.VerificationSummary == "" {
			return semanticError("semantic_validation", "verified Solution requires verification_summary", path+".verification_summary", nil)
		}
	}
	return nil
}

func recordFromSolution(record SolutionRecord) Record {
	return Record{Status: record.Status, Kind: record.Kind, Summary: record.Summary, Value: record.Value, VerificationSummary: record.VerificationSummary}
}

func (record Record) solutionRecord() SolutionRecord {
	return SolutionRecord{Status: record.Status, Kind: record.Kind, Summary: record.Summary, Value: record.Value, VerificationSummary: record.VerificationSummary}
}
