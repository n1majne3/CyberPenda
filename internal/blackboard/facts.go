// Package blackboard owns project-local memory: reusable facts, fact index
// views, and later fact relations/findings/evidence. It stores current fact
// state while keeping the service boundary small enough for HTTP, MCP, and CLI
// interfaces to share.
package blackboard

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/store"
)

type Confidence string

const (
	ConfidenceTentative  Confidence = "tentative"
	ConfidenceConfirmed  Confidence = "confirmed"
	ConfidenceDeprecated Confidence = "deprecated"
)

type ScopeStatus string

const (
	ScopeStatusInScope    ScopeStatus = "in_scope"
	ScopeStatusOutOfScope ScopeStatus = "out_of_scope"
)

type Fact struct {
	ID          string      `json:"id"`
	ProjectID   string      `json:"project_id"`
	FactKey     string      `json:"fact_key"`
	Category    string      `json:"category"`
	Summary     string      `json:"summary"`
	Body        string      `json:"body"`
	Confidence  Confidence  `json:"confidence"`
	ScopeStatus ScopeStatus `json:"scope_status,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type FactIndexEntry struct {
	FactKey     string      `json:"fact_key"`
	Category    string      `json:"category"`
	Summary     string      `json:"summary"`
	Confidence  Confidence  `json:"confidence"`
	ScopeStatus ScopeStatus `json:"scope_status,omitempty"`
}

type FactVersion struct {
	ID          string      `json:"id"`
	ProjectID   string      `json:"project_id"`
	FactKey     string      `json:"fact_key"`
	Version     int         `json:"version"`
	Category    string      `json:"category"`
	Summary     string      `json:"summary"`
	Body        string      `json:"body"`
	Confidence  Confidence  `json:"confidence"`
	ScopeStatus ScopeStatus `json:"scope_status,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
}

type FactRelation struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	SourceFactKey string    `json:"source_fact_key"`
	TargetFactKey string    `json:"target_fact_key"`
	Relation      string    `json:"relation"`
	Summary       string    `json:"summary"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type UpsertFactRequest struct {
	ProjectID   string
	FactKey     string
	Category    string
	Summary     string
	Body        string
	Confidence  Confidence
	ScopeStatus ScopeStatus
}

type UpsertFactRelationRequest struct {
	ProjectID     string
	SourceFactKey string
	TargetFactKey string
	Relation      string
	Summary       string
}

var ErrMissingFactKey = errors.New("fact key is required")
var ErrMissingSummary = errors.New("fact summary is required")
var ErrNotFound = errors.New("project fact not found")
var ErrMissingTargetFactKey = errors.New("target fact key is required")
var ErrMissingRelation = errors.New("fact relation is required")

type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

func (s *Service) UpsertFact(req UpsertFactRequest) (Fact, error) {
	req.FactKey = strings.TrimSpace(req.FactKey)
	req.Summary = strings.TrimSpace(req.Summary)
	if req.FactKey == "" {
		return Fact{}, ErrMissingFactKey
	}
	if req.Summary == "" {
		return Fact{}, ErrMissingSummary
	}
	if req.Confidence == "" {
		req.Confidence = ConfidenceTentative
	}

	existing, err := s.getByKey(req.ProjectID, req.FactKey)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Fact{}, err
	}

	now := time.Now().UTC()
	if errors.Is(err, ErrNotFound) {
		created := Fact{
			ID:          newID(),
			ProjectID:   req.ProjectID,
			FactKey:     req.FactKey,
			Category:    req.Category,
			Summary:     req.Summary,
			Body:        req.Body,
			Confidence:  req.Confidence,
			ScopeStatus: req.ScopeStatus,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		_, err := s.db.Exec(
			`INSERT INTO project_facts (id, project_id, fact_key, category, summary, body, confidence, scope_status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			created.ID, created.ProjectID, created.FactKey, created.Category, created.Summary, created.Body,
			string(created.Confidence), string(created.ScopeStatus), created.CreatedAt.Format(time.RFC3339Nano), created.UpdatedAt.Format(time.RFC3339Nano),
		)
		if err != nil {
			return Fact{}, fmt.Errorf("store project fact: %w", err)
		}
		if _, err := s.appendVersion(created); err != nil {
			return Fact{}, err
		}
		return created, nil
	}

	body := req.Body
	if body == "" {
		body = existing.Body
	}
	updated := existing
	updated.Category = req.Category
	updated.Summary = req.Summary
	updated.Body = body
	updated.Confidence = req.Confidence
	updated.ScopeStatus = req.ScopeStatus
	updated.UpdatedAt = now

	_, err = s.db.Exec(
		`UPDATE project_facts SET category = ?, summary = ?, body = ?, confidence = ?, scope_status = ?, updated_at = ?
		 WHERE project_id = ? AND fact_key = ?`,
		updated.Category, updated.Summary, updated.Body, string(updated.Confidence), string(updated.ScopeStatus),
		updated.UpdatedAt.Format(time.RFC3339Nano), updated.ProjectID, updated.FactKey,
	)
	if err != nil {
		return Fact{}, fmt.Errorf("update project fact: %w", err)
	}
	if _, err := s.appendVersion(updated); err != nil {
		return Fact{}, err
	}
	return updated, nil
}

