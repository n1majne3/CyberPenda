package blackboard

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type FindingStatus string

const (
	FindingStatusUnconfirmed FindingStatus = "unconfirmed"
	FindingStatusConfirmed   FindingStatus = "confirmed"
)

type Finding struct {
	ID             string        `json:"id"`
	ProjectID      string        `json:"project_id"`
	FindingKey     string        `json:"finding_key"`
	Version        int           `json:"version"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	Status         FindingStatus `json:"status"`
	Target         string        `json:"target"`
	Proof          string        `json:"proof"`
	Impact         string        `json:"impact"`
	Recommendation string        `json:"recommendation"`
	CVSSVersion    string        `json:"cvss_version"`
	CVSSVector     string        `json:"cvss_vector"`
	CVSSPending    bool          `json:"cvss_pending"`
	Severity       string        `json:"severity"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

type FindingVersion struct {
	ID             string        `json:"id"`
	ProjectID      string        `json:"project_id"`
	FindingKey     string        `json:"finding_key"`
	Version        int           `json:"version"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	Status         FindingStatus `json:"status"`
	Target         string        `json:"target"`
	Proof          string        `json:"proof"`
	Impact         string        `json:"impact"`
	Recommendation string        `json:"recommendation"`
	CVSSVersion    string        `json:"cvss_version"`
	CVSSVector     string        `json:"cvss_vector"`
	CVSSPending    bool          `json:"cvss_pending"`
	Severity       string        `json:"severity"`
	CreatedAt      time.Time     `json:"created_at"`
}

type UpsertFindingRequest struct {
	ProjectID      string
	FindingKey     string
	Title          string
	Description    string
	Status         FindingStatus
	Target         string
	Proof          string
	Impact         string
	Recommendation string
	CVSSVersion    string
	CVSSVector     string
}

var ErrMissingFindingKey = errors.New("finding key is required")
var ErrMissingFindingTitle = errors.New("finding title is required")
var ErrConfirmedFindingIncomplete = errors.New("confirmed finding requires CVSS vector, target, proof, impact, and recommendation")

func (s *Service) UpsertFinding(req UpsertFindingRequest) (Finding, error) {
	req.FindingKey = strings.TrimSpace(req.FindingKey)
	req.Title = strings.TrimSpace(req.Title)
	if req.FindingKey == "" {
		return Finding{}, ErrMissingFindingKey
	}

	existing, err := s.getFinding(req.ProjectID, req.FindingKey)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Finding{}, err
	}
	if errors.Is(err, ErrNotFound) {
		if req.Title == "" {
			return Finding{}, ErrMissingFindingTitle
		}
		if req.Status == "" {
			req.Status = FindingStatusUnconfirmed
		}
	} else {
		req = preserveFindingFields(req, existing)
	}
	if req.Status == FindingStatusConfirmed && !confirmedFindingComplete(req) {
		return Finding{}, ErrConfirmedFindingIncomplete
	}

	now := time.Now().UTC()
	finding := Finding{
		ID:             newID(),
		ProjectID:      req.ProjectID,
		FindingKey:     req.FindingKey,
		Version:        1,
		Title:          req.Title,
		Description:    req.Description,
		Status:         req.Status,
		Target:         req.Target,
		Proof:          req.Proof,
		Impact:         req.Impact,
		Recommendation: req.Recommendation,
		CVSSVersion:    req.CVSSVersion,
		CVSSVector:     req.CVSSVector,
		CVSSPending:    strings.TrimSpace(req.CVSSVector) == "",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	finding.Severity = deriveSeverity(finding.CVSSVector)
	if !errors.Is(err, ErrNotFound) {
		finding.ID = existing.ID
		finding.CreatedAt = existing.CreatedAt
		finding.Version = existing.Version + 1
	}

	if errors.Is(err, ErrNotFound) {
		_, err = s.db.Exec(
			`INSERT INTO findings (id, project_id, finding_key, version, title, description, status, target, proof, impact, recommendation, cvss_version, cvss_vector, cvss_pending, severity, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			finding.ID, finding.ProjectID, finding.FindingKey, finding.Version, finding.Title, finding.Description, string(finding.Status),
			finding.Target, finding.Proof, finding.Impact, finding.Recommendation, finding.CVSSVersion, finding.CVSSVector,
			boolInt(finding.CVSSPending), finding.Severity, finding.CreatedAt.Format(time.RFC3339Nano), finding.UpdatedAt.Format(time.RFC3339Nano),
		)
	} else {
		_, err = s.db.Exec(
			`UPDATE findings SET version = ?, title = ?, description = ?, status = ?, target = ?, proof = ?, impact = ?, recommendation = ?, cvss_version = ?, cvss_vector = ?, cvss_pending = ?, severity = ?, updated_at = ?
			 WHERE project_id = ? AND finding_key = ?`,
			finding.Version, finding.Title, finding.Description, string(finding.Status), finding.Target, finding.Proof, finding.Impact,
			finding.Recommendation, finding.CVSSVersion, finding.CVSSVector, boolInt(finding.CVSSPending), finding.Severity,
			finding.UpdatedAt.Format(time.RFC3339Nano), finding.ProjectID, finding.FindingKey,
		)
	}
	if err != nil {
		return Finding{}, fmt.Errorf("store finding: %w", err)
	}
	if _, err := s.appendFindingVersion(finding); err != nil {
		return Finding{}, err
	}
	return finding, nil
}

