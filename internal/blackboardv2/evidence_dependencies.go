package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

func collectEvidenceDependentConfirmedFacts(ctx context.Context, tx *sql.Tx, projectID, evidenceKey, path string, dependent map[string]string) error {
	record, err := loadCurrentRecord(ctx, tx, projectID, evidenceKey)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && record.typ != "evidence") {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT fact.key, fact.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS fact
		  ON fact.project_id=rel.project_id AND fact.key=rel.to_key AND fact.type='fact'
		WHERE rel.project_id=? AND rel.from_key=? AND rel.relation='evidences'
		ORDER BY fact.key`, projectID, evidenceKey)
	if err != nil {
		return fmt.Errorf("read Evidence-dependent Facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("scan Evidence-dependent Fact: %w", err)
		}
		var fact FactRecord
		if err := decodeJSON([]byte(raw), &fact); err != nil {
			return fmt.Errorf("decode Evidence-dependent Fact: %w", err)
		}
		if fact.Confidence == "confirmed" {
			dependent[key] = path
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Evidence-dependent Facts: %w", err)
	}
	return nil
}

func collectSupportingFactDependentConfirmedFacts(ctx context.Context, tx *sql.Tx, projectID, supportKey, path string, dependent map[string]string) error {
	record, err := loadCurrentRecord(ctx, tx, projectID, supportKey)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && record.typ != "fact") {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT target.key, target.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS target
		  ON target.project_id=rel.project_id AND target.key=rel.to_key AND target.type='fact'
		WHERE rel.project_id=? AND rel.from_key=? AND rel.relation='supports'
		ORDER BY target.key`, projectID, supportKey)
	if err != nil {
		return fmt.Errorf("read supporting-Fact-dependent Facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("scan supporting-Fact-dependent Fact: %w", err)
		}
		var fact FactRecord
		if err := decodeJSON([]byte(raw), &fact); err != nil {
			return fmt.Errorf("decode supporting-Fact-dependent Fact: %w", err)
		}
		if fact.Confidence == "confirmed" {
			dependent[key] = path
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate supporting-Fact-dependent Facts: %w", err)
	}
	return nil
}

func (s *Service) validateDependentConfirmedFactBases(ctx context.Context, tx *sql.Tx, projectID string, dependent map[string]string) error {
	keys := make([]string, 0, len(dependent))
	for key := range dependent {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fact, err := loadCurrentRecord(ctx, tx, projectID, key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if fact.typ != "fact" || fact.record.factRecord().Confidence != "confirmed" {
			continue
		}
		valid, err := s.factHasDurableBasis(ctx, tx, projectID, key)
		if err != nil {
			return err
		}
		if !valid {
			return semanticError("semantic_validation", "relationship or lifecycle change would leave a confirmed Fact without a valid basis", dependent[key], map[string]any{"fact": key})
		}
	}
	return nil
}

func (s *Service) factHasDurableBasis(ctx context.Context, tx *sql.Tx, projectID, factKey string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT evidence.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS evidence
		  ON evidence.project_id=rel.project_id AND evidence.key=rel.from_key AND evidence.type='evidence'
		WHERE rel.project_id=? AND rel.relation='evidences' AND rel.to_key=?
		ORDER BY evidence.key`, projectID, factKey)
	if err != nil {
		return false, fmt.Errorf("read durable Evidence bases: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan durable Evidence basis: %w", err)
		}
		var evidence EvidenceRecord
		if err := decodeJSON([]byte(raw), &evidence); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode durable Evidence basis: %w", err)
		}
		valid, err := s.evidenceIntegrityValid(projectID, evidence)
		if err != nil {
			rows.Close()
			return false, err
		}
		if valid {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("iterate durable Evidence bases: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close durable Evidence bases: %w", err)
	}
	var confirmedSupport int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS support
		  ON support.project_id=rel.project_id AND support.key=rel.from_key AND support.type='fact'
		WHERE rel.project_id=? AND rel.relation='supports' AND rel.to_key=?
		  AND json_extract(support.record_json,'$.confidence')='confirmed'`, projectID, factKey).Scan(&confirmedSupport); err != nil {
		return false, fmt.Errorf("read durable confirmed Fact bases: %w", err)
	}
	if confirmedSupport != 0 {
		return true, nil
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT attempt.record_json
		FROM blackboard_v2_relationship_history AS rel
		JOIN blackboard_v2_record_history AS attempt
		  ON attempt.project_id=rel.project_id AND attempt.key=rel.from_key AND attempt.type='attempt'
		WHERE rel.project_id=? AND rel.relation='produced' AND rel.to_key=?
		ORDER BY attempt.key, attempt.version`, projectID, factKey)
	if err != nil {
		return false, fmt.Errorf("read durable producing Attempt bases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, fmt.Errorf("scan durable producing Attempt basis: %w", err)
		}
		var attempt AttemptRecord
		if err := decodeJSON([]byte(raw), &attempt); err != nil {
			return false, fmt.Errorf("decode durable producing Attempt basis: %w", err)
		}
		if attempt.Status == "succeeded" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate durable producing Attempt bases: %w", err)
	}
	return false, nil
}
