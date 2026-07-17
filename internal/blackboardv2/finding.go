package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	gocvss31 "github.com/pandatix/go-cvss/31"
	gocvss40 "github.com/pandatix/go-cvss/40"
)

// findingOutputRecord is the server-owned persisted/detail shape. Callers use
// FindingRecord, which intentionally cannot express derived fields.
type findingOutputRecord struct {
	Status            string `json:"status"`
	Title             string `json:"title"`
	Target            string `json:"target,omitempty"`
	Description       string `json:"description,omitempty"`
	Proof             string `json:"proof,omitempty"`
	Impact            string `json:"impact,omitempty"`
	Recommendation    string `json:"recommendation,omitempty"`
	CVSSVersion       string `json:"cvss_version,omitempty"`
	CVSSVector        string `json:"cvss_vector,omitempty"`
	Severity          string `json:"severity,omitempty"`
	CVSSPending       bool   `json:"cvss_pending"`
	ResolutionSummary string `json:"resolution_summary,omitempty"`
}

func applyCreateFinding(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := ensurePentestProject(ctx, tx, projectID, path+".type"); err != nil {
		return revision, "", 0, false, err
	}
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	input, err := completeFindingRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	record, err := deriveFindingRecord(input, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "finding" && findingsEqual(existing.record.findingOutputRecord(), record) {
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
	return insertCurrentWorkRecord(ctx, tx, projectID, revision, change.Key, "finding", record, now)
}

func applyUpdateFinding(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "finding", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	patch, err := partialFindingRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	next, err := applyFindingPatch(existing.record.findingOutputRecord(), patch, change.Clear, path+".clear")
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateFindingOutputRecord(next, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if findingsEqual(existing.record.findingOutputRecord(), next) {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
}

func deriveFindingRecord(input FindingRecord, path string) (findingOutputRecord, error) {
	if !isOneOf(input.Status, "unconfirmed", "confirmed") {
		return findingOutputRecord{}, semanticError("semantic_validation", "Finding create status must be unconfirmed or confirmed", path+".status", nil)
	}
	record := findingOutputRecord{
		Status: input.Status, Title: input.Title, Target: input.Target, Description: input.Description,
		Proof: input.Proof, Impact: input.Impact, Recommendation: input.Recommendation,
		CVSSVersion: input.CVSSVersion, CVSSVector: input.CVSSVector,
	}
	severity, pending, err := scoreFindingCVSS(record.CVSSVersion, record.CVSSVector)
	if err != nil {
		return findingOutputRecord{}, semanticError("semantic_validation", err.Error(), path+".cvss_vector", nil)
	}
	record.Severity = severity
	record.CVSSPending = pending
	if err := validateFindingOutputRecord(record, path); err != nil {
		return findingOutputRecord{}, err
	}
	return record, nil
}

func applyFindingPatch(existing findingOutputRecord, patch FindingPatch, clear []string, path string) (findingOutputRecord, error) {
	next := existing
	if patch.Title != nil {
		next.Title = *patch.Title
	}
	if patch.Target != nil {
		next.Target = *patch.Target
	}
	if patch.Description != nil {
		next.Description = *patch.Description
	}
	if patch.Proof != nil {
		next.Proof = *patch.Proof
	}
	if patch.Impact != nil {
		next.Impact = *patch.Impact
	}
	if patch.Recommendation != nil {
		next.Recommendation = *patch.Recommendation
	}
	if patch.CVSSVersion != nil {
		next.CVSSVersion = *patch.CVSSVersion
	}
	if patch.CVSSVector != nil {
		next.CVSSVector = *patch.CVSSVector
	}
	seen := make(map[string]bool, len(clear))
	for _, field := range clear {
		if seen[field] {
			return findingOutputRecord{}, semanticError("semantic_validation", "Finding clear fields must be unique", path, map[string]any{"field": field})
		}
		seen[field] = true
		switch field {
		case "target":
			next.Target = ""
		case "description":
			next.Description = ""
		case "proof":
			next.Proof = ""
		case "impact":
			next.Impact = ""
		case "recommendation":
			next.Recommendation = ""
		case "cvss_version":
			next.CVSSVersion = ""
		case "cvss_vector":
			next.CVSSVector = ""
		default:
			return findingOutputRecord{}, semanticError("semantic_validation", "unsupported Finding clear field", path, map[string]any{"field": field})
		}
	}
	severity, pending, err := scoreFindingCVSS(next.CVSSVersion, next.CVSSVector)
	if err != nil {
		return findingOutputRecord{}, semanticError("semantic_validation", err.Error(), strings.TrimSuffix(path, ".clear")+".record.cvss_vector", nil)
	}
	next.Severity = severity
	next.CVSSPending = pending
	return next, nil
}

func validateFindingOutputRecord(record findingOutputRecord, path string) error {
	if !isOneOf(record.Status, "unconfirmed", "confirmed", "false_positive", "superseded") {
		return semanticError("semantic_validation", "Finding status must be unconfirmed or confirmed", path+".status", nil)
	}
	if err := validateConciseText(record.Title, path+".title"); err != nil {
		return err
	}
	if record.Target != "" {
		if err := validateConciseText(record.Target, path+".target"); err != nil {
			return err
		}
	}
	if record.Description != "" {
		if err := validateSemanticText(record.Description, path+".description"); err != nil {
			return err
		}
	}
	for _, field := range []struct{ name, value string }{
		{name: "proof", value: record.Proof},
		{name: "impact", value: record.Impact},
		{name: "recommendation", value: record.Recommendation},
	} {
		if field.value != "" && (!utf8.ValidString(field.value) || strings.TrimSpace(field.value) == "") {
			return semanticError("semantic_validation", "Finding report detail must be non-empty valid UTF-8", path+"."+field.name, nil)
		}
	}
	severity, pending, err := scoreFindingCVSS(record.CVSSVersion, record.CVSSVector)
	if err != nil {
		return semanticError("semantic_validation", err.Error(), path+".cvss_vector", nil)
	}
	if record.Severity != severity || record.CVSSPending != pending {
		return semanticError("semantic_validation", "Finding scoring fields must be server-derived", path, nil)
	}
	if record.Status == "confirmed" {
		if strings.TrimSpace(record.Target) == "" || strings.TrimSpace(record.Proof) == "" || strings.TrimSpace(record.Impact) == "" || strings.TrimSpace(record.Recommendation) == "" {
			return semanticError("semantic_validation", "confirmed Finding requires target, proof, impact, and recommendation", path+".status", nil)
		}
		if pending {
			return semanticError("semantic_validation", "confirmed Finding requires a complete valid CVSS vector", path+".cvss_vector", nil)
		}
	}
	return nil
}

func scoreFindingCVSS(version, vector string) (string, bool, error) {
	if version != strings.TrimSpace(version) || vector != strings.TrimSpace(vector) {
		return "", true, fmt.Errorf("CVSS fields must not contain surrounding whitespace")
	}
	if version == "" && vector == "" {
		return "", true, nil
	}
	if version == "" || vector == "" {
		return "", true, fmt.Errorf("cvss_version and cvss_vector must be provided or cleared together")
	}
	var rating string
	switch version {
	case "4.0":
		parsed, err := gocvss40.ParseVector(vector)
		if err != nil {
			return "", true, fmt.Errorf("CVSS v4.0 vector is incomplete or invalid: %w", err)
		}
		rating, err = gocvss40.Rating(parsed.Score())
		if err != nil {
			return "", true, fmt.Errorf("derive CVSS v4.0 severity: %w", err)
		}
	case "3.1":
		parsed, err := gocvss31.ParseVector(vector)
		if err != nil {
			return "", true, fmt.Errorf("CVSS v3.1 vector is incomplete or invalid: %w", err)
		}
		rating, err = gocvss31.Rating(parsed.EnvironmentalScore())
		if err != nil {
			return "", true, fmt.Errorf("derive CVSS v3.1 severity: %w", err)
		}
	default:
		return "", true, fmt.Errorf("cvss_version must be 3.1 or 4.0")
	}
	return strings.ToLower(rating), false, nil
}

func validateAllConfirmedFindings(ctx context.Context, tx *sql.Tx, projectID string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT key, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type = 'finding'
		ORDER BY key ASC`, projectID)
	if err != nil {
		return fmt.Errorf("read current Findings for final validation: %w", err)
	}
	defer rows.Close()
	type currentFinding struct {
		key    string
		record findingOutputRecord
	}
	current := make([]currentFinding, 0)
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("scan current Finding for final validation: %w", err)
		}
		var record findingOutputRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return fmt.Errorf("decode current Finding for final validation: %w", err)
		}
		if !isOneOf(record.Status, "unconfirmed", "confirmed") {
			rows.Close()
			return semanticError("semantic_validation", "terminal Finding remained in current Project Knowledge", key, map[string]any{"key": key, "next_action": "repair_finding_lifecycle"})
		}
		if err := validateFindingOutputRecord(record, key); err != nil {
			rows.Close()
			return err
		}
		current = append(current, currentFinding{key: key, record: record})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate current Findings for final validation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close current Findings for final validation: %w", err)
	}
	for _, finding := range current {
		if finding.record.Status != "confirmed" {
			continue
		}
		hasSupport, err := findingHasCurrentSupport(ctx, tx, projectID, finding.key)
		if err != nil {
			return err
		}
		if !hasSupport {
			return semanticError("semantic_validation", "confirmed Finding requires current Evidence or a confirmed supporting Fact", finding.key, map[string]any{"key": finding.key, "next_action": "restore_finding_support"})
		}
	}
	return nil
}

func findingHasCurrentSupport(ctx context.Context, tx *sql.Tx, projectID, findingKey string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT source.type, source.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.to_key = ? AND rel.relation IN ('supports', 'evidences')
		ORDER BY rel.relation ASC, source.key ASC`, projectID, findingKey)
	if err != nil {
		return false, fmt.Errorf("read current Finding support: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, raw string
		if err := rows.Scan(&typ, &raw); err != nil {
			return false, fmt.Errorf("scan current Finding support: %w", err)
		}
		switch typ {
		case "fact":
			var fact FactRecord
			if err := json.Unmarshal([]byte(raw), &fact); err != nil {
				return false, fmt.Errorf("decode supporting Fact: %w", err)
			}
			if fact.Confidence == "confirmed" {
				return true, nil
			}
		case "evidence":
			var evidence EvidenceRecord
			if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
				return false, fmt.Errorf("decode supporting Evidence: %w", err)
			}
			if evidence.Status == "available" {
				return true, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate current Finding support: %w", err)
	}
	return false, nil
}

func hasCurrentFindingInvalidationMeaning(ctx context.Context, tx *sql.Tx, projectID, findingKey string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM blackboard_v2_relationships AS rel
			JOIN blackboard_v2_records AS source
			  ON source.project_id = rel.project_id AND source.key = rel.from_key
			WHERE rel.project_id = ? AND rel.to_key = ? AND rel.relation = 'contradicts' AND source.type = 'fact'
		)`, projectID, findingKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check Finding invalidation meaning: %w", err)
	}
	return exists == 1, nil
}

func findingsEqual(a, b findingOutputRecord) bool {
	return a == b
}

func ensurePentestProject(ctx context.Context, tx *sql.Tx, projectID, path string) error {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id = ?`, projectID).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return semanticError("not_found", "Project was not found", path, nil)
		}
		return fmt.Errorf("read Finding Project kind: %w", err)
	}
	if kind != "pentest" {
		return semanticError("project_kind_mismatch", "Findings require a Pentest Project", path, nil)
	}
	return nil
}