func (s *Service) FindingVersions(projectID, findingKey string) ([]FindingVersion, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, finding_key, version, title, description, status, target, proof, impact, recommendation, cvss_version, cvss_vector, cvss_pending, severity, created_at
		 FROM finding_versions
		 WHERE project_id = ? AND finding_key = ?
		 ORDER BY version ASC`,
		projectID, findingKey,
	)
	if err != nil {
		return nil, fmt.Errorf("list finding versions: %w", err)
	}
	defer rows.Close()

	var versions []FindingVersion
	for rows.Next() {
		version, err := scanFindingVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list finding versions: %w", err)
	}
	return versions, nil
}

func (s *Service) CountFindings(projectID string) (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM findings WHERE project_id = ?`, projectID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count findings: %w", err)
	}
	return count, nil
}

func (s *Service) getFinding(projectID, findingKey string) (Finding, error) {
	var finding Finding
	var status string
	var cvssPending int
	var createdAt string
	var updatedAt string
	err := s.db.QueryRow(
		`SELECT id, project_id, finding_key, version, title, description, status, target, proof, impact, recommendation, cvss_version, cvss_vector, cvss_pending, severity, created_at, updated_at
		 FROM findings WHERE project_id = ? AND finding_key = ?`,
		projectID, findingKey,
	).Scan(
		&finding.ID, &finding.ProjectID, &finding.FindingKey, &finding.Version, &finding.Title, &finding.Description,
		&status, &finding.Target, &finding.Proof, &finding.Impact, &finding.Recommendation, &finding.CVSSVersion,
		&finding.CVSSVector, &cvssPending, &finding.Severity, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Finding{}, ErrNotFound
	}
	if err != nil {
		return Finding{}, err
	}
	finding.Status = FindingStatus(status)
	finding.CVSSPending = cvssPending != 0
	if finding.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Finding{}, err
	}
	if finding.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Finding{}, err
	}
	return finding, nil
}

func (s *Service) appendFindingVersion(finding Finding) (FindingVersion, error) {
	version := FindingVersion{
		ID:             newID(),
		ProjectID:      finding.ProjectID,
		FindingKey:     finding.FindingKey,
		Version:        finding.Version,
		Title:          finding.Title,
		Description:    finding.Description,
		Status:         finding.Status,
		Target:         finding.Target,
		Proof:          finding.Proof,
		Impact:         finding.Impact,
		Recommendation: finding.Recommendation,
		CVSSVersion:    finding.CVSSVersion,
		CVSSVector:     finding.CVSSVector,
		CVSSPending:    finding.CVSSPending,
		Severity:       finding.Severity,
		CreatedAt:      finding.UpdatedAt,
	}
	_, err := s.db.Exec(
		`INSERT INTO finding_versions (id, project_id, finding_key, version, title, description, status, target, proof, impact, recommendation, cvss_version, cvss_vector, cvss_pending, severity, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		version.ID, version.ProjectID, version.FindingKey, version.Version, version.Title, version.Description,
		string(version.Status), version.Target, version.Proof, version.Impact, version.Recommendation,
		version.CVSSVersion, version.CVSSVector, boolInt(version.CVSSPending), version.Severity,
		version.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return FindingVersion{}, fmt.Errorf("store finding version: %w", err)
	}
	return version, nil
}

func scanFindingVersion(scanner factVersionScanner) (FindingVersion, error) {
	var version FindingVersion
	var status string
	var cvssPending int
	var createdAt string
	err := scanner.Scan(
		&version.ID, &version.ProjectID, &version.FindingKey, &version.Version, &version.Title,
		&version.Description, &status, &version.Target, &version.Proof, &version.Impact,
		&version.Recommendation, &version.CVSSVersion, &version.CVSSVector, &cvssPending,
		&version.Severity, &createdAt,
	)
	if err != nil {
		return FindingVersion{}, fmt.Errorf("scan finding version: %w", err)
	}
	version.Status = FindingStatus(status)
	version.CVSSPending = cvssPending != 0
	if version.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return FindingVersion{}, fmt.Errorf("parse created_at: %w", err)
	}
	return version, nil
}

func preserveFindingFields(req UpsertFindingRequest, existing Finding) UpsertFindingRequest {
	if strings.TrimSpace(req.Title) == "" {
		req.Title = existing.Title
	}
	if strings.TrimSpace(req.Description) == "" {
		req.Description = existing.Description
	}
	if req.Status == "" {
		req.Status = existing.Status
	}
	if strings.TrimSpace(req.Target) == "" {
		req.Target = existing.Target
	}
	if strings.TrimSpace(req.Proof) == "" {
		req.Proof = existing.Proof
	}
	if strings.TrimSpace(req.Impact) == "" {
		req.Impact = existing.Impact
	}
	if strings.TrimSpace(req.Recommendation) == "" {
		req.Recommendation = existing.Recommendation
	}
	if strings.TrimSpace(req.CVSSVersion) == "" {
		req.CVSSVersion = existing.CVSSVersion
	}
	if strings.TrimSpace(req.CVSSVector) == "" {
		req.CVSSVector = existing.CVSSVector
	}
	return req
}

func confirmedFindingComplete(req UpsertFindingRequest) bool {
	return strings.TrimSpace(req.CVSSVector) != "" &&
		strings.TrimSpace(req.Target) != "" &&
		strings.TrimSpace(req.Proof) != "" &&
		strings.TrimSpace(req.Impact) != "" &&
		strings.TrimSpace(req.Recommendation) != ""
}

func deriveSeverity(vector string) string {
	vector = strings.TrimSpace(vector)
	if vector == "" {
		return "pending"
	}
	highs := 0
	for _, metric := range []string{"/VC:H", "/VI:H", "/VA:H", "/C:H", "/I:H", "/A:H"} {
		if strings.Contains(vector, metric) {
			highs++
		}
	}
	switch {
	case highs >= 2:
		return "critical"
	case highs == 1:
		return "high"
	default:
		return "medium"
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
