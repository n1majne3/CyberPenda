package blackboard

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type EvidenceAttachType string

const (
	EvidenceAttachFact    EvidenceAttachType = "fact"
	EvidenceAttachFinding EvidenceAttachType = "finding"
)

type EvidenceArtifact struct {
	ID           string             `json:"id"`
	ProjectID    string             `json:"project_id"`
	EvidenceKey  string             `json:"evidence_key"`
	AttachToType EvidenceAttachType `json:"attach_to_type"`
	AttachToKey  string             `json:"attach_to_key"`
	ArtifactType string             `json:"artifact_type"`
	SourcePath   string             `json:"source_path"`
	ManagedPath  string             `json:"managed_path"`
	SHA256       string             `json:"sha256"`
	Summary      string             `json:"summary"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

type AttachEvidenceRequest struct {
	ProjectID    string
	EvidenceKey  string
	AttachToType EvidenceAttachType
	AttachToKey  string
	ArtifactType string
	SourcePath   string
	SHA256       string
	Summary      string
}

var ErrMissingEvidenceKey = errors.New("evidence key is required")
var ErrMissingEvidenceTarget = errors.New("evidence attachment target is required")
var ErrMissingArtifactType = errors.New("evidence artifact type is required")
var ErrUnsupportedEvidenceTarget = errors.New("evidence attachment target must be fact or finding")

func (s *Service) AttachEvidence(req AttachEvidenceRequest) (EvidenceArtifact, error) {
	req.EvidenceKey = strings.TrimSpace(req.EvidenceKey)
	req.AttachToKey = strings.TrimSpace(req.AttachToKey)
	req.ArtifactType = strings.TrimSpace(req.ArtifactType)
	if req.EvidenceKey == "" {
		return EvidenceArtifact{}, ErrMissingEvidenceKey
	}
	if req.AttachToKey == "" {
		return EvidenceArtifact{}, ErrMissingEvidenceTarget
	}
	if req.ArtifactType == "" {
		return EvidenceArtifact{}, ErrMissingArtifactType
	}
	if err := s.validateEvidenceTarget(req.ProjectID, req.AttachToType, req.AttachToKey); err != nil {
		return EvidenceArtifact{}, err
	}

	existing, err := s.getEvidence(req.ProjectID, req.EvidenceKey)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return EvidenceArtifact{}, err
	}

	now := time.Now().UTC()
	artifact := EvidenceArtifact{
		ID:           newID(),
		ProjectID:    req.ProjectID,
		EvidenceKey:  req.EvidenceKey,
		AttachToType: req.AttachToType,
		AttachToKey:  req.AttachToKey,
		ArtifactType: req.ArtifactType,
		SourcePath:   req.SourcePath,
		ManagedPath:  managedEvidencePath(req.EvidenceKey, req.SourcePath),
		SHA256:       req.SHA256,
		Summary:      req.Summary,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if !errors.Is(err, ErrNotFound) {
		artifact.ID = existing.ID
		artifact.CreatedAt = existing.CreatedAt
	}

	if errors.Is(err, ErrNotFound) {
		_, err = s.db.Exec(
			`INSERT INTO evidence_artifacts (id, project_id, evidence_key, attach_to_type, attach_to_key, artifact_type, source_path, managed_path, sha256, summary, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			artifact.ID, artifact.ProjectID, artifact.EvidenceKey, string(artifact.AttachToType), artifact.AttachToKey,
			artifact.ArtifactType, artifact.SourcePath, artifact.ManagedPath, artifact.SHA256, artifact.Summary,
			artifact.CreatedAt.Format(time.RFC3339Nano), artifact.UpdatedAt.Format(time.RFC3339Nano),
		)
	} else {
		_, err = s.db.Exec(
			`UPDATE evidence_artifacts SET attach_to_type = ?, attach_to_key = ?, artifact_type = ?, source_path = ?, managed_path = ?, sha256 = ?, summary = ?, updated_at = ?
			 WHERE project_id = ? AND evidence_key = ?`,
			string(artifact.AttachToType), artifact.AttachToKey, artifact.ArtifactType, artifact.SourcePath,
			artifact.ManagedPath, artifact.SHA256, artifact.Summary, artifact.UpdatedAt.Format(time.RFC3339Nano),
			artifact.ProjectID, artifact.EvidenceKey,
		)
	}
	if err != nil {
		return EvidenceArtifact{}, fmt.Errorf("store evidence artifact: %w", err)
	}
	return artifact, nil
}

func (s *Service) CountEvidence(projectID string) (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM evidence_artifacts WHERE project_id = ?`, projectID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count evidence artifacts: %w", err)
	}
	return count, nil
}

func (s *Service) validateEvidenceTarget(projectID string, attachToType EvidenceAttachType, attachToKey string) error {
	switch attachToType {
	case EvidenceAttachFact:
		_, err := s.getByKey(projectID, attachToKey)
		return err
	case EvidenceAttachFinding:
		_, err := s.getFinding(projectID, attachToKey)
		return err
	default:
		return ErrUnsupportedEvidenceTarget
	}
}

func (s *Service) getEvidence(projectID, evidenceKey string) (EvidenceArtifact, error) {
	var artifact EvidenceArtifact
	var attachToType string
	var createdAt string
	var updatedAt string
	err := s.db.QueryRow(
		`SELECT id, project_id, evidence_key, attach_to_type, attach_to_key, artifact_type, source_path, managed_path, sha256, summary, created_at, updated_at
		 FROM evidence_artifacts WHERE project_id = ? AND evidence_key = ?`,
		projectID, evidenceKey,
	).Scan(
		&artifact.ID, &artifact.ProjectID, &artifact.EvidenceKey, &attachToType, &artifact.AttachToKey,
		&artifact.ArtifactType, &artifact.SourcePath, &artifact.ManagedPath, &artifact.SHA256, &artifact.Summary,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return EvidenceArtifact{}, ErrNotFound
	}
	if err != nil {
		return EvidenceArtifact{}, err
	}
	artifact.AttachToType = EvidenceAttachType(attachToType)
	if artifact.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return EvidenceArtifact{}, err
	}
	if artifact.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return EvidenceArtifact{}, err
	}
	return artifact, nil
}

func managedEvidencePath(evidenceKey, sourcePath string) string {
	base := filepath.Base(sourcePath)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "artifact"
	}
	return filepath.ToSlash(filepath.Join("artifacts", evidenceKey, base))
}