func (s *Service) FactIndex(projectID string) ([]FactIndexEntry, error) {
	rows, err := s.db.Query(
		`SELECT fact_key, category, summary, confidence, scope_status
		 FROM project_facts
		 WHERE project_id = ? AND confidence != ?
		 ORDER BY updated_at ASC`,
		projectID, string(ConfidenceDeprecated),
	)
	if err != nil {
		return nil, fmt.Errorf("list fact index: %w", err)
	}
	defer rows.Close()

	var entries []FactIndexEntry
	for rows.Next() {
		var entry FactIndexEntry
		var confidence string
		var scopeStatus string
		if err := rows.Scan(&entry.FactKey, &entry.Category, &entry.Summary, &confidence, &scopeStatus); err != nil {
			return nil, fmt.Errorf("scan fact index: %w", err)
		}
		entry.Confidence = Confidence(confidence)
		entry.ScopeStatus = ScopeStatus(scopeStatus)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fact index: %w", err)
	}
	return entries, nil
}

func (s *Service) CountFacts(projectID string) (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM project_facts WHERE project_id = ?`, projectID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count project facts: %w", err)
	}
	return count, nil
}

func (s *Service) GetFact(projectID, factKey string) (Fact, error) {
	return s.getByKey(projectID, factKey)
}

func (s *Service) FactVersions(projectID, factKey string) ([]FactVersion, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, fact_key, version, category, summary, body, confidence, scope_status, created_at
		 FROM project_fact_versions
		 WHERE project_id = ? AND fact_key = ?
		 ORDER BY version ASC`,
		projectID, factKey,
	)
	if err != nil {
		return nil, fmt.Errorf("list fact versions: %w", err)
	}
	defer rows.Close()

	var versions []FactVersion
	for rows.Next() {
		version, err := scanFactVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fact versions: %w", err)
	}
	return versions, nil
}

func (s *Service) UpsertFactRelation(req UpsertFactRelationRequest) (FactRelation, error) {
	req.SourceFactKey = strings.TrimSpace(req.SourceFactKey)
	req.TargetFactKey = strings.TrimSpace(req.TargetFactKey)
	req.Relation = strings.TrimSpace(req.Relation)
	if req.SourceFactKey == "" {
		return FactRelation{}, ErrMissingFactKey
	}
	if req.TargetFactKey == "" {
		return FactRelation{}, ErrMissingTargetFactKey
	}
	if req.Relation == "" {
		return FactRelation{}, ErrMissingRelation
	}
	if _, err := s.getByKey(req.ProjectID, req.SourceFactKey); err != nil {
		return FactRelation{}, err
	}
	if _, err := s.getByKey(req.ProjectID, req.TargetFactKey); err != nil {
		return FactRelation{}, err
	}

	existing, err := s.getRelation(req.ProjectID, req.SourceFactKey, req.TargetFactKey, req.Relation)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return FactRelation{}, err
	}

	now := time.Now().UTC()
	if errors.Is(err, ErrNotFound) {
		created := FactRelation{
			ID:            newID(),
			ProjectID:     req.ProjectID,
			SourceFactKey: req.SourceFactKey,
			TargetFactKey: req.TargetFactKey,
			Relation:      req.Relation,
			Summary:       req.Summary,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		_, err := s.db.Exec(
			`INSERT INTO project_fact_relations (id, project_id, source_fact_key, target_fact_key, relation, summary, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			created.ID, created.ProjectID, created.SourceFactKey, created.TargetFactKey, created.Relation, created.Summary,
			created.CreatedAt.Format(time.RFC3339Nano), created.UpdatedAt.Format(time.RFC3339Nano),
		)
		if err != nil {
			return FactRelation{}, fmt.Errorf("store fact relation: %w", err)
		}
		return created, nil
	}

	existing.Summary = req.Summary
	existing.UpdatedAt = now
	_, err = s.db.Exec(
		`UPDATE project_fact_relations SET summary = ?, updated_at = ?
		 WHERE project_id = ? AND source_fact_key = ? AND target_fact_key = ? AND relation = ?`,
		existing.Summary, existing.UpdatedAt.Format(time.RFC3339Nano), existing.ProjectID,
		existing.SourceFactKey, existing.TargetFactKey, existing.Relation,
	)
	if err != nil {
		return FactRelation{}, fmt.Errorf("update fact relation: %w", err)
	}
	return existing, nil
}

func (s *Service) FactRelations(projectID, sourceFactKey string) ([]FactRelation, error) {
	if _, err := s.getByKey(projectID, sourceFactKey); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT id, project_id, source_fact_key, target_fact_key, relation, summary, created_at, updated_at
		 FROM project_fact_relations
		 WHERE project_id = ? AND source_fact_key = ?
		 ORDER BY created_at ASC`,
		projectID, sourceFactKey,
	)
	if err != nil {
		return nil, fmt.Errorf("list fact relations: %w", err)
	}
	defer rows.Close()

	var relations []FactRelation
	for rows.Next() {
		relation, err := scanFactRelation(rows)
		if err != nil {
			return nil, err
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fact relations: %w", err)
	}
	return relations, nil
}

func (s *Service) getByKey(projectID, factKey string) (Fact, error) {
	var fact Fact
	var confidence string
	var scopeStatus string
	var createdAt string
	var updatedAt string
	err := s.db.QueryRow(
		`SELECT id, project_id, fact_key, category, summary, body, confidence, scope_status, created_at, updated_at
		 FROM project_facts WHERE project_id = ? AND fact_key = ?`,
		projectID, factKey,
	).Scan(
		&fact.ID, &fact.ProjectID, &fact.FactKey, &fact.Category, &fact.Summary, &fact.Body,
		&confidence, &scopeStatus, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Fact{}, ErrNotFound
	}
	if err != nil {
		return Fact{}, err
	}
	fact.Confidence = Confidence(confidence)
	fact.ScopeStatus = ScopeStatus(scopeStatus)
	if fact.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Fact{}, err
	}
	if fact.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Fact{}, err
	}
	return fact, nil
}

func (s *Service) getRelation(projectID, sourceFactKey, targetFactKey, relation string) (FactRelation, error) {
	return scanFactRelation(s.db.QueryRow(
		`SELECT id, project_id, source_fact_key, target_fact_key, relation, summary, created_at, updated_at
		 FROM project_fact_relations
		 WHERE project_id = ? AND source_fact_key = ? AND target_fact_key = ? AND relation = ?`,
		projectID, sourceFactKey, targetFactKey, relation,
	))
}

func (s *Service) appendVersion(fact Fact) (FactVersion, error) {
	var maxVersion sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT MAX(version) FROM project_fact_versions WHERE project_id = ? AND fact_key = ?`,
		fact.ProjectID, fact.FactKey,
	).Scan(&maxVersion); err != nil {
		return FactVersion{}, fmt.Errorf("read max fact version: %w", err)
	}

	version := FactVersion{
		ID:          newID(),
		ProjectID:   fact.ProjectID,
		FactKey:     fact.FactKey,
		Version:     int(maxVersion.Int64) + 1,
		Category:    fact.Category,
		Summary:     fact.Summary,
		Body:        fact.Body,
		Confidence:  fact.Confidence,
		ScopeStatus: fact.ScopeStatus,
		CreatedAt:   fact.UpdatedAt,
	}
	_, err := s.db.Exec(
		`INSERT INTO project_fact_versions (id, project_id, fact_key, version, category, summary, body, confidence, scope_status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		version.ID, version.ProjectID, version.FactKey, version.Version, version.Category, version.Summary,
		version.Body, string(version.Confidence), string(version.ScopeStatus), version.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return FactVersion{}, fmt.Errorf("store fact version: %w", err)
	}
	return version, nil
}

type factVersionScanner interface {
	Scan(dest ...any) error
}

func scanFactVersion(scanner factVersionScanner) (FactVersion, error) {
	var version FactVersion
	var confidence string
	var scopeStatus string
	var createdAt string
	err := scanner.Scan(
		&version.ID, &version.ProjectID, &version.FactKey, &version.Version, &version.Category,
		&version.Summary, &version.Body, &confidence, &scopeStatus, &createdAt,
	)
	if err != nil {
		return FactVersion{}, fmt.Errorf("scan fact version: %w", err)
	}
	version.Confidence = Confidence(confidence)
	version.ScopeStatus = ScopeStatus(scopeStatus)
	if version.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return FactVersion{}, fmt.Errorf("parse created_at: %w", err)
	}
	return version, nil
}

func scanFactRelation(scanner factVersionScanner) (FactRelation, error) {
	var relation FactRelation
	var createdAt string
	var updatedAt string
	err := scanner.Scan(
		&relation.ID, &relation.ProjectID, &relation.SourceFactKey, &relation.TargetFactKey,
		&relation.Relation, &relation.Summary, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return FactRelation{}, ErrNotFound
	}
	if err != nil {
		return FactRelation{}, fmt.Errorf("scan fact relation: %w", err)
	}
	if relation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return FactRelation{}, fmt.Errorf("parse created_at: %w", err)
	}
	if relation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return FactRelation{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return relation, nil
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}
